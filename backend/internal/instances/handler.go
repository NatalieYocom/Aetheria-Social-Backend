package instances

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
	r.Get("/api/instances/{id}", h.Get)
	r.Get("/api/worlds/{id}/instances", h.ListByWorld)
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware)
		r.Post("/api/instances", h.Create)
		r.Post("/api/instances/{id}/join", h.Join)
		r.Post("/api/instances/{id}/heartbeat", h.Heartbeat)
		r.Post("/api/instances/{id}/leave", h.Leave)
		r.Patch("/api/instances/{id}", h.Update)
		r.Delete("/api/instances/{id}", h.Delete)
	})
}

type createInstanceRequest struct {
	WorldID     uuid.UUID      `json:"worldId"`
	InstanceKey string         `json:"instanceKey"`
	Name        string         `json:"name"`
	Visibility  string         `json:"visibility"`
	LaunchURL   string         `json:"launchUrl"`
	Capacity    int            `json:"capacity"`
	ExpiresAt   *time.Time     `json:"expiresAt"`
	Metadata    map[string]any `json:"metadata"`
}

type updateInstanceRequest struct {
	Name         *string         `json:"name"`
	Visibility   *string         `json:"visibility"`
	LaunchURL    *string         `json:"launchUrl"`
	Capacity     *int            `json:"capacity"`
	CurrentUsers *int            `json:"currentUsers"`
	Status       *string         `json:"status"`
	ExpiresAt    *time.Time      `json:"expiresAt"`
	Metadata     *map[string]any `json:"metadata"`
}

type joinInstanceRequest struct {
	PresenceVisibility string         `json:"presenceVisibility"`
	ShowExactInstance  bool           `json:"showExactInstance"`
	Metadata           map[string]any `json:"metadata"`
}

type heartbeatInstanceRequest struct {
	Status             string         `json:"status"`
	PresenceVisibility string         `json:"presenceVisibility"`
	ShowExactInstance  bool           `json:"showExactInstance"`
	Metadata           map[string]any `json:"metadata"`
}

type heartbeatResponse struct {
	State             string    `json:"state"`
	PresenceExpiresAt time.Time `json:"presenceExpiresAt"`
}

type InstanceResponse struct {
	ID           uuid.UUID      `json:"id"`
	WorldID      uuid.UUID      `json:"worldId"`
	HostActorID  uuid.UUID      `json:"hostActorId"`
	InstanceKey  string         `json:"instanceKey"`
	Name         string         `json:"name"`
	Visibility   string         `json:"visibility"`
	LaunchURL    string         `json:"launchUrl"`
	Capacity     int            `json:"capacity"`
	CurrentUsers int            `json:"currentUsers"`
	Status       string         `json:"status"`
	ExpiresAt    *time.Time     `json:"expiresAt"`
	Metadata     map[string]any `json:"metadata"`
}

const joinInstanceSelectSQL = `
SELECT id, world_id, host_actor_id, visibility, capacity, current_users, status, expires_at
FROM instances
WHERE id = $1
FOR UPDATE`

const joinedMemberExistsSQL = `
SELECT EXISTS (
  SELECT 1
  FROM instance_members
  WHERE instance_id = $1 AND actor_id = $2 AND state = 'joined'
)`

const loadInstanceSQL = `
SELECT id, world_id, host_actor_id, instance_key, name, visibility, launch_url, capacity, current_users, status, expires_at, metadata
FROM instances
WHERE id = $1`

const heartbeatInstanceSelectSQL = `
SELECT world_id, status, expires_at
FROM instances
WHERE id = $1`

