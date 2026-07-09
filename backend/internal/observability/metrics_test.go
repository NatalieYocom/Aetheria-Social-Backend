package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestMetricsMiddlewareCountsRequestsByMethodRouteAndStatus(t *testing.T) {
	metrics := NewMetrics()
	router := chi.NewRouter()
	router.Use(metrics.Middleware)
	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	metricsRes := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsRes, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsRes.Body.String()
	if !strings.Contains(body, `basisvr_http_requests_total{method="GET",route="/healthz",status="204"} 1`) {
		t.Fatalf("metrics body = %s", body)
	}
	if !strings.Contains(body, "basisvr_http_request_duration_seconds_sum") {
		t.Fatalf("duration metric missing: %s", body)
	}
}

func TestMetricsMiddlewareTracksInFlightRequests(t *testing.T) {
	metrics := NewMetrics()
	wait := make(chan struct{})
	started := make(chan struct{})
	handler := metrics.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-wait
		w.WriteHeader(http.StatusOK)
	}))

	go handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/slow", nil))
	<-started

	metricsRes := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(metricsRes, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metricsRes.Body.String(), "basisvr_http_in_flight_requests 1") {
		t.Fatalf("metrics body = %s", metricsRes.Body.String())
	}
	close(wait)
}
