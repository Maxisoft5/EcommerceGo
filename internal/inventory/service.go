package inventory

import (
	"context"
	"crypto/ed25519"
	"encoding/json"

	inventoryv1 "flashcart/api/gen/inventory/v1"
	"flashcart/internal/domain"
	"flashcart/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Service struct {
	inventoryv1.UnimplementedInventoryServiceServer
	DB        *pgxpool.Pool
	PublicKey ed25519.PublicKey
}

func (s *Service) Reserve(ctx context.Context, r *inventoryv1.ReserveRequest) (*inventoryv1.ReserveResponse, error) {
	if len(r.OrderId) < 3 || len(r.OrderId) > 80 {
		return nil, status.Error(codes.InvalidArgument, "invalid order id")
	}
	items := make([]domain.Item, 0, len(r.Items))
	for _, i := range r.Items {
		if i == nil {
			return nil, status.Error(codes.InvalidArgument, "nil item")
		}
		items = append(items, domain.Item{SKU: i.Sku, Quantity: i.Quantity})
	}
	items, e := domain.Normalize(items)
	if e != nil {
		return nil, status.Error(codes.InvalidArgument, e.Error())
	}
	hash := domain.Hash(items)
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, status.Error(codes.Unavailable, "inventory database unavailable")
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,1))`, r.OrderId); e != nil {
		return nil, status.Error(codes.Unavailable, "reservation lock failed")
	}
	var previous string
	result := &inventoryv1.ReserveResponse{}
	e = tx.QueryRow(ctx, `SELECT request_hash,accepted,reason FROM reservations WHERE order_id=$1`, r.OrderId).Scan(&previous, &result.Accepted, &result.Reason)
	if e == nil {
		if hash != previous {
			return nil, status.Error(codes.AlreadyExists, "order id reused with different items")
		}
		return result, nil
	}
	if e != pgx.ErrNoRows {
		return nil, status.Error(codes.Unavailable, "reservation read failed")
	}
	result.Accepted = true
	// Lock every SKU in deterministic order before modifying any row.
	for _, i := range items {
		var available int32
		e = tx.QueryRow(ctx, `SELECT available FROM stock WHERE sku=$1 FOR UPDATE`, i.SKU).Scan(&available)
		if e == pgx.ErrNoRows {
			result.Accepted = false
			result.Reason = "unknown SKU"
			continue
		}
		if e != nil {
			return nil, status.Error(codes.Unavailable, "stock lock failed")
		}
		if available < i.Quantity {
			result.Accepted = false
			result.Reason = "insufficient stock"
		}
	}
	if result.Accepted {
		for _, i := range items {
			var stock domain.Stock
			if e = tx.QueryRow(ctx, `UPDATE stock SET available=available-$2,version=version+1 WHERE sku=$1 RETURNING sku,available,version`, i.SKU, i.Quantity).Scan(&stock.SKU, &stock.Available, &stock.Version); e != nil {
				return nil, status.Error(codes.Unavailable, "stock update failed")
			}
			if e = platform.Enqueue(ctx, tx, platform.NewEvent("stock.updated", stock.SKU, "", stock.Version, stock)); e != nil {
				return nil, status.Error(codes.Unavailable, "stock event write failed")
			}
		}
	}
	if _, e = tx.Exec(ctx, `INSERT INTO reservations(order_id,request_hash,accepted,reason) VALUES($1,$2,$3,$4)`, r.OrderId, hash, result.Accepted, result.Reason); e != nil {
		return nil, status.Error(codes.Unavailable, "reservation write failed")
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, status.Error(codes.Unavailable, "reservation commit uncertain; retry same order")
	}
	return result, nil
}

// Every product event carries its immutable initial allocation. Replays never refill an existing SKU.
func (s *Service) HandleProduct(ctx context.Context, event platform.Event) error {
	if event.Type != "product.updated" {
		return nil
	}
	var p domain.Product
	if e := json.Unmarshal(event.Data, &p); e != nil {
		return e
	}
	if p.SellerID == "" {
		return nil
	}
	if !domain.SKU.MatchString(p.SKU) || p.InitialStock < 0 || p.InitialStock > 1_000_000 || p.SKU != event.AggregateID {
		return status.Error(codes.InvalidArgument, "invalid product allocation")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `INSERT INTO stock(sku,available,seller_id) VALUES($1,$2,$3) ON CONFLICT(sku) DO NOTHING`, p.SKU, p.InitialStock, p.SellerID)
	if e != nil {
		return e
	}
	if tag.RowsAffected() > 0 {
		stock := domain.Stock{SKU: p.SKU, Available: p.InitialStock, Version: 1}
		if e = platform.Enqueue(ctx, tx, platform.NewEvent("stock.updated", p.SKU, "", 1, stock)); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
