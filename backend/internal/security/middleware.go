package security

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"basisvr-social-service/internal/common/httpx"
)

type FixedWindowConfig struct {
	Limit   int
	Window  time.Duration
	Now     func() time.Time
	MaxKeys int
}

type FixedWindowLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time
	buckets map[string]rateBucket
	maxKeys int
	sweptAt time.Time
}

type rateBucket struct {
	start time.Time
	count int
}

func NewFixedWindowLimiter(config FixedWindowConfig) *FixedWindowLimiter {
	limit := config.Limit
	if limit <= 0 {
		limit = 600
	}
	window := config.Window
	if window <= 0 {
		window = time.Minute
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	maxKeys := config.MaxKeys
	if maxKeys <= 0 {
		maxKeys = 10000
	}
	return &FixedWindowLimiter{
		maxKeys: maxKeys,
		limit:   limit,
		window:  window,
		now:     now,
		buckets: map[string]rateBucket{},
	}
}

func Headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-site")
		next.ServeHTTP(w, r)
	})
}

func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" && origin != "*" {
			allowed[origin] = struct{}{}
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := allowed[origin]; !ok {
				if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
					httpx.WriteError(w, http.StatusForbidden, "origin_not_allowed", "origin is not allowed")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Last-Event-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func BodyLimit(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if maxBytes <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxBytes {
				httpx.WriteError(w, http.StatusRequestEntityTooLarge, "request_body_too_large", "request body is too large")
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func RateLimit(limiter *FixedWindowLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if limiter == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			allowed, retryAfter := limiter.Allow(clientKey(r))
			if !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
				httpx.WriteError(w, http.StatusTooManyRequests, "rate_limited", "too many requests")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (l *FixedWindowLimiter) Allow(key string) (bool, time.Duration) {
	if key == "" {
		key = "unknown"
	}
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.sweptAt.IsZero() || now.Sub(l.sweptAt) >= l.window {
		for stored, bucket := range l.buckets {
			if now.Sub(bucket.start) >= l.window {
				delete(l.buckets, stored)
			}
		}
		l.sweptAt = now
	}
	bucket, exists := l.buckets[key]
	if !exists && len(l.buckets) >= l.maxKeys {
		return false, l.window
	}
	if bucket.start.IsZero() || now.Sub(bucket.start) >= l.window {
		l.buckets[key] = rateBucket{start: now, count: 1}
		return true, 0
	}
	if bucket.count >= l.limit {
		retryAfter := bucket.start.Add(l.window).Sub(now)
		if retryAfter < time.Second {
			retryAfter = time.Second
		}
		return false, retryAfter
	}
	bucket.count++
	l.buckets[key] = bucket
	return true, 0
}

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
