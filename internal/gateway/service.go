package gateway

import (
	"context"
	"crypto/ed25519"
	"embed"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"flashcart/internal/platform"
	"github.com/coder/websocket"
	"github.com/redis/go-redis/v9"
)

//go:embed web/*
var web embed.FS

type Service struct {
	Redis     *redis.Client
	PublicKey ed25519.PublicKey
	Hub       *Hub
	Origin    string
}

func (s *Service) Routes() http.Handler {
	m := http.NewServeMux()
	for _, route := range []struct{ pattern, key, defaultURL string }{{"/api/auth/", "IDENTITY_URL", "http://identity:8080"}, {"/api/products", "CATALOG_URL", "http://catalog:8080"}, {"/api/products/", "CATALOG_URL", "http://catalog:8080"}, {"/api/seller/products", "CATALOG_URL", "http://catalog:8080"}, {"/api/seller/products/", "CATALOG_URL", "http://catalog:8080"}, {"/api/stock", "INVENTORY_URL", "http://inventory:8080"}, {"/api/seller/stock/", "INVENTORY_URL", "http://inventory:8080"}, {"/api/seller/orders", "ORDERS_URL", "http://orders:8080"}, {"/api/orders", "ORDERS_URL", "http://orders:8080"}, {"/api/orders/", "ORDERS_URL", "http://orders:8080"}, {"/api/search", "PROJECTION_URL", "http://projection:8080"}, {"/api/analytics/", "PROJECTION_URL", "http://projection:8080"}} {
		u, e := url.Parse(platform.Env(route.key, route.defaultURL))
		if e != nil {
			panic(e)
		}
		p := httputil.NewSingleHostReverseProxy(u)
		p.Transport = &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 50, IdleConnTimeout: 60 * time.Second, ResponseHeaderTimeout: 8 * time.Second}
		p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) { platform.Error(w, 503, "upstream unavailable") }
		p.ModifyResponse = func(resp *http.Response) error { resp.Header.Del("Server"); return nil }
		m.Handle(route.pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			defer cancel()
			r.Header.Del("X-User-ID")
			r.Header.Del("X-Role")
			r.Header.Del("Forwarded")
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Forwarded-Host")
			r.Header.Del("X-Forwarded-Proto")
			p.ServeHTTP(w, r.WithContext(ctx))
		}))
	}
	m.Handle("POST /api/ws-ticket", platform.Auth(s.PublicKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ticket := platform.ID() + platform.ID()
		ttl := time.Until(platform.User(r.Context()).ExpiresAt.Time)
		if ttl > 30*time.Second {
			ttl = 30 * time.Second
		}
		if ttl <= 0 {
			platform.Error(w, 401, "token expired")
			return
		}
		value := platform.User(r.Context()).Subject + "|" + platform.User(r.Context()).ExpiresAt.Time.Format(time.RFC3339)
		if e := s.Redis.Set(r.Context(), "ws-ticket:"+ticket, value, ttl).Err(); e != nil {
			platform.Error(w, 503, "realtime unavailable")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		platform.JSON(w, 201, map[string]string{"ticket": ticket})
	})))
	m.HandleFunc("GET /ws", s.socket)
	m.HandleFunc("GET /ws/catalog", s.publicSocket)
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { platform.Error(w, 404, "route not found") })
	assets, _ := fs.Sub(web, "web")
	m.Handle("/", http.FileServer(http.FS(assets)))
	// Do not expose detailed readiness or Prometheus outside the internal HTTP port.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != s.Origin {
			platform.Error(w, 403, "origin forbidden")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws") {
			platform.RateLimit(s.Redis, m).ServeHTTP(w, r)
		} else {
			m.ServeHTTP(w, r)
		}
	})
}
func (s *Service) socket(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("ticket")
	if len(ticket) != 64 {
		platform.Error(w, 401, "ticket required")
		return
	}
	value, e := s.Redis.GetDel(r.Context(), "ws-ticket:"+ticket).Result()
	if e != nil {
		platform.Error(w, 401, "invalid or consumed ticket")
		return
	}
	parts := strings.Split(value, "|")
	if len(parts) != 2 {
		platform.Error(w, 401, "invalid ticket")
		return
	}
	expiry, e := time.Parse(time.RFC3339, parts[1])
	if e != nil || time.Now().After(expiry) {
		platform.Error(w, 401, "expired ticket")
		return
	}
	s.stream(w, r, parts[0], expiry)
}
func (s *Service) publicSocket(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, "public:catalog", time.Now().Add(30*time.Minute))
}
func (s *Service) stream(w http.ResponseWriter, r *http.Request, user string, expiry time.Time) {
	conn, e := websocket.Accept(w, r, nil)
	if e != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "session ended")
	ctx, cancel := context.WithDeadline(r.Context(), expiry)
	defer cancel()
	conn.SetReadLimit(1024)
	ctx = conn.CloseRead(ctx)
	events, remove := s.Hub.Add(user)
	defer remove()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-events:
			writeCtx, done := context.WithTimeout(ctx, 3*time.Second)
			e = conn.Write(writeCtx, websocket.MessageText, event)
			done()
			if e != nil {
				return
			}
		case <-ticker.C:
			pingCtx, done := context.WithTimeout(ctx, 3*time.Second)
			e = conn.Ping(pingCtx)
			done()
			if e != nil {
				return
			}
		}
	}
}
