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
	Limit  int
	Window time.Duration
	Now    func() time.Time
}

type FixedWindowLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	now     func() time.Time
	buckets map[string]rateBucket
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
	return &FixedWindowLimiter{
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

	bucket := l.buckets[key]
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
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwarded != "" {
		host := strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if host != "" {
			return host
		}
	}
	realIP := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if realIP != "" {
		return realIP
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
