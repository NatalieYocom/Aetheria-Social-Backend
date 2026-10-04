package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"basisvr-social-service/internal/activitypub"
	"basisvr-social-service/internal/actorcrypto"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/common/validate"
	"basisvr-social-service/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

type Handler struct {
	db     *sql.DB
	cfg    config.Config
	tokens TokenManager
}

func NewHandler(db *sql.DB, cfg config.Config, tokens TokenManager) *Handler {
	return &Handler{db: db, cfg: cfg, tokens: tokens}
}

func RegisterRoutes(r chi.Router, h *Handler, authMiddleware func(http.Handler) http.Handler) {
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", h.Register)
		r.Post("/login", h.Login)
		r.With(authMiddleware).Post("/logout", h.Logout)
		r.With(authMiddleware).Post("/logout-all", h.Logout)
		r.With(authMiddleware).Post("/logout-current", h.LogoutCurrent)
		r.With(authMiddleware).Get("/sessions", h.ListSessions)
		r.With(authMiddleware).Delete("/sessions/{id}", h.RevokeSession)
		r.Post("/refresh", h.Refresh)
	})
	r.With(authMiddleware).Get("/api/me", h.Me)
	r.With(authMiddleware).Get("/api/me/export", h.ExportAccount)
	r.With(authMiddleware).Delete("/api/me", h.DeleteAccount)
}

