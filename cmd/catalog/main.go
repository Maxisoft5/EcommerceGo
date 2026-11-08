package main

import (
	"flashcart/internal/catalog"
	"flashcart/internal/platform"
	"log/slog"
	"os"
)

func main() {
	if e := run(); e != nil {
		slog.Error("catalog stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := platform.Root()
	defer cancel()
	db, e := platform.OpenDB(ctx, platform.MustEnv("DATABASE_URL"), "catalog")
	if e != nil {
		return e
	}
	defer db.Close()
	key, e := platform.ReadPublic(platform.MustEnv("JWT_PUBLIC_KEY"))
	if e != nil {
		return e
	}
	cache := platform.Redis()
	defer cache.Close()
	s := &catalog.Service{DB: db, Cache: cache, PublicKey: key}
	if e = s.Seed(ctx); e != nil {
		return e
	}
	go platform.Relay(ctx, db)
	return platform.Serve(ctx, "catalog", s.Routes())
}
