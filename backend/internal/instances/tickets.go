package instances

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math/big"
	"net"
	"net/http"
	"strings"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/common/page"
	"basisvr-social-service/internal/realtime"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const (
	joinTicketPrefix       = "bvr_jt_"
	serverCredentialPrefix = "bvr_ws_"
	joinTicketTTL          = 75 * time.Second
	joinedPresenceTTL      = 90 * time.Second
)

const insertJoinTicketSQL = `
INSERT INTO instance_join_tickets (
  token_hash, actor_id, instance_id, presence_visibility,
  show_exact_instance, metadata, expires_at, client_did
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING id`

const loadJoinTicketForConsumeSQL = `
SELECT jt.id, jt.actor_id, jt.instance_id, jt.presence_visibility,
       jt.show_exact_instance, jt.metadata, jt.expires_at, jt.consumed_at,
       a.acct, u.status, COALESCE(jt.client_did,'')
FROM instance_join_tickets jt
JOIN actors a ON a.id = jt.actor_id
JOIN users u ON u.id = a.local_user_id
WHERE jt.token_hash = $1
FOR UPDATE OF jt`

const authenticateWorldServerSQL = `
UPDATE world_server_credentials
SET last_used_at = now()
WHERE token_hash = $1 AND status = 'active'
RETURNING id, name, allowed_world_id`

const listWorldServerCredentialsSQL = `
SELECT id, name, token_prefix, allowed_world_id, status, metadata,
       last_used_at, revoked_at, created_at
FROM world_server_credentials
WHERE $1::timestamptz IS NULL OR (created_at, id) < ($1, $2)
ORDER BY created_at DESC, id DESC
LIMIT $3`

const listInstanceJoinAuditSQL = `
SELECT id, ticket_id, credential_id, actor_id, instance_id,
       outcome, remote_ip, details, created_at
FROM instance_join_audit
WHERE $1::timestamptz IS NULL OR (created_at, id) < ($1, $2)
ORDER BY created_at DESC, id DESC
LIMIT $3`

type issueJoinTicketRequest struct {
	ClientDID          string         `json:"clientDid"`
	PresenceVisibility string         `json:"presenceVisibility"`
	ShowExactInstance  bool           `json:"showExactInstance"`
	Metadata           map[string]any `json:"metadata"`
}

type JoinTicketResponse struct {
	Ticket    string           `json:"ticket"`
	ExpiresAt time.Time        `json:"expiresAt"`
	Instance  InstanceResponse `json:"instance"`
}

type consumeJoinTicketRequest struct {
	InstanceID uuid.UUID `json:"instanceId"`
	ClientDID  string    `json:"clientDid"`
	Ticket     string    `json:"ticket"`
}

type ConsumeJoinTicketResponse struct {
	PresenceVisibility string           `json:"presenceVisibility"`
	ShowExactInstance  bool             `json:"showExactInstance"`
	State              string           `json:"state"`
	ActorID            uuid.UUID        `json:"actorId"`
	Acct               string           `json:"acct"`
	PresenceExpiresAt  time.Time        `json:"presenceExpiresAt"`
	Instance           InstanceResponse `json:"instance"`
}

type createWorldServerCredentialRequest struct {
	Name           string         `json:"name"`
	AllowedWorldID *uuid.UUID     `json:"allowedWorldId"`
	Metadata       map[string]any `json:"metadata"`
}

