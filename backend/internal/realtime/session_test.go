package realtime

import (
	"basisvr-social-service/internal/auth"
	"context"
	"errors"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebSocketClosesOnRevocationFailureAndExpiry(t *testing.T) {
	for _, mode := range []string{"revoked", "unavailable", "expired"} {
		t.Run(mode, func(t *testing.T) {
			h := NewHandler(NewBroker(BrokerConfig{})).SetSessionValidator(func(context.Context, auth.Principal) (bool, error) {
				if mode == "unavailable" {
					return false, errors.New("dependency unavailable")
				}
				return mode == "expired", nil
			})
			h.sessionCheckInterval = 10 * time.Millisecond
			expires := time.Now().Add(time.Hour)
			if mode == "expired" {
				expires = time.Now().Add(30 * time.Millisecond)
			}
			principal := auth.Principal{UserID: uuid.New(), ActorID: uuid.New(), SessionID: uuid.New(), ExpiresAt: expires}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				h.WebSocket(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), principal)))
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			var event Event
			if err = wsjson.Read(ctx, conn, &event); err != nil {
				t.Fatal(err)
			}
			err = wsjson.Read(ctx, conn, &event)
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("close=%v", err)
			}
		})
	}
}
func TestSSEClosesOnRevokedSession(t *testing.T) {
	h := NewHandler(NewBroker(BrokerConfig{})).SetSessionValidator(func(context.Context, auth.Principal) (bool, error) { return false, nil })
	h.sessionCheckInterval = time.Millisecond
	p := auth.Principal{UserID: uuid.New(), ActorID: uuid.New(), ExpiresAt: time.Now().Add(time.Hour)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := httptest.NewRequest("GET", "/api/realtime/events", nil)
		h.Events(httptest.NewRecorder(), r.WithContext(auth.ContextWithPrincipal(r.Context(), p)))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revoked SSE remains open")
	}
}
