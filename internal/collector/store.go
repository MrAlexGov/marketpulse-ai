package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MrAlexGov/marketpulse-ai/internal/market"
)

const (
	TopicPostings = "marketplace.postings"
	TopicStocks   = "marketplace.stocks"
)

const schema = `
CREATE TABLE IF NOT EXISTS posting_state (
    seller_id      text        NOT NULL,
    posting_number text        NOT NULL,
    status         text        NOT NULL,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (seller_id, posting_number)
);
CREATE TABLE IF NOT EXISTS outbox (
    id         bigserial   PRIMARY KEY,
    event_key  text        NOT NULL UNIQUE,
    topic      text        NOT NULL,
    msg_key    text        NOT NULL,
    payload    jsonb       NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    sent_at    timestamptz
);
CREATE INDEX IF NOT EXISTS outbox_pending ON outbox (id) WHERE sent_at IS NULL;
CREATE TABLE IF NOT EXISTS sync_state (
    seller_id        text PRIMARY KEY,
    backfill_done_at timestamptz NOT NULL
);
`

// statusRank — версия строки для ReplacingMergeTree в ClickHouse: более поздний
// статус всегда побеждает, даже если события пришли не по порядку.
var statusRank = map[string]uint8{"awaiting_packaging": 1, "delivering": 2, "delivered": 3, "cancelled": 4}

type PostingEvent struct {
	EventID       string  `json:"event_id"`
	SellerID      string  `json:"seller_id"`
	PostingNumber string  `json:"posting_number"`
	SKU           int64   `json:"sku"`
	OfferID       string  `json:"offer_id"`
	Name          string  `json:"name"`
	Status        string  `json:"status"`
	StatusRank    uint8   `json:"status_rank"`
	Quantity      int     `json:"quantity"`
	Price         float64 `json:"price"`
	InProcessAtMs int64   `json:"in_process_at_ms"`
	ObservedAtMs  int64   `json:"observed_at_ms"`
}

type StockEvent struct {
	EventID      string `json:"event_id"`
	SellerID     string `json:"seller_id"`
	SKU          int64  `json:"sku"`
	OfferID      string `json:"offer_id"`
	Present      int    `json:"present"`
	Reserved     int    `json:"reserved"`
	ObservedAtMs int64  `json:"observed_at_ms"`
}

// PostingEventKey — ключ идемпотентности: одно и то же отправление в одном и том же
// статусе порождает событие ровно один раз, сколько бы раз мы его ни перечитали.
func PostingEventKey(sellerID, postingNumber, status string, sku int64) string {
	return fmt.Sprintf("posting|%s|%s|%s|%d", sellerID, postingNumber, status, sku)
}

// StockEventKey — снимок остатка не чаще одного на 5-минутное окно.
func StockEventKey(sellerID string, sku int64, observed time.Time) string {
	return fmt.Sprintf("stock|%s|%d|%d", sellerID, sku, observed.Truncate(5*time.Minute).Unix())
}

type Store struct{ DB *pgxpool.Pool }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, schema)
	return err
}

func (s *Store) BackfillDone(ctx context.Context, sellerID string) (bool, error) {
	var t time.Time
	err := s.DB.QueryRow(ctx, `SELECT backfill_done_at FROM sync_state WHERE seller_id = $1`, sellerID).Scan(&t)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) MarkBackfillDone(ctx context.Context, sellerID string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO sync_state (seller_id, backfill_done_at) VALUES ($1, now())
		ON CONFLICT (seller_id) DO NOTHING`, sellerID)
	return err
}

// SavePostings — в одной транзакции обновляет состояние отправлений и пишет события
// в outbox (transactional outbox): либо сохранено и то и другое, либо ничего.
// Событие создаётся только для новых отправлений и при смене статуса.
func (s *Store) SavePostings(ctx context.Context, sellerID string, postings []market.Posting, observed time.Time) (int, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	batch := &pgx.Batch{}
	for _, p := range postings {
		batch.Queue(`INSERT INTO posting_state (seller_id, posting_number, status) VALUES ($1, $2, $3)
			ON CONFLICT (seller_id, posting_number) DO UPDATE
			SET status = EXCLUDED.status, updated_at = now()
			WHERE posting_state.status <> EXCLUDED.status
			RETURNING posting_number`, sellerID, p.PostingNumber, p.Status)
	}
	br := tx.SendBatch(ctx, batch)
	changed := make([]bool, len(postings))
	for i := range postings {
		var num string
		err := br.QueryRow().Scan(&num)
		switch {
		case err == nil:
			changed[i] = true
		case err == pgx.ErrNoRows:
			// статус не изменился — событие не нужно
		default:
			br.Close()
			return 0, err
		}
	}
	if err := br.Close(); err != nil {
		return 0, err
	}

	out := &pgx.Batch{}
	for i, p := range postings {
		if !changed[i] {
			continue
		}
		for _, pr := range p.Products {
			price, _ := strconv.ParseFloat(pr.Price, 64)
			ev := PostingEvent{
				EventID:       PostingEventKey(sellerID, p.PostingNumber, p.Status, pr.SKU),
				SellerID:      sellerID,
				PostingNumber: p.PostingNumber,
				SKU:           pr.SKU,
				OfferID:       pr.OfferID,
				Name:          pr.Name,
				Status:        p.Status,
				StatusRank:    statusRank[p.Status],
				Quantity:      pr.Quantity,
				Price:         price,
				InProcessAtMs: p.InProcessAt.UnixMilli(),
				ObservedAtMs:  observed.UnixMilli(),
			}
			queueOutbox(out, ev.EventID, TopicPostings, sellerID+"|"+p.PostingNumber, ev)
		}
	}
	n, err := execOutbox(ctx, tx, out)
	if err != nil {
		return 0, err
	}
	eventsEnqueued.WithLabelValues(TopicPostings).Add(float64(n))
	return n, tx.Commit(ctx)
}

func (s *Store) SaveStocks(ctx context.Context, sellerID string, items []market.StockItem, observed time.Time) (int, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	out := &pgx.Batch{}
	for _, it := range items {
		for _, st := range it.Stocks {
			ev := StockEvent{
				EventID:      StockEventKey(sellerID, st.SKU, observed),
				SellerID:     sellerID,
				SKU:          st.SKU,
				OfferID:      it.OfferID,
				Present:      st.Present,
				Reserved:     st.Reserved,
				ObservedAtMs: observed.Truncate(5 * time.Minute).UnixMilli(),
			}
			queueOutbox(out, ev.EventID, TopicStocks, fmt.Sprintf("%s|%d", sellerID, st.SKU), ev)
		}
	}
	n, err := execOutbox(ctx, tx, out)
	if err != nil {
		return 0, err
	}
	eventsEnqueued.WithLabelValues(TopicStocks).Add(float64(n))
	return n, tx.Commit(ctx)
}

func queueOutbox(b *pgx.Batch, eventKey, topic, msgKey string, payload any) {
	data, _ := json.Marshal(payload)
	b.Queue(`INSERT INTO outbox (event_key, topic, msg_key, payload) VALUES ($1, $2, $3, $4)
		ON CONFLICT (event_key) DO NOTHING`, eventKey, topic, msgKey, data)
}

func execOutbox(ctx context.Context, tx pgx.Tx, b *pgx.Batch) (int, error) {
	if b.Len() == 0 {
		return 0, nil
	}
	br := tx.SendBatch(ctx, b)
	defer br.Close()
	n := 0
	for i := 0; i < b.Len(); i++ {
		tag, err := br.Exec()
		if err != nil {
			return 0, err
		}
		n += int(tag.RowsAffected())
	}
	return n, br.Close()
}
