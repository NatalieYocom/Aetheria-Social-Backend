package invites

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
	"basisvr-social-service/internal/notifications"
	"basisvr-social-service/internal/privacy"
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
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/api/invites", h.Create)
		r.Get("/api/invites", h.List)
		r.Post("/api/invites/{id}/accept", h.Accept)
		r.Post("/api/invites/{id}/decline", h.Decline)
	})
}

type createInviteRequest struct {
	ToActorID  *uuid.UUID `json:"toActorId"`
	ToAcct     string     `json:"toAcct"`
	WorldID    *uuid.UUID `json:"worldId"`
	InstanceID *uuid.UUID `json:"instanceId"`
	EventID    *uuid.UUID `json:"eventId"`
	Message    string     `json:"message"`
	Visibility string     `json:"visibility"`
	ExpiresAt  *time.Time `json:"expiresAt"`
}

type InviteResponse struct {
	ID         uuid.UUID    `json:"id"`
	From       actorSummary `json:"from"`
	To         actorSummary `json:"to"`
	WorldID    *uuid.UUID   `json:"worldId,omitempty"`
	InstanceID *uuid.UUID   `json:"instanceId,omitempty"`
	EventID    *uuid.UUID   `json:"eventId,omitempty"`
	Message    string       `json:"message"`
	Visibility string       `json:"visibility"`
	State      string       `json:"state"`
	ExpiresAt  time.Time    `json:"expiresAt"`
	CreatedAt  time.Time    `json:"createdAt"`
}

type actorSummary struct {
	ActorID     uuid.UUID `json:"actorId"`
	Acct        string    `json:"acct"`
	DisplayName string    `json:"displayName"`
}

const worldAccessTargetSQL = `SELECT owner_actor_id, visibility FROM worlds WHERE id = $1`
const eventAccessTargetSQL = `SELECT owner_actor_id, visibility FROM events WHERE id = $1`
const instanceInviteTargetSQL = `
SELECT host_actor_id, visibility, status, expires_at
FROM instances
WHERE id = $1`

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req createInviteRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	toActorID, err := h.resolveTargetActor(r.Context(), req)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "target actor not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "resolve_actor_failed", err.Error())
		return
	}
	if toActorID == principal.ActorID {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_target", "cannot invite yourself")
		return
	}
	if req.WorldID == nil && req.InstanceID == nil && req.EventID == nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_target", "worldId, instanceId or eventId is required")
		return
	}
	allowed, err := h.canCreateInviteToTarget(r.Context(), principal.ActorID, req)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeInviteTargetNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "invite_target_access_failed", err.Error())
		return
	}
	if !allowed {
		writeInviteTargetNotFound(w)
		return
	}
	if req.Visibility == "" {
		req.Visibility = "direct"
	}
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	if req.ExpiresAt != nil {
		expiresAt = *req.ExpiresAt
	}

	var id uuid.UUID
	if err := h.db.QueryRowContext(r.Context(), `
INSERT INTO invites (from_actor_id, to_actor_id, instance_id, world_id, event_id, message, visibility, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id`,
		principal.ActorID,
		toActorID,
		dbx.NullUUID(req.InstanceID),
		dbx.NullUUID(req.WorldID),
		dbx.NullUUID(req.EventID),
		req.Message,
		req.Visibility,
		expiresAt,
	).Scan(&id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_invite_failed", err.Error())
		return
	}

	invite, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_invite_failed", err.Error())
		return
	}
	notification, err := notifications.CreateAndPublish(r.Context(), h.db, h.events, notifications.CreateInput{
		ActorID: invite.To.ActorID,
		Type:    "invite.created",
		Payload: inviteEventPayload(invite),
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "notification_failed", err.Error())
		return
	}
	_ = notification
	realtime.PublishActorEvent(h.events, []uuid.UUID{invite.From.ActorID, invite.To.ActorID}, "invite.created", principal.ActorID, inviteEventPayload(invite))
	httpx.WriteJSON(w, http.StatusCreated, invite)
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), inviteQuery()+`
WHERE i.from_actor_id = $1 OR i.to_actor_id = $1
ORDER BY i.created_at DESC
LIMIT 100`, principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_invites_failed", err.Error())
		return
	}
	defer rows.Close()

	invites := []InviteResponse{}
	for rows.Next() {
		invite, err := scanInvite(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_invite_failed", err.Error())
			return
		}
		invites = append(invites, invite)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_invites_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, invites)
}

func (h *Handler) Accept(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, "accepted")
}

func (h *Handler) Decline(w http.ResponseWriter, r *http.Request) {
	h.decide(w, r, "declined")
}

func (h *Handler) decide(w http.ResponseWriter, r *http.Request, state string) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}

	result, err := h.db.ExecContext(r.Context(), `
UPDATE invites
SET state = $3
WHERE id = $1 AND to_actor_id = $2 AND state = 'pending' AND expires_at > now()`,
		id, principal.ActorID, state)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "invite_decision_failed", err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "pending invite not found")
		return
	}

	invite, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_invite_failed", err.Error())
		return
	}
	eventType := "invite.declined"
	if state == "accepted" {
		eventType = "invite.accepted"
	}
	notification, err := notifications.CreateAndPublish(r.Context(), h.db, h.events, notifications.CreateInput{
		ActorID: invite.From.ActorID,
		Type:    eventType,
		Payload: inviteEventPayload(invite),
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "notification_failed", err.Error())
		return
	}
	_ = notification
	realtime.PublishActorEvent(h.events, []uuid.UUID{invite.From.ActorID, invite.To.ActorID}, eventType, principal.ActorID, inviteEventPayload(invite))
	httpx.WriteJSON(w, http.StatusOK, invite)
}

