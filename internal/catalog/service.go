package catalog

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"flashcart/internal/domain"
	"flashcart/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Service struct {
	DB        *pgxpool.Pool
	Cache     *redis.Client
	PublicKey ed25519.PublicKey
}

const columns = `sku,name,price_cents,version,seller_id,seller_name,description,category,image_key,old_price_cents,initial_stock,currency,fixture_initialized`

var requestKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,80}$`)

func scan(row pgx.Row) (domain.Product, error) {
	var p domain.Product
	e := row.Scan(&p.SKU, &p.Name, &p.PriceCents, &p.Version, &p.SellerID, &p.SellerName, &p.Description, &p.Category, &p.ImageKey, &p.OldPriceCents, &p.InitialStock, &p.Currency, &p.FixtureInitialized)
	return p, e
}
func (s *Service) Routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/products", s.list)
	m.HandleFunc("GET /api/products/{sku}", s.get)
	m.Handle("PUT /api/products/{sku}", platform.Auth(s.PublicKey, platform.Admin(http.HandlerFunc(s.update))))
	protected := func(h http.HandlerFunc) http.Handler { return platform.Auth(s.PublicKey, platform.Seller(h)) }
	m.Handle("GET /api/seller/products", protected(s.mine))
	m.Handle("POST /api/seller/products", protected(s.create))
	m.Handle("PUT /api/seller/products/{sku}", protected(s.update))
	platform.Health(m, s.DB.Ping)
	return m
}
func (s *Service) get(w http.ResponseWriter, r *http.Request) {
	sku := r.PathValue("sku")
	if !domain.SKU.MatchString(sku) {
		platform.Error(w, 400, "invalid SKU")
		return
	}
	key := "product:v2:" + sku
	if s.Cache != nil {
		if raw, e := s.Cache.Get(r.Context(), key).Bytes(); e == nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(raw)
			return
		}
	}
	p, e := scan(s.DB.QueryRow(r.Context(), `SELECT `+columns+` FROM products WHERE sku=$1`, sku))
	if e == pgx.ErrNoRows {
		platform.Error(w, 404, "product not found")
		return
	}
	if e != nil {
		platform.Error(w, 503, "catalog unavailable")
		return
	}
	if s.Cache != nil {
		b, _ := json.Marshal(p)
		_ = s.Cache.Set(r.Context(), key, b, 5*time.Second).Err()
	}
	platform.JSON(w, 200, p)
}
func (s *Service) list(w http.ResponseWriter, r *http.Request) { s.products(w, r, "") }
func (s *Service) mine(w http.ResponseWriter, r *http.Request) {
	s.products(w, r, platform.User(r.Context()).Subject)
}
func (s *Service) products(w http.ResponseWriter, r *http.Request, owner string) {
	rows, e := s.DB.Query(r.Context(), `SELECT `+columns+` FROM products WHERE ($1='' OR seller_id=$1) ORDER BY updated_at DESC,sku LIMIT 100`, owner)
	if e != nil {
		platform.Error(w, 503, "catalog unavailable")
		return
	}
	defer rows.Close()
	products := []domain.Product{}
	for rows.Next() {
		p, e := scan(rows)
		if e != nil {
			platform.Error(w, 503, "catalog unavailable")
			return
		}
		products = append(products, p)
	}
	if rows.Err() != nil {
		platform.Error(w, 503, "catalog unavailable")
		return
	}
	platform.JSON(w, 200, map[string]any{"items": products})
}

type productInput struct {
	Name          string `json:"name"`
	PriceCents    int64  `json:"price_cents"`
	Description   string `json:"description"`
	Category      string `json:"category"`
	ImageKey      string `json:"image_key"`
	OldPriceCents int64  `json:"old_price_cents"`
	InitialStock  int32  `json:"initial_stock"`
}

func validate(in *productInput, create bool) bool {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	if in.Category == "" {
		in.Category = "electronics"
	}
	if in.ImageKey == "" {
		in.ImageKey = "headphones"
	}
	return utf8.ValidString(in.Name) && utf8.ValidString(in.Description) && len(in.Name) > 0 && len(in.Name) <= 200 && len(in.Description) <= 4000 && in.PriceCents >= 1 && in.PriceCents <= 1_000_000_000 && (in.OldPriceCents == 0 || in.OldPriceCents >= in.PriceCents) && in.OldPriceCents <= 1_000_000_000 && in.InitialStock >= 0 && in.InitialStock <= 1_000_000 && (create || in.InitialStock == 0) && domain.CategoryAllowed(in.Category) && domain.ImageAllowed(in.ImageKey)
}
func (s *Service) create(w http.ResponseWriter, r *http.Request) {
	var in productInput
	if !platform.Decode(w, r, &in) {
		return
	}
	if !validate(&in, true) {
		platform.Error(w, 400, "invalid product details or initial stock")
		return
	}
	u := platform.User(r.Context())
	key := r.Header.Get("Idempotency-Key")
	if !requestKey.MatchString(key) {
		platform.Error(w, 400, "valid Idempotency-Key required")
		return
	}
	shop := u.ShopName
	if shop == "" {
		shop = "Место · Студия"
	}
	p := domain.Product{SKU: "sku-" + platform.ID()[:16], Name: in.Name, PriceCents: in.PriceCents, Version: 1, SellerID: u.Subject, SellerName: shop, Description: in.Description, Category: in.Category, ImageKey: in.ImageKey, OldPriceCents: in.OldPriceCents, InitialStock: in.InitialStock, Currency: "RUB"}
	tx, e := s.DB.Begin(r.Context())
	if e != nil {
		platform.Error(w, 503, "catalog write unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	ctx := r.Context()
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,4))`, u.Subject+":"+key); e != nil {
		platform.Error(w, 503, "catalog write unavailable")
		return
	}
	var previous, sku string
	e = tx.QueryRow(ctx, `SELECT payload_hash,sku FROM product_requests WHERE seller_id=$1 AND request_key=$2`, u.Subject, key).Scan(&previous, &sku)
	if e == nil {
		if previous != domain.Hash(in) {
			platform.Error(w, 409, "product key reused with different details")
			return
		}
		existing, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM products WHERE sku=$1`, sku))
		if err != nil {
			platform.Error(w, 503, "catalog read unavailable")
			return
		}
		w.Header().Set("Idempotency-Replayed", "true")
		platform.JSON(w, 201, existing)
		return
	}
	if e != pgx.ErrNoRows {
		platform.Error(w, 503, "catalog write unavailable")
		return
	}
	if e = insert(r.Context(), tx, p); e == nil {
		e = platform.Enqueue(r.Context(), tx, platform.NewEvent("product.updated", p.SKU, "", p.Version, p))
	}
	if e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO product_requests(seller_id,request_key,payload_hash,sku) VALUES($1,$2,$3,$4)`, u.Subject, key, domain.Hash(in), p.SKU)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		platform.Error(w, 503, "catalog write unavailable")
		return
	}
	platform.JSON(w, 201, p)
}
func insert(ctx context.Context, tx pgx.Tx, p domain.Product) error {
	_, e := tx.Exec(ctx, `INSERT INTO products(sku,name,price_cents,version,seller_id,seller_name,description,category,image_key,old_price_cents,initial_stock,currency,fixture_initialized) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,true)`, p.SKU, p.Name, p.PriceCents, p.Version, p.SellerID, p.SellerName, p.Description, p.Category, p.ImageKey, p.OldPriceCents, p.InitialStock, p.Currency)
	return e
}
func (s *Service) update(w http.ResponseWriter, r *http.Request) {
	var in productInput
	if !platform.Decode(w, r, &in) {
		return
	}
	if !validate(&in, false) || !domain.SKU.MatchString(r.PathValue("sku")) {
		platform.Error(w, 400, "invalid product details; adjust stock separately")
		return
	}
	p, e := s.Update(r.Context(), platform.User(r.Context()), r.PathValue("sku"), in)
	if errors.Is(e, pgx.ErrNoRows) {
		platform.Error(w, 404, "product not found")
		return
	}
	if e != nil {
		platform.Error(w, 503, "catalog write unavailable")
		return
	}
	if s.Cache != nil {
		_ = s.Cache.Del(r.Context(), "product:v2:"+p.SKU).Err()
	}
	platform.JSON(w, 200, p)
}
func (s *Service) Update(ctx context.Context, user *platform.Claims, sku string, in productInput) (domain.Product, error) {
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return domain.Product{}, e
	}
	defer tx.Rollback(ctx)
	p, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM products WHERE sku=$1 AND (seller_id=$2 OR $3='admin') FOR UPDATE`, sku, user.Subject, user.Role))
	if e != nil {
		return p, e
	}
	p.Name = in.Name
	p.PriceCents = in.PriceCents
	p.Description = in.Description
	p.Category = in.Category
	p.ImageKey = in.ImageKey
	p.OldPriceCents = in.OldPriceCents
	p.Version++
	_, e = tx.Exec(ctx, `UPDATE products SET name=$2,price_cents=$3,description=$4,category=$5,image_key=$6,old_price_cents=$7,version=$8,updated_at=now() WHERE sku=$1`, sku, p.Name, p.PriceCents, p.Description, p.Category, p.ImageKey, p.OldPriceCents, p.Version)
	if e == nil {
		e = platform.Enqueue(ctx, tx, platform.NewEvent("product.updated", sku, "", p.Version, p))
	}
	if e == nil {
		e = tx.Commit(ctx)
	}
	return p, e
}

// Fixture metadata is initialized once. Seller edits and historical stock are preserved on restart.
func (s *Service) Seed(ctx context.Context) error {
	for _, p := range fixtures() {
		tx, e := s.DB.Begin(ctx)
		if e != nil {
			return e
		}
		current, e := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM products WHERE sku=$1 FOR UPDATE`, p.SKU))
		if e == pgx.ErrNoRows {
			e = insert(ctx, tx, p)
		} else if e == nil && !current.FixtureInitialized {
			p.Version = current.Version + 1
			_, e = tx.Exec(ctx, `UPDATE products SET name=$2,price_cents=$3,description=$4,category=$5,image_key=$6,old_price_cents=$7,initial_stock=$8,seller_id=$9,seller_name=$10,version=$11,updated_at=now(),fixture_initialized=true WHERE sku=$1`, p.SKU, p.Name, p.PriceCents, p.Description, p.Category, p.ImageKey, p.OldPriceCents, p.InitialStock, p.SellerID, p.SellerName, p.Version)
		} else if e == nil {
			tx.Rollback(ctx)
			continue
		}
		if e == nil {
			e = platform.Enqueue(ctx, tx, platform.NewEvent("product.updated", p.SKU, "", p.Version, p))
		}
		if e == nil {
			e = tx.Commit(ctx)
		}
		if e != nil {
			tx.Rollback(ctx)
			return e
		}
	}
	return nil
}
