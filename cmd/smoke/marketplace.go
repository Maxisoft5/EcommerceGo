package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"flashcart/internal/domain"
	"flashcart/internal/platform"
	"github.com/coder/websocket"
)

func marketplaceCheck(public *client, httpClient *http.Client) {
	suffix := platform.ID()[:12]
	password := "Marketplace-" + platform.ID()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	wsURL := strings.Replace(public.base, "https://", "wss://", 1)
	live, _, e := websocket.Dial(ctx, wsURL+"/ws/catalog", &websocket.DialOptions{HTTPClient: httpClient})
	must(e)
	defer live.Close(websocket.StatusNormalClosure, "check complete")
	account := func(label, role, shop string) *client {
		c := &client{base: public.base, http: httpClient}
		email := label + "-" + suffix + "@example.com"
		must(c.request("POST", "/api/auth/register", map[string]string{"email": email, "password": password, "role": role, "shop_name": shop}, "", 201, nil))
		_, err := login(c, email, password)
		must(err)
		var me map[string]string
		must(c.request("GET", "/api/auth/me", nil, "", 200, &me))
		if me["role"] != role {
			panic("account role mismatch")
		}
		return c
	}
	sellerA := account("mono", "seller", "MONO")
	sellerB := account("forma", "seller", "Светлая форма")
	buyers := []*client{account("buyer-a", "customer", ""), account("buyer-b", "customer", ""), account("buyer-c", "customer", "")}
	body := map[string]any{"name": "Наушники SONO, песочные", "description": "Мягкие беспроводные наушники с зарядкой USB-C.", "price_cents": 459000, "old_price_cents": 599000, "category": "electronics", "image_key": "headphones", "initial_stock": 2}
	var product, repeat domain.Product
	key := "product-" + suffix
	must(sellerA.request("POST", "/api/seller/products", body, key, 201, &product))
	must(sellerA.request("POST", "/api/seller/products", body, key, 201, &repeat))
	if repeat.SKU != product.SKU {
		panic("product replay created another SKU")
	}
	expectStock := func(sku string, want int32) {
		deadline := time.Now().Add(20 * time.Second)
		for {
			var response struct {
				Items []domain.Stock `json:"items"`
			}
			must(public.request("GET", "/api/stock?skus="+sku, nil, "", 200, &response))
			if len(response.Items) == 1 && response.Items[0].Available == want {
				return
			}
			if time.Now().After(deadline) {
				panic(fmt.Sprintf("stock %s did not reach %d", sku, want))
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	expectPublic := func(sku string, want int32) {
		for {
			_, b, err := live.Read(ctx)
			must(err)
			var event platform.Event
			must(json.Unmarshal(b, &event))
			if strings.HasPrefix(event.Type, "order.") || strings.HasPrefix(event.Type, "seller.") {
				panic("private order leaked to public catalog")
			}
			if event.Type == "stock.updated" && event.AggregateID == sku {
				var stock domain.Stock
				must(json.Unmarshal(event.Data, &stock))
				if stock.Available == want {
					return
				}
			}
		}
	}
	expectStock(product.SKU, 2)
	expectPublic(product.SKU, 2)
	unauthorizedUpdate := map[string]any{"name": "Чужой товар", "price_cents": 10000, "category": "electronics", "image_key": "headphones"}
	must(sellerB.request("PUT", "/api/seller/products/"+product.SKU, unauthorizedUpdate, "", 404, nil))
	must(buyers[0].request("POST", "/api/seller/products", body, "customer-"+suffix, 403, nil))
	type result struct {
		order domain.Order
		err   error
	}
	results := make(chan result, 3)
	for i, buyer := range buyers {
		go func(i int, buyer *client) {
			var o domain.Order
			err := buyer.request("POST", "/api/orders", map[string]any{"items": []domain.Item{{SKU: product.SKU, Quantity: 1}}}, fmt.Sprintf("race-%s-%d", suffix, i), 202, &o)
			results <- result{o, err}
		}(i, buyer)
	}
	terminal := func(buyer *client, id string) domain.Order {
		deadline := time.Now().Add(25 * time.Second)
		for {
			var o domain.Order
			must(buyer.request("GET", "/api/orders/"+id, nil, "", 200, &o))
			if o.Status != "pending" {
				return o
			}
			if time.Now().After(deadline) {
				panic("order did not reach terminal state")
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	ordersByUser := make(map[string]domain.Order)
	for i := 0; i < 3; i++ {
		r := <-results
		must(r.err)
		ordersByUser[r.order.UserID] = r.order
	}
	confirmed, rejected := 0, 0
	for _, buyer := range buyers {
		var own struct {
			Items []domain.Order `json:"items"`
		}
		must(buyer.request("GET", "/api/orders", nil, "", 200, &own))
		if len(own.Items) != 1 {
			panic("unexpected buyer order count")
		}
		o := terminal(buyer, own.Items[0].ID)
		if o.Status == "confirmed" {
			confirmed++
		} else if o.Status == "rejected" {
			rejected++
		}
		if len(o.Items) != 1 || o.Items[0].SellerID != product.SellerID || o.Currency != "RUB" {
			panic("seller/price snapshot missing")
		}
	}
	if confirmed != 2 || rejected != 1 {
		panic(fmt.Sprintf("limited inventory violated: confirmed=%d rejected=%d", confirmed, rejected))
	}
	expectStock(product.SKU, 0)
	expectPublic(product.SKU, 0)
	var stock domain.Stock
	adjustKey := "restock-" + suffix
	adjustment := map[string]int{"delta": 3}
	must(sellerA.request("POST", "/api/seller/stock/"+product.SKU+"/adjustments", adjustment, adjustKey, 200, &stock))
	must(sellerA.request("POST", "/api/seller/stock/"+product.SKU+"/adjustments", adjustment, adjustKey, 200, &stock))
	if stock.Available != 3 {
		panic("restock duplicated")
	}
	must(sellerA.request("POST", "/api/seller/stock/"+product.SKU+"/adjustments", map[string]int{"delta": 4}, adjustKey, 409, nil))
	must(sellerB.request("POST", "/api/seller/stock/"+product.SKU+"/adjustments", adjustment, "foreign-"+suffix, 404, nil))
	expectStock(product.SKU, 3)
	expectPublic(product.SKU, 3)
	var other domain.Product
	must(sellerB.request("POST", "/api/seller/products", map[string]any{"name": "Камера SNAP, молочная", "description": "Моментальные снимки для ваших историй.", "price_cents": 1199000, "category": "electronics", "image_key": "camera", "initial_stock": 1}, "other-product-"+suffix, 201, &other))
	expectStock(other.SKU, 1)
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	must(sellerA.request("POST", "/api/ws-ticket", map[string]any{}, "", 201, &ticket))
	private, _, e := websocket.Dial(ctx, wsURL+"/ws?ticket="+ticket.Ticket, &websocket.DialOptions{HTTPClient: httpClient})
	must(e)
	defer private.Close(websocket.StatusNormalClosure, "check complete")
	var mixed domain.Order
	must(buyers[0].request("POST", "/api/orders", map[string]any{"items": []domain.Item{{SKU: product.SKU, Quantity: 1}, {SKU: other.SKU, Quantity: 1}}}, "mixed-"+suffix, 202, &mixed))
	mixed = terminal(buyers[0], mixed.ID)
	if mixed.Status != "confirmed" {
		panic("mixed seller order failed")
	}
	for _, tc := range []struct {
		seller *client
		sku    string
		amount int64
	}{{sellerA, product.SKU, product.PriceCents}, {sellerB, other.SKU, other.PriceCents}} {
		var list struct {
			Items []domain.SellerOrder `json:"items"`
		}
		must(tc.seller.request("GET", "/api/seller/orders", nil, "", 200, &list))
		found := false
		for _, sale := range list.Items {
			if sale.ID == mixed.ID {
				found = true
				if len(sale.Items) != 1 || sale.Items[0].SKU != tc.sku || sale.TotalCents != tc.amount {
					panic("seller saw another store's items")
				}
			}
		}
		if !found {
			panic("seller sale missing")
		}
	}
	for {
		_, b, err := private.Read(ctx)
		must(err)
		var event platform.Event
		must(json.Unmarshal(b, &event))
		if event.AggregateID == mixed.ID && event.Type == "seller.order.updated" && event.Version == 2 {
			var payload map[string]json.RawMessage
			must(json.Unmarshal(event.Data, &payload))
			if _, leaked := payload["user_id"]; leaked {
				panic("seller notification leaked buyer ID")
			}
			var sale domain.SellerOrder
			must(json.Unmarshal(event.Data, &sale))
			if len(sale.Items) != 1 || sale.Items[0].SKU != product.SKU {
				panic("seller notification leaked another store")
			}
			break
		}
	}
	expectStock(product.SKU, 2)
	expectStock(other.SKU, 0)
	fmt.Println("PASS: 2 seller accounts, 3 buyers, idempotent listing and restock, exactly 2 of 3 purchases accepted for stock=2, public WSS stock updates, mixed-seller order isolation, private seller WSS. Limited SKU:", product.SKU)
}
