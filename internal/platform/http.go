package platform

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var Requests = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "flashcart_http_requests_total", Help: "Requests by service, method and status class."}, []string{"service", "method", "status_class"})
var Duration = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "flashcart_http_duration_seconds", Help: "HTTP handler latency", Buckets: prometheus.DefBuckets}, []string{"service", "method"})
var Events = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "flashcart_events_total", Help: "Successfully processed events."}, []string{"consumer", "type"})

func init() { prometheus.MustRegister(Requests, Duration, Events) }
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func Error(w http.ResponseWriter, status int, message string) {
	JSON(w, status, map[string]string{"error": message})
}
func Decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		Error(w, 400, "invalid JSON body")
		return false
	}
	if e := d.Decode(new(any)); e != io.EOF {
		Error(w, 400, "exactly one JSON value required")
		return false
	}
	return true
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (w *recorder) WriteHeader(s int) {
	if w.status == 0 {
		w.status = s
		w.ResponseWriter.WriteHeader(s)
	}
}
func (w *recorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(b)
}
func (w *recorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func Middleware(service string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &recorder{ResponseWriter: w}
		requestID := ID()
		rw.Header().Set("X-Request-ID", requestID)
		rw.Header().Set("X-Content-Type-Options", "nosniff")
		rw.Header().Set("Referrer-Policy", "no-referrer")
		rw.Header().Set("X-Frame-Options", "DENY")
		rw.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
		defer func() {
			if v := recover(); v != nil {
				slog.Error("handler panic", "service", service, "request_id", requestID, "panic", v)
				if rw.status == 0 {
					Error(rw, 500, "internal error")
				}
			}
			status := rw.status
			if status == 0 {
				status = 200
			}
			if r.URL.Path != "/metrics" {
				Requests.WithLabelValues(service, r.Method, string(rune('0'+status/100))+"xx").Inc()
				Duration.WithLabelValues(service, r.Method).Observe(time.Since(start).Seconds())
				slog.Info("http", "service", service, "method", r.Method, "path", r.URL.Path, "status", status, "request_id", requestID, "duration_ms", time.Since(start).Milliseconds())
			}
		}()
		h.ServeHTTP(rw, r)
	})
}
func Health(mux *http.ServeMux, ready func(context.Context) error) {
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) { JSON(w, 200, map[string]string{"status": "alive"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if ready != nil {
			if e := ready(ctx); e != nil {
				Error(w, 503, "dependency unavailable")
				return
			}
		}
		JSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.Handle("GET /metrics", promhttp.Handler())
}
func Serve(ctx context.Context, service string, h http.Handler) error {
	s := &http.Server{Addr: Env("HTTP_ADDR", ":8080"), Handler: Middleware(service, h), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(c)
	}()
	slog.Info("http listening", "service", service, "address", s.Addr)
	var e error
	if Env("HTTP_TLS_CERT", "") != "" {
		e = s.ListenAndServeTLS(MustEnv("HTTP_TLS_CERT"), MustEnv("HTTP_TLS_KEY"))
	} else {
		e = s.ListenAndServe()
	}
	if errors.Is(e, http.ErrServerClosed) {
		return nil
	}
	return e
}