type WorldServerCredentialResponse struct {
	ID             uuid.UUID      `json:"id"`
	Name           string         `json:"name"`
	TokenPrefix    string         `json:"tokenPrefix"`
	Token          string         `json:"token,omitempty"`
	AllowedWorldID *uuid.UUID     `json:"allowedWorldId,omitempty"`
	Status         string         `json:"status"`
	Metadata       map[string]any `json:"metadata"`
	LastUsedAt     *time.Time     `json:"lastUsedAt,omitempty"`
	RevokedAt      *time.Time     `json:"revokedAt,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
}

type InstanceJoinAuditResponse struct {
	ID           uuid.UUID      `json:"id"`
	TicketID     *uuid.UUID     `json:"ticketId,omitempty"`
	CredentialID *uuid.UUID     `json:"credentialId,omitempty"`
	ActorID      *uuid.UUID     `json:"actorId,omitempty"`
	InstanceID   *uuid.UUID     `json:"instanceId,omitempty"`
	Outcome      string         `json:"outcome"`
	RemoteIP     string         `json:"remoteIp"`
	Details      map[string]any `json:"details"`
	CreatedAt    time.Time      `json:"createdAt"`
}

type worldServerIdentity struct {
	id             uuid.UUID
	name           string
	allowedWorldID uuid.NullUUID
}

type consumableJoinTicket struct {
	clientDID          string
	id                 uuid.UUID
	actorID            uuid.UUID
	instanceID         uuid.UUID
	presenceVisibility string
	showExactInstance  bool
	metadata           []byte
	expiresAt          time.Time
	consumedAt         sql.NullTime
	acct               string
	localUserStatus    string
}

func (h *Handler) IssueJoinTicket(w http.ResponseWriter, r *http.Request) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	instanceID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var req issueJoinTicketRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !validClientDID(req.ClientDID) {
		httpx.WriteError(w, 400, "invalid_client_did", "clientDid must be a canonical Ed25519 did:key")
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
	rawToken, err := newOpaqueToken(joinTicketPrefix)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "issue_join_ticket_failed", err.Error())
		return
	}
	expiresAt := time.Now().UTC().Add(joinTicketTTL)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "issue_join_ticket_failed", err.Error())
		return
	}
	defer tx.Rollback()
	if err := lockActor(r.Context(), tx, principal.ActorID); err != nil {
		httpx.WriteError(w, 500, "issue_join_ticket_failed", err.Error())
		return
	}
	target, err := h.loadJoinTarget(r.Context(), tx, instanceID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeInstanceNotFound(w)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "issue_join_ticket_failed", err.Error())
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
	alreadyJoined, err := h.isJoinedTx(r.Context(), tx, instanceID, principal.ActorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "issue_join_ticket_failed", err.Error())
		return
	}
	if target.capacityReached() && !alreadyJoined {
		httpx.WriteError(w, http.StatusConflict, "instance_full", "instance capacity has been reached")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE instance_join_tickets
SET expires_at = LEAST(expires_at, now())
WHERE actor_id = $1 AND instance_id = $2 AND consumed_at IS NULL`, principal.ActorID, instanceID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "issue_join_ticket_failed", err.Error())
		return
	}
	var ticketID uuid.UUID
	if err := tx.QueryRowContext(r.Context(), insertJoinTicketSQL,
		hashOpaqueToken(rawToken), principal.ActorID, instanceID, visibility,
		req.ShowExactInstance, metadata, expiresAt, req.ClientDID,
	).Scan(&ticketID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "issue_join_ticket_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "issue_join_ticket_failed", err.Error())
		return
	}
	instance, err := h.loadByID(r.Context(), instanceID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_instance_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, JoinTicketResponse{Ticket: rawToken, ExpiresAt: expiresAt, Instance: instance})
}

