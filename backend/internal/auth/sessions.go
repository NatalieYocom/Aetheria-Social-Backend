package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"basisvr-social-service/internal/common/httpx"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var ErrSessionInvalid = errors.New("session is invalid")
var ErrSessionLimit = errors.New("too many active sessions")

// SessionStore persists per-device session families. Every mutation locks user before session.
type SessionStore struct {
	db     *sql.DB
	tokens TokenManager
}

func NewSessionStore(db *sql.DB, tokens TokenManager) *SessionStore {
	return &SessionStore{db: db, tokens: tokens}
}

func lockSessionUser(ctx context.Context, tx *sql.Tx, subject TokenSubject) error {
	var version int64
	var status string
	err := tx.QueryRowContext(ctx, `SELECT auth_version, status FROM users WHERE id = $1 FOR UPDATE`, subject.UserID).Scan(&version, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSessionInvalid
	}
	if err != nil {
		return err
	}
	if status != "active" || version != subject.Version {
		return ErrSessionInvalid
	}
	return nil
}

func (s *SessionStore) Start(ctx context.Context, subject TokenSubject) (TokenPair, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenPair{}, err
	}
	defer tx.Rollback()
	pair, err := s.StartTx(ctx, tx, subject)
	if err != nil {
		return TokenPair{}, err
	}
	if err = tx.Commit(); err != nil {
		return TokenPair{}, err
	}
	return pair, nil
}

// StartTx lets identity linking and session creation commit together. The caller commits the transaction.
func (s *SessionStore) StartTx(ctx context.Context, tx *sql.Tx, subject TokenSubject) (TokenPair, error) {
	if err := lockSessionUser(ctx, tx, subject); err != nil {
		return TokenPair{}, err
	}
	// Expired/revoked rows cannot authenticate; prune them when the owner signs in again.
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id=$1 AND (expires_at <= clock_timestamp() OR revoked_at IS NOT NULL OR auth_version <> $2)`, subject.UserID, subject.Version); err != nil {
		return TokenPair{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM auth_sessions WHERE user_id=$1`, subject.UserID).Scan(&count); err != nil {
		return TokenPair{}, err
	}
	if count >= 100 {
		return TokenPair{}, ErrSessionLimit
	}
	id := uuid.NewString()
	expires := time.Now().UTC().Add(s.tokens.refreshTTL)
	pair, err := s.tokens.IssueSession(subject, id, expires)
	if err != nil {
		return TokenPair{}, err
	}
	hash := sha256.Sum256([]byte(pair.RefreshToken))
	_, err = tx.ExecContext(ctx, `INSERT INTO auth_sessions(id,user_id,auth_version,refresh_hash,expires_at) VALUES($1,$2,$3,$4,$5)`, id, subject.UserID, subject.Version, hash[:], expires)
	if err != nil {
		return TokenPair{}, err
	}
	return pair, nil
}

