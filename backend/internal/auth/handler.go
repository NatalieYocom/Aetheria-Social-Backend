package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"basisvr-social-service/internal/activitypub"
	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/common/validate"
	"basisvr-social-service/internal/config"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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
		r.Post("/refresh", h.Refresh)
	})
	r.With(authMiddleware).Get("/api/me", h.Me)
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
	if len(req.Password) < 8 {
		httpx.WriteError(w, http.StatusBadRequest, "weak_password", "password must be at least 8 characters")
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

	pair, err := h.tokens.Issue(TokenSubject{
		UserID:   user.ID.String(),
		ActorID:  user.ActorID.String(),
		Username: user.Username,
	})
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
	if login == "" || req.Password == "" {
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

	pair, err := h.tokens.Issue(TokenSubject{
		UserID:   user.ID.String(),
		ActorID:  user.ActorID.String(),
		Username: user.Username,
	})
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "token_failed", "could not issue token")
		return
	}

	httpx.WriteJSON(w, http.StatusOK, authResponse{TokenPair: pair, User: user})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid_json", err.Error())
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
		httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "user no longer exists")
		return
	}

	pair, err := h.tokens.Issue(TokenSubject{
		UserID:   user.ID.String(),
		ActorID:  user.ActorID.String(),
		Username: user.Username,
	})
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

func (h *Handler) createLocalUser(ctx context.Context, email, username, displayName, passwordHash string) (meResponse, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return meResponse{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var userID uuid.UUID
	if err := tx.QueryRowContext(ctx, `
INSERT INTO users (email, password_hash, username)
VALUES ($1, $2, $3)
RETURNING id`, email, passwordHash, username).Scan(&userID); err != nil {
		return meResponse{}, err
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO profiles (user_id, display_name)
VALUES ($1, $2)`, userID, displayName); err != nil {
		return meResponse{}, err
	}

	meta := activitypub.BuildLocalActorMetadata(activitypub.LocalActorInput{
		PublicURL:   h.cfg.Server.PublicURL,
		Username:    username,
		DisplayName: displayName,
	})
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		return meResponse{}, err
	}
	meta.PublicKeyPEM = keyPair.PublicKeyPEM
	meta.PrivateKeyEncrypted = keyPair.PrivateKeyPEM
	rawJSON, err := json.Marshal(activitypub.BuildPersonActorDocument(meta))
	if err != nil {
		return meResponse{}, err
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
		return meResponse{}, err
	}

	if err := tx.Commit(); err != nil {
		return meResponse{}, err
	}

	return h.loadMe(ctx, userID)
}

func (h *Handler) findLoginUser(ctx context.Context, login string) (meResponse, string, error) {
	var userID uuid.UUID
	var passwordHash string
	err := h.db.QueryRowContext(ctx, `
SELECT u.id, u.password_hash
FROM users u
WHERE (u.email = $1 OR u.username = $1) AND u.status = 'active'`, login).Scan(&userID, &passwordHash)
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
  u.id, u.email, u.username, u.status,
  p.display_name, p.bio, p.avatar_url, p.banner_url, p.status_text, p.links, p.privacy_settings,
  a.id, a.acct, a.actor_uri, a.inbox_url, a.outbox_url, a.followers_url, a.following_url
FROM users u
JOIN profiles p ON p.user_id = u.id
JOIN actors a ON a.local_user_id = u.id
WHERE u.id = $1`, userID).Scan(
		&res.ID, &res.Email, &res.Username, &res.Status,
		&res.Profile.DisplayName, &res.Profile.Bio, &res.Profile.AvatarURL, &res.Profile.BannerURL, &res.Profile.StatusText, &linksRaw, &privacyRaw,
		&res.ActorID, &res.Acct, &res.ActivityPub.ActorURI, &res.ActivityPub.InboxURL, &res.ActivityPub.OutboxURL, &res.ActivityPub.FollowersURL, &res.ActivityPub.FollowingURL,
	)
	if err != nil {
		return meResponse{}, err
	}
	res.Profile.Links = decodeArray(linksRaw)
	res.Profile.PrivacySettings = decodeObject(privacyRaw)
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
	return strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "SQLSTATE 23505")
}
