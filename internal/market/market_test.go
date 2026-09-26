package market

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

var origin = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func fixedNow() time.Time { return origin }

func TestGeneratorDeterministic(t *testing.T) {
	a := NewGenerator(DefaultSellers(), origin, fixedNow)
	b := NewGenerator(DefaultSellers(), origin, fixedNow)
	since := origin.Add(-24 * time.Hour)
	oa, ob := a.Orders("1001", since, origin), b.Orders("1001", since, origin)
	if len(oa) == 0 || len(oa) != len(ob) {
		t.Fatalf("expected equal non-empty order sets, got %d and %d", len(oa), len(ob))
	}
	for i := range oa {
		if oa[i] != ob[i] {
			t.Fatalf("order %d differs: %+v vs %+v", i, oa[i], ob[i])
		}
	}
}

func TestStockNeverNegativeAndLowStockScenario(t *testing.T) {
	g := NewGenerator(DefaultSellers(), origin, fixedNow)
	for _, st := range g.Stocks("2002") {
		if st.Present < 0 {
			t.Fatalf("negative stock for %d: %d", st.Product.SKU, st.Present)
		}
		if st.Product.SKU == 200201 {
			days := float64(st.Present) / st.Product.DailyDemand
			if days > 8 {
				t.Fatalf("plaid should be close to stock-out, has %.1f days of cover", days)
			}
		}
	}
}

func TestPriceHikeReducesDemand(t *testing.T) {
	g := NewGenerator(DefaultSellers(), origin, fixedNow)
	count := func(from, to time.Time) (n int) {
		for _, o := range g.Orders("1001", from, to) {
			if o.SKU == 100103 {
				n += o.Quantity
			}
		}
		return
	}
	recent := count(origin.Add(-3*24*time.Hour), origin)
	before := count(origin.Add(-17*24*time.Hour), origin.Add(-14*24*time.Hour))
	if float64(recent) > 0.6*float64(before) {
		t.Fatalf("expected demand drop after price hike: recent=%d before=%d", recent, before)
	}
}

func post(h http.Handler, path, client, key string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Client-Id", client)
	req.Header.Set("Api-Key", key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServerAuthRateLimitAndPagination(t *testing.T) {
	g := NewGenerator(DefaultSellers(), origin, fixedNow)
	h := NewServer(g, 1, 3).Handler()
	body := map[string]any{
		"limit":  50,
		"filter": map[string]any{"since": origin.Add(-24 * time.Hour), "to": origin},
	}

	if rec := post(h, "/v3/posting/fbs/list", "1001", "wrong", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	rec := post(h, "/v3/posting/fbs/list", "1001", "demo-key-1001", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body)
	}
	var resp PostingListResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Result.Postings) != 50 || !resp.Result.HasNext {
		t.Fatalf("expected full page with has_next, got %d, has_next=%v", len(resp.Result.Postings), resp.Result.HasNext)
	}

	got429 := false
	for i := 0; i < 5; i++ {
		if post(h, "/v3/posting/fbs/list", "1001", "demo-key-1001", body).Code == http.StatusTooManyRequests {
			got429 = true
		}
	}
	if !got429 {
		t.Fatal("expected 429 after exceeding burst")
	}
	// Лимит у каждого клиента свой.
	if rec := post(h, "/v3/posting/fbs/list", "2002", "demo-key-2002", body); rec.Code != http.StatusOK {
		t.Fatalf("other client should not be limited, got %d", rec.Code)
	}
}
