package relationships

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/notifications"
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
		r.Post("/api/relationships/follow", h.Follow)
		r.Post("/api/relationships/unfollow", h.Unfollow)
		r.Post("/api/friends/request", h.FriendRequest)
		r.Post("/api/friends/accept", h.FriendAccept)
		r.Post("/api/friends/reject", h.FriendReject)
		r.Get("/api/friends", h.Friends)
		r.Get("/api/followers", h.Followers)
		r.Get("/api/following", h.Following)
		r.Post("/api/users/{id}/block", h.Block)
		r.Post("/api/users/{id}/unblock", h.Unblock)
	})
}

type actorRefRequest struct {
	ActorID       *uuid.UUID `json:"actorId"`
	TargetActorID *uuid.UUID `json:"targetActorId"`
	Acct          string     `json:"acct"`
	TargetAcct    string     `json:"targetAcct"`
}

type actorSummary struct {
	ActorID     uuid.UUID `json:"actorId"`
	Acct        string    `json:"acct"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	AvatarURL   string    `json:"avatarUrl"`
}

func (h *Handler) Follow(w http.ResponseWriter, r *http.Request) {
	h.edgeAction(w, r, "follow", "accepted", "relationship.followed")
}

func (h *Handler) Unfollow(w http.ResponseWriter, r *http.Request) {
	principal, targetID, ok := h.resolveRequestTarget(w, r)
	if !ok {
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
DELETE FROM relationships
WHERE type = 'follow'
  AND ((actor_id = $1 AND target_actor_id = $2) OR (actor_id = $2 AND target_actor_id = $1))`,
		principal.ActorID, targetID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "unfollow_failed", err.Error())
		return
	}
	realtime.PublishActorEvent(h.events, []uuid.UUID{principal.ActorID, targetID}, "relationship.unfollowed", principal.ActorID, relationshipEventPayload(principal.ActorID, targetID, "follow", "removed"))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) FriendRequest(w http.ResponseWriter, r *http.Request) {
	h.edgeAction(w, r, "friend", "pending", "friend.requested")
}

func (h *Handler) FriendAccept(w http.ResponseWriter, r *http.Request) {
	h.friendDecision(w, r, "accepted", "friend.accepted")
}

func (h *Handler) FriendReject(w http.ResponseWriter, r *http.Request) {
	h.friendDecision(w, r, "rejected", "friend.rejected")
}

func (h *Handler) Friends(w http.ResponseWriter, r *http.Request) {
	h.listByRelation(w, r, "friend", "accepted", "")
}

func (h *Handler) Followers(w http.ResponseWriter, r *http.Request) {
	h.listByRelation(w, r, "follow", "accepted", "incoming")
}

func (h *Handler) Following(w http.ResponseWriter, r *http.Request) {
	h.listByRelation(w, r, "follow", "accepted", "outgoing")
}

func (h *Handler) Block(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	targetID, err := h.resolveActorParam(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "target actor not found")
		return
	}
	if targetID == principal.ActorID {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_target", "cannot block yourself")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
INSERT INTO relationships (actor_id, target_actor_id, type, direction, state)
VALUES ($1, $2, 'block', 'outgoing', 'accepted')
ON CONFLICT (actor_id, target_actor_id, type)
DO UPDATE SET direction = 'outgoing', state = 'accepted'`,
		principal.ActorID, targetID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "block_failed", err.Error())
		return
	}
	realtime.PublishActorEvent(h.events, []uuid.UUID{principal.ActorID}, "user.blocked", principal.ActorID, relationshipEventPayload(principal.ActorID, targetID, "block", "accepted"))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"state": "blocked"})
}

func (h *Handler) Unblock(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	targetID, err := h.resolveActorParam(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "target actor not found")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
DELETE FROM relationships
WHERE actor_id = $1 AND target_actor_id = $2 AND type = 'block'`, principal.ActorID, targetID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "unblock_failed", err.Error())
		return
	}
	realtime.PublishActorEvent(h.events, []uuid.UUID{principal.ActorID}, "user.unblocked", principal.ActorID, relationshipEventPayload(principal.ActorID, targetID, "block", "removed"))
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) edgeAction(w http.ResponseWriter, r *http.Request, relationType string, state string, eventType string) {
	principal, targetID, ok := h.resolveRequestTarget(w, r)
	if !ok {
		return
	}
	if targetID == principal.ActorID {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_target", "cannot target yourself")
		return
	}

	outgoingDirection := "outgoing"
	incomingDirection := "incoming"
	if relationType == "friend" && state == "accepted" {
		outgoingDirection = "mutual"
		incomingDirection = "mutual"
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "relationship_failed", err.Error())
		return
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO relationships (actor_id, target_actor_id, type, direction, state)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (actor_id, target_actor_id, type)
DO UPDATE SET direction = EXCLUDED.direction, state = EXCLUDED.state`,
		principal.ActorID, targetID, relationType, outgoingDirection, state); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "relationship_failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO relationships (actor_id, target_actor_id, type, direction, state)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (actor_id, target_actor_id, type)
DO UPDATE SET direction = EXCLUDED.direction, state = EXCLUDED.state`,
		targetID, principal.ActorID, relationType, incomingDirection, state); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "relationship_failed", err.Error())
		return
	}
	var notification notifications.NotificationResponse
	if shouldPersistRelationshipNotification(eventType) {
		notification, err = notifications.Insert(r.Context(), tx, notifications.CreateInput{
			ActorID: targetID,
			Type:    eventType,
			Payload: relationshipEventPayload(principal.ActorID, targetID, relationType, state),
		})
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "notification_failed", err.Error())
			return
		}
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "relationship_failed", err.Error())
		return
	}

	if notification.ID != uuid.Nil {
		notifications.PublishCreated(h.events, notification)
	}
	realtime.PublishActorEvent(h.events, []uuid.UUID{principal.ActorID, targetID}, eventType, principal.ActorID, relationshipEventPayload(principal.ActorID, targetID, relationType, state))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"type": relationType, "state": state})
}

