package platform

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

func Redis() *redis.Client {
	cfg, e := redis.ParseURL(MustEnv("REDIS_URL"))
	if e != nil {
		panic(e)
	}
	cfg.DialTimeout = time.Second
	cfg.ReadTimeout = time.Second
	cfg.WriteTimeout = time.Second
	cfg.PoolSize = 32
	return redis.NewClient(cfg)
}

var limiter = redis.NewScript(`local n=redis.call('INCR',KEYS[1]); if n==1 then redis.call('PEXPIRE',KEYS[1],ARGV[1]) end; return n`)

func RateLimit(client *redis.Client, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		limit, err := strconv.ParseInt(Env("RATE_LIMIT_PER_MINUTE", "120"), 10, 64)
		if err != nil || limit < 1 {
			Error(w, 503, "invalid rate limit configuration")
			return
		}
		prefix := "api:"
		if r.URL.Path == "/api/auth/login" || r.URL.Path == "/api/auth/register" || r.URL.Path == "/api/auth/refresh" {
			limit, err = strconv.ParseInt(Env("AUTH_RATE_LIMIT_PER_MINUTE", "20"), 10, 64)
			if err != nil || limit < 1 {
				Error(w, 503, "invalid auth rate limit configuration")
				return
			}
			prefix = "auth:"
		}
		n, e := limiter.Run(r.Context(), client, []string{"limit:" + prefix + ip}, 60000).Int64()
		if e != nil {
			Error(w, 503, "rate limiter unavailable")
			return
		}
		if n > limit {
			w.Header().Set("Retry-After", "60")
			Error(w, 429, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}
