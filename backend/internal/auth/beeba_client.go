package auth

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

type beeBaIdentity struct {
	ID            string  `json:"id"`
	Version       int64   `json:"identity_version"`
	Active        bool    `json:"active"`
	Username      string  `json:"username"`
	DisplayName   string  `json:"display_name"`
	Email         string  `json:"email"`
	EmailVerified bool    `json:"email_verified"`
	AvatarImageID *string `json:"avatar_image_id"`
}
type beeBaError struct {
	Status int
	Code   string
}

func (e *beeBaError) Error() string { return "BeeBa identity request failed" }

func beeBaRequest(ctx context.Context, cfg config.BeeBaConfig, method, path string, input, output any) error {
	if !cfg.Enabled {
		return &beeBaError{http.StatusServiceUnavailable, "identity_unavailable"}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.APIBaseURL+"/api/v1/internal/social"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.SharedSecret)
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	client := http.Client{Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return &beeBaError{http.StatusServiceUnavailable, "identity_unavailable"}
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 {
		return &beeBaError{http.StatusServiceUnavailable, "identity_unavailable"}
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		var failure struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(data, &failure)
		switch failure.Error.Code {
		case "authorization_pending", "access_denied", "expired_token", "invalid_grant", "slow_down":
			return &beeBaError{response.StatusCode, failure.Error.Code}
		}
		return &beeBaError{http.StatusServiceUnavailable, "identity_unavailable"}
	}
	if err = json.Unmarshal(data, output); err != nil {
		return &beeBaError{http.StatusServiceUnavailable, "identity_unavailable"}
	}
	return nil
}
func loadBeeBaIdentity(ctx context.Context, cfg config.BeeBaConfig, id string) (beeBaIdentity, error) {
	var response struct {
		Data beeBaIdentity `json:"data"`
	}
	if _, err := uuid.Parse(id); err != nil {
		return response.Data, errors.New("invalid identity id")
	}
	err := beeBaRequest(ctx, cfg, http.MethodGet, "/identities/"+id, nil, &response)
	if err != nil {
		return response.Data, err
	}
	if response.Data.ID != id || response.Data.Version < 0 {
		return response.Data, errors.New("invalid identity response")
	}
	return response.Data, nil
}

// A failed authority lookup is an availability error, never an anonymous or local-password fallback.
func LinkedIdentityValidator(db *sql.DB, cfg config.BeeBaConfig) SessionValidator {
	return func(ctx context.Context, principal Principal) (bool, error) {
		var issuer, id string
		var version sql.NullInt64
		err := db.QueryRowContext(ctx, `SELECT l.issuer,l.beeba_user_id,s.beeba_identity_version FROM beeba_identity_links l LEFT JOIN auth_sessions s ON s.user_id=l.user_id AND s.id=$1 AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() WHERE l.user_id=$2`, principal.SessionID, principal.UserID).Scan(&issuer, &id, &version)
		if errors.Is(err, sql.ErrNoRows) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if !cfg.Enabled || issuer != cfg.PublicURL || !version.Valid {
			return false, nil
		}
		identity, err := loadBeeBaIdentity(ctx, cfg, id)
		if err != nil {
			return false, err
		}
		if !identity.Active || !identity.EmailVerified || identity.Version != version.Int64 {
			return false, nil
		}
		// The upstream request runs outside the transaction. Recheck local authority
		// after it returns: deletion or revocation may have completed while waiting.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return false, err
		}
		defer tx.Rollback()
		if err = lockSessionUser(ctx, tx, TokenSubject{UserID: principal.UserID.String(), Version: principal.AuthVersion}); err != nil {
			if errors.Is(err, ErrSessionInvalid) {
				return false, nil
			}
			return false, err
		}
		var active bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM auth_sessions WHERE id=$1 AND user_id=$2 AND auth_version=$3 AND beeba_identity_version=$4 AND revoked_at IS NULL AND expires_at>clock_timestamp())`, principal.SessionID, principal.UserID, principal.AuthVersion, identity.Version).Scan(&active); err != nil {
			return false, err
		}
		if !active || !time.Now().Before(principal.ExpiresAt) {
			return false, nil
		}
		if err = syncBeeBaProfile(ctx, tx, principal.UserID, identity, cfg); err != nil {
			return false, err
		}
		if err = tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
}
func beeBaAvatar(cfg config.BeeBaConfig, id *string) string {
	if id == nil {
		return ""
	}
	if _, err := uuid.Parse(*id); err != nil {
		return ""
	}
	return strings.TrimRight(cfg.PublicURL, "/") + "/api/v1/media/" + *id
}

// One atomic statement updates derived identity fields only when their values change.
// Social-owned bio, actor URI, handle, relationships and 3D-avatar selection are preserved.
func syncBeeBaProfile(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, userID uuid.UUID, identity beeBaIdentity, cfg config.BeeBaConfig) error {
	_, err := db.ExecContext(ctx, `
WITH profile_update AS (
 UPDATE profiles SET display_name=$2, avatar_url=$3
 WHERE user_id=$1 AND (display_name IS DISTINCT FROM $2 OR avatar_url IS DISTINCT FROM $3)
)
UPDATE actors SET display_name=$2,
 raw_json=CASE WHEN $3='' THEN jsonb_set(raw_json,'{name}',to_jsonb($2::text))-'icon'
 ELSE jsonb_set(jsonb_set(raw_json,'{name}',to_jsonb($2::text)),'{icon}',jsonb_build_object('type','Image','url',$3::text)) END
WHERE local_user_id=$1 AND (display_name IS DISTINCT FROM $2
 OR COALESCE(raw_json->>'name','') IS DISTINCT FROM $2
 OR COALESCE(raw_json->'icon'->>'url','') IS DISTINCT FROM $3)`, userID, identity.DisplayName, beeBaAvatar(cfg, identity.AvatarImageID))
	return err
}