func (h *Handler) friendDecision(w http.ResponseWriter, r *http.Request, state string, eventType string) {
	principal, targetID, ok := h.resolveRequestTarget(w, r)
	if !ok {
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "friend_decision_failed", err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	result, err := tx.ExecContext(r.Context(), `
UPDATE relationships
SET state = $3, direction = CASE WHEN $3 = 'accepted' THEN 'mutual' ELSE direction END
WHERE type = 'friend'
  AND state = 'pending'
  AND (
    (actor_id = $1 AND target_actor_id = $2 AND direction = 'incoming')
    OR (actor_id = $2 AND target_actor_id = $1 AND direction = 'outgoing')
  )`,
		principal.ActorID, targetID, state)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "friend_decision_failed", err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "friend request not found")
		return
	}
	notification, err := notifications.Insert(r.Context(), tx, notifications.CreateInput{
		ActorID: targetID,
		Type:    eventType,
		Payload: relationshipEventPayload(principal.ActorID, targetID, "friend", state),
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "notification_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "friend_decision_failed", err.Error())
		return
	}
	committed = true
	notifications.PublishCreated(h.events, notification)
	realtime.PublishActorEvent(h.events, []uuid.UUID{principal.ActorID, targetID}, eventType, principal.ActorID, relationshipEventPayload(principal.ActorID, targetID, "friend", state))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"type": "friend", "state": state})
}

func shouldPersistRelationshipNotification(eventType string) bool {
	switch eventType {
	case "relationship.followed", "friend.requested":
		return true
	default:
		return false
	}
}

func relationshipEventPayload(actorID uuid.UUID, targetActorID uuid.UUID, relationType string, state string) map[string]string {
	return map[string]string{
		"actorId":       actorID.String(),
		"targetActorId": targetActorID.String(),
		"type":          relationType,
		"state":         state,
	}
}

func (h *Handler) listByRelation(w http.ResponseWriter, r *http.Request, relationType string, state string, direction string) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	query := `
SELECT a.id, a.acct, a.preferred_username, a.display_name, COALESCE(p.avatar_url, '')
FROM relationships rel
JOIN actors a ON a.id = rel.target_actor_id
LEFT JOIN users u ON u.id = a.local_user_id
LEFT JOIN profiles p ON p.user_id = u.id
WHERE rel.actor_id = $1 AND rel.type = $2 AND rel.state = $3`
	args := []any{principal.ActorID, relationType, state}
	if direction != "" {
		query += ` AND rel.direction = $4`
		args = append(args, direction)
	}
	query += ` ORDER BY a.acct LIMIT 200`

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_relationships_failed", err.Error())
		return
	}
	defer rows.Close()

	results := []actorSummary{}
	for rows.Next() {
		var actor actorSummary
		if err := rows.Scan(&actor.ActorID, &actor.Acct, &actor.Username, &actor.DisplayName, &actor.AvatarURL); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_failed", err.Error())
			return
		}
		results = append(results, actor)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_relationships_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, results)
}

func (h *Handler) resolveRequestTarget(w http.ResponseWriter, r *http.Request) (auth.Principal, uuid.UUID, bool) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return auth.Principal{}, uuid.Nil, false
	}

	var req actorRefRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return auth.Principal{}, uuid.Nil, false
	}

	targetID, err := h.resolveActorRef(r.Context(), req)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "target actor not found")
			return auth.Principal{}, uuid.Nil, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, "resolve_actor_failed", err.Error())
		return auth.Principal{}, uuid.Nil, false
	}
	return principal, targetID, true
}

func (h *Handler) resolveActorRef(ctx context.Context, req actorRefRequest) (uuid.UUID, error) {
	if req.TargetActorID != nil {
		return *req.TargetActorID, nil
	}
	if req.ActorID != nil {
		return *req.ActorID, nil
	}
	acct := strings.ToLower(strings.TrimSpace(req.TargetAcct))
	if acct == "" {
		acct = strings.ToLower(strings.TrimSpace(req.Acct))
	}
	if acct == "" {
		return uuid.Nil, sql.ErrNoRows
	}
	return h.resolveActorParam(ctx, acct)
}

func (h *Handler) resolveActorParam(ctx context.Context, value string) (uuid.UUID, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return uuid.Nil, sql.ErrNoRows
	}

	var id uuid.UUID
	if parsed, err := uuid.Parse(value); err == nil {
		err := h.db.QueryRowContext(ctx, `
SELECT id FROM actors WHERE id = $1 OR local_user_id = $1`, parsed).Scan(&id)
		return id, err
	}

	err := h.db.QueryRowContext(ctx, `
SELECT id FROM actors WHERE lower(acct) = $1 OR lower(preferred_username) = $1`, value).Scan(&id)
	return id, err
}