type registerRequest struct {
	Email       string `json:"email"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

type loginRequest struct {
	Login    string `json:"login"`
	Email    string `json:"email"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

type deleteAccountRequest struct {
	Password string `json:"password"`
}

type authResponse struct {
	TokenPair
	User meResponse `json:"user"`
}

type meResponse struct {
	ID          uuid.UUID        `json:"id"`
	ActorID     uuid.UUID        `json:"actorId"`
	Email       string           `json:"email"`
	Username    string           `json:"username"`
	Acct        string           `json:"acct"`
	Status      string           `json:"status"`
	Profile     profileResponse  `json:"profile"`
	ActivityPub actorAPIResponse `json:"activityPub"`
	AuthVersion int64            `json:"-"`
}

type profileResponse struct {
	DisplayName     string         `json:"displayName"`
	Bio             string         `json:"bio"`
	AvatarURL       string         `json:"avatarUrl"`
	BannerURL       string         `json:"bannerUrl"`
	StatusText      string         `json:"statusText"`
	Links           []any          `json:"links"`
	PrivacySettings map[string]any `json:"privacySettings"`
}

type actorAPIResponse struct {
	ActorURI     string `json:"actorUri"`
	InboxURL     string `json:"inboxUrl"`
	OutboxURL    string `json:"outboxUrl"`
	FollowersURL string `json:"followersUrl"`
	FollowingURL string `json:"followingUrl"`
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Security.RegistrationEnabled {
		httpx.WriteError(w, http.StatusForbidden, "registration_disabled", "registration is disabled")
		return
	}

	var req registerRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	email := strings.ToLower(strings.TrimSpace(req.Email))
	username := strings.ToLower(strings.TrimSpace(req.Username))
	if !validate.Email(email) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_email", "email is invalid")
		return
	}
	if !validate.Username(username) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_username", "username must be 3-32 characters and contain only letters, numbers, dot, dash or underscore")
		return
	}
	if len(req.Password) < 8 || len(req.Password) > 72 {
		httpx.WriteError(w, http.StatusBadRequest, "weak_password", "password must contain 8-72 bytes")
		return
	}

	passwordHash, err := HashPassword(req.Password)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "password_hash_failed", "could not hash password")
		return
	}

	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		displayName = username
	}

	user, err := h.createLocalUser(r.Context(), email, username, displayName, passwordHash)
	if err != nil {
		if isUniqueViolation(err) {
			httpx.WriteError(w, http.StatusConflict, "user_exists", "email or username is already registered")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "register_failed", err.Error())
		return
	}

	pair, err := NewSessionStore(h.db, h.tokens).Start(r.Context(), TokenSubject{
		UserID:   user.ID.String(),
		ActorID:  user.ActorID.String(),
		Username: user.Username,
		Version:  user.AuthVersion,
	})
	if errors.Is(err, ErrSessionInvalid) {
		httpx.WriteError(w, 401, "token_revoked", "account credentials changed; sign in again")
		return
	}
	if errors.Is(err, ErrSessionLimit) {
		httpx.WriteError(w, 409, "session_limit", "revoke an existing device session before signing in")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "token_failed", "could not issue token")
		return
	}

	httpx.WriteJSON(w, http.StatusCreated, authResponse{TokenPair: pair, User: user})
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	login := strings.ToLower(strings.TrimSpace(req.Login))
	if login == "" {
		login = strings.ToLower(strings.TrimSpace(req.Email))
	}
	if login == "" {
		login = strings.ToLower(strings.TrimSpace(req.Username))
	}
	if login == "" || req.Password == "" || len(req.Password) > 72 || len(login) > 320 {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_credentials", "login and password are required")
		return
	}

	user, passwordHash, err := h.findLoginUser(r.Context(), login)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "invalid login or password")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "login_failed", err.Error())
		return
	}
	if !VerifyPassword(passwordHash, req.Password) {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_credentials", "invalid login or password")
		return
	}

	pair, err := NewSessionStore(h.db, h.tokens).Start(r.Context(), TokenSubject{
		UserID:   user.ID.String(),
		ActorID:  user.ActorID.String(),
		Username: user.Username,
		Version:  user.AuthVersion,
	})
	if errors.Is(err, ErrSessionInvalid) {
		httpx.WriteError(w, 401, "token_revoked", "account credentials changed; sign in again")
		return
	}
	if errors.Is(err, ErrSessionLimit) {
		httpx.WriteError(w, 409, "session_limit", "revoke an existing device session before signing in")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "token_failed", "could not issue token")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, authResponse{TokenPair: pair, User: user})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	principal, err := RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `
UPDATE users SET auth_version = auth_version + 1 WHERE id = $1`, principal.UserID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "logout_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	if len(req.RefreshToken) > 8192 {
		httpx.WriteError(w, 401, "unauthorized", "invalid refresh token")
		return
	}
	claims, err := h.tokens.VerifyRefresh(req.RefreshToken)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid refresh token")
		return
	}

	userID, err := uuid.Parse(claims.Subject.UserID)
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid user id in token")
		return
	}
	user, err := h.loadMe(r.Context(), userID)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, 503, "auth_status_unavailable", "could not verify account status")
			return
		}
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "user no longer exists")
		return
	}
	if user.Status != "active" {
		httpx.WriteError(w, http.StatusUnauthorized, "account_inactive", "account is not active")
		return
	}
	if claims.Subject.Version != user.AuthVersion {
		httpx.WriteError(w, http.StatusUnauthorized, "token_revoked", "refresh token has been revoked")
		return
	}

	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		httpx.WriteError(w, 401, "unauthorized", "invalid refresh token")
		return
	}
	principal := Principal{UserID: user.ID, ActorID: user.ActorID, SessionID: sessionID, AuthVersion: user.AuthVersion, ExpiresAt: claims.ExpiresAt.Time}
	valid, err := LinkedIdentityValidator(h.db, h.cfg.BeeBa)(r.Context(), principal)
	if err != nil {
		httpx.WriteError(w, 503, "auth_status_unavailable", "could not verify identity status")
		return
	}
	if !valid {
		httpx.WriteError(w, 401, "token_revoked", "session identity has been revoked")
		return
	}

	pair, err := NewSessionStore(h.db, h.tokens).Rotate(r.Context(), req.RefreshToken, claims, TokenSubject{
		UserID:   user.ID.String(),
		ActorID:  user.ActorID.String(),
		Username: user.Username,
		Version:  user.AuthVersion,
	})
	if errors.Is(err, ErrSessionInvalid) {
		httpx.WriteError(w, 401, "token_revoked", "refresh token has been revoked")
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "token_failed", "could not issue token")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, authResponse{TokenPair: pair, User: user})
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	principal, err := RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}

	user, err := h.loadMe(r.Context(), principal.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "load_me_failed", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, user)
}

