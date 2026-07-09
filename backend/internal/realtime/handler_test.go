package realtime

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/auth"

	"github.com/google/uuid"
)

func TestHandlerStreamsPublishedEventsAsSSE(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	handler := NewHandler(broker)
	actorID := uuid.New()
	principal := auth.Principal{UserID: uuid.New(), ActorID: actorID, Username: "alice"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/realtime/events", nil).
		WithContext(auth.ContextWithPrincipal(ctx, principal))
	res := newStreamRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.Events(res, req)
	}()

	firstFrame := res.waitForWrite(t)
	if !strings.Contains(firstFrame, "event: realtime.connected") {
		t.Fatalf("first frame = %q", firstFrame)
	}

	broker.Publish(actorID, Event{
		Type:    "presence.updated",
		ActorID: actorID,
		Payload: map[string]any{"status": "online"},
	})

	frame := res.waitForWrite(t)
	if !strings.Contains(frame, "event: presence.updated") {
		t.Fatalf("event frame = %q", frame)
	}
	if !strings.Contains(frame, `"type":"presence.updated"`) {
		t.Fatalf("event frame missing JSON event type: %q", frame)
	}
	if !strings.Contains(frame, `"actorId":"`+actorID.String()+`"`) {
		t.Fatalf("event frame missing actor ID: %q", frame)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stream did not stop after context cancel")
	}
}

func TestHandlerRequiresAuthentication(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	handler := NewHandler(broker)
	req := httptest.NewRequest(http.MethodGet, "/api/realtime/events", nil)
	res := httptest.NewRecorder()

	handler.Events(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

type streamRecorder struct {
	header http.Header
	writes chan string
	body   bytes.Buffer
}

func newStreamRecorder() *streamRecorder {
	return &streamRecorder{
		header: make(http.Header),
		writes: make(chan string, 8),
	}
}

func (r *streamRecorder) Header() http.Header {
	return r.header
}

func (r *streamRecorder) WriteHeader(statusCode int) {
}

func (r *streamRecorder) Write(data []byte) (int, error) {
	r.body.Write(data)
	r.writes <- string(data)
	return len(data), nil
}

func (r *streamRecorder) Flush() {
}

func (r *streamRecorder) waitForWrite(t *testing.T) string {
	t.Helper()
	select {
	case data := <-r.writes:
		return data
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for stream write")
		return ""
	}
}
