package realtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"basisvr-social-service/internal/auth"
	"github.com/google/uuid"
)

// Embedded as ResponseWriter, deliberately hiding the underlying Flusher.
type unwrappedWriter struct{ http.ResponseWriter }

func (w unwrappedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type plainWriter struct{ http.ResponseWriter }

func TestSSEUnsupportedTransportRemainsExplicitFailure(t *testing.T) {
	broker := NewBroker(BrokerConfig{})
	handler := NewHandler(broker)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/realtime/events", nil)
	req = req.WithContext(auth.ContextWithPrincipal(context.Background(), auth.Principal{ActorID: uuid.New()}))
	handler.Events(unwrappedWriter{plainWriter{recorder}}, req)
	if recorder.Code != 500 || !strings.Contains(recorder.Body.String(), "streaming_unsupported") {
		t.Fatalf("unexpected unsupported transport response: %d %s", recorder.Code, recorder.Body.String())
	}
	if broker.Stats().ActiveSubscribers != 0 {
		t.Fatal("unsupported stream leaked subscriber")
	}
}
