package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestItemKey(t *testing.T) {
	if got := itemKey(42); got != "item:42" {
		t.Fatalf("itemKey(42) = %q", got)
	}
}

func TestTTLWithJitterBounds(t *testing.T) {
	base := 30 * time.Second
	lo, hi := base-base/8, base+base/8
	for range 5000 {
		got := ttlWithJitter(base)
		if got < lo || got > hi {
			t.Fatalf("ttlWithJitter(%v) = %v, want within [%v, %v]", base, got, lo, hi)
		}
	}
}

func TestTTLWithJitterNonPositive(t *testing.T) {
	if got := ttlWithJitter(0); got != 0 {
		t.Fatalf("ttlWithJitter(0) = %v", got)
	}
}

func TestHealth(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if body := rec.Body.String(); body != "{\"status\":\"ok\"}" {
		t.Fatalf("body = %q", body)
	}
}
