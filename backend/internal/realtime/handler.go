package realtime

import (
	"basisvr-social-service/internal/privacy"
	"context"
	"database/sql"
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
	privacyDB            *sql.DB
	broker               *Broker
	originPatterns       []string
	replayLimit          int
	sessionValidator     auth.SessionValidator
	sessionCheckInterval time.Duration
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

// SetSessionValidator bounds active streams after logout/revocation and dependency outages.
func (h *Handler) SetSessionValidator(validate auth.SessionValidator) *Handler {
	h.sessionValidator = validate
	h.sessionCheckInterval = 5 * time.Second
	return h
}
func (h *Handler) sessionChecks(ctx context.Context, p auth.Principal) (<-chan time.Time, func(), func() bool) {
	if h.sessionValidator == nil {
		return nil, func() {}, func() bool { return true }
	}
	interval := h.sessionCheckInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	return ticker.C, ticker.Stop, func() bool {
		if !time.Now().Before(p.ExpiresAt) {
			return false
		}
		checkCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		valid, err := h.sessionValidator(checkCtx, p)
		return err == nil && valid
	}
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
				if err := writeWebSocketEvent(ctx, conn, h.privateEvent(ctx, principal.ActorID, event)); err != nil {
					return
				}
				lastCursor = event.Cursor
			}
		}
	}

	sessionTick, stopSessionChecks, validSession := h.sessionChecks(ctx, principal)
	defer stopSessionChecks()
	expiryDelay := time.Until(principal.ExpiresAt)
	if principal.ExpiresAt.IsZero() {
		expiryDelay = 24 * time.Hour
	}
	expiry := time.NewTimer(max(time.Duration(0), expiryDelay))
	defer expiry.Stop()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-expiry.C:
			_ = conn.Close(websocket.StatusPolicyViolation, "session expired")
			return
		case <-sessionTick:
			if !validSession() {
				_ = conn.Close(websocket.StatusPolicyViolation, "session no longer active")
				return
			}
		case event, ok := <-events:
			if !ok {
				return
			}
			if replayedOrOlder(event.Cursor, lastCursor) {
				continue
			}
			if err := writeWebSocketEvent(ctx, conn, h.privateEvent(ctx, principal.ActorID, event)); err != nil {
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
	// Logging/metrics wrap ResponseWriter and expose Unwrap rather than Flusher.
	// ResponseController follows that chain to the actual HTTP transport.
	controller := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	if err := controller.Flush(); err != nil {
		if errors.Is(err, http.ErrNotSupported) {
			w.Header().Del("Connection")
			w.Header().Del("X-Accel-Buffering")
			httpx.WriteError(w, http.StatusInternalServerError, "streaming_unsupported", "response writer does not support streaming")
		}
		return
	}

	events, unsubscribe := h.broker.Subscribe(r.Context(), principal.ActorID)
	defer unsubscribe()

	if err := writeSSE(w, controller, Event{
		Type:    "realtime.connected",
		ActorID: principal.ActorID,
		Payload: map[string]any{
			"username": principal.Username,
		},
	}); err != nil {
		return
	}
	lastCursor := cursor
	if cursor != "" {
		result, replayErr := h.broker.Replay(r.Context(), principal.ActorID, cursor, h.replayLimit)
		if replayErr != nil || result.Truncated {
			if err := writeSSE(w, controller, resyncRequiredEvent(principal.ActorID, replayErr, result.Truncated)); err != nil {
				return
			}
		} else {
			for _, event := range result.Events {
				if err := writeSSE(w, controller, h.privateEvent(r.Context(), principal.ActorID, event)); err != nil {
					return
				}
				lastCursor = event.Cursor
			}
		}
	}

	sessionTick, stopSessionChecks, validSession := h.sessionChecks(r.Context(), principal)
	defer stopSessionChecks()
	expiryDelay := time.Until(principal.ExpiresAt)
	if principal.ExpiresAt.IsZero() {
		expiryDelay = 24 * time.Hour
	}
	expiry := time.NewTimer(max(time.Duration(0), expiryDelay))
	defer expiry.Stop()
	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-expiry.C:
			return
		case <-sessionTick:
			if !validSession() {
				return
			}
		case event, ok := <-events:
			if !ok {
				return
			}
			if replayedOrOlder(event.Cursor, lastCursor) {
				continue
			}
			if err := writeSSE(w, controller, h.privateEvent(r.Context(), principal.ActorID, event)); err != nil {
				return
			}
			lastCursor = event.Cursor
			if event.Type == "user.suspended" {
				return
			}
		case <-keepalive.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, controller *http.ResponseController, event Event) error {
	if event.ID == "" {
		event.ID = fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	eventType := strings.ReplaceAll(event.Type, "\n", "")
	cursor := strings.ReplaceAll(event.Cursor, "\n", "")
	if cursor != "" {
		_, err = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", cursor, eventType, data)
	} else {
		_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	}
	if err != nil {
		return err
	}
	return controller.Flush()
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

func (h *Handler) SetPrivacyDB(db *sql.DB) *Handler { h.privacyDB = db; return h }
func (h *Handler) privateEvent(ctx context.Context, viewer uuid.UUID, event Event) Event {
	if h.privacyDB == nil || event.ActorID == uuid.Nil {
		return event
	}
	if event.Type == "presence.updated" {
		payload, err := loadPresenceForRealtime(ctx, h.privacyDB, event.ActorID)
		allowed := err == nil && payload.ExpiresAt.After(time.Now())
		if allowed && viewer != event.ActorID {
			var friend bool
			err = h.privacyDB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM relationships WHERE actor_id=$1 AND target_actor_id=$2 AND type='friend' AND state='accepted')`, viewer, event.ActorID).Scan(&friend)
			blocked, blockErr := privacy.HasBlock(ctx, h.privacyDB, viewer, event.ActorID)
			allowed = err == nil && blockErr == nil && friend && !blocked && payload.visibleToFriends()
			payload = payload.publicView()
		}
		if allowed {
			event.Payload = payload
		} else {
			event.Type = "presence.removed"
			event.Payload = map[string]string{"actorId": event.ActorID.String()}
		}
		return event
	}
	if viewer != event.ActorID && event.Type != "presence.removed" {
		blocked, err := privacy.HasBlock(ctx, h.privacyDB, viewer, event.ActorID)
		if blocked || err != nil {
			event.Type = "realtime.resync_required"
			event.ActorID = viewer
			event.Payload = map[string]string{"reason": "privacy_changed"}
		}
	}
	return event
}