func (h *Handler) ExportAccount(w http.ResponseWriter, r *http.Request) {
	principal, err := RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var rawJSON []byte
	err = h.db.QueryRowContext(r.Context(), `
SELECT jsonb_build_object(
  'schemaVersion', 1,
  'generatedAt', now(),
  'account', (SELECT to_jsonb(u) - 'password_hash' FROM users u WHERE u.id = $1),
  'profile', (SELECT to_jsonb(p) FROM profiles p WHERE p.user_id = $1),
  'actor', (SELECT to_jsonb(a) - 'private_key_pem_encrypted' FROM actors a WHERE a.id = $2),
  'relationships', COALESCE((
    SELECT jsonb_agg(to_jsonb(rel) ORDER BY rel.created_at)
    FROM relationships rel WHERE rel.actor_id = $2 OR rel.target_actor_id = $2
  ), '[]'::jsonb),
  'worlds', COALESCE((
    SELECT jsonb_agg(to_jsonb(world) ORDER BY world.created_at)
    FROM worlds world WHERE world.owner_actor_id = $2
  ), '[]'::jsonb),
  'events', COALESCE((
    SELECT jsonb_agg(to_jsonb(event) ORDER BY event.created_at)
    FROM events event WHERE event.owner_actor_id = $2
  ), '[]'::jsonb),
  'invites', COALESCE((
    SELECT jsonb_agg(to_jsonb(invite) ORDER BY invite.created_at)
    FROM invites invite WHERE invite.from_actor_id = $2 OR invite.to_actor_id = $2
  ), '[]'::jsonb),
  'groupMemberships', COALESCE((
    SELECT jsonb_agg(to_jsonb(member) ORDER BY member.created_at)
    FROM group_members member WHERE member.actor_id = $2
  ), '[]'::jsonb),
  'notifications', COALESCE((
    SELECT jsonb_agg(to_jsonb(notification) ORDER BY notification.created_at)
    FROM notifications notification WHERE notification.actor_id = $2
  ), '[]'::jsonb)
)`, principal.UserID, principal.ActorID).Scan(&rawJSON)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "account_export_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="basisvr-account-export.json"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(rawJSON)
}

