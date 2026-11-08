package tests

import (
	"crypto/ed25519"
	"crypto/rand"
	"flashcart/internal/platform"
	"github.com/golang-jwt/jwt/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJWTPolicy(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	raw, e := platform.Issue(priv, "user-a", "customer")
	if e != nil {
		t.Fatal(e)
	}
	c, e := platform.Verify(pub, raw)
	if e != nil || c.Subject != "user-a" {
		t.Fatalf("valid token rejected: %v", e)
	}
	if _, e = platform.Verify(other, raw); e == nil {
		t.Fatal("foreign issuer key accepted")
	}
	for _, mutation := range []string{"expired", "issuer", "audience", "subject", "role", "no-exp"} {
		t.Run(mutation, func(t *testing.T) {
			claims := platform.Claims{Role: "customer", RegisteredClaims: jwt.RegisteredClaims{Subject: "user-a", Issuer: "flashcart.identity", Audience: []string{"flashcart"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
			switch mutation {
			case "expired":
				claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
			case "issuer":
				claims.Issuer = "attacker"
			case "audience":
				claims.Audience = []string{"other"}
			case "subject":
				claims.Subject = ""
			case "role":
				claims.Role = "root"
			case "no-exp":
				claims.ExpiresAt = nil
			}
			token, _ := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims).SignedString(priv)
			if _, e := platform.Verify(pub, token); e == nil {
				t.Fatal("unsafe claims accepted")
			}
		})
	}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "user-a"}).SignedString([]byte(pub))
	if _, e = platform.Verify(pub, token); e == nil {
		t.Fatal("algorithm confusion")
	}
	protected := platform.Auth(pub, platform.Admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	for _, tc := range []struct {
		auth   string
		status int
	}{{"", 401}, {"Bearer " + raw, 403}} {
		r := httptest.NewRequest("PUT", "/", nil)
		r.Header.Set("Authorization", tc.auth)
		w := httptest.NewRecorder()
		protected.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("expected %d got %d", tc.status, w.Code)
		}
	}
}
func TestStrictJSON(t *testing.T) {
	for _, raw := range []string{`{"known":1,"extra":2}`, `{"known":1} {"known":2}`, `{"known":`, strings.Repeat(" ", 65537) + `{}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		w := httptest.NewRecorder()
		var b struct {
			Known int `json:"known"`
		}
		if platform.Decode(w, r, &b) {
			t.Errorf("accepted malformed or oversized JSON")
		}
	}
}
