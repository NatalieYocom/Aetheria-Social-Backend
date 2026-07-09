package observability

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

func RequestLogger(out io.Writer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)

			entry := map[string]any{
				"ts":          time.Now().UTC().Format(time.RFC3339Nano),
				"msg":         "http_request",
				"request_id":  middleware.GetReqID(r.Context()),
				"method":      r.Method,
				"path":        r.URL.Path,
				"route":       routePattern(r),
				"status":      recorder.status,
				"duration_ms": float64(time.Since(start).Microseconds()) / 1000,
				"remote_ip":   remoteIP(r),
				"user_agent":  r.UserAgent(),
			}
			_ = json.NewEncoder(out).Encode(entry)
		})
	}
}

func remoteIP(r *http.Request) string {
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
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
