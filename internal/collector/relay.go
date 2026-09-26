package collector

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
)

// Relay публикует события из outbox в Kafka. Гарантия — at-least-once: если процесс
// упадёт между отправкой в Kafka и отметкой sent_at, событие уйдёт повторно, и его
// схлопнет ClickHouse (ReplacingMergeTree по ключу отправления).
type Relay struct {
	DB     *pgxpool.Pool
	Writer *kafka.Writer
	Batch  int
}

func NewWriter(brokers []string) *kafka.Writer {
	return &kafka.Writer{
		Addr:                   kafka.TCP(brokers...),
		Balancer:               &kafka.Hash{}, // события одного отправления — в одну партицию, порядок сохраняется
		RequiredAcks:           kafka.RequireAll,
		BatchTimeout:           50 * time.Millisecond,
		AllowAutoTopicCreation: false,
	}
}

// EnsureTopics создаёт топики, если их ещё нет.
func EnsureTopics(ctx context.Context, broker string, partitions int, topics ...string) error {
	conn, err := kafka.DialContext(ctx, "tcp", broker)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctrl, err := conn.Controller()
	if err != nil {
		return err
	}
	cc, err := kafka.DialContext(ctx, "tcp", net.JoinHostPort(ctrl.Host, strconv.Itoa(ctrl.Port)))
	if err != nil {
		return err
	}
	defer cc.Close()
	cfgs := make([]kafka.TopicConfig, 0, len(topics))
	for _, t := range topics {
		cfgs = append(cfgs, kafka.TopicConfig{Topic: t, NumPartitions: partitions, ReplicationFactor: 1})
	}
	err = cc.CreateTopics(cfgs...)
	if errors.Is(err, kafka.TopicAlreadyExists) {
		return nil
	}
	return err
}

func (r *Relay) Run(ctx context.Context) {
	cleanup := time.NewTicker(10 * time.Minute)
	defer cleanup.Stop()
	for {
		n, err := r.once(ctx)
		if err != nil {
			slog.Error("relay: publish failed", "err", err)
		}
		r.updatePending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-cleanup.C:
			// Отправленные события храним 3 дня — этого хватает для дедупликации окна
			// перечитывания (48 часов), дальше таблица не растёт.
			if _, err := r.DB.Exec(ctx, `DELETE FROM outbox WHERE sent_at < now() - interval '3 days'`); err != nil {
				slog.Error("relay: cleanup failed", "err", err)
			}
		default:
			if n == 0 || err != nil {
				time.Sleep(500 * time.Millisecond)
			}
		}
	}
}

func (r *Relay) once(ctx context.Context) (int, error) {
	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	// SKIP LOCKED — несколько экземпляров relay могут работать параллельно, не мешая друг другу.
	rows, err := tx.Query(ctx, `SELECT id, topic, msg_key, payload::text FROM outbox
		WHERE sent_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, r.Batch)
	if err != nil {
		return 0, err
	}
	var ids []int64
	var msgs []kafka.Message
	perTopic := map[string]int{}
	for rows.Next() {
		var id int64
		var topic, key, payload string
		if err := rows.Scan(&id, &topic, &key, &payload); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
		msgs = append(msgs, kafka.Message{Topic: topic, Key: []byte(key), Value: []byte(payload)})
		perTopic[topic]++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(msgs) == 0 {
		return 0, nil
	}
	if err := r.Writer.WriteMessages(ctx, msgs...); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE outbox SET sent_at = now() WHERE id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	for t, n := range perTopic {
		eventsPublished.WithLabelValues(t).Add(float64(n))
	}
	return len(msgs), nil
}

func (r *Relay) updatePending(ctx context.Context) {
	var n int64
	if err := r.DB.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE sent_at IS NULL`).Scan(&n); err == nil {
		outboxPending.Set(float64(n))
	}
}