const worldAccessTargetSQL = `SELECT owner_actor_id, visibility FROM worlds WHERE id = $1`

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	var req createInstanceRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.WorldID == uuid.Nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_world", "worldId is required")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = "BasisVR instance"
	}
	if req.InstanceKey == "" {
		req.InstanceKey = uuid.NewString()
	}
	if req.Visibility == "" {
		req.Visibility = "public"
	}
	if req.Capacity < 0 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_capacity", "capacity cannot be negative")
		return
	}
	allowed, err := h.canViewWorldByID(r.Context(), req.WorldID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeInstanceNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "world_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeInstanceNotFound(w)
		return
	}
	metadata, err := dbx.MarshalJSON(req.Metadata, "{}")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_metadata", "metadata must be JSON")
		return
	}

	var id uuid.UUID
	if err := h.db.QueryRowContext(r.Context(), `
INSERT INTO instances (world_id, host_actor_id, instance_key, name, visibility, launch_url, capacity, expires_at, metadata)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING id`,
		req.WorldID,
		principal.ActorID,
		req.InstanceKey,
		req.Name,
		req.Visibility,
		req.LaunchURL,
		req.Capacity,
		nullTimePtr(req.ExpiresAt),
		metadata,
	).Scan(&id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_instance_failed", err.Error())
		return
	}

	instance, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_instance_failed", err.Error())
		return
	}
	_ = realtime.PublishInstanceChanged(r.Context(), h.db, h.events, id, "instance.created")
	httpx.WriteJSON(w, http.StatusCreated, instance)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	instance, err := h.loadByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeInstanceNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_instance_failed", err.Error())
		return
	}
	allowed, err := h.canViewInstance(r.Context(), instance)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "instance_access_check_failed", err.Error())
		return
	}
	if !allowed {
		writeInstanceNotFound(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, instance)
}

func (h *Handler) Join(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}

	var req joinInstanceRequest
	if r.Body != nil {
		if err := httpx.DecodeJSON(r, &req); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
	}
	presenceVisibility := normalizePresenceVisibility(req.PresenceVisibility)
	if presenceVisibility == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_presence_visibility", "presenceVisibility must be nobody, friends, followers or public")
		return
	}
	metadata, err := dbx.MarshalJSON(req.Metadata, "{}")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_metadata", "metadata must be JSON")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "join_instance_failed", err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	target, err := h.loadJoinTarget(r.Context(), tx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeInstanceNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "join_instance_failed", err.Error())
		return
	}
	if !target.active() {
		httpx.WriteError(w, http.StatusConflict, "instance_not_active", "instance is not active")
		return
	}
	allowed, err := h.canJoin(r.Context(), tx, target, principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "join_access_check_failed", err.Error())
		return
	}
	if !allowed {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "you cannot join this instance")
		return
	}
	alreadyJoined, err := h.isJoinedTx(r.Context(), tx, id, principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "join_instance_failed", err.Error())
		return
	}
	if target.capacityReached() && !alreadyJoined {
		httpx.WriteError(w, http.StatusConflict, "instance_full", "instance capacity has been reached")
		return
	}

	delta, err := h.upsertMemberJoined(r.Context(), tx, id, principal.ActorID, metadata)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "join_instance_failed", err.Error())
		return
	}
	if delta > 0 {
		if _, err := tx.ExecContext(r.Context(), `
UPDATE instances SET current_users = current_users + 1 WHERE id = $1`, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "join_instance_failed", err.Error())
			return
		}
	}
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO presence_sessions (actor_id, world_id, instance_id, status, visibility, show_exact_instance, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (actor_id)
DO UPDATE SET world_id = EXCLUDED.world_id,
              instance_id = EXCLUDED.instance_id,
              status = EXCLUDED.status,
              visibility = EXCLUDED.visibility,
              show_exact_instance = EXCLUDED.show_exact_instance,
              expires_at = EXCLUDED.expires_at,
              updated_at = now()`,
		principal.ActorID,
		target.worldID,
		id,
		"online",
		presenceVisibility,
		req.ShowExactInstance,
		time.Now().UTC().Add(90*time.Second),
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "join_presence_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "join_instance_failed", err.Error())
		return
	}
	committed = true

	instance, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_instance_failed", err.Error())
		return
	}
	_ = realtime.PublishPresenceChanged(r.Context(), h.db, h.events, principal.ActorID)
	_ = realtime.PublishInstanceChanged(r.Context(), h.db, h.events, id, "instance.updated")
	httpx.WriteJSON(w, http.StatusOK, instance)
}

func (h *Handler) Heartbeat(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}

	var req heartbeatInstanceRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	status := normalizePresenceStatus(req.Status)
	if status == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_status", "status must be online, away, busy or invisible")
		return
	}
	presenceVisibility := normalizePresenceVisibility(req.PresenceVisibility)
	if presenceVisibility == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_presence_visibility", "presenceVisibility must be nobody, friends, followers or public")
		return
	}
	metadata, err := dbx.MarshalJSON(req.Metadata, "{}")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_metadata", "metadata must be JSON")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "heartbeat_failed", err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var worldID uuid.UUID
	var instanceStatus string
	var expiresAt sql.NullTime
	if err := tx.QueryRowContext(r.Context(), heartbeatInstanceSelectSQL, id).Scan(&worldID, &instanceStatus, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "instance not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "heartbeat_failed", err.Error())
		return
	}
	if instanceStatus != "active" || (expiresAt.Valid && !expiresAt.Time.After(time.Now().UTC())) {
		httpx.WriteError(w, http.StatusConflict, "instance_not_active", "instance is not active")
		return
	}

	result, err := tx.ExecContext(r.Context(), `
UPDATE instance_members
SET last_seen_at = now(), metadata = $3
WHERE instance_id = $1 AND actor_id = $2 AND state = 'joined'`,
		id, principal.ActorID, metadata)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "heartbeat_failed", err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		httpx.WriteError(w, http.StatusConflict, "not_joined", "actor has not joined this instance")
		return
	}

	presenceExpiresAt := time.Now().UTC().Add(90 * time.Second)
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO presence_sessions (actor_id, world_id, instance_id, status, visibility, show_exact_instance, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (actor_id)
DO UPDATE SET world_id = EXCLUDED.world_id,
              instance_id = EXCLUDED.instance_id,
              status = EXCLUDED.status,
              visibility = EXCLUDED.visibility,
              show_exact_instance = EXCLUDED.show_exact_instance,
              expires_at = EXCLUDED.expires_at,
              updated_at = now()`,
		principal.ActorID,
		worldID,
		id,
		status,
		presenceVisibility,
		req.ShowExactInstance,
		presenceExpiresAt,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "heartbeat_presence_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "heartbeat_failed", err.Error())
		return
	}
	committed = true
	_ = realtime.PublishPresenceChanged(r.Context(), h.db, h.events, principal.ActorID)
	httpx.WriteJSON(w, http.StatusOK, heartbeatResponse{State: "joined", PresenceExpiresAt: presenceExpiresAt})
}

