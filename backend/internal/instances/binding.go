package instances

import (
	"basisvr-social-service/internal/common/httpx"
	"database/sql"
	"errors"
	"github.com/google/uuid"
	"net/http"
)

func (h *Handler) BindWorldServer(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	instanceID, ok := parseUUIDParam(w, r, "id")
	if !ok {
		return
	}
	var req struct {
		CredentialID uuid.UUID `json:"credentialId"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil || req.CredentialID == uuid.Nil {
		httpx.WriteError(w, 400, "invalid_credential", "credentialId is required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, 500, "bind_failed", err.Error())
		return
	}
	defer tx.Rollback()
	target, err := loadRuntimeInstance(r.Context(), tx, instanceID)
	if err != nil {
		writeRuntimeInstanceError(w, err)
		return
	}
	var credentialWorld uuid.NullUUID
	err = tx.QueryRowContext(r.Context(), `SELECT allowed_world_id FROM world_server_credentials WHERE id=$1 AND status='active' FOR SHARE`, req.CredentialID).Scan(&credentialWorld)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteError(w, 404, "not_found", "active credential not found")
		return
	}
	if err != nil {
		httpx.WriteError(w, 500, "bind_failed", err.Error())
		return
	}
	if !credentialWorld.Valid || credentialWorld.UUID != target.worldID {
		httpx.WriteError(w, 403, "credential_scope_denied", "credential must be scoped to this exact world")
		return
	}
	var users int
	if err = tx.QueryRowContext(r.Context(), `SELECT current_users FROM instances WHERE id=$1`, instanceID).Scan(&users); err != nil {
		httpx.WriteError(w, 500, "bind_failed", err.Error())
		return
	}
	if users > 0 && (!target.credentialID.Valid || target.credentialID.UUID != req.CredentialID) {
		httpx.WriteError(w, 409, "instance_occupied", "cannot replace credential while instance is occupied")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE instances SET world_server_credential_id=$2 WHERE id=$1`, instanceID, req.CredentialID); err != nil {
		httpx.WriteError(w, 500, "bind_failed", err.Error())
		return
	}
	if _, err = tx.ExecContext(r.Context(), `INSERT INTO instance_join_audit (credential_id,actor_id,instance_id,outcome,remote_ip) VALUES ($1,$2,$3,'server_bound',$4)`, req.CredentialID, principal.ActorID, instanceID, requestIP(r)); err != nil {
		httpx.WriteError(w, 500, "bind_failed", err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		httpx.WriteError(w, 500, "bind_failed", err.Error())
		return
	}
	w.WriteHeader(204)
}
