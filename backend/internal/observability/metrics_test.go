package observability

import (
	"context"
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
	if !strings.Contains(body, `basisvr_http_request_duration_seconds_bucket{method="GET",route="/healthz",status="204",le="+Inf"} 1`) {
		t.Fatalf("duration histogram missing: %s", body)
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

func TestMetricsExportsRealtimeSnapshot(t *testing.T) {
	metrics := NewMetrics()
	metrics.SetRealtimeProvider(func() RealtimeMetrics {
		return RealtimeMetrics{
			ReplayStored: 12, ReplayStoreFailures: 2, ReplayRequests: 5,
			ReplayDelivered: 21, ReplayResyncs: 1, ActiveSubscribers: 7,
		}
	})

	res := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := res.Body.String()
	for _, expected := range []string{
		"basisvr_realtime_replay_stored_total 12",
		"basisvr_realtime_replay_store_failures_total 2",
		"basisvr_realtime_replay_requests_total 5",
		"basisvr_realtime_replay_delivered_total 21",
		"basisvr_realtime_replay_resync_required_total 1",
		"basisvr_realtime_active_subscribers 7",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in metrics body:\n%s", expected, body)
		}
	}
}

func TestMetricsExportsFederationQueueSnapshot(t *testing.T) {
	metrics := NewMetrics()
	metrics.SetFederationProvider(func(context.Context) (FederationMetrics, error) {
		return FederationMetrics{
			InboxPending: 3, InboxFailed: 1, OutboxPending: 5,
			OutboxRetry: 2, OutboxFailed: 4, OldestPendingSeconds: 90,
		}, nil
	})
	res := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := res.Body.String()
	for _, expected := range []string{
		`basisvr_activitypub_inbox_messages{state="pending"} 3`,
		`basisvr_activitypub_inbox_messages{state="failed"} 1`,
		`basisvr_activitypub_outbox_jobs{state="pending"} 5`,
		`basisvr_activitypub_outbox_jobs{state="retry"} 2`,
		`basisvr_activitypub_outbox_jobs{state="failed"} 4`,
		"basisvr_activitypub_outbox_oldest_pending_seconds 90",
		"basisvr_activitypub_metrics_scrape_error 0",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("missing %q in metrics body:\n%s", expected, body)
		}
	}
}
