package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/MrAlexGov/marketpulse-ai/internal/market"
)

// Client — клиент к API маркетплейса для одного продавца. У каждого продавца свой
// лимитер: общий лимит API делится между клиентами, и один "шумный" продавец не
// выедает квоту остальных.
type Client struct {
	BaseURL  string
	ClientID string
	APIKey   string
	HTTP     *http.Client
	Limiter  *rate.Limiter
	// MaxAttempts — сколько раз повторять запрос при 429/5xx/сетевой ошибке.
	MaxAttempts int
	// PageLimit — размер страницы отправлений (у Ozon максимум 1000).
	PageLimit int
	sleep     func(context.Context, time.Duration) error
}

func NewClient(baseURL, clientID, apiKey string, rps float64) *Client {
	return &Client{
		BaseURL: baseURL, ClientID: clientID, APIKey: apiKey,
		HTTP:        &http.Client{Timeout: 30 * time.Second},
		Limiter:     rate.NewLimiter(rate.Limit(rps), 1),
		MaxAttempts: 6,
		PageLimit:   1000,
		sleep:       sleepCtx,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// backoff — экспоненциальная задержка с джиттером; Retry-After от сервера имеет приоритет.
func backoff(attempt int, retryAfter string) time.Duration {
	if s, err := strconv.Atoi(retryAfter); err == nil && s > 0 {
		return time.Duration(s)*time.Second + time.Duration(rand.Intn(250))*time.Millisecond
	}
	d := time.Duration(200*(1<<attempt)) * time.Millisecond
	return d + time.Duration(rand.Int63n(int64(d/2)+1))
}

func (c *Client) post(ctx context.Context, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < c.MaxAttempts; attempt++ {
		if err := c.Limiter.Wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Client-Id", c.ClientID)
		req.Header.Set("Api-Key", c.APIKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			apiRequests.WithLabelValues(c.ClientID, path, "network_error").Inc()
			if err := c.sleep(ctx, backoff(attempt, "")); err != nil {
				return err
			}
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		apiRequests.WithLabelValues(c.ClientID, path, strconv.Itoa(resp.StatusCode)).Inc()

		switch {
		case resp.StatusCode == http.StatusOK:
			return json.Unmarshal(data, out)
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, data)
			if err := c.sleep(ctx, backoff(attempt, resp.Header.Get("Retry-After"))); err != nil {
				return err
			}
		default:
			// 4xx кроме 429 — ошибка запроса, повтор не поможет.
			return fmt.Errorf("%s: HTTP %d: %s", path, resp.StatusCode, data)
		}
	}
	return fmt.Errorf("giving up after %d attempts: %w", c.MaxAttempts, lastErr)
}

// ListPostings обходит все страницы (offset + has_next) за интервал [since, to).
func (c *Client) ListPostings(ctx context.Context, since, to time.Time, page func([]market.Posting) error) error {
	limit := c.PageLimit
	for offset := 0; ; offset += limit {
		var req market.PostingListRequest
		req.Dir = "ASC"
		req.Filter.Since, req.Filter.To = since, to
		req.Limit, req.Offset = limit, offset
		var resp market.PostingListResponse
		if err := c.post(ctx, "/v3/posting/fbs/list", req, &resp); err != nil {
			return err
		}
		if len(resp.Result.Postings) > 0 {
			if err := page(resp.Result.Postings); err != nil {
				return err
			}
		}
		if !resp.Result.HasNext {
			return nil
		}
	}
}

// ListStocks обходит остатки по cursor-пагинации.
func (c *Client) ListStocks(ctx context.Context) ([]market.StockItem, error) {
	var all []market.StockItem
	cursor := ""
	for {
		var resp market.StocksResponse
		if err := c.post(ctx, "/v4/product/info/stocks", market.StocksRequest{Cursor: cursor, Limit: 100}, &resp); err != nil {
			return nil, err
		}
		all = append(all, resp.Items...)
		if resp.Cursor == "" {
			return all, nil
		}
		cursor = resp.Cursor
	}
}
