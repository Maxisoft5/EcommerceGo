package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"flashcart/internal/platform"
	"github.com/redis/go-redis/v9"
)

type Hub struct {
	mu    sync.RWMutex
	users map[string]map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{users: make(map[string]map[chan []byte]struct{})} }
func (h *Hub) Add(user string) (chan []byte, func()) {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	if h.users[user] == nil {
		h.users[user] = make(map[chan []byte]struct{})
	}
	h.users[user][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.users[user], ch)
		if len(h.users[user]) == 0 {
			delete(h.users, user)
		}
		h.mu.Unlock()
	}
}
func (h *Hub) Publish(user string, b []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.users[user] {
		select {
		case ch <- b:
		default: /* Slow clients recover through REST snapshots. */
		}
	}
}
func (h *Hub) Run(ctx context.Context, r *redis.Client) {
	sub := r.PSubscribe(ctx, "notify:*", "catalog:events")
	defer sub.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-sub.Channel(redis.WithChannelSize(1024)):
			if !ok {
				return
			}
			if msg.Channel == "catalog:events" {
				h.Publish("public:catalog", []byte(msg.Payload))
				continue
			}
			var e platform.Event
			if err := json.Unmarshal([]byte(msg.Payload), &e); err != nil {
				slog.Warn("invalid notification")
				continue
			}
			h.Publish(e.UserID, []byte(msg.Payload))
		}
	}
}
