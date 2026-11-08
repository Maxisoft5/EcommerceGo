package main

import (
	inventoryv1 "flashcart/api/gen/inventory/v1"
	"flashcart/internal/orders"
	"flashcart/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"log/slog"
	"net/http"
	"os"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("orders stopped", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := platform.Root()
	defer cancel()
	s := &orders.Service{CatalogURL: platform.Env("CATALOG_URL", "http://catalog:8080"), HTTP: &http.Client{Timeout: 2 * time.Second}}
	for i, key := range []string{"ORDERS_SHARD_0_URL", "ORDERS_SHARD_1_URL"} {
		db, e := platform.OpenDB(ctx, platform.MustEnv(key), "orders")
		if e != nil {
			return e
		}
		s.Shards[i] = db
		defer db.Close()
		go platform.Relay(ctx, db)
	}
	key, e := platform.ReadPublic(platform.MustEnv("JWT_PUBLIC_KEY"))
	if e != nil {
		return e
	}
	s.PublicKey = key
	tls, e := platform.MutualTLS(false)
	if e != nil {
		return e
	}
	conn, e := grpc.NewClient(platform.Env("INVENTORY_ADDR", "inventory:9090"), grpc.WithTransportCredentials(credentials.NewTLS(tls)))
	if e != nil {
		return e
	}
	defer conn.Close()
	s.Inventory = inventoryv1.NewInventoryServiceClient(conn)
	for i := 0; i < 4; i++ {
		go platform.Consume(ctx, "orders-saga-v1", s.Handle)
	}
	return platform.Serve(ctx, "orders", s.Routes())
}
