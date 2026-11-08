//go:build integration

package tests

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	inventoryv1 "flashcart/api/gen/inventory/v1"
	"flashcart/internal/domain"
	"flashcart/internal/identity"
	"flashcart/internal/inventory"
	"flashcart/internal/orders"
	"flashcart/internal/platform"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type lostReply struct {
	inventory *inventory.Service
	calls     atomic.Int32
}

func (l *lostReply) Reserve(ctx context.Context, r *inventoryv1.ReserveRequest, _ ...grpc.CallOption) (*inventoryv1.ReserveResponse, error) {
	result, e := l.inventory.Reserve(ctx, r)
	if e != nil {
		return nil, e
	}
	if l.calls.Add(1) == 1 {
		return nil, status.Error(codes.Unavailable, "simulated lost reply after successful inventory commit")
	}
	return result, nil
}

func TestForwardRecoveryAfterLostRPCReply(t *testing.T) {
	ctx := context.Background()
	stockDB := database(t, "inventory")
	shards := [2]*pgxpool.Pool{database(t, "orders"), database(t, "orders")}
	remote := &lostReply{inventory: &inventory.Service{DB: stockDB}}
	quote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(domain.Product{SKU: "sku-keyboard", PriceCents: 12900, Version: 1})
	}))
	defer quote.Close()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s := &orders.Service{Shards: shards, PublicKey: pub, Inventory: remote, CatalogURL: quote.URL, HTTP: quote.Client()}
	user := "recovery-owner"
	o, _, e := s.Create(ctx, user, "recovery-123", []domain.Item{{SKU: "sku-keyboard", Quantity: 1}})
	if e != nil {
		t.Fatal(e)
	}
	event := platform.NewEvent("order.created", o.ID, user, 1, o)
	if e = s.Handle(ctx, event); status.Code(e) != codes.Unavailable {
		t.Fatalf("expected ambiguous RPC failure: %v", e)
	}
	var stock int
	if e = stockDB.QueryRow(ctx, `SELECT available FROM stock WHERE sku='sku-keyboard'`).Scan(&stock); e != nil || stock != 999 {
		t.Fatalf("reserve did not commit before failure: stock=%d error=%v", stock, e)
	}
	if e = s.Handle(ctx, event); e != nil {
		t.Fatal(e)
	}
	if e = s.Handle(ctx, event); e != nil {
		t.Fatal(e)
	}
	if remote.calls.Load() != 2 {
		t.Fatalf("terminal replay invoked reserve again: %d", remote.calls.Load())
	}
	if e = stockDB.QueryRow(ctx, `SELECT available FROM stock WHERE sku='sku-keyboard'`).Scan(&stock); e != nil || stock != 999 {
		t.Fatalf("double reserve: %d %v", stock, e)
	}
	db := shards[domain.Shard(user)]
	var state string
	var version int64
	if e = db.QueryRow(ctx, `SELECT status,version FROM orders WHERE id=$1`, o.ID).Scan(&state, &version); e != nil || state != "confirmed" || version != 2 {
		t.Fatalf("transition %s/%d %v", state, version, e)
	}
	var count int
	if e = db.QueryRow(ctx, `SELECT count(*) FROM outbox`).Scan(&count); e != nil || count != 2 {
		t.Fatalf("expected created+confirmed outbox, got %d: %v", count, e)
	}
	// A different user on the SAME physical shard must still get 404.
	attacker := ""
	for i := 0; i < 100; i++ {
		candidate := fmt.Sprintf("attacker-%d", i)
		if domain.Shard(candidate) == domain.Shard(user) {
			attacker = candidate
			break
		}
	}
	token, e := platform.Issue(priv, attacker, "customer")
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "/api/orders/"+o.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("same-shard ownership leak: %d", w.Code)
	}
}

func TestRefreshRotationAndReuseRevokesFamily(t *testing.T) {
	ctx := context.Background()
	db := database(t, "identity")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s := identity.New(db, priv)
	if e := s.BootstrapAdmin(ctx, "admin@example.com", "TwelveBytes-123456"); e != nil {
		t.Fatal(e)
	}
	routes := s.Routes()
	call := func(path string, body any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s want=%d actual=%d body=%s", path, want, w.Code, w.Body.String())
		}
		result := map[string]any{}
		if want != 204 {
			if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
				t.Fatal(e)
			}
		}
		return result
	}
	login := call("/api/auth/login", map[string]string{"email": "admin@example.com", "password": "TwelveBytes-123456"}, 200)
	old := login["refresh_token"].(string)
	rotated := call("/api/auth/refresh", map[string]string{"refresh_token": old}, 200)
	current := rotated["refresh_token"].(string)
	if old == current {
		t.Fatal("refresh token was not rotated")
	}
	call("/api/auth/refresh", map[string]string{"refresh_token": old}, 401)
	call("/api/auth/refresh", map[string]string{"refresh_token": current}, 401)
	var active int
	if e := db.QueryRow(ctx, `SELECT count(*) FROM refresh_tokens WHERE revoked=false`).Scan(&active); e != nil || active != 0 {
		t.Fatalf("family not revoked: %d %v", active, e)
	}
	again := call("/api/auth/login", map[string]string{"email": "admin@example.com", "password": "TwelveBytes-123456"}, 200)
	call("/api/auth/logout", map[string]string{"refresh_token": again["refresh_token"].(string)}, 204)
	call("/api/auth/refresh", map[string]string{"refresh_token": again["refresh_token"].(string)}, 401)
}
