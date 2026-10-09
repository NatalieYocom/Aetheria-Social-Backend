package integration

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

func TestRealtimeSSEFlushesThroughProductionObservabilityMiddleware(t *testing.T) {
	_, router := stage8Setup(t, func(cfg *config.Config) {
		cfg.Observability.JSONLogsEnabled = true
		cfg.Observability.MetricsEnabled = true
	})
	user := registerIntegrationUser(t, router, "sse-"+uuid.NewString()[:8])
	server := httptest.NewServer(router)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/api/v1/realtime/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+user.AccessToken)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("stream request failed: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream response: status%d type%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	readEvent := func(expected string) {
		t.Helper()
		for scanner.Scan() {
			if scanner.Text() == "event: "+expected {
				return
			}
		}
		t.Fatalf("%s was not flushed: %v", expected, scanner.Err())
	}
	readEvent("realtime.connected")
	// Initial headers alone are insufficient: a later domain mutation must also
	// stream through both nested observability recorders without closing HTTP.
	requestJSON(t, router, "POST", "/api/v1/presence", user.AccessToken, "", map[string]any{"status": "away", "visibility": "nobody"}, 200)
	readEvent("presence.updated")
	cancel()
	response.Body.Close()
}
