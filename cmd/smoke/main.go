// smoke verifies the public HTTPS contract against a running Compose or Kubernetes stack.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flashcart/internal/domain"
	"flashcart/internal/platform"
	"fmt"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

type client struct {
	base, token string
	http        *http.Client
}

func (c *client) request(method, path string, body any, key string, want int, out any) error {
	var b []byte
	if body != nil {
		b, _ = json.Marshal(body)
	}
	req, e := http.NewRequest(method, c.base+path, bytes.NewReader(b))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, e := c.http.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return e
	}
	if resp.StatusCode != want {
		return fmt.Errorf("%s %s: want %d got %d: %.200s", method, path, want, resp.StatusCode, raw)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}
func login(c *client, email, password string) (string, error) {
	var result struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	e := c.request("POST", "/api/auth/login", map[string]string{"email": email, "password": password}, "", 200, &result)
	c.token = result.Access
	return result.Refresh, e
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func envfile() map[string]string {
	b, e := os.ReadFile(".env")
	must(e)
	values := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && !strings.HasPrefix(k, "#") {
			values[k] = v
		}
	}
	return values
}
func main() {
	values := envfile()
	ca, e := os.ReadFile(platform.Env("SMOKE_CA", ".secrets/ca.pem"))
	must(e)
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		panic("invalid CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}
	hc := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	base := platform.Env("BASE_URL", "https://localhost:8443")
	c := &client{base: base, http: hc}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		e = c.request("GET", "/api/products", nil, "", 200, nil)
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			panic(e)
		}
		time.Sleep(time.Second)
	}
	must(c.request("POST", "/api/orders", map[string]any{"items": []domain.Item{{SKU: "sku-keyboard", Quantity: 1}}}, "unauth-key", 401, nil))
	if platform.Env("MARKETPLACE_CHECK", "") == "1" {
		marketplaceCheck(c, hc)
		return
	}
	suffix := platform.ID()
	email := "smoke-" + suffix + "@example.com"
	password := "Smoke-" + platform.ID()
	creds := map[string]string{"email": email, "password": password}
	must(c.request("POST", "/api/auth/register", creds, "", 201, nil))
	oldRefresh, e := login(c, email, password)
	must(e)
	must(c.request("GET", "/api/analytics/summary", nil, "", 403, nil))
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	must(c.request("POST", "/api/ws-ticket", map[string]any{}, "", 201, &ticket))
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	conn, _, e := websocket.Dial(ctx, strings.Replace(base, "https://", "wss://", 1)+"/ws?ticket="+ticket.Ticket, &websocket.DialOptions{HTTPClient: hc})
	must(e)
	defer conn.Close(websocket.StatusNormalClosure, "smoke complete")
	replay, resp, e := websocket.Dial(ctx, strings.Replace(base, "https://", "wss://", 1)+"/ws?ticket="+ticket.Ticket, &websocket.DialOptions{HTTPClient: hc})
	if e == nil {
		replay.CloseNow()
		panic("WebSocket ticket replay accepted")
	}
	if resp == nil || resp.StatusCode != 401 {
		panic("unexpected ticket replay response")
	}
	body := map[string]any{"items": []domain.Item{{SKU: "sku-keyboard", Quantity: 1}}}
	chaos := platform.Env("CHAOS_INVENTORY", "") == "1"
	if chaos {
		must(exec.Command("docker", "compose", "stop", "inventory").Run())
		defer exec.Command("docker", "compose", "start", "inventory").Run()
	}
	key := "smoke-" + suffix
	var order, again domain.Order
	must(c.request("POST", "/api/orders", body, key, 202, &order))
	if chaos {
		time.Sleep(2 * time.Second)
		must(c.request("GET", "/api/orders/"+order.ID, nil, "", 200, &again))
		if again.Status != "pending" {
			panic("order was not pending during Inventory outage")
		}
		must(exec.Command("docker", "compose", "start", "inventory").Run())
		fmt.Println("PASS: order remained pending during real Inventory outage; service restarted")
	}
	must(c.request("POST", "/api/orders", body, key, 202, &again))
	if order.ID != again.ID {
		panic("idempotency violated")
	}
	must(c.request("POST", "/api/orders", map[string]any{"items": []domain.Item{{SKU: "sku-keyboard", Quantity: 2}}}, key, 409, nil))
	for {
		_, raw, e := conn.Read(ctx)
		must(e)
		var event platform.Event
		must(json.Unmarshal(raw, &event))
		if event.AggregateID == order.ID && event.Type == "order.confirmed" {
			break
		}
		if event.AggregateID == order.ID && event.Type == "order.rejected" {
			panic("stock exhausted: reset dedicated test inventory before smoke")
		}
	}
	must(c.request("GET", "/api/orders/"+order.ID, nil, "", 200, &again))
	if again.Status != "confirmed" {
		panic("REST state not confirmed")
	}
	other := &client{base: base, http: hc}
	otherEmail := "other-" + suffix + "@example.com"
	must(other.request("POST", "/api/auth/register", map[string]string{"email": otherEmail, "password": password}, "", 201, nil))
	_, e = login(other, otherEmail, password)
	must(e)
	must(other.request("GET", "/api/orders/"+order.ID, nil, "", 404, nil))
	var rotated struct {
		Refresh string `json:"refresh_token"`
	}
	must(c.request("POST", "/api/auth/refresh", map[string]string{"refresh_token": oldRefresh}, "", 200, &rotated))
	must(c.request("POST", "/api/auth/refresh", map[string]string{"refresh_token": oldRefresh}, "", 401, nil))
	must(c.request("POST", "/api/auth/refresh", map[string]string{"refresh_token": rotated.Refresh}, "", 401, nil))
	admin := &client{base: base, http: hc}
	_, e = login(admin, values["ADMIN_EMAIL"], values["ADMIN_PASSWORD"])
	must(e)
	for deadline = time.Now().Add(30 * time.Second); ; {
		var search struct {
			Hits struct {
				Hits []any `json:"hits"`
			} `json:"hits"`
		}
		e = c.request("GET", "/api/search?q=TYPE", nil, "", 200, &search)
		if e == nil && len(search.Hits.Hits) > 0 {
			break
		}
		if time.Now().After(deadline) {
			panic("search projection not ready")
		}
		time.Sleep(time.Second)
	}
	for deadline = time.Now().Add(30 * time.Second); ; {
		var summary map[string]any
		e = admin.request("GET", "/api/analytics/summary", nil, "", 200, &summary)
		if e == nil && fmt.Sprint(summary["confirmed"]) != "0" && summary["confirmed"] != nil {
			break
		}
		if time.Now().After(deadline) {
			panic("analytics projection not ready")
		}
		time.Sleep(time.Second)
	}
	fmt.Println("PASS: HTTPS, JWT/RBAC, idempotency/conflict, ownership, gRPC reservation, Kafka, WebSocket ticket replay, refresh reuse revocation, OpenSearch, ClickHouse. Order:", order.ID)
}
