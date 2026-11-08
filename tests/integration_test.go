//go:build integration

package tests

import (
	"context"
	"encoding/json"
	inventoryv1 "flashcart/api/gen/inventory/v1"
	"flashcart/internal/domain"
	"flashcart/internal/inventory"
	"flashcart/internal/orders"
	"flashcart/internal/platform"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Every test owns a random schema. No existing schema or data is reused or reset.
func database(t *testing.T, service string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL is required for integration tests")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	schema := "test_" + platform.ID()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+schema); e != nil {
		t.Fatal(e)
	}
	cfg, e := pgxpool.ParseConfig(dsn)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 20
	db, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	if e = platform.Migrate(ctx, db, service); e != nil {
		t.Fatal(e)
	}
	return db
}
func TestInventoryConcurrentReservations(t *testing.T) {
	db := database(t, "inventory")
	ctx := context.Background()
	if _, e := db.Exec(ctx, `UPDATE stock SET available=5 WHERE sku='sku-keyboard'`); e != nil {
		t.Fatal(e)
	}
	s := &inventory.Service{DB: db}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("order-%03d", i)
			request := &inventoryv1.ReserveRequest{OrderId: id, Items: []*inventoryv1.Item{{Sku: "sku-keyboard", Quantity: 1}}}
			result, e := s.Reserve(ctx, request)
			if e != nil {
				t.Error(e)
				return
			}
			again, e := s.Reserve(ctx, request)
			if e != nil || again.Accepted != result.Accepted {
				t.Errorf("idempotent replay: %+v %v", again, e)
			}
			if result.Accepted {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 5 {
		t.Fatalf("overselling or lost stock: %d", accepted.Load())
	}
	var remaining int
	if e := db.QueryRow(ctx, `SELECT available FROM stock WHERE sku='sku-keyboard'`).Scan(&remaining); e != nil {
		t.Fatal(e)
	}
	if remaining != 0 {
		t.Fatalf("remaining %d", remaining)
	}
	_, e := s.Reserve(ctx, &inventoryv1.ReserveRequest{OrderId: "order-000", Items: []*inventoryv1.Item{{Sku: "sku-keyboard", Quantity: 2}}})
	if status.Code(e) != codes.AlreadyExists {
		t.Fatalf("changed replay accepted: %v", e)
	}
	result, e := s.Reserve(ctx, &inventoryv1.ReserveRequest{OrderId: "multi-item", Items: []*inventoryv1.Item{{Sku: "sku-headphones", Quantity: 2}, {Sku: "sku-keyboard", Quantity: 1}}})
	if e != nil || result.Accepted {
		t.Fatalf("partial reservation accepted: %v %v", result, e)
	}
	if e = db.QueryRow(ctx, `SELECT available FROM stock WHERE sku='sku-headphones'`).Scan(&remaining); e != nil || remaining != 1000 {
		t.Fatalf("partial stock mutation: %d %v", remaining, e)
	}
}
func TestCheckoutConcurrentIdempotency(t *testing.T) {
	shards := [2]*pgxpool.Pool{database(t, "orders"), database(t, "orders")}
	quote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(domain.Product{SKU: "sku-keyboard", PriceCents: 12900, Version: 1})
	}))
	defer quote.Close()
	s := &orders.Service{Shards: shards, CatalogURL: quote.URL, HTTP: quote.Client()}
	user := "user-test"
	var wg sync.WaitGroup
	ids := make(chan string, 30)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			o, _, e := s.Create(ctx, user, "same-key-123", []domain.Item{{SKU: "sku-keyboard", Quantity: 2}})
			if e != nil {
				t.Error(e)
				return
			}
			if o.TotalCents != 25800 {
				t.Errorf("wrong total %d", o.TotalCents)
			}
			ids <- o.ID
		}()
	}
	wg.Wait()
	close(ids)
	var first string
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatal("duplicate orders created")
		}
	}
	if first == "" {
		t.Fatal("no orders created")
	}
	db := shards[domain.Shard(user)]
	for _, table := range []string{"orders", "idempotency", "outbox"} {
		var n int
		if e := db.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); e != nil || n != 1 {
			t.Fatalf("%s count %d: %v", table, n, e)
		}
	}
	_, _, e := s.Create(context.Background(), user, "same-key-123", []domain.Item{{SKU: "sku-keyboard", Quantity: 3}})
	if e != orders.ErrConflict {
		t.Fatalf("changed key payload accepted: %v", e)
	}
}
