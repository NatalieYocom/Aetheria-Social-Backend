package observability

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestReadinessHandlerReturnsOKWhenChecksPass(t *testing.T) {
	handler := ReadinessHandler(ReadinessConfig{
		Timeout: 100 * time.Millisecond,
		Checks: []Check{
			{Name: "postgres", Check: func(ctx context.Context) error { return nil }},
			{Name: "redis", Check: func(ctx context.Context) error { return nil }},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body ReadinessResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("Status = %q", body.Status)
	}
	if body.Checks["postgres"].Status != "ok" {
		t.Fatalf("postgres status = %q", body.Checks["postgres"].Status)
	}
}

func TestReadinessHandlerReturnsUnavailableWhenCheckFails(t *testing.T) {
	handler := ReadinessHandler(ReadinessConfig{
		Timeout: 100 * time.Millisecond,
		Checks: []Check{
			{Name: "postgres", Check: func(ctx context.Context) error { return errors.New("db down") }},
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body ReadinessResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Status != "degraded" {
		t.Fatalf("Status = %q", body.Status)
	}
	if body.Checks["postgres"].Error == "" {
		t.Fatal("postgres error is empty")
	}
}
