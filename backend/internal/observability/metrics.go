package observability

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

type Metrics struct {
	mu                 sync.Mutex
	inFlight           int
	requests           map[metricKey]requestMetric
	realtimeProvider   func() RealtimeMetrics
	federationProvider func(context.Context) (FederationMetrics, error)
}

type RealtimeMetrics struct {
	ReplayStored        int64
	ReplayStoreFailures int64
	ReplayRequests      int64
	ReplayDelivered     int64
	ReplayResyncs       int64
	ActiveSubscribers   int64
}

type FederationMetrics struct {
	InboxPending         int64
	InboxFailed          int64
	OutboxPending        int64
	OutboxRetry          int64
	OutboxFailed         int64
	OldestPendingSeconds float64
}

type metricKey struct {
	Method string
	Route  string
	Status int
}

type requestMetric struct {
	Count       int64
	DurationSum float64
	Buckets     [8]int64
}

var requestDurationBuckets = [8]float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5}

func NewMetrics() *Metrics {
	return &Metrics{requests: map[metricKey]requestMetric{}}
}

func (m *Metrics) SetRealtimeProvider(provider func() RealtimeMetrics) {
	m.mu.Lock()
	m.realtimeProvider = provider
	m.mu.Unlock()
}

func (m *Metrics) SetFederationProvider(provider func(context.Context) (FederationMetrics, error)) {
	m.mu.Lock()
	m.federationProvider = provider
	m.mu.Unlock()
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		m.addInFlight(1)
		defer m.addInFlight(-1)

		next.ServeHTTP(recorder, r)

		route := routePattern(r)
		key := metricKey{Method: r.Method, Route: route, Status: recorder.status}
		m.mu.Lock()
		item := m.requests[key]
		duration := time.Since(start).Seconds()
		item.Count++
		item.DurationSum += duration
		for index, upperBound := range requestDurationBuckets {
			if duration <= upperBound {
				item.Buckets[index]++
			}
		}
		m.requests[key] = item
		m.mu.Unlock()
	})
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.mu.Lock()
		inFlight := m.inFlight
		keys := make([]metricKey, 0, len(m.requests))
		for key := range m.requests {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].Method != keys[j].Method {
				return keys[i].Method < keys[j].Method
			}
			if keys[i].Route != keys[j].Route {
				return keys[i].Route < keys[j].Route
			}
			return keys[i].Status < keys[j].Status
		})
		snapshot := make(map[metricKey]requestMetric, len(m.requests))
		for _, key := range keys {
			snapshot[key] = m.requests[key]
		}
		realtimeProvider := m.realtimeProvider
		federationProvider := m.federationProvider
		m.mu.Unlock()

		_, _ = fmt.Fprintln(w, "# HELP basisvr_http_in_flight_requests Current in-flight HTTP requests.")
		_, _ = fmt.Fprintln(w, "# TYPE basisvr_http_in_flight_requests gauge")
		_, _ = fmt.Fprintf(w, "basisvr_http_in_flight_requests %d\n", inFlight)
		_, _ = fmt.Fprintln(w, "# HELP basisvr_http_requests_total Total HTTP requests.")
		_, _ = fmt.Fprintln(w, "# TYPE basisvr_http_requests_total counter")
		for _, key := range keys {
			item := snapshot[key]
			_, _ = fmt.Fprintf(w, "basisvr_http_requests_total{method=%q,route=%q,status=%q} %d\n",
				key.Method, key.Route, fmt.Sprintf("%d", key.Status), item.Count)
		}
		_, _ = fmt.Fprintln(w, "# HELP basisvr_http_request_duration_seconds HTTP request duration by route and status.")
		_, _ = fmt.Fprintln(w, "# TYPE basisvr_http_request_duration_seconds histogram")
		for _, key := range keys {
			item := snapshot[key]
			for index, upperBound := range requestDurationBuckets {
				_, _ = fmt.Fprintf(w, "basisvr_http_request_duration_seconds_bucket{method=%q,route=%q,status=%q,le=%q} %d\n",
					key.Method, key.Route, fmt.Sprintf("%d", key.Status), fmt.Sprintf("%g", upperBound), item.Buckets[index])
			}
			_, _ = fmt.Fprintf(w, "basisvr_http_request_duration_seconds_bucket{method=%q,route=%q,status=%q,le=%q} %d\n",
				key.Method, key.Route, fmt.Sprintf("%d", key.Status), "+Inf", item.Count)
			_, _ = fmt.Fprintf(w, "basisvr_http_request_duration_seconds_sum{method=%q,route=%q,status=%q} %.6f\n",
				key.Method, key.Route, fmt.Sprintf("%d", key.Status), item.DurationSum)
			_, _ = fmt.Fprintf(w, "basisvr_http_request_duration_seconds_count{method=%q,route=%q,status=%q} %d\n",
				key.Method, key.Route, fmt.Sprintf("%d", key.Status), item.Count)
		}
		if realtimeProvider != nil {
			writeRealtimeMetrics(w, realtimeProvider())
		}
		if federationProvider != nil {
			metricsCtx, cancel := context.WithTimeout(r.Context(), time.Second)
			snapshot, err := federationProvider(metricsCtx)
			cancel()
			writeFederationMetrics(w, snapshot, err)
		}
	})
}

