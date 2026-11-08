package main

import (
	"flashcart/internal/identity"
	"flashcart/internal/platform"
	"log/slog"
	"os"
)

func main() {
	if e := run(); e != nil {
		slog.Error("identity stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := platform.Root()
	defer cancel()
	db, e := platform.OpenDB(ctx, platform.MustEnv("DATABASE_URL"), "identity")
	if e != nil {
		return e
	}
	defer db.Close()
	key, e := platform.ReadPrivate(platform.MustEnv("JWT_PRIVATE_KEY"))
	if e != nil {
		return e
	}
	s := identity.New(db, key)
	if email := platform.Env("ADMIN_EMAIL", ""); email != "" {
		if e = s.BootstrapAdmin(ctx, email, platform.MustEnv("ADMIN_PASSWORD")); e != nil {
			return e
		}
	}
	return platform.Serve(ctx, "identity", s.Routes())
}
