package market

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/time/rate"
)

var requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "mock_marketplace_requests_total",
	Help: "Запросы к эмулятору по клиенту, методу и коду ответа.",
}, []string{"client_id", "path", "code"})

// Server отдаёт подмножество Ozon Seller API:
//
//	POST /v3/posting/fbs/list    — отправления, пагинация offset + has_next
//	POST /v4/product/info/stocks — остатки, пагинация cursor
type Server struct {
	gen   *Generator
	rps   rate.Limit
	burst int

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

func NewServer(gen *Generator, rps float64, burst int) *Server {
	return &Server{gen: gen, rps: rate.Limit(rps), burst: burst, limiters: map[string]*rate.Limiter{}}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v3/posting/fbs/list", s.guard(s.postingsList))
	mux.HandleFunc("POST /v4/product/info/stocks", s.guard(s.stocks))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return mux
}

type apiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (s *Server) limiter(clientID string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.limiters[clientID]
	if !ok {
		l = rate.NewLimiter(s.rps, s.burst)
		s.limiters[clientID] = l
	}
	return l
}

// guard — авторизация по Client-Id/Api-Key и лимит запросов на клиента (как у Ozon:
// при превышении — 429, клиент должен подождать и повторить).
func (s *Server) guard(next func(http.ResponseWriter, *http.Request, Seller) int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		clientID := r.Header.Get("Client-Id")
		code := func() int {
			seller, ok := s.gen.Seller(clientID)
			if !ok || r.Header.Get("Api-Key") != seller.APIKey {
				writeJSON(w, http.StatusUnauthorized, apiError{16, "Client-Id and Api-Key headers are required"})
				return http.StatusUnauthorized
			}
			if !s.limiter(clientID).Allow() {
				w.Header().Set("Retry-After", "1")
				writeJSON(w, http.StatusTooManyRequests, apiError{8, "rate limit exceeded"})
				return http.StatusTooManyRequests
			}
			return next(w, r, seller)
		}()
		requestsTotal.WithLabelValues(clientID, r.URL.Path, strconv.Itoa(code)).Inc()
	}
}

type PostingListRequest struct {
	Dir    string `json:"dir"`
	Filter struct {
		Since  time.Time `json:"since"`
		To     time.Time `json:"to"`
		Status string    `json:"status"`
	} `json:"filter"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type PostingProduct struct {
	SKU      int64  `json:"sku"`
	OfferID  string `json:"offer_id"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
	Price    string `json:"price"` // Ozon отдаёт цену строкой
}

type Posting struct {
	PostingNumber string           `json:"posting_number"`
	Status        string           `json:"status"`
	InProcessAt   time.Time        `json:"in_process_at"`
	Products      []PostingProduct `json:"products"`
}

type PostingListResponse struct {
	Result struct {
		Postings []Posting `json:"postings"`
		HasNext  bool      `json:"has_next"`
	} `json:"result"`
}

func (s *Server) postingsList(w http.ResponseWriter, r *http.Request, seller Seller) int {
	var req PostingListRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{3, "invalid json: " + err.Error()})
		return http.StatusBadRequest
	}
	if req.Limit <= 0 || req.Limit > 1000 {
		writeJSON(w, http.StatusBadRequest, apiError{3, "limit must be in 1..1000"})
		return http.StatusBadRequest
	}
	if req.Filter.Since.IsZero() || req.Filter.To.IsZero() || !req.Filter.Since.Before(req.Filter.To) {
		writeJSON(w, http.StatusBadRequest, apiError{3, "filter.since and filter.to are required"})
		return http.StatusBadRequest
	}
	now := s.gen.now().UTC()
	all := s.gen.Orders(seller.ClientID, req.Filter.Since, req.Filter.To)
	var filtered []Posting
	for _, o := range all {
		st := Status(o, now)
		if req.Filter.Status != "" && st != req.Filter.Status {
			continue
		}
		p := seller.productBySKU(o.SKU)
		filtered = append(filtered, Posting{
			PostingNumber: o.PostingNumber,
			Status:        st,
			InProcessAt:   o.At,
			Products: []PostingProduct{{
				SKU: o.SKU, OfferID: p.OfferID, Name: p.Name, Quantity: o.Quantity,
				Price: fmt.Sprintf("%.2f", o.Price),
			}},
		})
	}
	var resp PostingListResponse
	resp.Result.Postings = []Posting{}
	if req.Offset < len(filtered) {
		end := min(req.Offset+req.Limit, len(filtered))
		resp.Result.Postings = filtered[req.Offset:end]
		resp.Result.HasNext = end < len(filtered)
	}
	writeJSON(w, http.StatusOK, resp)
	return http.StatusOK
}

func (s Seller) productBySKU(sku int64) Product {
	for _, p := range s.Products {
		if p.SKU == sku {
			return p
		}
	}
	return Product{}
}

type StocksRequest struct {
	Cursor string `json:"cursor"`
	Limit  int    `json:"limit"`
}

type StockEntry struct {
	Type     string `json:"type"`
	SKU      int64  `json:"sku"`
	Present  int    `json:"present"`
	Reserved int    `json:"reserved"`
}

type StockItem struct {
	ProductID int64        `json:"product_id"`
	OfferID   string       `json:"offer_id"`
	Stocks    []StockEntry `json:"stocks"`
}

type StocksResponse struct {
	Cursor string      `json:"cursor"`
	Items  []StockItem `json:"items"`
	Total  int         `json:"total"`
}

func (s *Server) stocks(w http.ResponseWriter, r *http.Request, seller Seller) int {
	var req StocksRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{3, "invalid json: " + err.Error()})
		return http.StatusBadRequest
	}
	if req.Limit <= 0 || req.Limit > 1000 {
		writeJSON(w, http.StatusBadRequest, apiError{3, "limit must be in 1..1000"})
		return http.StatusBadRequest
	}
	offset := 0
	if req.Cursor != "" {
		n, err := strconv.Atoi(req.Cursor)
		if err != nil || n < 0 {
			writeJSON(w, http.StatusBadRequest, apiError{3, "invalid cursor"})
			return http.StatusBadRequest
		}
		offset = n
	}
	all := s.gen.Stocks(seller.ClientID)
	resp := StocksResponse{Items: []StockItem{}, Total: len(all)}
	if offset < len(all) {
		end := min(offset+req.Limit, len(all))
		for _, st := range all[offset:end] {
			resp.Items = append(resp.Items, StockItem{
				ProductID: st.Product.SKU,
				OfferID:   st.Product.OfferID,
				Stocks:    []StockEntry{{Type: "fbo", SKU: st.Product.SKU, Present: st.Present}},
			})
		}
		if end < len(all) {
			resp.Cursor = strconv.Itoa(end)
		}
	}
	writeJSON(w, http.StatusOK, resp)
	return http.StatusOK
}