func writeFederationMetrics(w http.ResponseWriter, snapshot FederationMetrics, scrapeErr error) {
	_, _ = fmt.Fprintln(w, "# HELP basisvr_activitypub_inbox_messages Stored inbox messages by processing state.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_activitypub_inbox_messages gauge")
	_, _ = fmt.Fprintf(w, "basisvr_activitypub_inbox_messages{state=%q} %d\n", "pending", snapshot.InboxPending)
	_, _ = fmt.Fprintf(w, "basisvr_activitypub_inbox_messages{state=%q} %d\n", "failed", snapshot.InboxFailed)
	_, _ = fmt.Fprintln(w, "# HELP basisvr_activitypub_outbox_jobs Delivery jobs by state.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_activitypub_outbox_jobs gauge")
	_, _ = fmt.Fprintf(w, "basisvr_activitypub_outbox_jobs{state=%q} %d\n", "pending", snapshot.OutboxPending)
	_, _ = fmt.Fprintf(w, "basisvr_activitypub_outbox_jobs{state=%q} %d\n", "retry", snapshot.OutboxRetry)
	_, _ = fmt.Fprintf(w, "basisvr_activitypub_outbox_jobs{state=%q} %d\n", "failed", snapshot.OutboxFailed)
	_, _ = fmt.Fprintln(w, "# HELP basisvr_activitypub_outbox_oldest_pending_seconds Age of the oldest pending or retry delivery job.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_activitypub_outbox_oldest_pending_seconds gauge")
	_, _ = fmt.Fprintf(w, "basisvr_activitypub_outbox_oldest_pending_seconds %.0f\n", snapshot.OldestPendingSeconds)
	_, _ = fmt.Fprintln(w, "# HELP basisvr_activitypub_metrics_scrape_error Whether federation queue metrics failed to load.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_activitypub_metrics_scrape_error gauge")
	if scrapeErr != nil {
		_, _ = fmt.Fprintln(w, "basisvr_activitypub_metrics_scrape_error 1")
	} else {
		_, _ = fmt.Fprintln(w, "basisvr_activitypub_metrics_scrape_error 0")
	}
}

func writeRealtimeMetrics(w http.ResponseWriter, snapshot RealtimeMetrics) {
	_, _ = fmt.Fprintln(w, "# HELP basisvr_realtime_replay_stored_total Events stored for realtime replay.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_realtime_replay_stored_total counter")
	_, _ = fmt.Fprintf(w, "basisvr_realtime_replay_stored_total %d\n", snapshot.ReplayStored)
	_, _ = fmt.Fprintln(w, "# HELP basisvr_realtime_replay_store_failures_total Failed attempts to store replay events.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_realtime_replay_store_failures_total counter")
	_, _ = fmt.Fprintf(w, "basisvr_realtime_replay_store_failures_total %d\n", snapshot.ReplayStoreFailures)
	_, _ = fmt.Fprintln(w, "# HELP basisvr_realtime_replay_requests_total Realtime replay requests.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_realtime_replay_requests_total counter")
	_, _ = fmt.Fprintf(w, "basisvr_realtime_replay_requests_total %d\n", snapshot.ReplayRequests)
	_, _ = fmt.Fprintln(w, "# HELP basisvr_realtime_replay_delivered_total Events delivered from replay storage.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_realtime_replay_delivered_total counter")
	_, _ = fmt.Fprintf(w, "basisvr_realtime_replay_delivered_total %d\n", snapshot.ReplayDelivered)
	_, _ = fmt.Fprintln(w, "# HELP basisvr_realtime_replay_resync_required_total Replay requests requiring canonical state reload.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_realtime_replay_resync_required_total counter")
	_, _ = fmt.Fprintf(w, "basisvr_realtime_replay_resync_required_total %d\n", snapshot.ReplayResyncs)
	_, _ = fmt.Fprintln(w, "# HELP basisvr_realtime_active_subscribers Current SSE and WebSocket subscriptions.")
	_, _ = fmt.Fprintln(w, "# TYPE basisvr_realtime_active_subscribers gauge")
	_, _ = fmt.Fprintf(w, "basisvr_realtime_active_subscribers %d\n", snapshot.ActiveSubscribers)
}

func (m *Metrics) addInFlight(delta int) {
	m.mu.Lock()
	m.inFlight += delta
	m.mu.Unlock()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func routePattern(r *http.Request) string {
	if ctx := chi.RouteContext(r.Context()); ctx != nil {
		if pattern := ctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	if strings.TrimSpace(r.URL.Path) == "" {
		return "unknown"
	}
	return r.URL.Path
}
