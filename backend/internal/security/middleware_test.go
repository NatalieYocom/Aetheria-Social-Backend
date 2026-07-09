package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHeadersMiddlewareSetsAPISecurityHeaders(t *testing.T) {
	handler := Headers(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", res.Header().Get("X-Content-Type-Options"))
	}
	if res.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options = %q", res.Header().Get("X-Frame-Options"))
	}
	if res.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", res.Header().Get("Referrer-Policy"))
	}
}

func TestBodyLimitRejectsOversizedContentLength(t *testing.T) {
	handler := BodyLimit(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/worlds", strings.NewReader(`{"name":"too large"}`))
	req.ContentLength = 20
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "request_body_too_large") {
		t.Fatalf("body = %s", res.Body.String())
	}
}

func TestRateLimitRejectsAfterLimitAndResetsAfterWindow(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	limiter := NewFixedWindowLimiter(FixedWindowConfig{
		Limit:  2,
		Window: time.Minute,
		Now: func() time.Time {
			return now
		},
	})
	handler := RateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for i := range 2 {
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		req.RemoteAddr = "203.0.113.10:12000"
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusNoContent {
			t.Fatalf("request %d status = %d, body = %s", i+1, res.Code, res.Body.String())
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.RemoteAddr = "203.0.113.10:12000"
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if res.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header is empty")
	}

	now = now.Add(time.Minute)
	req = httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.RemoteAddr = "203.0.113.10:12000"
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status after reset = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestRateLimitUsesSeparateIPBuckets(t *testing.T) {
	limiter := NewFixedWindowLimiter(FixedWindowConfig{Limit: 1, Window: time.Minute})
	handler := RateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	first := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	first.RemoteAddr = "203.0.113.10:12000"
	firstRes := httptest.NewRecorder()
	handler.ServeHTTP(firstRes, first)

	second := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	second.RemoteAddr = "203.0.113.11:12000"
	secondRes := httptest.NewRecorder()
	handler.ServeHTTP(secondRes, second)

	if firstRes.Code != http.StatusNoContent {
		t.Fatalf("first status = %d", firstRes.Code)
	}
	if secondRes.Code != http.StatusNoContent {
		t.Fatalf("second status = %d", secondRes.Code)
	}
}
