package presence

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/realtime"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type Handler struct {
	db     *sql.DB
	events *realtime.Broker
}

func NewHandler(db *sql.DB, brokers ...*realtime.Broker) *Handler {
	var broker *realtime.Broker
	if len(brokers) > 0 {
		broker = brokers[0]
	}
	return &Handler{db: db, events: broker}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.With(authMiddleware).Post("/api/presence", h.Upsert)
	r.With(authMiddleware).Delete("/api/presence", h.Delete)
	r.With(authMiddleware).Get("/api/presence/me", h.Me)
	r.With(authMiddleware).Get("/api/presence/friends", h.Friends)
}

type upsertPresenceRequest struct {
	WorldID           *uuid.UUID `json:"worldId"`
	InstanceID        *uuid.UUID `json:"instanceId"`
	Status            string     `json:"status"`
	Visibility        string     `json:"visibility"`
	ShowExactInstance bool       `json:"showExactInstance"`
	ExpiresAt         *time.Time `json:"expiresAt"`
}

type PresenceResponse struct {
	ID                uuid.UUID  `json:"id"`
	ActorID           uuid.UUID  `json:"actorId"`
	Acct              string     `json:"acct,omitempty"`
	DisplayName       string     `json:"displayName,omitempty"`
	WorldID           *uuid.UUID `json:"worldId,omitempty"`
	InstanceID        *uuid.UUID `json:"instanceId,omitempty"`
	Status            string     `json:"status"`
	Visibility        string     `json:"visibility"`
	ShowExactInstance bool       `json:"showExactInstance"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

func (h *Handler) Upsert(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req upsertPresenceRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	status := normalizeStatus(req.Status)
	if status == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_status", "status must be online, away, busy or invisible")
		return
	}
	visibility := normalizeVisibility(req.Visibility)
	if visibility == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_visibility", "visibility must be nobody, friends, followers or public")
		return
	}
	expiresAt := time.Now().UTC().Add(90 * time.Second)
	if req.ExpiresAt != nil {
		expiresAt = *req.ExpiresAt
	}

	var id uuid.UUID
	if err := h.db.QueryRowContext(r.Context(), `
INSERT INTO presence_sessions (actor_id, world_id, instance_id, status, visibility, show_exact_instance, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (actor_id)
DO UPDATE SET world_id = EXCLUDED.world_id,
              instance_id = EXCLUDED.instance_id,
              status = EXCLUDED.status,
              visibility = EXCLUDED.visibility,
              show_exact_instance = EXCLUDED.show_exact_instance,
              expires_at = EXCLUDED.expires_at,
              updated_at = now()
RETURNING id`,
		principal.ActorID,
		dbx.NullUUID(req.WorldID),
		dbx.NullUUID(req.InstanceID),
		status,
		visibility,
		req.ShowExactInstance,
		expiresAt,
	).Scan(&id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update_presence_failed", err.Error())
		return
	}

	presence, err := h.loadByActor(r.Context(), principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_presence_failed", err.Error())
		return
	}
	presence.ID = id
	_ = realtime.PublishPresenceChanged(r.Context(), h.db, h.events, principal.ActorID)
	httpx.WriteJSON(w, http.StatusOK, presence)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM presence_sessions WHERE actor_id = $1`, principal.ActorID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "delete_presence_failed", err.Error())
		return
	}
	_ = realtime.PublishPresenceRemoved(r.Context(), h.db, h.events, principal.ActorID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	presence, err := h.loadByActor(r.Context(), principal.ActorID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "presence not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_presence_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, presence)
}

func (h *Handler) Friends(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT ps.id, ps.actor_id, a.acct, a.display_name, ps.world_id,
       CASE WHEN ps.show_exact_instance THEN ps.instance_id ELSE NULL END AS instance_id,
       ps.status, ps.visibility, ps.show_exact_instance, ps.expires_at, ps.updated_at
FROM relationships rel
JOIN presence_sessions ps ON ps.actor_id = rel.target_actor_id
JOIN actors a ON a.id = ps.actor_id
WHERE rel.actor_id = $1
  AND rel.type = 'friend'
  AND rel.state = 'accepted'
  AND ps.visibility IN ('friends', 'public')
  AND ps.status <> 'invisible'
  AND ps.expires_at > now()
ORDER BY ps.updated_at DESC`, principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_presence_failed", err.Error())
		return
	}
	defer rows.Close()

	items := []PresenceResponse{}
	for rows.Next() {
		item, err := scanPresence(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_presence_failed", err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_presence_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, items)
}

func (h *Handler) loadByActor(ctx context.Context, actorID uuid.UUID) (PresenceResponse, error) {
	row := h.db.QueryRowContext(ctx, `
SELECT ps.id, ps.actor_id, a.acct, a.display_name, ps.world_id, ps.instance_id,
       ps.status, ps.visibility, ps.show_exact_instance, ps.expires_at, ps.updated_at
FROM presence_sessions ps
JOIN actors a ON a.id = ps.actor_id
WHERE ps.actor_id = $1`, actorID)
	return scanPresence(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPresence(row scanner) (PresenceResponse, error) {
	var presence PresenceResponse
	var worldID uuid.NullUUID
	var instanceID uuid.NullUUID
	if err := row.Scan(
		&presence.ID,
		&presence.ActorID,
		&presence.Acct,
		&presence.DisplayName,
		&worldID,
		&instanceID,
		&presence.Status,
		&presence.Visibility,
		&presence.ShowExactInstance,
		&presence.ExpiresAt,
		&presence.UpdatedAt,
	); err != nil {
		return PresenceResponse{}, err
	}
	presence.WorldID = dbx.UUIDPtr(worldID)
	presence.InstanceID = dbx.UUIDPtr(instanceID)
	return presence, nil
}

func normalizeStatus(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "online"
	}
	switch value {
	case "online", "away", "busy", "invisible":
		return value
	default:
		return ""
	}
}

func normalizeVisibility(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "nobody"
	}
	switch value {
	case "nobody", "friends", "followers", "public":
		return value
	default:
		return ""
	}
}
