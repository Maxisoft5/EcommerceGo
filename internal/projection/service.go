package projection

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"flashcart/internal/domain"
	"flashcart/internal/platform"
	"github.com/redis/go-redis/v9"
)

type Service struct {
	SearchURL, ClickURL, ClickUser, ClickPassword string
	HTTP                                          *http.Client
	Redis                                         *redis.Client
	PublicKey                                     ed25519.PublicKey
}

func (s *Service) search(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	req, e := http.NewRequestWithContext(ctx, method, s.SearchURL+path, bytes.NewReader(body))
	if e != nil {
		return nil, 0, e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := s.HTTP.Do(req)
	if e != nil {
		return nil, 0, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	return b, resp.StatusCode, e
}
func (s *Service) click(ctx context.Context, query string, body []byte) ([]byte, error) {
	u := s.ClickURL + "/?database=commerce&query=" + url.QueryEscape(query)
	req, e := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	req.SetBasicAuth(s.ClickUser, s.ClickPassword)
	resp, e := s.HTTP.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return nil, e
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("clickhouse HTTP %d: %.300s", resp.StatusCode, b)
	}
	return b, nil
}
func (s *Service) Init(ctx context.Context) error {
	mapping := []byte(`{"settings":{"number_of_shards":3,"number_of_replicas":0},"mappings":{"dynamic":"strict","properties":{"sku":{"type":"keyword"},"name":{"type":"text"},"price_cents":{"type":"long"},"version":{"type":"long"},"seller_id":{"type":"keyword"},"seller_name":{"type":"text"},"description":{"type":"text"},"category":{"type":"keyword"},"image_key":{"type":"keyword"},"old_price_cents":{"type":"long"},"initial_stock":{"type":"integer"},"currency":{"type":"keyword"}}}}`)
	b, status, e := s.search(ctx, "PUT", "/products-v2", mapping)
	if e != nil {
		return e
	}
	if status != 200 && !(status == 400 && bytes.Contains(b, []byte("resource_already_exists_exception"))) {
		return fmt.Errorf("search init HTTP %d", status)
	}
	_, e = s.click(ctx, `CREATE TABLE IF NOT EXISTS order_events (event_id String, order_id String, event_type LowCardinality(String), amount_cents Int64, at DateTime64(3, 'UTC'), version UInt64) ENGINE=ReplacingMergeTree(version) PARTITION BY toYYYYMM(at) ORDER BY (event_id)`, nil)
	if e != nil {
		return e
	}
	_, e = s.click(ctx, `ALTER TABLE order_events ADD COLUMN IF NOT EXISTS currency LowCardinality(String) DEFAULT 'USD'`, nil)
	return e
}
func (s *Service) SearchEvent(ctx context.Context, e platform.Event) error {
	if e.Type != "product.updated" {
		return nil
	}
	var p domain.Product
	if err := json.Unmarshal(e.Data, &p); err != nil {
		return err
	}
	if p.SKU != e.AggregateID || p.Version != e.Version {
		return fmt.Errorf("invalid product event")
	}
	b, _ := json.Marshal(p)
	_, status, err := s.search(ctx, "PUT", "/products-v2/_doc/"+url.PathEscape(p.SKU)+"?version_type=external_gte&version="+fmt.Sprint(p.Version), b)
	if err != nil {
		return err
	}
	if status == 409 {
		return nil
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("search index HTTP %d", status)
	}
	return nil
}
func (s *Service) AnalyticsEvent(ctx context.Context, e platform.Event) error {
	if !strings.HasPrefix(e.Type, "order.") {
		return nil
	}
	var o domain.Order
	if err := json.Unmarshal(e.Data, &o); err != nil {
		return err
	}
	currency := o.Currency
	if currency == "" {
		currency = "USD"
	}
	row := map[string]any{"event_id": e.ID, "order_id": o.ID, "event_type": e.Type, "amount_cents": o.TotalCents, "at": e.At.UTC().Format("2006-01-02 15:04:05.000"), "version": e.Version, "currency": currency}
	b, _ := json.Marshal(row)
	_, err := s.click(ctx, "INSERT INTO order_events FORMAT JSONEachRow", b)
	return err
}
func (s *Service) NotificationEvent(ctx context.Context, e platform.Event) error {
	if e.Type == "stock.updated" || e.Type == "product.updated" {
		b, _ := json.Marshal(e)
		return s.Redis.Publish(ctx, "catalog:events", b).Err()
	}
	if !strings.HasPrefix(e.Type, "order.") {
		return nil
	}
	b, _ := json.Marshal(e)
	if err := s.Redis.Publish(ctx, "notify:"+e.UserID, b).Err(); err != nil {
		return err
	}
	var o domain.Order
	if err := json.Unmarshal(e.Data, &o); err != nil {
		return err
	}
	sellers := map[string]bool{}
	for _, item := range o.Items {
		if item.SellerID != "" {
			sellers[item.SellerID] = true
		}
	}
	for seller := range sellers {
		notice := e
		notice.Type = "seller.order.updated"
		notice.UserID = seller
		notice.Data, _ = json.Marshal(domain.ForSeller(o, seller))
		body, _ := json.Marshal(notice)
		if err := s.Redis.Publish(ctx, "notify:"+seller, body).Err(); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		q := strings.TrimSpace(r.URL.Query().Get("q"))
		if len(q) > 200 {
			platform.Error(w, 400, "query too long")
			return
		}
		query := map[string]any{"match_all": map[string]any{}}
		if q != "" {
			query = map[string]any{"multi_match": map[string]any{"query": q, "fields": []string{"name^3", "description", "seller_name", "sku"}, "operator": "and"}}
		}
		body, _ := json.Marshal(map[string]any{"size": 20, "query": query})
		b, status, e := s.search(r.Context(), "POST", "/products-v2/_search", body)
		if e != nil || status != 200 {
			platform.Error(w, 503, "search unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	m.Handle("GET /api/analytics/summary", platform.Auth(s.PublicKey, platform.Admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, e := s.click(r.Context(), `SELECT countIf(event_type='order.created') AS created, countIf(event_type='order.confirmed') AS confirmed, countIf(event_type='order.rejected') AS rejected, sumIf(amount_cents,event_type='order.confirmed') AS revenue_cents, 'RUB' AS report_currency FROM order_events FINAL WHERE currency='RUB' FORMAT JSONEachRow`, nil)
		if e != nil {
			platform.Error(w, 503, "analytics unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))))
	platform.Health(m, func(ctx context.Context) error {
		_, status, e := s.search(ctx, "GET", "/_cluster/health", nil)
		if e != nil {
			return e
		}
		if status != 200 {
			return fmt.Errorf("search unhealthy")
		}
		_, e = s.click(ctx, "SELECT 1", nil)
		return e
	})
	return m
}
func New() *Service {
	return &Service{SearchURL: platform.Env("OPENSEARCH_URL", "http://opensearch:9200"), ClickURL: platform.Env("CLICKHOUSE_URL", "http://clickhouse:8123"), ClickUser: platform.Env("CLICKHOUSE_USER", "flashcart"), ClickPassword: platform.MustEnv("CLICKHOUSE_PASSWORD"), HTTP: &http.Client{Timeout: 4 * time.Second}}
}
