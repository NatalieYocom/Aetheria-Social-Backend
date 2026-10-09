package auth

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"basisvr-social-service/internal/common/httpx"
	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

// BeeBaCommunityMiddleware delegates only to the explicitly mounted community
// handlers. It neither creates a Social session nor authorizes other API routes.
func BeeBaCommunityMiddleware(db *sql.DB, cfg config.BeeBaConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if !cfg.Enabled {
				httpx.WriteError(w, 503, "identity_unavailable", "Identity service is unavailable.")
				return
			}
			if len(cfg.SharedSecret) < 32 || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-BeeBa-Service-Key")), []byte(cfg.SharedSecret)) != 1 {
				httpx.WriteError(w, 401, "unauthorized", "Service authentication required.")
				return
			}
			id, err := uuid.Parse(r.Header.Get("X-BeeBa-User-ID"))
			if err != nil {
				httpx.WriteError(w, 400, "invalid_request", "Invalid identity.")
				return
			}
			identity, err := loadBeeBaIdentity(r.Context(), cfg, id.String())
			if err != nil {
				writeBeeBaError(w, err)
				return
			}
			if !identity.Active || !identity.EmailVerified {
				httpx.WriteError(w, 403, "account_inactive", "Verified active identity required.")
				return
			}
			var p Principal
			err = db.QueryRowContext(r.Context(), `SELECT u.id,a.id,u.username,u.auth_version FROM beeba_identity_links l JOIN users u ON u.id=l.user_id JOIN actors a ON a.local_user_id=u.id WHERE l.issuer=$1 AND l.beeba_user_id=$2 AND u.status='active'`, cfg.PublicURL, id).Scan(&p.UserID, &p.ActorID, &p.Username, &p.AuthVersion)
			if errors.Is(err, sql.ErrNoRows) {
				httpx.WriteError(w, 403, "social_not_linked", "Connect your Basis account first.")
				return
			}
			if err != nil {
				httpx.WriteError(w, 503, "identity_unavailable", "Identity service is unavailable.")
				return
			}
			tx, err := db.BeginTx(r.Context(), nil)
			if err != nil {
				httpx.WriteError(w, 503, "identity_unavailable", "Identity service is unavailable.")
				return
			}
			defer tx.Rollback()
			if err = lockSessionUser(r.Context(), tx, TokenSubject{UserID: p.UserID.String(), Version: p.AuthVersion}); err != nil {
				if errors.Is(err, ErrSessionInvalid) {
					httpx.WriteError(w, 403, "account_inactive", "Active identity required.")
				} else {
					httpx.WriteError(w, 503, "identity_unavailable", "Identity service is unavailable.")
				}
				return
			}
			var linked bool
			if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM beeba_identity_links WHERE issuer=$1 AND beeba_user_id=$2 AND user_id=$3)`, cfg.PublicURL, id, p.UserID).Scan(&linked); err != nil {
				httpx.WriteError(w, 503, "identity_unavailable", "Identity service is unavailable.")
				return
			}
			if !linked {
				httpx.WriteError(w, 403, "social_not_linked", "Connected identity required.")
				return
			}
			if err = syncBeeBaProfile(r.Context(), tx, p.UserID, identity, cfg); err != nil {
				httpx.WriteError(w, 503, "identity_unavailable", "Identity service is unavailable.")
				return
			}
			if err = tx.Commit(); err != nil {
				httpx.WriteError(w, 503, "identity_unavailable", "Identity service is unavailable.")
				return
			}
			p.ExpiresAt = time.Now().Add(cfg.Timeout)
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), p)))
		})
	}
}
