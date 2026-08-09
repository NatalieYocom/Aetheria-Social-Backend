package instances

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/realtime"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	defaultRuntimeTTL = 90 * time.Second
	maxRuntimeTTL     = 5 * time.Minute
)

var errInstanceOwnedByAnotherServer = errors.New("instance is owned by another world server")

const lockRuntimeInstanceSQL = `
SELECT world_id, status, expires_at, world_server_credential_id
FROM instances
WHERE id = $1
FOR UPDATE`

const claimRuntimeInstanceSQL = `
UPDATE instances
SET world_server_credential_id = $2,
    last_heartbeat_at = COALESCE(last_heartbeat_at, now())
WHERE id = $1
  AND (world_server_credential_id IS NULL OR world_server_credential_id = $2)`

const decrementRuntimeInstanceUsersSQL = `
UPDATE instances
SET current_users = GREATEST(current_users - 1, 0)
WHERE id = $1`

type serviceInstanceHeartbeatRequest struct {
	TTLSeconds int `json:"ttlSeconds"`
}

type serviceMemberHeartbeatRequest struct {
	Status             string         `json:"status"`
	PresenceVisibility string         `json:"presenceVisibility"`
	ShowExactInstance  bool           `json:"showExactInstance"`
	Metadata           map[string]any `json:"metadata"`
}

type serviceInstanceHeartbeatResponse struct {
	State        string    `json:"state"`
	ExpiresAt    time.Time `json:"expiresAt"`
	ServerTime   time.Time `json:"serverTime"`
	CredentialID uuid.UUID `json:"credentialId"`
}

type serviceMemberHeartbeatResponse struct {
	State             string    `json:"state"`
	ActorID           uuid.UUID `json:"actorId"`
	PresenceExpiresAt time.Time `json:"presenceExpiresAt"`
}

type runtimeInstance struct {
	worldID      uuid.UUID
	status       string
	expiresAt    sql.NullTime
	credentialID uuid.NullUUID
}

func (h *Handler) ServiceInstanceHeartbeat(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authenticateWorldServer(w, r)
	if !ok {
		return
	}
	instanceID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var req serviceInstanceHeartbeatRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	ttl := defaultRuntimeTTL
	if req.TTLSeconds != 0 {
		ttl = time.Duration(req.TTLSeconds) * time.Second
	}
	if ttl < 30*time.Second || ttl > maxRuntimeTTL {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_ttl", "ttlSeconds must be between 30 and 300")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "instance_heartbeat_failed", err.Error())
		return
	}
	defer tx.Rollback()
	target, err := loadRuntimeInstance(r.Context(), tx, instanceID)
	if err != nil {
		writeRuntimeInstanceError(w, err)
		return
	}
	if target.status != "active" {
		httpx.WriteError(w, http.StatusConflict, "instance_not_active", "instance is not active")
		return
	}
	if !identity.canAccessWorld(target.worldID) {
		httpx.WriteError(w, http.StatusForbidden, "credential_scope_denied", "credential is not allowed for this world")
		return
	}
	if err := claimRuntimeInstance(r.Context(), tx, instanceID, identity.id); err != nil {
		writeRuntimeOwnershipError(w, err)
		return
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)
	if _, err := tx.ExecContext(r.Context(), `
UPDATE instances
SET last_heartbeat_at = $2, expires_at = $3
WHERE id = $1`, instanceID, now, expiresAt); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "instance_heartbeat_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "instance_heartbeat_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, serviceInstanceHeartbeatResponse{
		State: "active", ExpiresAt: expiresAt, ServerTime: now, CredentialID: identity.id,
	})
}