func (s *SessionStore) Rotate(ctx context.Context, raw string, claims *TokenClaims, subject TokenSubject) (TokenPair, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return TokenPair{}, err
	}
	defer tx.Rollback()
	if subject.UserID != claims.Subject.UserID || subject.Version != claims.Subject.Version {
		return TokenPair{}, ErrSessionInvalid
	}
	if err = lockSessionUser(ctx, tx, subject); err != nil {
		return TokenPair{}, err
	}
	var hash []byte
	var expires time.Time
	var revoked sql.NullTime
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT refresh_hash, expires_at, revoked_at, auth_version FROM auth_sessions WHERE id=$1 AND user_id=$2 FOR UPDATE`, claims.SessionID, subject.UserID).Scan(&hash, &expires, &revoked, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return TokenPair{}, ErrSessionInvalid
	}
	if err != nil {
		return TokenPair{}, err
	}
	if revoked.Valid || !time.Now().Before(expires) || version != subject.Version {
		return TokenPair{}, ErrSessionInvalid
	}
	incoming := sha256.Sum256([]byte(raw))
	if subtle.ConstantTimeCompare(hash, incoming[:]) != 1 {
		// A signed, unexpired token from this family was already rotated. Revoke the family atomically.
		if _, err = tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at=clock_timestamp() WHERE id=$1`, claims.SessionID); err != nil {
			return TokenPair{}, err
		}
		if err = tx.Commit(); err != nil {
			return TokenPair{}, err
		}
		return TokenPair{}, ErrSessionInvalid
	}
	pair, err := s.tokens.IssueSession(subject, claims.SessionID, expires)
	if err != nil {
		return TokenPair{}, err
	}
	next := sha256.Sum256([]byte(pair.RefreshToken))
	result, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET refresh_hash=$2,last_used_at=clock_timestamp() WHERE id=$1 AND expires_at > clock_timestamp()`, claims.SessionID, next[:])
	if err != nil {
		return TokenPair{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return TokenPair{}, err
	}
	if n != 1 {
		return TokenPair{}, ErrSessionInvalid
	}
	if err = tx.Commit(); err != nil {
		return TokenPair{}, err
	}
	return pair, nil
}

func (h *Handler) LogoutCurrent(w http.ResponseWriter, r *http.Request) {
	p, err := RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, 401, "unauthorized", "authentication required")
		return
	}
	h.revokeSession(w, r, p.SessionID, p)
}
func (h *Handler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	p, err := RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, 401, "unauthorized", "authentication required")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		httpx.WriteError(w, 400, "invalid_session_id", "invalid session id")
		return
	}
	h.revokeSession(w, r, id, p)
}
func (h *Handler) revokeSession(w http.ResponseWriter, r *http.Request, id uuid.UUID, p Principal) {
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		httpx.WriteError(w, 503, "session_revoke_failed", "could not revoke session")
		return
	}
	defer tx.Rollback()
	// Match refresh and identity linking: acquire the authority row before the
	// target session, so a revoked linking proof cannot race a new linked session.
	if err = lockSessionUser(r.Context(), tx, TokenSubject{UserID: p.UserID.String(), Version: p.AuthVersion}); err != nil {
		if errors.Is(err, ErrSessionInvalid) {
			httpx.WriteError(w, 401, "token_revoked", "session authority changed")
		} else {
			httpx.WriteError(w, 503, "session_revoke_failed", "could not revoke session")
		}
		return
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE auth_sessions SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE id=$1 AND user_id=$2`, id, p.UserID)
	if err != nil {
		httpx.WriteError(w, 500, "session_revoke_failed", "could not revoke session")
		return
	}
	n, err := result.RowsAffected()
	if err != nil {
		httpx.WriteError(w, 500, "session_revoke_failed", "could not revoke session")
		return
	}
	if n == 0 {
		httpx.WriteError(w, 404, "not_found", "session not found")
		return
	}
	if err = tx.Commit(); err != nil {
		httpx.WriteError(w, 503, "session_revoke_failed", "could not revoke session")
		return
	}
	w.WriteHeader(204)
}

type deviceSession struct {
	ID         uuid.UUID `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Current    bool      `json:"current"`
}

func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	p, err := RequirePrincipal(r.Context())
	if err != nil {
		httpx.WriteError(w, 401, "unauthorized", "authentication required")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `SELECT id,created_at,last_used_at,expires_at FROM auth_sessions WHERE user_id=$1 AND auth_version=$2 AND revoked_at IS NULL AND expires_at>clock_timestamp() ORDER BY created_at DESC,id DESC LIMIT 100`, p.UserID, p.AuthVersion)
	if err != nil {
		httpx.WriteError(w, 500, "sessions_failed", "could not load sessions")
		return
	}
	defer rows.Close()
	items := []deviceSession{}
	for rows.Next() {
		var item deviceSession
		if err := rows.Scan(&item.ID, &item.CreatedAt, &item.LastUsedAt, &item.ExpiresAt); err != nil {
			httpx.WriteError(w, 500, "sessions_failed", "could not load sessions")
			return
		}
		item.Current = item.ID == p.SessionID
		items = append(items, item)
	}
	if rows.Err() != nil {
		httpx.WriteError(w, 500, "sessions_failed", "could not load sessions")
		return
	}
	httpx.WriteJSON(w, 200, map[string]any{"items": items})
}
