package collector

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	apiRequests = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "collector_api_requests_total",
		Help: "Запросы к API маркетплейса по продавцу, методу и результату.",
	}, []string{"client_id", "path", "code"})

	eventsEnqueued = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "collector_outbox_enqueued_total",
		Help: "События, записанные в outbox (после дедупликации).",
	}, []string{"topic"})

	eventsPublished = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "collector_outbox_published_total",
		Help: "События, опубликованные в Kafka.",
	}, []string{"topic"})

	outboxPending = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "collector_outbox_pending",
		Help: "Неопубликованные события в outbox.",
	})

	syncDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "collector_sync_duration_seconds",
		Help:    "Длительность цикла синхронизации.",
		Buckets: prometheus.ExponentialBuckets(0.05, 2, 12),
	}, []string{"client_id", "kind"})
)
