package notifications

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/realtime"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	db     *sql.DB
	events *realtime.Broker
}

func NewHandler(db *sql.DB, broker *realtime.Broker) *Handler {
	return &Handler{db: db, events: broker}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Get("/api/notifications", h.List)
		r.Get("/api/notifications/unread-count", h.UnreadCount)
		r.Post("/api/notifications/read-all", h.MarkAllRead)
		r.Post("/api/notifications/{id}/read", h.MarkRead)
	})
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	limit := parseLimit(r, 50, 100)
	unreadOnly := r.URL.Query().Get("unreadOnly") == "true"
	query := listNotificationsSQL
	if unreadOnly {
		query = listUnreadNotificationsSQL
	}
	rows, err := h.db.QueryContext(r.Context(), query, principal.ActorID, limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_notifications_failed", err.Error())
		return
	}
	defer rows.Close()

	items := []NotificationResponse{}
	for rows.Next() {
		item, err := scanNotification(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_notification_failed", err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_notifications_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) UnreadCount(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var count int
	if err := h.db.QueryRowContext(r.Context(), unreadCountSQL, principal.ActorID).Scan(&count); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "count_notifications_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"count": count})
}

func (h *Handler) MarkRead(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "id must be a uuid")
		return
	}
	notification, err := scanNotification(h.db.QueryRowContext(r.Context(), markReadSQL, id, principal.ActorID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "notification not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "mark_notification_read_failed", err.Error())
		return
	}
	realtime.PublishActorEvent(h.events, []uuid.UUID{principal.ActorID}, "notification.read", principal.ActorID, notification)
	httpx.WriteJSON(w, http.StatusOK, notification)
}

func (h *Handler) MarkAllRead(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	result, err := h.db.ExecContext(r.Context(), markAllReadSQL, principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "mark_notifications_read_failed", err.Error())
		return
	}
	updated, _ := result.RowsAffected()
	realtime.PublishActorEvent(h.events, []uuid.UUID{principal.ActorID}, "notifications.read_all", principal.ActorID, map[string]int64{"updated": updated})
	httpx.WriteJSON(w, http.StatusOK, map[string]int64{"updated": updated})
}

func parseLimit(r *http.Request, fallback int, max int) int {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	if parsed > max {
		return max
	}
	return parsed
}
