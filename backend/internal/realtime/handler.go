package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/httpx"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	broker         *Broker
	originPatterns []string
	replayLimit    int
}

func NewHandler(broker *Broker, allowedOrigins ...string) *Handler {
	if broker == nil {
		broker = NewBroker(BrokerConfig{})
	}
	patterns := make([]string, 0, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		parsed, err := url.Parse(strings.TrimSpace(origin))
		if err == nil && parsed.Host != "" {
			patterns = append(patterns, parsed.Host)
		}
	}
	return &Handler{broker: broker, originPatterns: patterns, replayLimit: 500}
}

func (h *Handler) SetReplayLimit(limit int) *Handler {
	if limit > 0 {
		h.replayLimit = limit
	}
	return h
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.With(authMiddleware).Get("/api/realtime/events", h.Events)
	r.With(authMiddleware).Get("/api/ws", h.WebSocket)
}

func (h *Handler) WebSocket(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	cursor := strings.TrimSpace(r.URL.Query().Get("since"))
	if cursor != "" && !validReplayCursor(cursor) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_replay_cursor", "realtime replay cursor is invalid")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
		Subprotocols:    []string{"basisvr.realtime.v1"},
		OriginPatterns:  h.originPatterns,
	})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 * 1024)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = conn.CloseRead(ctx)
	events, unsubscribe := h.broker.Subscribe(ctx, principal.ActorID)
	defer unsubscribe()

	connected := normalizeEvent(principal.ActorID, Event{
		ID: uuid.NewString(), Type: "realtime.connected", ActorID: principal.ActorID,
		Payload: map[string]any{"username": principal.Username},
	})
	if err := writeWebSocketEvent(ctx, conn, connected); err != nil {
		return
	}
	lastCursor := cursor
	if cursor != "" {
		result, replayErr := h.broker.Replay(ctx, principal.ActorID, cursor, h.replayLimit)
		if replayErr != nil || result.Truncated {
			if err := writeWebSocketEvent(ctx, conn, resyncRequiredEvent(principal.ActorID, replayErr, result.Truncated)); err != nil {
				return
			}
		} else {
			for _, event := range result.Events {
				if err := writeWebSocketEvent(ctx, conn, event); err != nil {
					return
				}
				lastCursor = event.Cursor
			}
		}
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if replayedOrOlder(event.Cursor, lastCursor) {
				continue
			}
			if err := writeWebSocketEvent(ctx, conn, event); err != nil {
				return
			}
			lastCursor = event.Cursor
			if event.Type == "user.suspended" {
				_ = conn.Close(websocket.StatusPolicyViolation, "account suspended")
				return
			}
		case <-keepalive.C:
			pingCtx, pingCancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				return
			}
		}
	}
}

func writeWebSocketEvent(ctx context.Context, conn *websocket.Conn, event Event) error {
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return wsjson.Write(writeCtx, conn, event)
}

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	cursor := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if cursor != "" && !validReplayCursor(cursor) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_replay_cursor", "realtime replay cursor is invalid")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support streaming")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	events, unsubscribe := h.broker.Subscribe(r.Context(), principal.ActorID)
	defer unsubscribe()

	writeSSE(w, flusher, Event{
		Type:    "realtime.connected",
		ActorID: principal.ActorID,
		Payload: map[string]any{
			"username": principal.Username,
		},
	})
	lastCursor := cursor
	if cursor != "" {
		result, replayErr := h.broker.Replay(r.Context(), principal.ActorID, cursor, h.replayLimit)
		if replayErr != nil || result.Truncated {
			writeSSE(w, flusher, resyncRequiredEvent(principal.ActorID, replayErr, result.Truncated))
		} else {
			for _, event := range result.Events {
				writeSSE(w, flusher, event)
				lastCursor = event.Cursor
			}
		}
	}

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if replayedOrOlder(event.Cursor, lastCursor) {
				continue
			}
			writeSSE(w, flusher, event)
			lastCursor = event.Cursor
			if event.Type == "user.suspended" {
				return
			}
		case <-keepalive.C:
			_, _ = w.Write([]byte(": keepalive\n\n"))
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event Event) {
	if event.ID == "" {
		event.ID = fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	eventType := strings.ReplaceAll(event.Type, "\n", "")
	cursor := strings.ReplaceAll(event.Cursor, "\n", "")
	if cursor != "" {
		_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", cursor, eventType, data)
	} else {
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	}
	flusher.Flush()
}

func resyncRequiredEvent(actorID uuid.UUID, replayErr error, truncated bool) Event {
	reason := "replay_unavailable"
	if truncated {
		reason = "cursor_trimmed"
	} else if errors.Is(replayErr, ErrInvalidReplayCursor) {
		reason = "invalid_cursor"
	}
	return normalizeEvent(actorID, Event{
		Type: "realtime.resync_required", ActorID: actorID, Payload: map[string]any{"reason": reason},
	})
}

func replayedOrOlder(cursor, lastCursor string) bool {
	return validReplayCursor(cursor) && validReplayCursor(lastCursor) && compareStreamIDs(cursor, lastCursor) <= 0
}
