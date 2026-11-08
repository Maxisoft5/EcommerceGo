//go:build integration

package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	inventoryv1 "flashcart/api/gen/inventory/v1"
	"flashcart/internal/catalog"
	"flashcart/internal/domain"
	"flashcart/internal/identity"
	"flashcart/internal/inventory"
	"flashcart/internal/orders"
	"flashcart/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func requestJSON(t *testing.T, h http.Handler, method, path, token, key string, body any, want int) []byte {
	t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s want=%d got=%d: %s", method, path, want, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}
func decodeProduct(t *testing.T, b []byte) domain.Product {
	t.Helper()
	var p domain.Product
	if e := json.Unmarshal(b, &p); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestSellerOwnershipAndFilteredOrderViews(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	sellerA, _ := platform.IssueWithShop(key, "seller-a", "seller", "Магазин А")
	sellerB, _ := platform.IssueWithShop(key, "seller-b", "seller", "Магазин Б")
	buyer, _ := platform.Issue(key, "buyer", "customer")
	c := &catalog.Service{DB: database(t, "catalog"), PublicKey: pub}
	routes := c.Routes()
	body := map[string]any{"name": "Наушники", "price_cents": 149900, "category": "electronics", "image_key": "headphones", "initial_stock": 2}
	requestJSON(t, routes, "POST", "/api/seller/products", buyer, "create-key-001", body, 403)
	a := decodeProduct(t, requestJSON(t, routes, "POST", "/api/seller/products", sellerA, "create-key-001", body, 201))
	again := decodeProduct(t, requestJSON(t, routes, "POST", "/api/seller/products", sellerA, "create-key-001", body, 201))
	if a.SKU != again.SKU || a.SellerID != "seller-a" || a.SellerName != "Магазин А" {
		t.Fatal("product owner or idempotency mismatch")
	}
	changed := map[string]any{"name": "Другое", "price_cents": 149900, "category": "electronics", "image_key": "headphones", "initial_stock": 2}
	requestJSON(t, routes, "POST", "/api/seller/products", sellerA, "create-key-001", changed, 409)
	update := map[string]any{"name": "Чужой товар", "price_cents": 10000, "category": "electronics", "image_key": "headphones"}
	requestJSON(t, routes, "PUT", "/api/seller/products/"+a.SKU, sellerB, "", update, 404)
	b := decodeProduct(t, requestJSON(t, routes, "POST", "/api/seller/products", sellerB, "create-key-002", body, 201))
	quote := httptest.NewServer(routes)
	defer quote.Close()
	s := &orders.Service{PublicKey: pub, Shards: [2]*pgxpool.Pool{database(t, "orders"), database(t, "orders")}, CatalogURL: quote.URL, HTTP: quote.Client()}
	orderRoutes := s.Routes()
	raw := requestJSON(t, orderRoutes, "POST", "/api/orders", buyer, "mixed-order-key", map[string]any{"items": []domain.Item{{SKU: a.SKU, Quantity: 1}, {SKU: b.SKU, Quantity: 2}}}, 202)
	var o domain.Order
	if e := json.Unmarshal(raw, &o); e != nil {
		t.Fatal(e)
	}
	if o.TotalCents != 449700 || o.Currency != "RUB" {
		t.Fatalf("bad price snapshots: %+v", o)
	}
	for _, tc := range []struct {
		token, seller string
		quantity      int32
	}{{sellerA, "seller-a", 1}, {sellerB, "seller-b", 2}} {
		raw = requestJSON(t, orderRoutes, "GET", "/api/seller/orders", tc.token, "", nil, 200)
		if bytes.Contains(raw, []byte(`"user_id"`)) {
			t.Fatal("seller response leaked buyer ID")
		}
		var response struct {
			Items []domain.SellerOrder `json:"items"`
		}
		if e := json.Unmarshal(raw, &response); e != nil {
			t.Fatal(e)
		}
		if len(response.Items) != 1 || len(response.Items[0].Items) != 1 || response.Items[0].Items[0].SellerID != tc.seller || response.Items[0].TotalCents != 149900*int64(tc.quantity) {
			t.Fatalf("cross-seller order leakage: %s", raw)
		}
	}
	requestJSON(t, orderRoutes, "GET", "/api/seller/orders", buyer, "", nil, 403)
	var count int
	if e := c.DB.QueryRow(context.Background(), `SELECT count(*) FROM product_requests`).Scan(&count); e != nil || count != 2 {
		t.Fatalf("product requests: %d %v", count, e)
	}
}

func TestStockProvisionReplayAndConcurrentRestock(t *testing.T) {
	ctx := context.Background()
	db := database(t, "inventory")
	s := &inventory.Service{DB: db}
	p := domain.Product{SKU: "limited-stock", SellerID: "seller-a", InitialStock: 2, Version: 1}
	event := platform.NewEvent("product.updated", p.SKU, "", 1, p)
	if e := s.HandleProduct(ctx, event); e != nil {
		t.Fatal(e)
	}
	if e := s.HandleProduct(ctx, event); e != nil {
		t.Fatal(e)
	}
	reserved, e := s.Reserve(ctx, &inventoryv1.ReserveRequest{OrderId: "first-purchase", Items: []*inventoryv1.Item{{Sku: p.SKU, Quantity: 1}}})
	if e != nil || !reserved.Accepted {
		t.Fatalf("reserve %v %v", reserved, e)
	}
	p.Version = 2
	if e = s.HandleProduct(ctx, platform.NewEvent("product.updated", p.SKU, "", 2, p)); e != nil {
		t.Fatal(e)
	}
	var available int
	if e = db.QueryRow(ctx, `SELECT available FROM stock WHERE sku=$1`, p.SKU).Scan(&available); e != nil || available != 1 {
		t.Fatalf("product replay refilled stock: %d %v", available, e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stock, _, err := s.Adjust(ctx, "seller-a", "restock-key-001", p.SKU, 4)
			if err != nil || stock.Available != 5 {
				t.Errorf("restock replay: %+v %v", stock, err)
			}
		}()
	}
	wg.Wait()
	if _, _, e = s.Adjust(ctx, "seller-a", "restock-key-001", p.SKU, 5); !errors.Is(e, inventory.ErrConflict) {
		t.Fatalf("changed restock accepted: %v", e)
	}
	if _, _, e = s.Adjust(ctx, "seller-b", "other-key-001", p.SKU, 5); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatalf("foreign seller changed stock: %v", e)
	}
	if _, _, e = s.Adjust(ctx, "seller-a", "negative-key-001", p.SKU, -6); !errors.Is(e, inventory.ErrStockRange) {
		t.Fatalf("negative stock allowed: %v", e)
	}
	for i := 0; i < 5; i++ {
		result, err := s.Reserve(ctx, &inventoryv1.ReserveRequest{OrderId: fmt.Sprintf("after-restock-%d", i), Items: []*inventoryv1.Item{{Sku: p.SKU, Quantity: 1}}})
		if err != nil || !result.Accepted {
			t.Fatalf("restocked item not purchasable: %+v %v", result, err)
		}
	}
	if e = db.QueryRow(ctx, `SELECT available FROM stock WHERE sku=$1`, p.SKU).Scan(&available); e != nil || available != 0 {
		t.Fatalf("stock conservation failed: %d %v", available, e)
	}
	var events int
	if e = db.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&events); e != nil || events != 8 {
		t.Fatalf("expected 8 durable stock changes, got %d: %v", events, e)
	}
}

func TestSellerRegistrationClaims(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	s := identity.New(database(t, "identity"), key)
	routes := s.Routes()
	credentials := map[string]string{"email": "shop@example.com", "password": "Seller-password-123", "role": "seller", "shop_name": "Мой магазин"}
	requestJSON(t, routes, "POST", "/api/auth/register", "", "", credentials, 201)
	raw := requestJSON(t, routes, "POST", "/api/auth/login", "", "", map[string]string{"email": credentials["email"], "password": credentials["password"]}, 200)
	var tokens struct {
		Access string `json:"access_token"`
	}
	if e := json.Unmarshal(raw, &tokens); e != nil {
		t.Fatal(e)
	}
	claims, e := platform.Verify(pub, tokens.Access)
	if e != nil || claims.Role != "seller" || claims.ShopName != "Мой магазин" {
		t.Fatalf("seller claim %v %v", claims, e)
	}
	requestJSON(t, routes, "GET", "/api/auth/me", tokens.Access, "", nil, 200)
	credentials["email"] = "admin-forged@example.com"
	credentials["role"] = "admin"
	requestJSON(t, routes, "POST", "/api/auth/register", "", "", credentials, 400)
}
