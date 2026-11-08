package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"regexp"
	"sort"
	"time"
)

var SKU = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

type Item struct {
	SKU        string `json:"sku"`
	Quantity   int32  `json:"quantity"`
	PriceCents int64  `json:"price_cents,omitempty"`
	Name       string `json:"name,omitempty"`
	SellerID   string `json:"seller_id,omitempty"`
	SellerName string `json:"seller_name,omitempty"`
	ImageKey   string `json:"image_key,omitempty"`
}
type Order struct {
	ID         string    `json:"id"`
	UserID     string    `json:"user_id"`
	Status     string    `json:"status"`
	Items      []Item    `json:"items"`
	TotalCents int64     `json:"total_cents"`
	Reason     string    `json:"reason"`
	Version    int64     `json:"version"`
	Currency   string    `json:"currency"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type Product struct {
	SKU                string `json:"sku"`
	Name               string `json:"name"`
	PriceCents         int64  `json:"price_cents"`
	Version            int64  `json:"version"`
	SellerID           string `json:"seller_id"`
	SellerName         string `json:"seller_name"`
	Description        string `json:"description"`
	Category           string `json:"category"`
	ImageKey           string `json:"image_key"`
	OldPriceCents      int64  `json:"old_price_cents"`
	InitialStock       int32  `json:"initial_stock"`
	Currency           string `json:"currency"`
	FixtureInitialized bool   `json:"-"`
}

type Stock struct {
	SKU       string `json:"sku"`
	Available int32  `json:"available"`
	Version   int64  `json:"version"`
}
type SellerOrder struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`
	Items      []Item    `json:"items"`
	TotalCents int64     `json:"total_cents"`
	Currency   string    `json:"currency"`
	CreatedAt  time.Time `json:"created_at"`
	Version    int64     `json:"version"`
}

func ForSeller(o Order, seller string) SellerOrder {
	v := SellerOrder{ID: o.ID, Status: o.Status, Items: []Item{}, Currency: o.Currency, CreatedAt: o.CreatedAt, Version: o.Version}
	for _, item := range o.Items {
		if item.SellerID == seller {
			v.Items = append(v.Items, item)
			v.TotalCents += item.PriceCents * int64(item.Quantity)
		}
	}
	return v
}
func ImageAllowed(key string) bool {
	switch key {
	case "headphones", "keyboard", "monitor", "backpack", "lamp", "sneakers", "kettle", "watch", "camera":
		return true
	}
	return false
}
func CategoryAllowed(category string) bool {
	switch category {
	case "electronics", "home", "fashion", "sport":
		return true
	}
	return false
}

func Normalize(items []Item) ([]Item, error) {
	if len(items) == 0 || len(items) > 20 {
		return nil, errors.New("1..20 unique items required")
	}
	copyItems := append([]Item(nil), items...)
	sort.Slice(copyItems, func(i, j int) bool { return copyItems[i].SKU < copyItems[j].SKU })
	for i, v := range copyItems {
		if !SKU.MatchString(v.SKU) || v.Quantity < 1 || v.Quantity > 100 || v.PriceCents != 0 || v.Name != "" || v.SellerID != "" || v.SellerName != "" || v.ImageKey != "" {
			return nil, errors.New("invalid sku, quantity or client supplied price")
		}
		if i > 0 && copyItems[i-1].SKU == v.SKU {
			return nil, errors.New("duplicate sku")
		}
	}
	return copyItems, nil
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Fixed topology v1: changing shard count requires a migration, never a config-only change.
func Shard(user string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(user))
	return int(h.Sum32() % 2)
}
