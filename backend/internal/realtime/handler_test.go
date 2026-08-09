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

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
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

func TestHandlerReplaysSSEEventsAfterLastEventID(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	broker.AttachReplayStore(stubReplayStore{
		replay: func(_ context.Context, gotActorID uuid.UUID, after string, limit int) (ReplayResult, error) {
			if gotActorID != actorID || after != "1710000000000-0" || limit <= 0 {
				t.Fatalf("Replay(%s, %q, %d)", gotActorID, after, limit)
			}
			return ReplayResult{Events: []Event{{
				ID: uuid.NewString(), Cursor: "1710000000001-0", Type: "presence.updated", ActorID: actorID,
			}}}, nil
		},
	}, 100*time.Millisecond)
	handler := NewHandler(broker)
	principal := auth.Principal{UserID: uuid.New(), ActorID: actorID, Username: "alice"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/realtime/events", nil).
		WithContext(auth.ContextWithPrincipal(ctx, principal))
	req.Header.Set("Last-Event-ID", "1710000000000-0")
	res := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.Events(res, req)
	}()

	_ = res.waitForWrite(t)
	replayed := res.waitForWrite(t)
	if !strings.Contains(replayed, "id: 1710000000001-0") || !strings.Contains(replayed, "event: presence.updated") {
		t.Fatalf("replayed frame = %q", replayed)
	}
	cancel()
	<-done
}

func TestHandlerRejectsMalformedReplayCursor(t *testing.T) {
	handler := NewHandler(NewBroker(BrokerConfig{}))
	principal := auth.Principal{UserID: uuid.New(), ActorID: uuid.New(), Username: "alice"}
	req := httptest.NewRequest(http.MethodGet, "/api/realtime/events", nil).
		WithContext(auth.ContextWithPrincipal(context.Background(), principal))
	req.Header.Set("Last-Event-ID", "not-a-stream-id")
	res := httptest.NewRecorder()

	handler.Events(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestHandlerSignalsReplayResyncWhenCursorWasTrimmed(t *testing.T) {
	broker := NewBroker(BrokerConfig{})
	actorID := uuid.New()
	broker.AttachReplayStore(stubReplayStore{
		replay: func(context.Context, uuid.UUID, string, int) (ReplayResult, error) {
			return ReplayResult{Truncated: true}, nil
		},
	}, 100*time.Millisecond)
	handler := NewHandler(broker)
	principal := auth.Principal{UserID: uuid.New(), ActorID: actorID, Username: "alice"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/realtime/events", nil).
		WithContext(auth.ContextWithPrincipal(ctx, principal))
	req.Header.Set("Last-Event-ID", "1710000000000-0")
	res := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.Events(res, req)
	}()

	_ = res.waitForWrite(t)
	frame := res.waitForWrite(t)
	if !strings.Contains(frame, "event: realtime.resync_required") || !strings.Contains(frame, `"reason":"cursor_trimmed"`) {
		t.Fatalf("resync frame = %q", frame)
	}
	cancel()
	<-done
}

func TestHandlerClosesStreamAfterUserSuspension(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	handler := NewHandler(broker)
	actorID := uuid.New()
	principal := auth.Principal{UserID: uuid.New(), ActorID: actorID, Username: "alice"}
	req := httptest.NewRequest(http.MethodGet, "/api/realtime/events", nil).
		WithContext(auth.ContextWithPrincipal(context.Background(), principal))
	res := newStreamRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.Events(res, req)
	}()
	_ = res.waitForWrite(t)

	broker.Publish(actorID, Event{Type: "user.suspended", ActorID: actorID})
	frame := res.waitForWrite(t)
	if !strings.Contains(frame, "event: user.suspended") {
		t.Fatalf("frame = %q", frame)
	}
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("stream remained open after suspension")
	}
}

func TestWebSocketStreamsActorEvents(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	handler := NewHandler(broker)
	actorID := uuid.New()
	principal := auth.Principal{UserID: uuid.New(), ActorID: actorID, Username: "alice"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.WebSocket(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), principal)))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), &websocket.DialOptions{
		Subprotocols: []string{"basisvr.realtime.v1", "bearer.test-token"},
	})
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.CloseNow()
	if conn.Subprotocol() != "basisvr.realtime.v1" {
		t.Fatalf("negotiated subprotocol = %q", conn.Subprotocol())
	}
	var event Event
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatalf("read connected event: %v", err)
	}
	if event.Type != "realtime.connected" || event.ActorID != actorID {
		t.Fatalf("connected event = %+v", event)
	}

	broker.Publish(actorID, Event{Type: "presence.updated", Payload: map[string]any{"status": "online"}})
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatalf("read published event: %v", err)
	}
	if event.Type != "presence.updated" || event.ActorID != actorID {
		t.Fatalf("published event = %+v", event)
	}
}

func TestWebSocketReplaysEventsAfterCursor(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	broker.AttachReplayStore(stubReplayStore{
		replay: func(_ context.Context, _ uuid.UUID, after string, _ int) (ReplayResult, error) {
			if after != "1710000000000-0" {
				t.Fatalf("after = %q", after)
			}
			return ReplayResult{Events: []Event{{
				ID: uuid.NewString(), Cursor: "1710000000001-0", Type: "presence.updated", ActorID: actorID,
			}}}, nil
		},
	}, 100*time.Millisecond)
	handler := NewHandler(broker)
	principal := auth.Principal{UserID: uuid.New(), ActorID: actorID, Username: "alice"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.WebSocket(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), principal)))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"?since=1710000000000-0", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	var event Event
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatal(err)
	}
	if err := wsjson.Read(ctx, conn, &event); err != nil {
		t.Fatal(err)
	}
	if event.Cursor != "1710000000001-0" || event.Type != "presence.updated" {
		t.Fatalf("replayed event = %+v", event)
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
