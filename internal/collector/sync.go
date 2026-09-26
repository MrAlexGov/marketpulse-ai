package collector

import (
	"context"
	"log/slog"
	"time"

	"github.com/MrAlexGov/marketpulse-ai/internal/market"
)

// Syncer — цикл синхронизации одного продавца.
type Syncer struct {
	Client   *Client
	Store    *Store
	Interval time.Duration
	// BackfillDays — глубина первичной загрузки истории.
	BackfillDays int
	// RecheckWindow — сколько последних часов перечитываем каждый цикл, чтобы поймать
	// смену статусов (отправление переходит в "delivered" в течение суток).
	RecheckWindow time.Duration
}

func (s *Syncer) Run(ctx context.Context) {
	log := slog.With("client_id", s.Client.ClientID)
	for {
		if err := s.syncOnce(ctx); err != nil {
			log.Error("sync failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.Interval):
		}
	}
}

func (s *Syncer) syncOnce(ctx context.Context) error {
	id := s.Client.ClientID
	now := time.Now().UTC()

	done, err := s.Store.BackfillDone(ctx, id)
	if err != nil {
		return err
	}
	since := now.Add(-s.RecheckWindow)
	if !done {
		since = now.AddDate(0, 0, -s.BackfillDays)
	}

	start := time.Now()
	total := 0
	err = s.Client.ListPostings(ctx, since, now, func(page []market.Posting) error {
		n, err := s.Store.SavePostings(ctx, id, page, now)
		total += n
		return err
	})
	if err != nil {
		return err
	}
	kind := "incremental"
	if !done {
		kind = "backfill"
		if err := s.Store.MarkBackfillDone(ctx, id); err != nil {
			return err
		}
	}
	syncDuration.WithLabelValues(id, kind).Observe(time.Since(start).Seconds())

	start = time.Now()
	items, err := s.Client.ListStocks(ctx)
	if err != nil {
		return err
	}
	ns, err := s.Store.SaveStocks(ctx, id, items, now)
	if err != nil {
		return err
	}
	syncDuration.WithLabelValues(id, "stocks").Observe(time.Since(start).Seconds())
	slog.Info("sync done", "client_id", id, "kind", kind, "since", since.Format(time.RFC3339),
		"posting_events", total, "stock_events", ns)
	return nil
}