func (h *Handler) Leave(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "leave_instance_failed", err.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	result, err := tx.ExecContext(r.Context(), `
UPDATE instance_members
SET state = 'left', left_at = now(), last_seen_at = now()
WHERE instance_id = $1 AND actor_id = $2 AND state = 'joined'`, id, principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "leave_instance_failed", err.Error())
		return
	}
	rows, _ := result.RowsAffected()
	if rows > 0 {
		if _, err := tx.ExecContext(r.Context(), `
UPDATE instances SET current_users = GREATEST(current_users - 1, 0) WHERE id = $1`, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "leave_instance_failed", err.Error())
			return
		}
	}
	if _, err := tx.ExecContext(r.Context(), `
DELETE FROM presence_sessions
WHERE actor_id = $1 AND instance_id = $2`, principal.ActorID, id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "leave_presence_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "leave_instance_failed", err.Error())
		return
	}
	committed = true
	_ = realtime.PublishPresenceRemoved(r.Context(), h.db, h.events, principal.ActorID)
	_ = realtime.PublishInstanceChanged(r.Context(), h.db, h.events, id, "instance.updated")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if !h.isHost(r.Context(), id, principal.ActorID) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "only host can modify this instance")
		return
	}

	var req updateInstanceRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	var metadata any
	if req.Metadata != nil {
		data, err := dbx.MarshalJSON(*req.Metadata, "{}")
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid_metadata", "metadata must be JSON")
			return
		}
		metadata = data
	}

	if _, err := h.db.ExecContext(r.Context(), `
UPDATE instances
SET name = COALESCE($2, name),
    visibility = COALESCE($3, visibility),
    launch_url = COALESCE($4, launch_url),
    capacity = COALESCE($5, capacity),
    current_users = COALESCE($6, current_users),
    status = COALESCE($7, status),
    expires_at = COALESCE($8, expires_at),
    metadata = COALESCE($9::jsonb, metadata)
WHERE id = $1`,
		id,
		dbx.NullString(req.Name),
		dbx.NullString(req.Visibility),
		dbx.NullString(req.LaunchURL),
		nullInt(req.Capacity),
		nullInt(req.CurrentUsers),
		dbx.NullString(req.Status),
		nullTimePtr(req.ExpiresAt),
		metadata,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "update_instance_failed", err.Error())
		return
	}

	instance, err := h.loadByID(r.Context(), id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_instance_failed", err.Error())
		return
	}
	_ = realtime.PublishInstanceChanged(r.Context(), h.db, h.events, id, "instance.updated")
	httpx.WriteJSON(w, http.StatusOK, instance)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	id, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	if !h.isHost(r.Context(), id, principal.ActorID) {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "only host can close this instance")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `UPDATE instances SET status = 'closed' WHERE id = $1`, id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "close_instance_failed", err.Error())
		return
	}
	_ = realtime.PublishInstanceChanged(r.Context(), h.db, h.events, id, "instance.closed")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListByWorld(w http.ResponseWriter, r *http.Request) {
	worldID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
SELECT id, world_id, host_actor_id, instance_key, name, visibility, launch_url, capacity, current_users, status, expires_at, metadata
FROM instances
WHERE world_id = $1 AND status = 'active'
ORDER BY created_at DESC
LIMIT 100`, worldID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_instances_failed", err.Error())
		return
	}
	defer rows.Close()

	instances := []InstanceResponse{}
	for rows.Next() {
		instance, err := scanInstance(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "scan_instance_failed", err.Error())
			return
		}
		allowed, err := h.canViewInstance(r.Context(), instance)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "instance_access_check_failed", err.Error())
			return
		}
		if !allowed {
			continue
		}
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_instances_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, instances)
}

func (h *Handler) isHost(ctx context.Context, instanceID uuid.UUID, actorID uuid.UUID) bool {
	var exists bool
	if err := h.db.QueryRowContext(ctx, `
SELECT EXISTS (SELECT 1 FROM instances WHERE id = $1 AND host_actor_id = $2)`, instanceID, actorID).Scan(&exists); err != nil {
		return false
	}
	return exists
}

func (h *Handler) loadByID(ctx context.Context, id uuid.UUID) (InstanceResponse, error) {
	row := h.db.QueryRowContext(ctx, loadInstanceSQL, id)
	return scanInstance(row)
}

func (h *Handler) canViewWorldByID(ctx context.Context, id uuid.UUID) (bool, error) {
	var ownerID uuid.UUID
	var visibility string
	if err := h.db.QueryRowContext(ctx, worldAccessTargetSQL, id).Scan(&ownerID, &visibility); err != nil {
		return false, err
	}
	return privacy.CanView(ctx, h.db, privacy.ViewInput{
		OwnerActorID:  ownerID,
		ViewerActorID: viewerActorID(ctx),
		Visibility:    visibility,
	})
}

func (h *Handler) canViewInstance(ctx context.Context, instance InstanceResponse) (bool, error) {
	viewerID := viewerActorID(ctx)
	if instance.Visibility == "public" {
		return true, nil
	}
	if viewerID.Valid && viewerID.UUID == instance.HostActorID {
		return true, nil
	}
	if !viewerID.Valid {
		return false, nil
	}

	switch instance.Visibility {
	case "friends":
		var exists bool
		err := h.db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM relationships
  WHERE actor_id = $1 AND target_actor_id = $2 AND type = 'friend' AND state = 'accepted'
)`, viewerID.UUID, instance.HostActorID).Scan(&exists)
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
)`, viewerID.UUID, instance.ID).Scan(&exists)
		return exists, err
	case "private":
		return false, nil
	default:
		return false, nil
	}
}

func viewerActorID(ctx context.Context) uuid.NullUUID {
	principal, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: principal.ActorID, Valid: true}
}

func writeInstanceNotFound(w http.ResponseWriter) {
	httpx.WriteError(w, http.StatusNotFound, "not_found", "instance not found")
}

type joinTarget struct {
	id           uuid.UUID
	worldID      uuid.UUID
	hostActorID  uuid.UUID
	visibility   string
	capacity     int
	currentUsers int
	status       string
	expiresAt    sql.NullTime
}

func (t joinTarget) active() bool {
	if t.status != "active" {
		return false
	}
	return !t.expiresAt.Valid || t.expiresAt.Time.After(time.Now().UTC())
}

func (t joinTarget) capacityReached() bool {
	return t.capacity > 0 && t.currentUsers >= t.capacity
}

func (h *Handler) loadJoinTarget(ctx context.Context, tx *sql.Tx, id uuid.UUID) (joinTarget, error) {
	var target joinTarget
	err := tx.QueryRowContext(ctx, joinInstanceSelectSQL, id).Scan(
		&target.id,
		&target.worldID,
		&target.hostActorID,
		&target.visibility,
		&target.capacity,
		&target.currentUsers,
		&target.status,
		&target.expiresAt,
	)
	return target, err
}

func (h *Handler) isJoinedTx(ctx context.Context, tx *sql.Tx, instanceID uuid.UUID, actorID uuid.UUID) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, joinedMemberExistsSQL, instanceID, actorID).Scan(&exists)
	return exists, err
}

func (h *Handler) canJoin(ctx context.Context, tx *sql.Tx, target joinTarget, actorID uuid.UUID) (bool, error) {
	if actorID == target.hostActorID {
		return true, nil
	}
	switch target.visibility {
	case "public":
		return true, nil
	case "private":
		return false, nil
	case "friends":
		var exists bool
		err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM relationships
  WHERE actor_id = $1 AND target_actor_id = $2 AND type = 'friend' AND state = 'accepted'
)`, actorID, target.hostActorID).Scan(&exists)
		return exists, err
	case "invite_only":
		var exists bool
		err := tx.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM invites
  WHERE to_actor_id = $1
    AND instance_id = $2
    AND state IN ('pending', 'accepted')
    AND expires_at > now()
)`, actorID, target.id).Scan(&exists)
		return exists, err
	default:
		return false, nil
	}
}

func (h *Handler) upsertMemberJoined(ctx context.Context, tx *sql.Tx, instanceID uuid.UUID, actorID uuid.UUID, metadata []byte) (int64, error) {
	result, err := tx.ExecContext(ctx, `
