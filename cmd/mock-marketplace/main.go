// mock-marketplace — эмулятор Ozon Seller API на синтетических данных.
package main

import (
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/MrAlexGov/marketpulse-ai/internal/market"
)

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return v
	}
	return def
}

func main() {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":8080"
	}
	gen := market.NewGenerator(market.DefaultSellers(), time.Now(), time.Now)
	srv := market.NewServer(gen, envFloat("RATE_LIMIT_RPS", 5), int(envFloat("RATE_LIMIT_BURST", 10)))

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/", srv.Handler())

	slog.Info("mock marketplace started", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