func (h *Handler) DeleteAccount(w http.ResponseWriter, r *http.Request) {
	principal, err := RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "authentication required")
		return
	}
	var req deleteAccountRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.Password) == "" || len(req.Password) > 72 {
		httpx.WriteError(w, http.StatusBadRequest, "password_required", "password is required")
		return
	}
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "account_delete_failed", err.Error())
		return
	}
	defer func() { _ = tx.Rollback() }()

	var passwordHash, domain string
	if err := tx.QueryRowContext(r.Context(), `
SELECT user_account.password_hash, actor.domain
FROM users user_account
JOIN actors actor ON actor.local_user_id = user_account.id
WHERE user_account.id = $1 AND user_account.status = 'active'
FOR UPDATE OF user_account, actor`, principal.UserID).Scan(&passwordHash, &domain); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			httpx.WriteError(w, http.StatusNotFound, "not_found", "active account not found")
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, "account_delete_failed", err.Error())
		return
	}
	var managed bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM beeba_identity_links WHERE user_id=$1)`, principal.UserID).Scan(&managed); err != nil {
		httpx.WriteError(w, 503, "auth_status_unavailable", "could not verify identity status")
		return
	}
	if managed {
		httpx.WriteError(w, 409, "managed_identity", "linked account deletion requires BeeBa identity confirmation")
		return
	}
	if !VerifyPassword(passwordHash, req.Password) {
		httpx.WriteError(w, http.StatusUnauthorized, "invalid_password", "password is invalid")
		return
	}
	replacementHash, err := HashPassword(uuid.NewString() + uuid.NewString())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "account_delete_failed", err.Error())
		return
	}

	cleanupStatements := []string{
		`DELETE FROM inbox_messages WHERE recipient_actor_id = $1 OR sender_actor_id = $1`,
		`DELETE FROM activities WHERE actor_id = $1`,
		`DELETE FROM activitypub_objects WHERE attributed_to_actor_id = $1`,
		`DELETE FROM notifications WHERE actor_id = $1`,
		`DELETE FROM invites WHERE from_actor_id = $1 OR to_actor_id = $1`,
		`DELETE FROM presence_sessions WHERE actor_id = $1`,
		`DELETE FROM relationships WHERE actor_id = $1 OR target_actor_id = $1`,
		`DELETE FROM event_rsvps WHERE actor_id = $1`,
		`DELETE FROM world_favorites WHERE actor_id = $1`,
		`DELETE FROM group_members WHERE actor_id = $1`,
		`DELETE FROM group_worlds WHERE added_by_actor_id = $1`,
		`DELETE FROM group_events WHERE added_by_actor_id = $1`,
		`WITH owned AS (DELETE FROM groups WHERE owner_actor_id = $1 RETURNING actor_id)
         DELETE FROM actors WHERE id IN (SELECT actor_id FROM owned)`,
		`DELETE FROM worlds WHERE owner_actor_id = $1`,
	}
	for _, statement := range cleanupStatements {
		if _, err := tx.ExecContext(r.Context(), statement, principal.ActorID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "account_delete_failed", err.Error())
			return
		}
	}
	anonymized := "deleted_" + strings.ReplaceAll(principal.UserID.String(), "-", "")
	if _, err := tx.ExecContext(r.Context(), `
UPDATE profiles
SET display_name = 'Deleted user', bio = '', avatar_url = '', banner_url = '',
    status_text = '', links = '[]'::jsonb, privacy_settings = '{}'::jsonb
WHERE user_id = $1`, principal.UserID); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "account_delete_failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE actors
SET acct = $2, preferred_username = $3, display_name = 'Deleted user',
    public_key_pem = '', private_key_pem_encrypted = NULL, raw_json = '{}'::jsonb
WHERE id = $1`, principal.ActorID, anonymized+"@"+domain, anonymized); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "account_delete_failed", err.Error())
		return
	}
	if _, err := tx.ExecContext(r.Context(), `
UPDATE users
SET email = $2, username = $3, password_hash = $4,
    status = 'deleted', auth_version = auth_version + 1
WHERE id = $1`, principal.UserID, anonymized+"@deleted.invalid", anonymized, replacementHash); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "account_delete_failed", err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "account_delete_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) createLocalUser(ctx context.Context, email, username, displayName, passwordHash string) (meResponse, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return meResponse{}, err
	}
	defer tx.Rollback()
	id, err := h.createLocalUserTx(ctx, tx, email, username, displayName, passwordHash)
	if err != nil {
		return meResponse{}, err
	}
	if err = tx.Commit(); err != nil {
		return meResponse{}, err
	}
	return h.loadMe(ctx, id)
}

