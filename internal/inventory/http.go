package inventory

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"flashcart/internal/domain"
	"flashcart/internal/platform"
	"github.com/jackc/pgx/v5"
)

var adjustmentKey = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,80}$`)
var ErrConflict = errors.New("adjustment key reused with another request")
var ErrStockRange = errors.New("stock must remain between zero and one million")

func (s *Service) Routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/stock", s.snapshot)
	m.Handle("POST /api/seller/stock/{sku}/adjustments", platform.Auth(s.PublicKey, platform.Seller(http.HandlerFunc(s.adjust))))
	platform.Health(m, s.DB.Ping)
	return m
}
func (s *Service) snapshot(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("skus")
	if len(raw) > 6500 {
		platform.Error(w, 400, "too many SKUs")
		return
	}
	skus := []string{}
	if raw != "" {
		skus = strings.Split(raw, ",")
		if len(skus) > 100 {
			platform.Error(w, 400, "at most 100 SKUs")
			return
		}
		for _, sku := range skus {
			if !domain.SKU.MatchString(sku) {
				platform.Error(w, 400, "invalid SKU")
				return
			}
		}
	}
	rows, e := s.DB.Query(r.Context(), `SELECT sku,available,version FROM stock WHERE (cardinality($1::text[])=0 OR sku=ANY($1)) ORDER BY sku LIMIT 100`, skus)
	if e != nil {
		platform.Error(w, 503, "stock unavailable")
		return
	}
	defer rows.Close()
	items := []domain.Stock{}
	for rows.Next() {
		var stock domain.Stock
		if e = rows.Scan(&stock.SKU, &stock.Available, &stock.Version); e != nil {
			platform.Error(w, 503, "stock unavailable")
			return
		}
		items = append(items, stock)
	}
	if rows.Err() != nil {
		platform.Error(w, 503, "stock unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	platform.JSON(w, 200, map[string]any{"items": items})
}
func (s *Service) adjust(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Delta int32 `json:"delta"`
	}
	if !platform.Decode(w, r, &body) {
		return
	}
	sku := r.PathValue("sku")
	key := r.Header.Get("Idempotency-Key")
	if !domain.SKU.MatchString(sku) || !adjustmentKey.MatchString(key) || body.Delta == 0 || body.Delta < -100000 || body.Delta > 100000 {
		platform.Error(w, 400, "valid SKU, adjustment key and nonzero delta within 100000 required")
		return
	}
	stock, replay, e := s.Adjust(r.Context(), platform.User(r.Context()).Subject, key, sku, body.Delta)
	switch {
	case errors.Is(e, pgx.ErrNoRows):
		platform.Error(w, 404, "product stock not found; initial allocation may still be processing")
		return
	case errors.Is(e, ErrConflict):
		platform.Error(w, 409, e.Error())
		return
	case errors.Is(e, ErrStockRange):
		platform.Error(w, 409, e.Error())
		return
	case e != nil:
		platform.Error(w, 503, "stock adjustment unavailable; retry the same key")
		return
	}
	if replay {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	platform.JSON(w, 200, stock)
}

// Additive adjustments serialize with Reserve on the SAME stock row, avoiding lost sales/restocks.
func (s *Service) Adjust(ctx context.Context, seller, key, sku string, delta int32) (domain.Stock, bool, error) {
	var stock domain.Stock
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return stock, false, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,3))`, seller+":"+key); e != nil {
		return stock, false, e
	}
	var previousSKU string
	var previousDelta int32
	e = tx.QueryRow(ctx, `SELECT sku,delta,available_after,version_after FROM stock_adjustments WHERE seller_id=$1 AND request_key=$2`, seller, key).Scan(&previousSKU, &previousDelta, &stock.Available, &stock.Version)
	if e == nil {
		stock.SKU = previousSKU
		if previousSKU != sku || previousDelta != delta {
			return stock, false, ErrConflict
		}
		return stock, true, nil
	}
	if e != pgx.ErrNoRows {
		return stock, false, e
	}
	e = tx.QueryRow(ctx, `SELECT sku,available,version FROM stock WHERE sku=$1 AND seller_id=$2 FOR UPDATE`, sku, seller).Scan(&stock.SKU, &stock.Available, &stock.Version)
	if e != nil {
		return stock, false, e
	}
	next := int64(stock.Available) + int64(delta)
	if next < 0 || next > 1_000_000 {
		return stock, false, ErrStockRange
	}
	e = tx.QueryRow(ctx, `UPDATE stock SET available=available+$2,version=version+1 WHERE sku=$1 RETURNING available,version`, sku, delta).Scan(&stock.Available, &stock.Version)
	if e != nil {
		return stock, false, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO stock_adjustments(seller_id,request_key,sku,delta,available_after,version_after) VALUES($1,$2,$3,$4,$5,$6)`, seller, key, sku, delta, stock.Available, stock.Version); e != nil {
		return stock, false, e
	}
	if e = platform.Enqueue(ctx, tx, platform.NewEvent("stock.updated", sku, "", stock.Version, stock)); e != nil {
		return stock, false, e
	}
	return stock, false, tx.Commit(ctx)
}
