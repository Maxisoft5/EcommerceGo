package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"flashcart/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	DB    *pgxpool.Pool
	Key   ed25519.PrivateKey
	dummy []byte
}
type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}
type registration struct {
	credentials
	Role     string `json:"role"`
	ShopName string `json:"shop_name"`
}

func New(db *pgxpool.Pool, key ed25519.PrivateKey) *Service {
	dummy, _ := bcrypt.GenerateFromPassword([]byte(platform.ID()), 12)
	return &Service{DB: db, Key: key, dummy: dummy}
}
func (s *Service) Routes() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/auth/register", s.register)
	m.HandleFunc("POST /api/auth/login", s.login)
	m.HandleFunc("POST /api/auth/refresh", s.refresh)
	m.HandleFunc("POST /api/auth/logout", s.logout)
	m.Handle("GET /api/auth/me", platform.Auth(s.Key.Public().(ed25519.PublicKey), http.HandlerFunc(s.me)))
	platform.Health(m, s.DB.Ping)
	return m
}
func valid(c *credentials) bool {
	c.Email = strings.ToLower(strings.TrimSpace(c.Email))
	a, e := mail.ParseAddress(c.Email)
	return e == nil && a.Address == c.Email && len(c.Email) <= 254 && len(c.Password) >= 12 && len(c.Password) <= 72
}
func digest(raw string) string { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func (s *Service) register(w http.ResponseWriter, r *http.Request) {
	var c registration
	if !platform.Decode(w, r, &c) {
		return
	}
	if !valid(&c.credentials) {
		platform.Error(w, 400, "valid email and password of 12..72 bytes required")
		return
	}
	if c.Role == "" {
		c.Role = "customer"
	}
	c.ShopName = strings.TrimSpace(c.ShopName)
	if (c.Role != "customer" && c.Role != "seller") || (c.Role == "seller" && (len(c.ShopName) < 2 || len(c.ShopName) > 120)) || (c.Role == "customer" && c.ShopName != "") {
		platform.Error(w, 400, "choose customer or seller; seller requires a shop name")
		return
	}
	hash, e := bcrypt.GenerateFromPassword([]byte(c.Password), 12)
	if e != nil {
		platform.Error(w, 500, "password hashing failed")
		return
	}
	id := platform.ID()
	_, e = s.DB.Exec(r.Context(), `INSERT INTO users(id,email,password_hash,role,shop_name) VALUES($1,$2,$3,$4,$5)`, id, c.Email, string(hash), c.Role, c.ShopName)
	if e != nil {
		var pg *pgconn.PgError
		if errors.As(e, &pg) && pg.Code == "23505" {
			platform.Error(w, 409, "account already exists")
		} else {
			platform.Error(w, 503, "registration unavailable")
		}
		return
	}
	platform.JSON(w, 201, map[string]string{"id": id})
}
func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !platform.Decode(w, r, &c) {
		return
	}
	if !valid(&c) {
		platform.Error(w, 401, "invalid credentials")
		return
	}
	var id, hash, role string
	e := s.DB.QueryRow(r.Context(), `SELECT id,password_hash,role FROM users WHERE email=$1`, c.Email).Scan(&id, &hash, &role)
	if e != nil {
		_ = bcrypt.CompareHashAndPassword(s.dummy, []byte(c.Password))
		if e != pgx.ErrNoRows {
			platform.Error(w, 503, "login unavailable")
		} else {
			platform.Error(w, 401, "invalid credentials")
		}
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(c.Password)) != nil {
		platform.Error(w, 401, "invalid credentials")
		return
	}
	raw := platform.ID() + platform.ID()
	_, e = s.DB.Exec(r.Context(), `INSERT INTO refresh_tokens(hash,family,user_id,expires_at) VALUES($1,$2,$3,$4)`, digest(raw), platform.ID(), id, time.Now().Add(7*24*time.Hour))
	if e != nil {
		platform.Error(w, 503, "login unavailable")
		return
	}
	s.tokens(w, r, id, role, raw)
}
func (s *Service) tokens(w http.ResponseWriter, r *http.Request, id, role, refresh string) {
	var shop string
	if e := s.DB.QueryRow(r.Context(), `SELECT shop_name FROM users WHERE id=$1`, id).Scan(&shop); e != nil {
		platform.Error(w, 503, "profile unavailable")
		return
	}
	access, e := platform.IssueWithShop(s.Key, id, role, shop)
	if e != nil {
		platform.Error(w, 500, "signing failed")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	platform.JSON(w, 200, map[string]any{"access_token": access, "refresh_token": refresh, "token_type": "Bearer", "expires_in": 600})
}
func (s *Service) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"refresh_token"`
	}
	if !platform.Decode(w, r, &body) {
		return
	}
	if len(body.Token) != 64 {
		platform.Error(w, 401, "invalid refresh token")
		return
	}
	ctx := r.Context()
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		platform.Error(w, 503, "refresh unavailable")
		return
	}
	defer tx.Rollback(ctx)
	// Serialize the whole family, including replays of previously rotated tokens.
	var family string
	e = tx.QueryRow(ctx, `SELECT family FROM refresh_tokens WHERE hash=$1`, digest(body.Token)).Scan(&family)
	if e != nil {
		if e == pgx.ErrNoRows {
			platform.Error(w, 401, "invalid refresh token")
		} else {
			platform.Error(w, 503, "refresh unavailable")
		}
		return
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, family); e != nil {
		platform.Error(w, 503, "refresh unavailable")
		return
	}
	var id, role string
	var expires time.Time
	var used *time.Time
	var revoked bool
	e = tx.QueryRow(ctx, `SELECT t.user_id,u.role,t.expires_at,t.used_at,t.revoked FROM refresh_tokens t JOIN users u ON u.id=t.user_id WHERE t.hash=$1 FOR UPDATE OF t`, digest(body.Token)).Scan(&id, &role, &expires, &used, &revoked)
	if e != nil {
		platform.Error(w, 503, "refresh unavailable")
		return
	}
	if used != nil || revoked || time.Now().After(expires) {
		if _, e = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked=true WHERE family=$1`, family); e != nil {
			platform.Error(w, 503, "refresh unavailable")
			return
		}
		if e = tx.Commit(ctx); e != nil {
			platform.Error(w, 503, "refresh unavailable")
			return
		}
		platform.Error(w, 401, "refresh expired or reused; family revoked")
		return
	}
	raw := platform.ID() + platform.ID()
	if _, e = tx.Exec(ctx, `UPDATE refresh_tokens SET used_at=now() WHERE hash=$1`, digest(body.Token)); e == nil {
		_, e = tx.Exec(ctx, `INSERT INTO refresh_tokens(hash,family,user_id,expires_at) VALUES($1,$2,$3,$4)`, digest(raw), family, id, expires)
	}
	if e != nil {
		platform.Error(w, 503, "refresh unavailable")
		return
	}
	if e = tx.Commit(ctx); e != nil {
		platform.Error(w, 503, "refresh unavailable")
		return
	}
	s.tokens(w, r, id, role, raw)
}
func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Token string `json:"refresh_token"`
	}
	if !platform.Decode(w, r, &b) {
		return
	}
	tx, e := s.DB.Begin(r.Context())
	if e != nil {
		platform.Error(w, 503, "logout unavailable")
		return
	}
	defer tx.Rollback(r.Context())
	var family string
	e = tx.QueryRow(r.Context(), `SELECT family FROM refresh_tokens WHERE hash=$1`, digest(b.Token)).Scan(&family)
	if e == pgx.ErrNoRows {
		w.WriteHeader(204)
		return
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, family)
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `UPDATE refresh_tokens SET revoked=true WHERE family=$1`, family)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		platform.Error(w, 503, "logout unavailable")
		return
	}
	w.WriteHeader(204)
}
func (s *Service) BootstrapAdmin(ctx context.Context, email, password string) error {
	c := credentials{Email: email, Password: password}
	if !valid(&c) {
		return errors.New("invalid bootstrap admin credentials")
	}
	h, e := bcrypt.GenerateFromPassword([]byte(password), 12)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(ctx, `INSERT INTO users(id,email,password_hash,role) VALUES($1,$2,$3,'admin') ON CONFLICT(email) DO NOTHING`, platform.ID(), c.Email, string(h))
	return e
}

func (s *Service) me(w http.ResponseWriter, r *http.Request) {
	var id, email, role, shop string
	e := s.DB.QueryRow(r.Context(), `SELECT id,email,role,shop_name FROM users WHERE id=$1`, platform.User(r.Context()).Subject).Scan(&id, &email, &role, &shop)
	if e != nil {
		platform.Error(w, 503, "profile unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	platform.JSON(w, 200, map[string]string{"id": id, "email": email, "role": role, "shop_name": shop})
}
