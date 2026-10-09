package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestForwardingOnlyFromExplicitTrustedPeers(t *testing.T) {
	for _, tt := range []struct{ name, peer, chain, want string }{
		{"untrusted", "203.0.113.2:123", "1.2.3.4", "203.0.113.2"},
		{"trusted strips spoofed left side", "10.0.0.2:123", "1.2.3.4, 203.0.113.8, 10.0.0.3", "203.0.113.8"},
		{"malformed", "10.0.0.2:123", "invalid", "10.0.0.2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := TrustedProxy([]string{"10.0.0.0/24"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := clientKey(r); got != tt.want {
					t.Fatalf("IP=%s", got)
				}
				if r.Header.Get("X-Forwarded-For") != "" {
					t.Fatal("forwarded header retained")
				}
			}))
			r := httptest.NewRequest("POST", "/api/auth/login", nil)
			r.RemoteAddr = tt.peer
			r.Header.Set("X-Forwarded-For", tt.chain)
			h.ServeHTTP(httptest.NewRecorder(), r)
		})
	}
}
func TestLimiterBoundsMemoryAndExpiresBuckets(t *testing.T) {
	now := time.Now()
	l := NewFixedWindowLimiter(FixedWindowConfig{Limit: 2, MaxKeys: 2, Window: time.Minute, Now: func() time.Time { return now }})
	l.Allow("a")
	l.Allow("b")
	if allowed, _ := l.Allow("c"); allowed {
		t.Fatal("capacity bypass")
	}
	if len(l.buckets) != 2 {
		t.Fatal("map grew")
	}
	now = now.Add(time.Minute)
	if allowed, _ := l.Allow("c"); !allowed {
		t.Fatal("expired bucket not reclaimed")
	}
	if len(l.buckets) != 1 {
		t.Fatal("expired buckets retained")
	}
}
func TestAuthLimitCannotBeBypassedByForwardingOrVersionAlias(t *testing.T) {
	h := AuthRateLimit(1, 100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	for i, path := range []string{"/api/auth/login", "/api/v1/auth/login", "/api/auth/refresh"} {
		r := httptest.NewRequest("POST", path, nil)
		r.RemoteAddr = "203.0.113.1:44"
		r.Header.Set("X-Forwarded-For", string(rune('a'+i)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := 204
		if i == 1 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("%s status=%d want%d", path, w.Code, want)
		}
	}
}
