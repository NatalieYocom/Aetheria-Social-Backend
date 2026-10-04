package auth

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"basisvr-social-service/internal/common/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func RegisterBeeBaRoutes(r chi.Router, h *Handler) {
	r.Post("/api/auth/beeba/start", h.BeeBaStart)
	r.Post("/api/auth/beeba/complete", h.BeeBaComplete)
	r.Get("/api/internal/beeba/identities/{id}", h.BeeBaLinkStatus)
}
func writeBeeBaError(w http.ResponseWriter, err error) {
	var upstream *beeBaError
	if errors.As(err, &upstream) {
		httpx.WriteError(w, upstream.Status, upstream.Code, "Identity authorization could not complete.")
		return
	}
	httpx.WriteError(w, http.StatusServiceUnavailable, "identity_unavailable", "Identity service is unavailable.")
}
func (h *Handler) BeeBaStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CodeChallenge string `json:"codeChallenge"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, 400, "invalid_request", "Invalid authorization request.")
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(req.CodeChallenge)
	if err != nil || len(raw) != 32 || len(req.CodeChallenge) != 43 {
		httpx.WriteError(w, 400, "invalid_request", "S256 proof challenge is required.")
		return
	}
	input := map[string]string{"code_challenge": req.CodeChallenge}
	if p, ok := PrincipalFromContext(r.Context()); ok {
		input["target_user_id"] = p.UserID.String()
		input["target_username"] = p.Username
		input["target_session_id"] = p.SessionID.String()
	}
	var response struct {
		Data struct {
			DeviceCode      string `json:"device_code"`
			UserCode        string `json:"user_code"`
			VerificationURI string `json:"verification_uri"`
			ExpiresIn       int    `json:"expires_in"`
			Interval        int    `json:"interval"`
		} `json:"data"`
	}
	if err := beeBaRequest(r.Context(), h.cfg.BeeBa, http.MethodPost, "/authorizations", input, &response); err != nil {
		writeBeeBaError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, 201, map[string]any{"deviceCode": response.Data.DeviceCode, "userCode": response.Data.UserCode, "verificationUri": response.Data.VerificationURI, "expiresIn": response.Data.ExpiresIn, "interval": response.Data.Interval})
}
func (h *Handler) BeeBaComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceCode   string `json:"deviceCode"`
		CodeVerifier string `json:"codeVerifier"`
	}
	if err := httpx.DecodeJSON(r, &req); err != nil || len(req.DeviceCode) > 256 || len(req.DeviceCode) < 32 || len(req.CodeVerifier) < 43 || len(req.CodeVerifier) > 128 {
		httpx.WriteError(w, 400, "invalid_request", "Invalid authorization proof.")
		return
	}
	var response struct {
		Data struct {
			Identity        beeBaIdentity `json:"identity"`
			TargetUserID    string        `json:"target_user_id"`
			TargetSessionID string        `json:"target_session_id"`
		} `json:"data"`
	}
	if err := beeBaRequest(r.Context(), h.cfg.BeeBa, http.MethodPost, "/authorizations/redeem", map[string]string{"device_code": req.DeviceCode, "code_verifier": req.CodeVerifier}, &response); err != nil {
		writeBeeBaError(w, err)
		return
	}
	proof := response.Data
	id, err := uuid.Parse(proof.Identity.ID)
	if err != nil || !proof.Identity.Active || !proof.Identity.EmailVerified || proof.Identity.Version < 0 {
		httpx.WriteError(w, 403, "account_inactive", "Identity is unavailable.")
		return
	}
	// Recheck after consumption: recovery, bans and deletion fence a code while it was in flight.
	identity, err := loadBeeBaIdentity(r.Context(), h.cfg.BeeBa, id.String())
	if err != nil {
		writeBeeBaError(w, err)
		return
	}
	if !identity.Active || !identity.EmailVerified || identity.Version != proof.Identity.Version {
		httpx.WriteError(w, 401, "token_revoked", "Authorization was revoked.")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeBeeBaError(w, err)
		return
	}
	defer tx.Rollback()
	// Serialize provisioning/linking for one immutable authority subject, not email or username.
	if _, err = tx.ExecContext(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, h.cfg.BeeBa.PublicURL+":"+id.String()); err != nil {
		writeBeeBaError(w, err)
		return
	}
	var userID uuid.UUID
	err = tx.QueryRowContext(r.Context(), `SELECT user_id FROM beeba_identity_links WHERE issuer=$1 AND beeba_user_id=$2`, h.cfg.BeeBa.PublicURL, id).Scan(&userID)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		writeBeeBaError(w, err)
		return
	}
	if proof.TargetUserID != "" {
		target, parseErr := uuid.Parse(proof.TargetUserID)
		sessionID, sessionErr := uuid.Parse(proof.TargetSessionID)
		if parseErr != nil || sessionErr != nil {
			httpx.WriteError(w, 400, "invalid_grant", "Invalid link target.")
			return
		}
		if exists && userID != target {
			httpx.WriteError(w, 409, "identity_already_linked", "Identity is already linked.")
			return
		}
		var valid bool
		// User lock is also used by refresh/logout/delete, preventing a revoked proof from linking.
		if err = tx.QueryRowContext(r.Context(), `SELECT status='active' FROM users WHERE id=$1 FOR UPDATE`, target).Scan(&valid); err != nil || !valid {
			httpx.WriteError(w, 401, "token_revoked", "Link session is unavailable.")
			return
		}
		if err = tx.QueryRowContext(r.Context(), `SELECT true FROM auth_sessions s JOIN users u ON u.id=s.user_id WHERE s.id=$1 AND s.user_id=$2 AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() AND s.auth_version=u.auth_version FOR UPDATE OF s`, sessionID, target).Scan(&valid); err != nil || !valid {
			httpx.WriteError(w, 401, "token_revoked", "Link session is unavailable.")
			return
		}
		userID = target
	} else if !exists {
		// Internal email is not a login or recovery channel. Never match an existing email/handle.
		nonce := strings.ReplaceAll(uuid.NewString(), "-", "")
		username := "beeba_" + nonce[:26]
		userID, err = h.createLocalUserTx(r.Context(), tx, "beeba-"+nonce+"@identity.invalid", username, identity.DisplayName, "!beeba-identity-only")
		if err != nil {
			writeBeeBaError(w, err)
			return
		}
	}
	var status string
	var version int64
	var actorID uuid.UUID
	var username string
	err = tx.QueryRowContext(r.Context(), `SELECT u.status,u.auth_version,a.id,u.username FROM users u JOIN actors a ON a.local_user_id=u.id WHERE u.id=$1 FOR UPDATE OF u`, userID).Scan(&status, &version, &actorID, &username)
	if err != nil {
		writeBeeBaError(w, err)
		return
	}
	if status != "active" {
		httpx.WriteError(w, 403, "account_inactive", "Account is inactive.")
		return
	}
	if !exists {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO beeba_identity_links(issuer,beeba_user_id,user_id) VALUES($1,$2,$3)`, h.cfg.BeeBa.PublicURL, id, userID)
		if isUniqueViolation(err) {
			httpx.WriteError(w, 409, "identity_already_linked", "Account is already linked.")
			return
		}
		if err != nil {
			writeBeeBaError(w, err)
			return
		}
		// Retire the former credential permanently; removing a bridge must not revive it.
		if _, err = tx.ExecContext(r.Context(), `UPDATE users SET password_hash='!beeba-identity-only' WHERE id=$1`, userID); err != nil {
			writeBeeBaError(w, err)
			return
		}
		if _, err = tx.ExecContext(r.Context(), `UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE user_id=$1`, userID); err != nil {
			writeBeeBaError(w, err)
			return
		}
	}
	if err = syncBeeBaProfile(r.Context(), tx, userID, identity, h.cfg.BeeBa); err != nil {
		writeBeeBaError(w, err)
		return
	}
	pair, err := NewSessionStore(h.db, h.tokens).StartTx(r.Context(), tx, TokenSubject{UserID: userID.String(), ActorID: actorID.String(), Username: username, Version: version})
	if err != nil {
		writeBeeBaError(w, err)
		return
	}
	claims, err := h.tokens.VerifyAccess(pair.AccessToken)
	if err != nil {
		writeBeeBaError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE auth_sessions SET beeba_identity_version=$2 WHERE id=$1`, claims.SessionID, identity.Version); err != nil {
		writeBeeBaError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeBeeBaError(w, err)
		return
	}
	user, err := h.loadMe(r.Context(), userID)
	if err != nil {
		writeBeeBaError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, 200, authResponse{TokenPair: pair, User: user})
}
func (h *Handler) BeeBaLinkStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	secret := strings.TrimPrefix(r.Header.Get("X-BeeBa-Service-Key"), "Bearer ")
	if !h.cfg.BeeBa.Enabled || len(secret) != len(h.cfg.BeeBa.SharedSecret) || subtle.ConstantTimeCompare([]byte(secret), []byte(h.cfg.BeeBa.SharedSecret)) != 1 {
		httpx.WriteError(w, 401, "unauthorized", "Service authentication required.")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, 400, "invalid_request", "Invalid identity.")
		return
	}
	var userID, actorID uuid.UUID
	var username, status string
	err = h.db.QueryRowContext(r.Context(), `SELECT u.id,a.id,u.username,u.status FROM beeba_identity_links l JOIN users u ON u.id=l.user_id JOIN actors a ON a.local_user_id=u.id WHERE l.issuer=$1 AND l.beeba_user_id=$2`, h.cfg.BeeBa.PublicURL, id).Scan(&userID, &actorID, &username, &status)
	if errors.Is(err, sql.ErrNoRows) {
		httpx.WriteJSON(w, 200, map[string]any{"linked": false})
		return
	}
	if err != nil {
		writeBeeBaError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, 200, map[string]any{"linked": true, "social_user_id": userID, "social_username": username, "actor_id": actorID, "status": status})
}

func (h *Handler) applyBeeBaProfile(ctx context.Context, user *meResponse) error {
	if !h.cfg.BeeBa.Enabled {
		return nil
	}
	var issuer, id string
	err := h.db.QueryRowContext(ctx, `SELECT issuer,beeba_user_id FROM beeba_identity_links WHERE user_id=$1`, user.ID).Scan(&issuer, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if issuer != h.cfg.BeeBa.PublicURL {
		return errors.New("identity authority unavailable")
	}
	identity, err := loadBeeBaIdentity(ctx, h.cfg.BeeBa, id)
	if err != nil {
		return err
	}
	if !identity.Active || !identity.EmailVerified {
		return errors.New("identity inactive")
	}
	user.Email = identity.Email
	user.Profile.DisplayName = identity.DisplayName
	user.Profile.AvatarURL = beeBaAvatar(h.cfg.BeeBa, identity.AvatarImageID)
	return nil
}