func (h *Handler) createLocalUserTx(ctx context.Context, tx *sql.Tx, email, username, displayName, passwordHash string) (uuid.UUID, error) {
	var userID uuid.UUID
	if err := tx.QueryRowContext(ctx, `
INSERT INTO users (email, password_hash, username)
VALUES ($1, $2, $3)
RETURNING id`, email, passwordHash, username).Scan(&userID); err != nil {
		return uuid.Nil, err
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO profiles (user_id, display_name)
VALUES ($1, $2)`, userID, displayName); err != nil {
		return uuid.Nil, err
	}

	meta := activitypub.BuildLocalActorMetadata(activitypub.LocalActorInput{
		PublicURL:   h.cfg.Server.PublicURL,
		Username:    username,
		DisplayName: displayName,
	})
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		return uuid.Nil, err
	}
	meta.PublicKeyPEM = keyPair.PublicKeyPEM
	meta.PrivateKeyEncrypted, err = actorcrypto.Encrypt(h.cfg.ActivityPub.ActorKeyEncryptionKey, meta.ActorURI, keyPair.PrivateKeyPEM)
	if err != nil {
		return uuid.Nil, err
	}
	rawJSON, err := json.Marshal(activitypub.BuildPersonActorDocument(meta))
	if err != nil {
		return uuid.Nil, err
	}

	var actorID uuid.UUID
	if err := tx.QueryRowContext(ctx, `
INSERT INTO actors (
  local_user_id, actor_uri, acct, type, preferred_username, display_name, domain,
  inbox_url, outbox_url, followers_url, following_url, shared_inbox_url,
  public_key_pem, private_key_pem_encrypted, is_local, raw_json
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, true, $15)
RETURNING id`,
		userID, meta.ActorURI, meta.Acct, meta.Type, meta.PreferredUsername, meta.DisplayName, meta.Domain,
		meta.InboxURL, meta.OutboxURL, meta.FollowersURL, meta.FollowingURL, meta.SharedInboxURL,
		meta.PublicKeyPEM, meta.PrivateKeyEncrypted, rawJSON,
	).Scan(&actorID); err != nil {
		return uuid.Nil, err
	}

	return userID, nil
}

func (h *Handler) findLoginUser(ctx context.Context, login string) (meResponse, string, error) {
	var userID uuid.UUID
	var passwordHash string
	err := h.db.QueryRowContext(ctx, `
SELECT u.id, u.password_hash
FROM users u
WHERE (u.email = $1 OR u.username = $1) AND u.status = 'active'
 AND NOT EXISTS (SELECT 1 FROM beeba_identity_links l WHERE l.user_id=u.id)`, login).Scan(&userID, &passwordHash)
	if err != nil {
		return meResponse{}, "", err
	}
	user, err := h.loadMe(ctx, userID)
	return user, passwordHash, err
}

func (h *Handler) loadMe(ctx context.Context, userID uuid.UUID) (meResponse, error) {
	var res meResponse
	var linksRaw []byte
	var privacyRaw []byte
	err := h.db.QueryRowContext(ctx, `
SELECT
  u.id, u.email, u.username, u.status, u.auth_version,
  p.display_name, p.bio, p.avatar_url, p.banner_url, p.status_text, p.links, p.privacy_settings,
  a.id, a.acct, a.actor_uri, a.inbox_url, a.outbox_url, a.followers_url, a.following_url
FROM users u
JOIN profiles p ON p.user_id = u.id
JOIN actors a ON a.local_user_id = u.id
WHERE u.id = $1`, userID).Scan(
		&res.ID, &res.Email, &res.Username, &res.Status, &res.AuthVersion,
		&res.Profile.DisplayName, &res.Profile.Bio, &res.Profile.AvatarURL, &res.Profile.BannerURL, &res.Profile.StatusText, &linksRaw, &privacyRaw,
		&res.ActorID, &res.Acct, &res.ActivityPub.ActorURI, &res.ActivityPub.InboxURL, &res.ActivityPub.OutboxURL, &res.ActivityPub.FollowersURL, &res.ActivityPub.FollowingURL,
	)
	if err != nil {
		return meResponse{}, err
	}
	res.Profile.Links = decodeArray(linksRaw)
	res.Profile.PrivacySettings = decodeObject(privacyRaw)
	if err := h.applyBeeBaProfile(ctx, &res); err != nil {
		return meResponse{}, err
	}
	return res, nil
}

func decodeArray(data []byte) []any {
	var value []any
	if err := json.Unmarshal(data, &value); err != nil {
		return []any{}
	}
	return value
}

func decodeObject(data []byte) map[string]any {
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		return map[string]any{}
	}
	return value
}

func isUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == "23505"
}