func (h *Handler) ConsumeJoinTicket(w http.ResponseWriter, r *http.Request) {
	identity, ok := h.authenticateWorldServer(w, r)
	if !ok {
		return
	}
	var req consumeJoinTicketRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.InstanceID == uuid.Nil {
		httpx.WriteError(w, 400, "invalid_instance", "instanceId is required")
		return
	}
	if !validClientDID(req.ClientDID) {
		httpx.WriteError(w, 400, "invalid_client_did", "clientDid must be a canonical Ed25519 did:key")
		return
	}
	req.Ticket = strings.TrimSpace(req.Ticket)
	if !strings.HasPrefix(req.Ticket, joinTicketPrefix) {
		h.recordJoinAudit(r, identity, nil, "invalid_ticket")
		httpx.WriteError(w, http.StatusNotFound, "invalid_ticket", "join ticket is invalid")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	defer tx.Rollback()
	// Lock actor before ticket, matching issuance invalidation order. Otherwise an
	// issue/consume race can deadlock (actor -> ticket vs ticket -> actor).
	var actorID uuid.UUID
	if err = tx.QueryRowContext(r.Context(), `SELECT actor_id FROM instance_join_tickets WHERE token_hash=$1`, hashOpaqueToken(req.Ticket)).Scan(&actorID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.rollbackAndRecordJoinAudit(r, tx, identity, nil, "invalid_ticket")
			httpx.WriteError(w, 404, "invalid_ticket", "join ticket is invalid")
		} else {
			httpx.WriteError(w, 500, "consume_join_ticket_failed", err.Error())
		}
		return
	}
	if err = lockActor(r.Context(), tx, actorID); err != nil {
		httpx.WriteError(w, 500, "consume_join_ticket_failed", err.Error())
		return
	}
	ticket, err := loadConsumableJoinTicket(r, tx, hashOpaqueToken(req.Ticket))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			h.rollbackAndRecordJoinAudit(r, tx, identity, nil, "invalid_ticket")
			httpx.WriteError(w, http.StatusNotFound, "invalid_ticket", "join ticket is invalid")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	if ticket.instanceID != req.InstanceID {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "instance_mismatch")
		httpx.WriteError(w, 409, "ticket_instance_mismatch", "ticket targets another instance")
		return
	}
	if ticket.clientDID != req.ClientDID {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "identity_mismatch")
		httpx.WriteError(w, 409, "ticket_identity_mismatch", "ticket belongs to another client key")
		return
	}
	if ticket.consumedAt.Valid {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "already_consumed")
		httpx.WriteError(w, http.StatusConflict, "ticket_consumed", "join ticket has already been consumed")
		return
	}
	if !ticket.expiresAt.After(time.Now().UTC()) {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "expired")
		httpx.WriteError(w, http.StatusConflict, "ticket_expired", "join ticket has expired")
		return
	}
	if ticket.localUserStatus != "active" {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "user_inactive")
		httpx.WriteError(w, http.StatusForbidden, "user_inactive", "user is not active")
		return
	}

	target, err := h.loadJoinTarget(r.Context(), tx, ticket.instanceID)
	if err != nil || !target.active() || !target.expiresAt.Valid {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "instance_inactive")
		httpx.WriteError(w, http.StatusConflict, "instance_not_active", "instance is not active")
		return
	}
	if identity.allowedWorldID.Valid && identity.allowedWorldID.UUID != target.worldID {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "credential_scope_denied")
		httpx.WriteError(w, http.StatusForbidden, "credential_scope_denied", "credential is not allowed for this world")
		return
	}
	if err := claimRuntimeInstance(r.Context(), tx, ticket.instanceID, identity.id); err != nil {
		if errors.Is(err, errInstanceOwnedByAnotherServer) {
			h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "instance_owner_conflict")
			httpx.WriteError(w, http.StatusConflict, "instance_owner_conflict", "instance is owned by another world-server credential")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	allowed, err := h.canJoin(r.Context(), tx, target, ticket.actorID)
	if err != nil || !allowed {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "join_denied")
		httpx.WriteError(w, http.StatusForbidden, "join_denied", "actor cannot join this instance")
		return
	}
	alreadyJoined, err := h.isJoinedTx(r.Context(), tx, ticket.instanceID, ticket.actorID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	if target.capacityReached() && !alreadyJoined {
		h.rollbackAndRecordJoinAudit(r, tx, identity, &ticket, "instance_full")
		httpx.WriteError(w, http.StatusConflict, "instance_full", "instance capacity has been reached")
		return
	}
	leftInstances, err := h.leaveOtherInstances(r.Context(), tx, ticket.actorID, ticket.instanceID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	delta, err := h.upsertMemberJoined(r.Context(), tx, ticket.instanceID, ticket.actorID, ticket.metadata)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	if delta > 0 {
		if _, err := tx.ExecContext(r.Context(), `UPDATE instances SET current_users = current_users + 1 WHERE id = $1`, ticket.instanceID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
			return
		}
	}
	presenceExpiresAt := time.Now().UTC().Add(joinedPresenceTTL)
	if _, err := tx.ExecContext(r.Context(), `
INSERT INTO presence_sessions (actor_id, world_id, instance_id, status, visibility, show_exact_instance, expires_at)
VALUES ($1, $2, $3, 'online', $4, $5, $6)
ON CONFLICT (actor_id) DO UPDATE
SET world_id = EXCLUDED.world_id, instance_id = EXCLUDED.instance_id,
    status = EXCLUDED.status, visibility = EXCLUDED.visibility,
    show_exact_instance = EXCLUDED.show_exact_instance,
    expires_at = EXCLUDED.expires_at, updated_at = now()`,
		ticket.actorID, target.worldID, ticket.instanceID, ticket.presenceVisibility,
		ticket.showExactInstance, presenceExpiresAt,
	); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE instance_join_tickets
SET consumed_at = now(), consumed_by_credential_id = $2
WHERE id = $1 AND consumed_at IS NULL`, ticket.id, identity.id); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	if err := insertJoinAudit(r, tx, identity, &ticket, "accepted"); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "consume_join_ticket_failed", err.Error())
		return
	}
	instance, err := h.loadByID(r.Context(), ticket.instanceID)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "load_instance_failed", err.Error())
		return
	}
	_ = realtime.PublishPresenceChanged(r.Context(), h.db, h.events, ticket.actorID)
	for _, previousInstanceID := range leftInstances {
		_ = realtime.PublishInstanceChanged(r.Context(), h.db, h.events, previousInstanceID, "instance.updated")
	}
	_ = realtime.PublishInstanceChanged(r.Context(), h.db, h.events, ticket.instanceID, "instance.updated")
	httpx.WriteJSON(w, http.StatusOK, ConsumeJoinTicketResponse{
		State: "joined", ActorID: ticket.actorID, Acct: ticket.acct, PresenceVisibility: ticket.presenceVisibility, ShowExactInstance: ticket.showExactInstance,
		PresenceExpiresAt: presenceExpiresAt, Instance: instance,
	})
}

func (h *Handler) CreateWorldServerCredential(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var req createWorldServerCredentialRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 120 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_name", "name must contain 1 to 120 characters")
		return
	}
	metadata, err := dbx.MarshalJSON(req.Metadata, "{}")
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_metadata", "metadata must be JSON")
		return
	}
	token, err := newOpaqueToken(serverCredentialPrefix)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_credential_failed", err.Error())
		return
	}
	prefix := token
	if len(prefix) > 18 {
		prefix = prefix[:18]
	}
	credential, err := scanWorldServerCredential(h.db.QueryRowContext(r.Context(), `
INSERT INTO world_server_credentials (
  name, token_prefix, token_hash, allowed_world_id, created_by_user_id, metadata
)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, name, token_prefix, allowed_world_id, status, metadata,
          last_used_at, revoked_at, created_at`,
		req.Name, prefix, hashOpaqueToken(token), dbx.NullUUID(req.AllowedWorldID), principal.UserID, metadata,
	))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "create_credential_failed", err.Error())
		return
	}
	credential.Token = token
	httpx.WriteJSON(w, http.StatusCreated, credential)
}

func (h *Handler) ListWorldServerCredentials(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	requestPage, err := page.ParseRequest(r, 50, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	cursorTime, cursorID := cursorTimeAndID(requestPage.Cursor)
	rows, err := h.db.QueryContext(r.Context(), listWorldServerCredentialsSQL,
		cursorTime, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_credentials_failed", err.Error())
		return
	}
	defer rows.Close()
	items := []WorldServerCredentialResponse{}
	for rows.Next() {
		item, err := scanWorldServerCredential(rows)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "list_credentials_failed", err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_credentials_failed", err.Error())
		return
	}
	nextCursor := (*string)(nil)
	if len(items) > requestPage.Limit {
		last := items[requestPage.Limit-1]
		nextCursor = page.NextCursor(page.Cursor{SortTime: last.CreatedAt, ID: last.ID})
		items = items[:requestPage.Limit]
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[WorldServerCredentialResponse]{
		Data: items, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit},
	})
}

func (h *Handler) RevokeWorldServerCredential(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "credentialId"))
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_id", "credentialId must be a uuid")
		return
	}
	result, err := h.db.ExecContext(r.Context(), `
UPDATE world_server_credentials
SET status = 'revoked', revoked_at = COALESCE(revoked_at, now())
WHERE id = $1`, id)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "revoke_credential_failed", err.Error())
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		httpx.WriteError(w, http.StatusNotFound, "not_found", "credential not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListInstanceJoinAudit(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireAdmin(w, r); !ok {
		return
	}
	requestPage, err := page.ParseRequest(r, 50, 100)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_pagination", err.Error())
		return
	}
	cursorTime, cursorID := cursorTimeAndID(requestPage.Cursor)
	rows, err := h.db.QueryContext(r.Context(), listInstanceJoinAuditSQL,
		cursorTime, cursorID, requestPage.Limit+1)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_join_audit_failed", err.Error())
		return
	}
	defer rows.Close()
	items := []InstanceJoinAuditResponse{}
	for rows.Next() {
		var item InstanceJoinAuditResponse
		var ticketID, credentialID, actorID, instanceID uuid.NullUUID
		var details []byte
		if err := rows.Scan(
			&item.ID, &ticketID, &credentialID, &actorID, &instanceID,
			&item.Outcome, &item.RemoteIP, &details, &item.CreatedAt,
		); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "list_join_audit_failed", err.Error())
			return
		}
		item.TicketID = dbx.UUIDPtr(ticketID)
		item.CredentialID = dbx.UUIDPtr(credentialID)
		item.ActorID = dbx.UUIDPtr(actorID)
		item.InstanceID = dbx.UUIDPtr(instanceID)
		item.Details = dbx.DecodeJSON(details, map[string]any{})
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "list_join_audit_failed", err.Error())
		return
	}
	nextCursor := (*string)(nil)
	if len(items) > requestPage.Limit {
		last := items[requestPage.Limit-1]
		nextCursor = page.NextCursor(page.Cursor{SortTime: last.CreatedAt, ID: last.ID})
		items = items[:requestPage.Limit]
	}
	httpx.WriteJSON(w, http.StatusOK, page.Response[InstanceJoinAuditResponse]{
		Data: items, Pagination: page.Metadata{NextCursor: nextCursor, Limit: requestPage.Limit},
	})
}

func (h *Handler) authenticateWorldServer(w http.ResponseWriter, r *http.Request) (worldServerIdentity, bool) {
	token := strings.TrimSpace(r.Header.Get("X-Basis-Service-Token"))
	if !strings.HasPrefix(token, serverCredentialPrefix) {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_service_credential", "valid world-server credential required")
		return worldServerIdentity{}, false
	}
	var identity worldServerIdentity
	err := h.db.QueryRowContext(r.Context(), authenticateWorldServerSQL, hashOpaqueToken(token)).Scan(
		&identity.id, &identity.name, &identity.allowedWorldID,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusUnauthorized, "invalid_service_credential", "valid world-server credential required")
			return worldServerIdentity{}, false
		}
		httpx.WriteError(w, http.StatusInternalServerError, "service_auth_failed", err.Error())
		return worldServerIdentity{}, false
	}
	return identity, true
}

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, err := auth.RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return auth.Principal{}, false
	}
	var role string
	if err := h.db.QueryRowContext(r.Context(), `
SELECT role FROM users WHERE id = $1 AND status = 'active'`, principal.UserID).Scan(&role); err != nil {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "admin role required")
		return auth.Principal{}, false
	}
	if role != "admin" {
		httpx.WriteError(w, http.StatusForbidden, "forbidden", "admin role required")
		return auth.Principal{}, false
	}
	return principal, true
}

func loadConsumableJoinTicket(r *http.Request, tx *sql.Tx, hash string) (consumableJoinTicket, error) {
	var ticket consumableJoinTicket
	err := tx.QueryRowContext(r.Context(), loadJoinTicketForConsumeSQL, hash).Scan(
		&ticket.id, &ticket.actorID, &ticket.instanceID, &ticket.presenceVisibility,
		&ticket.showExactInstance, &ticket.metadata, &ticket.expiresAt,
		&ticket.consumedAt, &ticket.acct, &ticket.localUserStatus, &ticket.clientDID,
	)
	return ticket, err
}

func scanWorldServerCredential(row scanner) (WorldServerCredentialResponse, error) {
	var item WorldServerCredentialResponse
	var allowedWorldID uuid.NullUUID
	var metadata []byte
	var lastUsedAt sql.NullTime
	var revokedAt sql.NullTime
	if err := row.Scan(
		&item.ID, &item.Name, &item.TokenPrefix, &allowedWorldID, &item.Status,
		&metadata, &lastUsedAt, &revokedAt, &item.CreatedAt,
	); err != nil {
		return WorldServerCredentialResponse{}, err
	}
	item.AllowedWorldID = dbx.UUIDPtr(allowedWorldID)
	item.Metadata = dbx.DecodeJSON(metadata, map[string]any{})
	if lastUsedAt.Valid {
		item.LastUsedAt = &lastUsedAt.Time
	}
	if revokedAt.Valid {
		item.RevokedAt = &revokedAt.Time
	}
	return item, nil
}

func (h *Handler) recordJoinAudit(r *http.Request, identity worldServerIdentity, ticket *consumableJoinTicket, outcome string) {
	_, _ = h.db.ExecContext(r.Context(), `
INSERT INTO instance_join_audit (
  ticket_id, credential_id, actor_id, instance_id, outcome, remote_ip
)
VALUES ($1, $2, $3, $4, $5, $6)`, auditTicketID(ticket), identity.id, auditActorID(ticket), auditInstanceID(ticket), outcome, requestIP(r))
}

func (h *Handler) rollbackAndRecordJoinAudit(r *http.Request, tx *sql.Tx, identity worldServerIdentity, ticket *consumableJoinTicket, outcome string) {
	_ = tx.Rollback()
	h.recordJoinAudit(r, identity, ticket, outcome)
}

func insertJoinAudit(r *http.Request, tx *sql.Tx, identity worldServerIdentity, ticket *consumableJoinTicket, outcome string) error {
	_, err := tx.ExecContext(r.Context(), `
INSERT INTO instance_join_audit (
  ticket_id, credential_id, actor_id, instance_id, outcome, remote_ip
)
VALUES ($1, $2, $3, $4, $5, $6)`, auditTicketID(ticket), identity.id, auditActorID(ticket), auditInstanceID(ticket), outcome, requestIP(r))
	return err
}

func auditTicketID(ticket *consumableJoinTicket) uuid.NullUUID {
	if ticket == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: ticket.id, Valid: true}
}

func auditActorID(ticket *consumableJoinTicket) uuid.NullUUID {
	if ticket == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: ticket.actorID, Valid: true}
}

func auditInstanceID(ticket *consumableJoinTicket) uuid.NullUUID {
	if ticket == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: ticket.instanceID, Valid: true}
}

func newOpaqueToken(prefix string) (string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(random), nil
}

func hashOpaqueToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func requestIP(r *http.Request) string {
	value := strings.TrimSpace(r.RemoteAddr)
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

// Canonical Ed25519 multicodec (0xed, 0x01) and exactly 32 key bytes, base58btc.
func validClientDID(value string) bool {
	if !strings.HasPrefix(value, "did:key:z") || len(value) > 64 {
		return false
	}
	encoded := strings.TrimPrefix(value, "did:key:z")
	if len(encoded) != 47 {
		return false
	}
	n := new(big.Int)
	radix := big.NewInt(58)
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for _, char := range encoded {
		digit := strings.IndexRune(alphabet, char)
		if digit < 0 {
			return false
		}
		n.Mul(n, radix)
		n.Add(n, big.NewInt(int64(digit)))
	}
	decoded := n.Bytes()
	return len(decoded) == 34 && decoded[0] == 0xed && decoded[1] == 0x01
}
