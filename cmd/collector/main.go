// collector — забирает отправления и остатки из API маркетплейса, пишет события в
// Postgres (transactional outbox) и публикует их в Kafka.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/MrAlexGov/marketpulse-ai/internal/collector"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, env("PG_DSN", "postgres://marketpulse:marketpulse@postgres:5432/marketpulse"))
	if err != nil {
		slog.Error("postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	store := &collector.Store{DB: pool}
	if err := retry(ctx, 30, func() error { return store.Migrate(ctx) }); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	brokers := strings.Split(env("KAFKA_BROKERS", "kafka:9092"), ",")
	if err := retry(ctx, 30, func() error {
		return collector.EnsureTopics(ctx, brokers[0], 3, collector.TopicPostings, collector.TopicStocks)
	}); err != nil {
		slog.Error("kafka topics", "err", err)
		os.Exit(1)
	}
	writer := collector.NewWriter(brokers)
	defer writer.Close()
	go (&collector.Relay{DB: pool, Writer: writer, Batch: 1000}).Run(ctx)

	// SELLERS=client_id:api_key,client_id:api_key
	rps, _ := strconv.ParseFloat(env("CLIENT_RPS", "4"), 64)
	interval, _ := time.ParseDuration(env("POLL_INTERVAL", "60s"))
	for _, pair := range strings.Split(env("SELLERS", "1001:demo-key-1001,2002:demo-key-2002"), ",") {
		id, key, _ := strings.Cut(strings.TrimSpace(pair), ":")
		s := &collector.Syncer{
			Client:        collector.NewClient(env("MARKET_URL", "http://mock-marketplace:8080"), id, key, rps),
			Store:         store,
			Interval:      interval,
			BackfillDays:  envInt("BACKFILL_DAYS", 30),
			RecheckWindow: 48 * time.Hour,
		}
		go s.Run(ctx)
	}

	http.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: env("METRICS_ADDR", ":9100")}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	slog.Info("collector started", "brokers", brokers, "interval", interval)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("metrics server", "err", err)
	}
}

func retry(ctx context.Context, attempts int, f func() error) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = f(); err == nil {
			return nil
		}
		slog.Warn("dependency not ready, retrying", "err", err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return err
}
