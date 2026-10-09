package security

import (
	"basisvr-social-service/internal/common/httpx"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// TrustedProxy accepts forwarding only from configured immediate peers, walking right-to-left.
// Malformed chains fail closed to the TCP peer. Original headers are removed after normalization.
func TrustedProxy(cidrs []string) func(http.Handler) http.Handler {
	prefixes := []netip.Prefix{}
	for _, cidr := range cidrs {
		if prefix, err := netip.ParsePrefix(cidr); err == nil {
			prefixes = append(prefixes, prefix)
		}
	}
	trusted := func(ip netip.Addr) bool {
		for _, p := range prefixes {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := clientKey(r)
			ip, err := netip.ParseAddr(peer)
			if err == nil && trusted(ip.Unmap()) {
				chain := r.Header.Get("X-Forwarded-For")
				if chain == "" {
					chain = r.Header.Get("X-Real-IP")
				}
				if len(chain) <= 4096 && chain != "" {
					parts := strings.Split(chain, ",")
					candidate := ip.Unmap()
					valid := true
					for i := len(parts) - 1; i >= 0 && trusted(candidate); i-- {
						parsed, e := netip.ParseAddr(strings.TrimSpace(parts[i]))
						if e != nil {
							valid = false
							break
						}
						candidate = parsed.Unmap()
					}
					if valid {
						r.RemoteAddr = net.JoinHostPort(candidate.String(), "0")
					}
				}
			}
			r.Header.Del("X-Forwarded-For")
			r.Header.Del("X-Real-IP")
			r.Header.Del("Forwarded")
			next.ServeHTTP(w, r)
		})
	}
}

func AuthRateLimit(limit, maxKeys int) func(http.Handler) http.Handler {
	if limit <= 0 {
		limit = 20
	}
	limiter := NewFixedWindowLimiter(FixedWindowConfig{Limit: limit, Window: time.Minute, MaxKeys: maxKeys})
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scope := ""
			path := strings.TrimPrefix(r.URL.Path, "/api/v1")
			if path == r.URL.Path {
				path = strings.TrimPrefix(path, "/api")
			}
			switch path {
			case "/auth/register":
				scope = "register"
			case "/auth/login":
				scope = "login"
			case "/auth/refresh":
				scope = "refresh"
			default:
				if strings.HasPrefix(path, "/auth/") {
					scope = "session"
				}
			}
			if scope != "" {
				allowed, retry := limiter.Allow(scope + ":" + clientKey(r))
				if !allowed {
					w.Header().Set("Retry-After", strconv.Itoa(max(1, int(retry.Seconds()))))
					httpx.WriteError(w, 429, "rate_limited", "too many authentication requests")
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
