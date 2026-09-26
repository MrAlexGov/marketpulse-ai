package collector

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrAlexGov/marketpulse-ai/internal/market"
)

func TestEventKeysAreIdempotent(t *testing.T) {
	a := PostingEventKey("1001", "1001-00001-001", "delivering", 100101)
	b := PostingEventKey("1001", "1001-00001-001", "delivering", 100101)
	c := PostingEventKey("1001", "1001-00001-001", "delivered", 100101)
	if a != b {
		t.Fatal("same posting/status must produce same key")
	}
	if a == c {
		t.Fatal("status change must produce a new event")
	}
	t1 := time.Date(2026, 9, 26, 10, 1, 0, 0, time.UTC)
	t2 := time.Date(2026, 9, 26, 10, 4, 59, 0, time.UTC)
	t3 := time.Date(2026, 9, 26, 10, 5, 0, 0, time.UTC)
	if StockEventKey("1001", 1, t1) != StockEventKey("1001", 1, t2) {
		t.Fatal("stock snapshots within one 5-minute window must dedupe")
	}
	if StockEventKey("1001", 1, t1) == StockEventKey("1001", 1, t3) {
		t.Fatal("next window must produce a new snapshot")
	}
}

func TestClientRetriesOn429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Write([]byte(`{"cursor":"","items":[],"total":0}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "1001", "k", 100)
	var slept []time.Duration
	c.sleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }

	if _, err := c.ListStocks(context.Background()); err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if calls.Load() != 3 || len(slept) != 2 {
		t.Fatalf("expected 3 calls and 2 backoffs, got %d calls, %d sleeps", calls.Load(), len(slept))
	}
	if slept[0] < time.Second {
		t.Fatalf("Retry-After must be honored, slept %v", slept[0])
	}
}

func TestClientDoesNotRetryOn400(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "1001", "k", 100)
	c.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := c.ListStocks(context.Background()); err == nil || calls.Load() != 1 {
		t.Fatalf("expected immediate failure, err=%v calls=%d", err, calls.Load())
	}
}

func TestListPostingsWalksAllPages(t *testing.T) {
	origin := time.Now().UTC()
	gen := market.NewGenerator(market.DefaultSellers(), origin, func() time.Time { return origin })
	srv := httptest.NewServer(market.NewServer(gen, 1000, 1000).Handler())
	defer srv.Close()

	c := NewClient(srv.URL, "1001", "demo-key-1001", 1000)
	c.PageLimit = 100
	since := origin.Add(-72 * time.Hour)
	want := len(gen.Orders("1001", since, origin))
	got, pages := 0, 0
	err := c.ListPostings(context.Background(), since, origin, func(p []market.Posting) error {
		got += len(p)
		pages++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want || pages < 2 {
		t.Fatalf("expected %d postings over several pages, got %d in %d pages", want, got, pages)
	}
}
