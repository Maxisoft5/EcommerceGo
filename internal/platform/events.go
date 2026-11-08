package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"
)

const Topic = "commerce.events.v1"

type Event struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Schema      int             `json:"schema"`
	AggregateID string          `json:"aggregate_id"`
	UserID      string          `json:"user_id,omitempty"`
	Version     int64           `json:"version"`
	At          time.Time       `json:"at"`
	Data        json.RawMessage `json:"data"`
}

func NewEvent(kind, aggregate, user string, version int64, data any) Event {
	b, e := json.Marshal(data)
	if e != nil {
		panic(e)
	}
	return Event{ID: ID(), Type: kind, Schema: 1, AggregateID: aggregate, UserID: user, Version: version, At: time.Now().UTC(), Data: b}
}
func Enqueue(ctx context.Context, tx pgx.Tx, e Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO outbox(id,aggregate_id,payload) VALUES($1,$2,$3)`, e.ID, e.AggregateID, b)
	return err
}
func Writer() *kafka.Writer {
	return &kafka.Writer{Addr: kafka.TCP(Brokers()...), Balancer: &kafka.Hash{}, RequiredAcks: kafka.RequireAll, WriteTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, BatchTimeout: 10 * time.Millisecond, AllowAutoTopicCreation: false}
}
func Relay(ctx context.Context, db *pgxpool.Pool) {
	w := Writer()
	defer w.Close()
	for ctx.Err() == nil {
		if err := relayBatch(ctx, db, w); err != nil {
			slog.Error("outbox publish failed", "error", err)
			if !Pause(ctx, time.Second) {
				return
			}
		} else if !Pause(ctx, 200*time.Millisecond) {
			return
		}
	}
}
func relayBatch(ctx context.Context, db *pgxpool.Pool, w *kafka.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	tx, e := db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT id,aggregate_id,payload FROM outbox WHERE published_at IS NULL ORDER BY created_at,id LIMIT 20 FOR UPDATE SKIP LOCKED`)
	if e != nil {
		return e
	}
	var ids []string
	var messages []kafka.Message
	for rows.Next() {
		var id, key string
		var body []byte
		if e = rows.Scan(&id, &key, &body); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
		messages = append(messages, kafka.Message{Topic: Topic, Key: []byte(key), Value: body})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(ids) == 0 {
		return tx.Commit(ctx)
	}
	if e = w.WriteMessages(ctx, messages...); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE outbox SET published_at=now() WHERE id=ANY($1)`, ids); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// A transient failure blocks this reader's assigned partitions. Offsets advance only after success.
// Run multiple group members for bounded concurrency; no fire-and-forget goroutines per message.
func Consume(ctx context.Context, group string, handler func(context.Context, Event) error) {
	r := kafka.NewReader(kafka.ReaderConfig{Brokers: Brokers(), Topic: Topic, GroupID: group, MinBytes: 1, MaxBytes: 1 << 20, MaxWait: time.Second, CommitInterval: 0, StartOffset: kafka.FirstOffset, WatchPartitionChanges: true})
	defer r.Close()
	dlq := Writer()
	defer dlq.Close()
	for ctx.Err() == nil {
		msg, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Error("kafka fetch", "group", group, "error", err)
			Pause(ctx, time.Second)
			continue
		}
		var event Event
		err = json.Unmarshal(msg.Value, &event)
		invalid := err != nil || event.Schema != 1 || event.ID == "" || event.AggregateID == "" || event.Version < 1
		delay := 100 * time.Millisecond
		for ctx.Err() == nil {
			attempt, cancel := context.WithTimeout(ctx, 10*time.Second)
			if invalid {
				body, _ := json.Marshal(map[string]any{"source_topic": msg.Topic, "partition": msg.Partition, "offset": msg.Offset, "consumer": group, "error": "invalid envelope or unsupported schema", "raw": string(msg.Value)})
				err = dlq.WriteMessages(attempt, kafka.Message{Topic: "commerce.dlq.v1", Key: msg.Key, Value: body})
			} else {
				err = handler(attempt, event)
			}
			cancel()
			if err == nil {
				err = r.CommitMessages(ctx, msg)
				if err == nil {
					Events.WithLabelValues(group, event.Type).Inc()
					break
				}
			}
			slog.Error("event retry", "group", group, "event_id", event.ID, "error", fmt.Sprint(err))
			if !Pause(ctx, delay) {
				return
			}
			if delay < 5*time.Second {
				delay *= 2
			}
		}
	}
}
