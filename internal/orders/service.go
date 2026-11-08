package orders

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"time"

	inventoryv1 "flashcart/api/gen/inventory/v1"
	"flashcart/internal/domain"
	"flashcart/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	Shards     [2]*pgxpool.Pool
	PublicKey  ed25519.PublicKey
	Inventory  inventoryv1.InventoryServiceClient
	CatalogURL string
	HTTP       *http.Client
}

var idemKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,80}$`)

func (s *Service) Routes() *http.ServeMux {
	m := http.NewServeMux()
	m.Handle("POST /api/orders", platform.Auth(s.PublicKey, http.HandlerFunc(s.create)))
	m.Handle("GET /api/orders/{id}", platform.Auth(s.PublicKey, http.HandlerFunc(s.get)))
	m.Handle("GET /api/orders", platform.Auth(s.PublicKey, http.HandlerFunc(s.list)))
	m.Handle("GET /api/seller/orders", platform.Auth(s.PublicKey, platform.Seller(http.HandlerFunc(s.sellerOrders))))
	platform.Health(m, func(ctx context.Context) error {
		for _, db := range s.Shards {
			if e := db.Ping(ctx); e != nil {
				return e
			}
		}
		return nil
	})
	return m
}
func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []domain.Item `json:"items"`
	}
	if !platform.Decode(w, r, &body) {
		return
	}
	items, e := domain.Normalize(body.Items)
	if e != nil {
		platform.Error(w, 400, e.Error())
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if !idemKey.MatchString(key) {
		platform.Error(w, 400, "Idempotency-Key must contain 8..80 letters, digits, underscores or hyphens")
		return
	}
	user := platform.User(r.Context()).Subject
	order, replay, e := s.Create(r.Context(), user, key, items)
	if errors.Is(e, ErrConflict) {
		platform.Error(w, 409, e.Error())
		return
	}
	if e != nil {
		platform.Error(w, 503, "checkout unavailable; retry with the same Idempotency-Key")
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.Header().Set("Location", "/api/orders/"+order.ID)
	platform.JSON(w, 202, order)
}

var ErrConflict = errors.New("idempotency key reused with a different request")

func (s *Service) Create(ctx context.Context, user, key string, items []domain.Item) (domain.Order, bool, error) {
	items = append([]domain.Item(nil), items...)
	shard := domain.Shard(user)
	db := s.Shards[shard]
	var o domain.Order
	hash := domain.Hash(items)
	tx, e := db.Begin(ctx)
	if e != nil {
		return o, false, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,2))`, user+":"+key); e != nil {
		return o, false, e
	}
	var previous, id string
	e = tx.QueryRow(ctx, `SELECT request_hash,order_id FROM idempotency WHERE user_id=$1 AND key=$2`, user, key).Scan(&previous, &id)
	if e == nil {
		if previous != hash {
			return o, false, ErrConflict
		}
		o, e = read(ctx, tx, id, user)
		return o, true, e
	}
	if e != pgx.ErrNoRows {
		return o, false, e
	}
	for i := range items {
		quoteCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		req, _ := http.NewRequestWithContext(quoteCtx, "GET", s.CatalogURL+"/api/products/"+url.PathEscape(items[i].SKU), nil)
		resp, err := s.HTTP.Do(req)
		if err != nil {
			cancel()
			return o, false, err
		}
		var p domain.Product
		err = json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 16<<10)).Decode(&p)
		resp.Body.Close()
		cancel()
		if err != nil || resp.StatusCode != 200 || p.PriceCents <= 0 || p.PriceCents > 1_000_000_000 {
			return o, false, fmt.Errorf("product unavailable: %s", items[i].SKU)
		}
		items[i].PriceCents = p.PriceCents
		items[i].Name = p.Name
		items[i].SellerID = p.SellerID
		items[i].SellerName = p.SellerName
		items[i].ImageKey = p.ImageKey
		if p.Currency != "" && p.Currency != "RUB" {
			return o, false, errors.New("unsupported product currency")
		}
		o.TotalCents += p.PriceCents * int64(items[i].Quantity)
	}
	o.ID = fmt.Sprintf("%d-%s", shard, platform.ID())
	o.UserID = user
	o.Status = "pending"
	o.Items = items
	o.Version = 1
	o.Currency = "RUB"
	b, e := json.Marshal(items)
	if e != nil {
		return o, false, e
	}
	if e = tx.QueryRow(ctx, `INSERT INTO orders(id,user_id,status,items,total_cents,currency) VALUES($1,$2,$3,$4,$5,$6) RETURNING created_at,updated_at`, o.ID, user, o.Status, b, o.TotalCents, o.Currency).Scan(&o.CreatedAt, &o.UpdatedAt); e != nil {
		return o, false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO idempotency(user_id,key,request_hash,order_id) VALUES($1,$2,$3,$4)`, user, key, hash, o.ID); e != nil {
		return o, false, e
	}
	if e = platform.Enqueue(ctx, tx, platform.NewEvent("order.created", o.ID, user, 1, o)); e != nil {
		return o, false, e
	}
	return o, false, tx.Commit(ctx)
}

type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func read(ctx context.Context, db queryer, id, user string) (domain.Order, error) {
	var o domain.Order
	var raw []byte
	e := db.QueryRow(ctx, `SELECT id,user_id,status,items,total_cents,reason,version,currency,created_at,updated_at FROM orders WHERE id=$1 AND user_id=$2`, id, user).Scan(&o.ID, &o.UserID, &o.Status, &raw, &o.TotalCents, &o.Reason, &o.Version, &o.Currency, &o.CreatedAt, &o.UpdatedAt)
	if e != nil {
		return o, e
	}
	e = json.Unmarshal(raw, &o.Items)
	return o, e
}
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	u := platform.User(r.Context()).Subject
	o, e := read(r.Context(), s.Shards[domain.Shard(u)], r.PathValue("id"), u)
	if e == pgx.ErrNoRows {
		platform.Error(w, 404, "order not found")
		return
	}
	if e != nil {
		platform.Error(w, 503, "order read unavailable")
		return
	}
	platform.JSON(w, 200, o)
}
func (s *Service) list(w http.ResponseWriter, r *http.Request) {
	u := platform.User(r.Context()).Subject
	rows, e := s.Shards[domain.Shard(u)].Query(r.Context(), `SELECT id,user_id,status,items,total_cents,reason,version,currency,created_at,updated_at FROM orders WHERE user_id=$1 ORDER BY created_at DESC,id LIMIT 50`, u)
	if e != nil {
		platform.Error(w, 503, "order list unavailable")
		return
	}
	defer rows.Close()
	items := []domain.Order{}
	for rows.Next() {
		var o domain.Order
		var b []byte
		if e = rows.Scan(&o.ID, &o.UserID, &o.Status, &b, &o.TotalCents, &o.Reason, &o.Version, &o.Currency, &o.CreatedAt, &o.UpdatedAt); e != nil {
			platform.Error(w, 503, "order list unavailable")
			return
		}
		if e = json.Unmarshal(b, &o.Items); e != nil {
			platform.Error(w, 503, "order decode unavailable")
			return
		}
		items = append(items, o)
	}
	if rows.Err() != nil {
		platform.Error(w, 503, "order list unavailable")
		return
	}
	platform.JSON(w, 200, map[string]any{"items": items})
}
func (s *Service) Handle(ctx context.Context, event platform.Event) error {
	if event.Type != "order.created" {
		return nil
	}
	var input domain.Order
	if e := json.Unmarshal(event.Data, &input); e != nil {
		return e
	}
	if input.ID != event.AggregateID || input.UserID != event.UserID || input.Status != "pending" {
		return errors.New("invalid order event")
	}
	db := s.Shards[domain.Shard(input.UserID)]
	// Read canonical order from its owner, rather than trusting message prices/items.
	o, e := read(ctx, db, input.ID, input.UserID)
	if e != nil {
		return e
	}
	if o.Status != "pending" {
		return nil
	}
	request := &inventoryv1.ReserveRequest{OrderId: o.ID}
	for _, i := range o.Items {
		request.Items = append(request.Items, &inventoryv1.Item{Sku: i.SKU, Quantity: i.Quantity})
	}
	rpcCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	result, e := s.Inventory.Reserve(rpcCtx, request)
	cancel()
	if e != nil {
		return e
	}
	tx, e := db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `INSERT INTO inbox(event_id) VALUES($1) ON CONFLICT DO NOTHING`, event.ID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	state := "rejected"
	if result.Accepted {
		state = "confirmed"
	}
	e = tx.QueryRow(ctx, `UPDATE orders SET status=$2,reason=$3,version=version+1,updated_at=now() WHERE id=$1 AND status='pending' RETURNING version,updated_at`, o.ID, state, result.Reason).Scan(&o.Version, &o.UpdatedAt)
	if e == pgx.ErrNoRows {
		return tx.Commit(ctx)
	}
	if e != nil {
		return e
	}
	o.Status = state
	o.Reason = result.Reason
	if e = platform.Enqueue(ctx, tx, platform.NewEvent("order."+state, o.ID, o.UserID, o.Version, o)); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Service) sellerOrders(w http.ResponseWriter, r *http.Request) {
	seller := platform.User(r.Context()).Subject
	filter, _ := json.Marshal([]map[string]string{{"seller_id": seller}})
	result := []domain.SellerOrder{}
	for _, db := range s.Shards {
		rows, e := db.Query(r.Context(), `SELECT id,status,items,currency,created_at,version FROM orders WHERE items @> $1::jsonb ORDER BY created_at DESC,id LIMIT 50`, filter)
		if e != nil {
			platform.Error(w, 503, "seller orders unavailable")
			return
		}
		for rows.Next() {
			var o domain.Order
			var b []byte
			if e = rows.Scan(&o.ID, &o.Status, &b, &o.Currency, &o.CreatedAt, &o.Version); e != nil {
				rows.Close()
				platform.Error(w, 503, "seller orders unavailable")
				return
			}
			if e = json.Unmarshal(b, &o.Items); e != nil {
				rows.Close()
				platform.Error(w, 503, "seller order decoding failed")
				return
			}
			result = append(result, domain.ForSeller(o, seller))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			platform.Error(w, 503, "seller orders unavailable")
			return
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].ID > result[j].ID
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})
	if len(result) > 50 {
		result = result[:50]
	}
	w.Header().Set("Cache-Control", "no-store")
	platform.JSON(w, 200, map[string]any{"items": result})
}
