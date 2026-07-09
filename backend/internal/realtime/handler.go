package realtime

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/httpx"

	"github.com/go-chi/chi/v5"
)

type Handler struct {
	broker *Broker
}

func NewHandler(broker *Broker) *Handler {
	if broker == nil {
		broker = NewBroker(BrokerConfig{})
	}
	return &Handler{broker: broker}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.With(authMiddleware).Get("/api/realtime/events", h.Events)
	r.With(authMiddleware).Get("/api/ws", h.Events)
}

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
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
			writeSSE(w, flusher, event)
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
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, data)
	flusher.Flush()
}
