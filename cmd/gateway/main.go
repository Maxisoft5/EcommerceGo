package main

import (
	"context"
	"flashcart/internal/gateway"
	"flashcart/internal/platform"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("gateway stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := platform.Root()
	defer cancel()
	key, e := platform.ReadPublic(platform.MustEnv("JWT_PUBLIC_KEY"))
	if e != nil {
		return e
	}
	cache := platform.Redis()
	defer cache.Close()
	hub := gateway.NewHub()
	go hub.Run(ctx, cache)
	s := &gateway.Service{Redis: cache, PublicKey: key, Hub: hub, Origin: platform.Env("PUBLIC_ORIGIN", "https://localhost:8443")}
	health := http.NewServeMux()
	platform.Health(health, func(ctx context.Context) error { return cache.Ping(ctx).Err() })
	admin := &http.Server{Addr: ":8081", Handler: health, ReadHeaderTimeout: 3 * time.Second}
	go func() {
		if e := admin.ListenAndServe(); e != nil && e != http.ErrServerClosed {
			slog.Error("admin server failed", "error", e)
			cancel()
		}
	}()
	defer admin.Close()
	return platform.Serve(ctx, "gateway", s.Routes())
}
