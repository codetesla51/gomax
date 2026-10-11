// Command gomax is a practical JSON API shaped for Render free tier:
// net/http, pooled pgx + Redis, timeouts everywhere, cache-aside reads.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Config struct {
	Addr        string
	DatabaseURL string
	RedisURL    string
	RedisAddr   string
	RedisPass   string
	PoolMax     int32
	CacheTTL    time.Duration
}

func configFromEnv() Config {
	return Config{
		Addr:        envOr("ADDR", defaultPort()),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		RedisURL:    os.Getenv("REDIS_URL"),
		RedisAddr:   envOr("REDIS_ADDR", "localhost:6379"),
		RedisPass:   os.Getenv("REDIS_PASSWORD"),
		PoolMax:     int32(envIntOr("PG_POOL_MAX", 8)),
		CacheTTL:    time.Duration(envIntOr("CACHE_TTL_SECS", 30)) * time.Second,
	}
}

// Render injects PORT; local runs default to :8080.
func defaultPort() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":8080"
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envIntOr(k string, d int) int {
	v, err := strconv.Atoi(os.Getenv(k))
	if err != nil || v <= 0 {
		return d
	}
	return v
}

type Server struct {
	db    *pgxpool.Pool
	rdb   *redis.Client
	ttl   time.Duration
	log   *slog.Logger
	items *itemStore
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := configFromEnv()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	srv, err := newServer(ctx, cfg, log)
	if err != nil {
		log.Error("init failed", "err", err)
		os.Exit(1)
	}
	defer srv.close()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", srv.handleHealth)
	mux.HandleFunc("GET /v1/items/{id}", srv.handleGetItem)
	mux.HandleFunc("POST /v1/items", srv.handleCreateItem)

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", cfg.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("serve failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutCtx)
	log.Info("stopped")
}

func newServer(ctx context.Context, cfg Config, log *slog.Logger) (*Server, error) {
	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	poolCfg.MaxConns = cfg.PoolMax
	poolCfg.MinConns = 1
	poolCfg.MaxConnLifetime = 5 * time.Minute
	poolCfg.MaxConnIdleTime = 30 * time.Second

	db, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("pg pool: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.Ping(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("pg ping: %w", err)
	}

	var rdb *redis.Client
	if cfg.RedisURL != "" {
		opts, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("parse REDIS_URL: %w", err)
		}
		opts.DialTimeout = 2 * time.Second
		opts.ReadTimeout = 500 * time.Millisecond
		opts.WriteTimeout = 500 * time.Millisecond
		if opts.PoolSize == 0 {
			opts.PoolSize = 10
		}
		rdb = redis.NewClient(opts)
	} else {
		rdb = redis.NewClient(&redis.Options{
			Addr:         cfg.RedisAddr,
			Password:     cfg.RedisPass,
			PoolSize:     10,
			DialTimeout:  2 * time.Second,
			ReadTimeout:  500 * time.Millisecond,
			WriteTimeout: 500 * time.Millisecond,
		})
	}
	rpingCtx, rcancel := context.WithTimeout(ctx, 2*time.Second)
	defer rcancel()
	if err := rdb.Ping(rpingCtx).Err(); err != nil {
		db.Close()
		_ = rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	s := &Server{db: db, rdb: rdb, ttl: cfg.CacheTTL, log: log}
	s.items = &itemStore{db: db, rdb: rdb, ttl: cfg.CacheTTL}
	if err := s.items.ensureSchema(ctx); err != nil {
		s.close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	return s, nil
}

func (s *Server) close() {
	if s.db != nil {
		s.db.Close()
	}
	if s.rdb != nil {
		_ = s.rdb.Close()
	}
}