UPDATE instance_members
SET state = 'joined', left_at = NULL, last_seen_at = now(), metadata = $3
WHERE instance_id = $1 AND actor_id = $2 AND state <> 'joined'`,
		instanceID, actorID, metadata)
	if err != nil {
		return 0, err
	}
	changed, _ := result.RowsAffected()
	if changed > 0 {
		return changed, nil
	}
	result, err = tx.ExecContext(ctx, `
INSERT INTO instance_members (instance_id, actor_id, state, metadata)
VALUES ($1, $2, 'joined', $3)
ON CONFLICT (instance_id, actor_id) DO NOTHING`,
		instanceID, actorID, metadata)
	if err != nil {
		return 0, err
	}
	inserted, _ := result.RowsAffected()
	return inserted, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanInstance(row scanner) (InstanceResponse, error) {
	var instance InstanceResponse
	var expiresAt sql.NullTime
	var metadataRaw []byte
	if err := row.Scan(
		&instance.ID,
		&instance.WorldID,
		&instance.HostActorID,
		&instance.InstanceKey,
		&instance.Name,
		&instance.Visibility,
		&instance.LaunchURL,
		&instance.Capacity,
		&instance.CurrentUsers,
		&instance.Status,
		&expiresAt,
		&metadataRaw,
	); err != nil {
		return InstanceResponse{}, err
	}
	if expiresAt.Valid {
		instance.ExpiresAt = &expiresAt.Time
	}
	instance.Metadata = dbx.DecodeJSON(metadataRaw, map[string]any{})
	return instance, nil
}

func parseUUIDParam(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", name+" must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func nullInt(value *int) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*value), Valid: true}
}

func nullTimePtr(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}

func normalizePresenceVisibility(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "friends"
	}
	switch value {
	case "nobody", "friends", "followers", "public":
		return value
	default:
		return ""
	}
}

func normalizePresenceStatus(value string) string {
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
