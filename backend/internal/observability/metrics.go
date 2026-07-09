package observability

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

type Metrics struct {
	mu       sync.Mutex
	inFlight int
	requests map[metricKey]requestMetric
}

type metricKey struct {
	Method string
	Route  string
	Status int
}

type requestMetric struct {
	Count       int64
	DurationSum float64
}

func NewMetrics() *Metrics {
	return &Metrics{requests: map[metricKey]requestMetric{}}
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
		item.Count++
		item.DurationSum += time.Since(start).Seconds()
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
		_, _ = fmt.Fprintln(w, "# HELP basisvr_http_request_duration_seconds_sum Total HTTP request duration.")
		_, _ = fmt.Fprintln(w, "# TYPE basisvr_http_request_duration_seconds_sum counter")
		for _, key := range keys {
			item := snapshot[key]
			_, _ = fmt.Fprintf(w, "basisvr_http_request_duration_seconds_sum{method=%q,route=%q,status=%q} %.6f\n",
				key.Method, key.Route, fmt.Sprintf("%d", key.Status), item.DurationSum)
		}
	})
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
