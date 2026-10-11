package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

var healthOK = []byte(`{"status":"ok"}`)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(healthOK)
}

type Item struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Price int64  `json:"price_cents"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// GET /v1/items/{id} — cache-aside: Redis first, Postgres PK lookup on miss.
func (s *Server) handleGetItem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, "bad id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	it, err := s.items.get(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "not found")
			return
		}
		s.log.Error("get item", "id", id, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, it)
}

type createItemReq struct {
	Name  string `json:"name"`
	Price int64  `json:"price_cents"`
}

// POST /v1/items {"name":..., "price_cents":...} — insert + invalidate.
func (s *Server) handleCreateItem(w http.ResponseWriter, r *http.Request) {
	var req createItemReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if req.Name == "" || len(req.Name) > 200 || req.Price < 0 {
		writeErr(w, http.StatusBadRequest, "bad fields")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	it, err := s.items.create(ctx, req.Name, req.Price)
	if err != nil {
		s.log.Error("create item", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusCreated, it)
}