func (h *Handler) resolveTargetActor(ctx context.Context, req createInviteRequest) (uuid.UUID, error) {
	if req.ToActorID != nil {
		return *req.ToActorID, nil
	}
	acct := strings.ToLower(strings.TrimSpace(req.ToAcct))
	if acct == "" {
		return uuid.Nil, sql.ErrNoRows
	}
	var id uuid.UUID
	err := h.db.QueryRowContext(ctx, `
SELECT id FROM actors WHERE lower(acct) = $1 OR lower(preferred_username) = $1`, acct).Scan(&id)
	return id, err
}

func (h *Handler) canCreateInviteToTarget(ctx context.Context, actorID uuid.UUID, req createInviteRequest) (bool, error) {
	if req.WorldID != nil {
		allowed, err := h.canViewOwnedObject(ctx, *req.WorldID, worldAccessTargetSQL, actorID)
		if err != nil || !allowed {
			return allowed, err
		}
	}
	if req.EventID != nil {
		allowed, err := h.canViewOwnedObject(ctx, *req.EventID, eventAccessTargetSQL, actorID)
		if err != nil || !allowed {
			return allowed, err
		}
	}
	if req.InstanceID != nil {
		allowed, err := h.canViewInviteInstance(ctx, *req.InstanceID, actorID)
		if err != nil || !allowed {
			return allowed, err
		}
	}
	return true, nil
}

func (h *Handler) canViewOwnedObject(ctx context.Context, id uuid.UUID, query string, actorID uuid.UUID) (bool, error) {
	var ownerID uuid.UUID
	var visibility string
	if err := h.db.QueryRowContext(ctx, query, id).Scan(&ownerID, &visibility); err != nil {
		return false, err
	}
	return privacy.CanView(ctx, h.db, privacy.ViewInput{
		OwnerActorID:  ownerID,
		ViewerActorID: uuid.NullUUID{UUID: actorID, Valid: true},
		Visibility:    visibility,
	})
}

func (h *Handler) canViewInviteInstance(ctx context.Context, instanceID uuid.UUID, actorID uuid.UUID) (bool, error) {
	var hostID uuid.UUID
	var visibility string
	var status string
	var expiresAt sql.NullTime
	if err := h.db.QueryRowContext(ctx, instanceInviteTargetSQL, instanceID).Scan(&hostID, &visibility, &status, &expiresAt); err != nil {
		return false, err
	}
	if status != "active" || (expiresAt.Valid && !expiresAt.Time.After(time.Now().UTC())) {
		return false, nil
	}
	if actorID == hostID || visibility == "public" {
		return true, nil
	}
	switch visibility {
	case "friends":
		var exists bool
		err := h.db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM relationships
  WHERE actor_id = $1 AND target_actor_id = $2 AND type = 'friend' AND state = 'accepted'
)`, actorID, hostID).Scan(&exists)
		return exists, err
	case "invite_only":
		var exists bool
		err := h.db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM invites
  WHERE to_actor_id = $1
    AND instance_id = $2
    AND state IN ('pending', 'accepted')
    AND expires_at > now()
)`, actorID, instanceID).Scan(&exists)
		return exists, err
	default:
		return false, nil
	}
}

func (h *Handler) loadByID(ctx context.Context, id uuid.UUID) (InviteResponse, error) {
	row := h.db.QueryRowContext(ctx, inviteQuery()+` WHERE i.id = $1`, id)
	return scanInvite(row)
}

func inviteQuery() string {
	return `
SELECT i.id,
       from_actor.id, from_actor.acct, from_actor.display_name,
       to_actor.id, to_actor.acct, to_actor.display_name,
       i.world_id, i.instance_id, i.event_id, i.message, i.visibility, i.state, i.expires_at, i.created_at
FROM invites i
JOIN actors from_actor ON from_actor.id = i.from_actor_id
JOIN actors to_actor ON to_actor.id = i.to_actor_id`
}

type scanner interface {
	Scan(dest ...any) error
}

func scanInvite(row scanner) (InviteResponse, error) {
	var invite InviteResponse
	var worldID uuid.NullUUID
	var instanceID uuid.NullUUID
	var eventID uuid.NullUUID
	if err := row.Scan(
		&invite.ID,
		&invite.From.ActorID,
		&invite.From.Acct,
		&invite.From.DisplayName,
		&invite.To.ActorID,
		&invite.To.Acct,
		&invite.To.DisplayName,
		&worldID,
		&instanceID,
		&eventID,
		&invite.Message,
		&invite.Visibility,
		&invite.State,
		&invite.ExpiresAt,
		&invite.CreatedAt,
	); err != nil {
		return InviteResponse{}, err
	}
	invite.WorldID = dbx.UUIDPtr(worldID)
	invite.InstanceID = dbx.UUIDPtr(instanceID)
	invite.EventID = dbx.UUIDPtr(eventID)
	return invite, nil
}

func inviteEventPayload(invite InviteResponse) map[string]any {
	payload := map[string]any{
		"inviteId":    invite.ID.String(),
		"fromActorId": invite.From.ActorID.String(),
		"toActorId":   invite.To.ActorID.String(),
		"state":       invite.State,
	}
	if invite.WorldID != nil {
		payload["worldId"] = invite.WorldID.String()
	}
	if invite.InstanceID != nil {
		payload["instanceId"] = invite.InstanceID.String()
	}
	if invite.EventID != nil {
		payload["eventId"] = invite.EventID.String()
	}
	return payload
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func writeInviteTargetNotFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "invite target not found")
}
