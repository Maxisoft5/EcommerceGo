package main

import (
	"flashcart/internal/platform"
	"flashcart/internal/projection"
	"log/slog"
	"os"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("projection stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := platform.Root()
	defer cancel()
	s := projection.New()
	s.Redis = platform.Redis()
	defer s.Redis.Close()
	key, e := platform.ReadPublic(platform.MustEnv("JWT_PUBLIC_KEY"))
	if e != nil {
		return e
	}
	s.PublicKey = key
	for {
		if e = s.Init(ctx); e == nil {
			break
		}
		slog.Warn("projection init waiting", "error", e)
		if !platform.Pause(ctx, 2*time.Second) {
			return ctx.Err()
		}
	}
	go platform.Consume(ctx, "projection-search-v2", s.SearchEvent)
	go platform.Consume(ctx, "projection-analytics-v1", s.AnalyticsEvent)
	go platform.Consume(ctx, "projection-notifications-v1", s.NotificationEvent)
	return platform.Serve(ctx, "projection", s.Routes())
}
