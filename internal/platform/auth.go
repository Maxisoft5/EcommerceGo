package platform

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Claims struct {
	Role     string `json:"role"`
	ShopName string `json:"shop_name,omitempty"`
	jwt.RegisteredClaims
}
type authKey struct{}

func User(ctx context.Context) *Claims { v, _ := ctx.Value(authKey{}).(*Claims); return v }
func ReadPrivate(path string) (ed25519.PrivateKey, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, errors.New("invalid private PEM")
	}
	k, e := x509.ParsePKCS8PrivateKey(p.Bytes)
	if e != nil {
		return nil, e
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("expected Ed25519 key")
	}
	return key, nil
}
func ReadPublic(path string) (ed25519.PublicKey, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, errors.New("invalid public PEM")
	}
	k, e := x509.ParsePKIXPublicKey(p.Bytes)
	if e != nil {
		return nil, e
	}
	key, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("expected Ed25519 key")
	}
	return key, nil
}
func Issue(key ed25519.PrivateKey, id, role string) (string, error) {
	return IssueWithShop(key, id, role, "")
}
func IssueWithShop(key ed25519.PrivateKey, id, role, shop string) (string, error) {
	now := time.Now()
	return jwt.NewWithClaims(jwt.SigningMethodEdDSA, Claims{Role: role, ShopName: shop, RegisteredClaims: jwt.RegisteredClaims{Issuer: "flashcart.identity", Subject: id, Audience: jwt.ClaimStrings{"flashcart"}, ID: ID(), IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute))}}).SignedString(key)
}
func Verify(key ed25519.PublicKey, raw string) (*Claims, error) {
	c := &Claims{}
	_, e := jwt.ParseWithClaims(raw, c, func(t *jwt.Token) (any, error) { return key, nil }, jwt.WithValidMethods([]string{"EdDSA"}), jwt.WithIssuer("flashcart.identity"), jwt.WithAudience("flashcart"), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(5*time.Second))
	if e != nil || c.Subject == "" || (c.Role != "admin" && c.Role != "customer" && c.Role != "seller") {
		return nil, errors.New("invalid access token")
	}
	return c, nil
}
func Auth(key ed25519.PublicKey, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == r.Header.Get("Authorization") {
			Error(w, 401, "unauthorized")
			return
		}
		c, e := Verify(key, raw)
		if e != nil {
			Error(w, 401, "unauthorized")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authKey{}, c)))
	})
}
func Admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if User(r.Context()) == nil || User(r.Context()).Role != "admin" {
			Error(w, 403, "forbidden")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func Seller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := User(r.Context())
		if u == nil || (u.Role != "seller" && u.Role != "admin") {
			Error(w, 403, "seller account required")
			return
		}
		next.ServeHTTP(w, r)
	})
}