func (h *Handler) ServiceMemberHeartbeat(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authenticateWorldServer(w, r)
	if !ok {
		return
	}
	instanceID, actorID, ok := parseRuntimeIDs(w, r)
	if !ok {
		return
	}
	var req serviceMemberHeartbeatRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	status := normalizePresenceStatus(req.Status)
	if status == "" {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_status", "status must be online, away, busy or invisible")
		return
	}
	visibility := normalizePresenceVisibility(req.PresenceVisibility)
	if visibility == "" {
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
		httpx.WriteError(w, http.StatusInternalServerError, "member_heartbeat_failed", err.Error())
		return
	}
	defer tx.Rollback()
	target, err := h.authorizeRuntimeInstance(r.Context(), tx, identity, instanceID, true)
	if err != nil {
		writeRuntimeAuthorizationError(w, err)
		return
	}
	result, err := tx.ExecContext(r.Context(), `
UPDATE instance_members im
SET last_seen_at = now(), metadata = $3
WHERE im.instance_id = $1 AND im.actor_id = $2 AND im.state = 'joined'
  AND EXISTS (
    SELECT 1 FROM actors a JOIN users u ON u.id = a.local_user_id
    WHERE a.id = im.actor_id AND u.status = 'active'
  )`, instanceID, actorID, metadata)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_heartbeat_failed", err.Error())
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		httpx.WriteError(w, http.StatusNotFound, "member_not_joined", "actor is not joined to this instance")
		return
	}
	expiresAt := time.Now().UTC().Add(joinedPresenceTTL)
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO presence_sessions (actor_id, world_id, instance_id, status, visibility, show_exact_instance, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (actor_id) DO UPDATE
SET world_id = EXCLUDED.world_id, instance_id = EXCLUDED.instance_id,
    status = EXCLUDED.status, visibility = EXCLUDED.visibility,
    show_exact_instance = EXCLUDED.show_exact_instance,
    expires_at = EXCLUDED.expires_at, updated_at = now()`,
		actorID, target.worldID, instanceID, status, visibility, req.ShowExactInstance, expiresAt,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_heartbeat_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_heartbeat_failed", err.Error())
		return
	}
	_ = realtime.PublishPresenceChanged(r.Context(), h.db, h.events, actorID)
	httpx.WriteJSON(w, http.StatusOK, serviceMemberHeartbeatResponse{
		State: "joined", ActorID: actorID, PresenceExpiresAt: expiresAt,
	})
}

func (h *Handler) ServiceMemberLeave(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authenticateWorldServer(w, r)
	if !ok {
		return
	}
	instanceID, actorID, ok := parseRuntimeIDs(w, r)
	if !ok {
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_leave_failed", err.Error())
		return
	}
	defer tx.Rollback()
	if _, err := h.authorizeRuntimeInstance(r.Context(), tx, identity, instanceID, false); err != nil {
		writeRuntimeAuthorizationError(w, err)
		return
	}
	result, err := tx.ExecContext(r.Context(), `
UPDATE instance_members
SET state = 'left', left_at = now(), last_seen_at = now()
WHERE instance_id = $1 AND actor_id = $2 AND state = 'joined'`, instanceID, actorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_leave_failed", err.Error())
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		httpx.WriteError(w, http.StatusNotFound, "member_not_joined", "actor is not joined to this instance")
		return
	}
	if _, err := tx.ExecContext(r.Context(), decrementRuntimeInstanceUsersSQL, instanceID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_leave_failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM presence_sessions WHERE actor_id = $1 AND instance_id = $2`, actorID, instanceID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_leave_failed", err.Error())
		return
	}
	if err := insertRuntimeAudit(r, tx, identity, &actorID, instanceID, "member_left"); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_leave_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "member_leave_failed", err.Error())
		return
	}
	_ = realtime.PublishPresenceRemoved(r.Context(), h.db, h.events, actorID)
	_ = realtime.PublishInstanceChanged(r.Context(), h.db, h.events, instanceID, "instance.updated")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) authorizeRuntimeInstance(ctx context.Context, tx *sql.Tx, identity worldServerIdentity, instanceID uuid.UUID, requireLiveLease bool) (runtimeInstance, error) {
	target, err := loadRuntimeInstance(ctx, tx, instanceID)
	if err != nil {
		return runtimeInstance{}, err
	}
	if target.status != "active" || (requireLiveLease && (!target.expiresAt.Valid || !target.expiresAt.Time.After(time.Now().UTC()))) {
		return runtimeInstance{}, errRuntimeInstanceInactive
	}
	if !identity.canAccessWorld(target.worldID) {
		return runtimeInstance{}, errRuntimeScopeDenied
	}
	if err := claimRuntimeInstance(ctx, tx, instanceID, identity.id); err != nil {
		return runtimeInstance{}, err
	}
	return target, nil
}

var (
	errRuntimeInstanceInactive = errors.New("runtime instance is inactive")
	errRuntimeScopeDenied      = errors.New("runtime credential scope denied")
)

func loadRuntimeInstance(ctx context.Context, tx *sql.Tx, instanceID uuid.UUID) (runtimeInstance, error) {
	var target runtimeInstance
	err := tx.QueryRowContext(ctx, lockRuntimeInstanceSQL, instanceID).Scan(
		&target.worldID, &target.status, &target.expiresAt, &target.credentialID,
	)
	return target, err
}

func claimRuntimeInstance(ctx context.Context, tx *sql.Tx, instanceID, credentialID uuid.UUID) error {
	result, err := tx.ExecContext(ctx, claimRuntimeInstanceSQL, instanceID, credentialID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errInstanceOwnedByAnotherServer
	}
	return nil
}

func (identity worldServerIdentity) canAccessWorld(worldID uuid.UUID) bool {
	return !identity.allowedWorldID.Valid || identity.allowedWorldID.UUID == worldID
}

func parseRuntimeIDs(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	instanceID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return uuid.Nil, uuid.Nil, false
	}
	actorID, err := uuid.Parse(chi.URLParam(r, "actorId"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "actorId must be a uuid")
		return uuid.Nil, uuid.Nil, false
	}
	return instanceID, actorID, true
}

func insertRuntimeAudit(r *http.Request, tx *sql.Tx, identity worldServerIdentity, actorID *uuid.UUID, instanceID uuid.UUID, outcome string) error {
	_, err := tx.ExecContext(r.Context(), `
INSERT INTO instance_join_audit (credential_id, actor_id, instance_id, outcome, remote_ip)
VALUES ($1, $2, $3, $4, $5)`, identity.id, dbx.NullUUID(actorID), instanceID, outcome, requestIP(r))
	return err
}

func writeRuntimeInstanceError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeInstanceNotFound(w)
		return
	}
	httpx.WriteError(w, http.StatusInternalServerError, "load_instance_failed", err.Error())
}

func writeRuntimeOwnershipError(w http.ResponseWriter, err error) {
	if errors.Is(err, errInstanceOwnedByAnotherServer) {
		httpx.WriteError(w, http.StatusConflict, "instance_owner_conflict", "instance is owned by another world-server credential")
		return
	}
	httpx.WriteError(w, http.StatusInternalServerError, "instance_ownership_failed", err.Error())
}

func writeRuntimeAuthorizationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeInstanceNotFound(w)
	case errors.Is(err, errRuntimeInstanceInactive):
		httpx.WriteError(w, http.StatusConflict, "instance_not_active", "instance lease is not active")
	case errors.Is(err, errRuntimeScopeDenied):
		httpx.WriteError(w, http.StatusForbidden, "credential_scope_denied", "credential is not allowed for this world")
	case errors.Is(err, errInstanceOwnedByAnotherServer):
		writeRuntimeOwnershipError(w, err)
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "runtime_authorization_failed", err.Error())
	}
}
