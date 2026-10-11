package main

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

type itemStore struct {
	db  *pgxpool.Pool
	rdb *redis.Client
	ttl time.Duration
	sf  singleflight.Group // collapses concurrent miss-path loads per key
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
	_, err = st.db.Exec(ctx, `INSERT INTO items (id, name, price_cents)
		VALUES (1, 'warm', 100) ON CONFLICT (id) DO NOTHING`)
	return err
}

func itemKey(id int64) string {
	return "item:" + strconv.FormatInt(id, 10)
}

func (st *itemStore) get(ctx context.Context, id int64) (Item, error) {
	key := itemKey(id)
	rctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	cached, err := st.rdb.Get(rctx, key).Bytes()
	cancel()
	if err == nil {
		var it Item
		if jerr := json.Unmarshal(cached, &it); jerr == nil {
			return it, nil
		}
		// Corrupt cache entry: fall through to DB.
	} else if !isRedisMiss(err) {
		// Redis error (not a miss): fail open to DB, don't 500 on cache trouble.
	}

	// Miss path: one shared DB load per key, no matter how many
	// goroutines miss together. The fetch runs on a detached context
	// so one departing client can't cancel the load others are waiting on.
	v, err, _ := st.sf.Do(key, func() (any, error) {
		fctx, fcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer fcancel()
		var it Item
		qctx, qcancel := context.WithTimeout(fctx, 1500*time.Millisecond)
		defer qcancel()
		if derr := st.db.QueryRow(qctx,
			`SELECT id, name, price_cents FROM items WHERE id = $1`, id,
		).Scan(&it.ID, &it.Name, &it.Price); derr != nil {
			return Item{}, derr
		}
		if buf, merr := json.Marshal(it); merr == nil {
			_ = st.rdb.Set(fctx, key, buf, ttlWithJitter(st.ttl)).Err()
		}
		return it, nil
	})
	if err != nil {
		return Item{}, err
	}
	it, _ := v.(Item)
	return it, nil
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
	// Fresh row was never cached, but delete defensively (cheap, runs once per write).
	_ = st.rdb.Del(ctx, itemKey(it.ID)).Err()
	return it, nil
}

func isRedisMiss(err error) bool {
	return err == redis.Nil
}
