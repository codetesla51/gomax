package main

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/maypok86/otter/v2"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

type itemStore struct {
	db  *pgxpool.Pool
	rdb *redis.Client
	ttl time.Duration
	// L1: bounded in-process cache in front of Redis. Short TTL bounds
	// cross-instance divergence; invalidation (not expiry) is the
	// primary coherence mechanism — see create().
	l1    *otter.Cache[string, Item]
	l1TTL time.Duration
	sf    singleflight.Group // collapses concurrent miss-path loads per key
}

func newItemStore(db *pgxpool.Pool, rdb *redis.Client, ttl, l1TTL time.Duration) (*itemStore, error) {
	l1, err := otter.New[string, Item](&otter.Options[string, Item]{
		MaximumSize: 10_000, // ~2MB at our value sizes; negligible on 512MB
		ExpiryCalculator: otter.ExpiryCreatingFunc[string, Item](
			func(otter.Entry[string, Item]) time.Duration {
				return ttlWithJitter(l1TTL)
			}),
	})
	if err != nil {
		return nil, err
	}
	return &itemStore{db: db, rdb: rdb, ttl: ttl, l1: l1, l1TTL: l1TTL}, nil
}

// ttlWithJitter spreads expiry over [7/8, 9/8] of base so hot keys
// never expire in lockstep and herd the database.
func ttlWithJitter(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	q := int64(base) / 8
	return base - time.Duration(q) + time.Duration(rand.Int64N(2*q+1))
}

func (st *itemStore) ensureSchema(ctx context.Context) error {
	_, err := st.db.Exec(ctx, `CREATE TABLE IF NOT EXISTS items (
		id BIGSERIAL PRIMARY KEY,
		name TEXT NOT NULL,
		price_cents BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return err
	}
	// One warm row so the read path is testable immediately.
	// NOTE: explicit ids don't advance the BIGSERIAL sequence, so park it
	// past max(id) or the next auto-insert collides (live 500, 23505).
	_, err = st.db.Exec(ctx, `INSERT INTO items (id, name, price_cents)
		VALUES (1, 'warm', 100) ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		return err
	}
	_, err = st.db.Exec(ctx, `SELECT setval(pg_get_serial_sequence('items', 'id'),
		COALESCE((SELECT max(id) FROM items), 0) + 1, false)`)
	return err
}

func itemKey(id int64) string {
	return "item:" + strconv.FormatInt(id, 10)
}

// loadSource names which layer served the data. Exposed as X-Cache so
// barrage runs prove layer behavior instead of guessing it.
type loadSource string

const (
	srcL1    loadSource = "L1"
	srcRedis loadSource = "Redis"
	srcDB    loadSource = "DB"
)

type loadResult struct {
	item Item
	src  loadSource
}

func (st *itemStore) get(ctx context.Context, id int64) (Item, loadSource, error) {
	key := itemKey(id)
	if it, ok := st.l1.GetIfPresent(key); ok {
		return it, srcL1, nil
	}

	// Miss path: one shared load per key, no matter how many goroutines
	// miss together. The fetch runs on a detached context so one
	// departing client can't cancel the load others are waiting on.
	v, err, _ := st.sf.Do(key, func() (any, error) {
		fctx, fcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer fcancel()

		rctx, rcancel := context.WithTimeout(fctx, 300*time.Millisecond)
		cached, rerr := st.rdb.Get(rctx, key).Bytes()
		rcancel()
		if rerr == nil {
			var it Item
			if jerr := json.Unmarshal(cached, &it); jerr == nil {
				st.l1.Set(key, it)
				return loadResult{item: it, src: srcRedis}, nil
			}
			// Corrupt cache entry: fall through to DB.
		} // Redis miss OR error: fail open to DB, never 500 on cache trouble.

		var it Item
		qctx, qcancel := context.WithTimeout(fctx, 1500*time.Millisecond)
		defer qcancel()
		if derr := st.db.QueryRow(qctx,
			`SELECT id, name, price_cents FROM items WHERE id = $1`, id,
		).Scan(&it.ID, &it.Name, &it.Price); derr != nil {
			return loadResult{}, derr
		}
		if buf, merr := json.Marshal(it); merr == nil {
			_ = st.rdb.Set(fctx, key, buf, ttlWithJitter(st.ttl)).Err()
		}
		st.l1.Set(key, it)
		return loadResult{item: it, src: srcDB}, nil
	})
	if err != nil {
		return Item{}, srcDB, err
	}
	res, _ := v.(loadResult)
	return res.item, res.src, nil
}

func (st *itemStore) create(ctx context.Context, name string, price int64) (Item, error) {
	var it Item
	err := st.db.QueryRow(ctx,
		`INSERT INTO items (name, price_cents) VALUES ($1, $2)
		 RETURNING id, name, price_cents`, name, price,
	).Scan(&it.ID, &it.Name, &it.Price)
	if err != nil {
		return Item{}, err
	}
	// Invalidate both layers, same order on every mutation path: Redis
	// first, then L1. A reader slipping between the two sees at most
	// nanoseconds of L1 staleness, healed by the short L1 TTL.
	_ = st.rdb.Del(ctx, itemKey(it.ID)).Err()
	st.l1.Invalidate(itemKey(it.ID))
	return it, nil
}
