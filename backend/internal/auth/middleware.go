package auth

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"basisvr-social-service/internal/common/httpx"

	"github.com/google/uuid"
)

const activePrincipalSQL = `
SELECT EXISTS (
  SELECT 1
  FROM users u
  JOIN actors a ON a.local_user_id = u.id
 JOIN auth_sessions s ON s.user_id = u.id AND s.id = $4
  WHERE u.id = $1 AND a.id = $2 AND u.status = 'active' AND u.auth_version = $3
 AND s.auth_version = u.auth_version AND s.revoked_at IS NULL AND s.expires_at > clock_timestamp()
)`

type SessionValidator func(context.Context, Principal) (bool, error)
type validatedPrincipalKey struct{}

func Middleware(tokens TokenManager, db *sql.DB, validators ...SessionValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if validated, _ := r.Context().Value(validatedPrincipalKey{}).(bool); validated {
				next.ServeHTTP(w, r)
				return
			}
			rawToken := requestBearerToken(r)
			if rawToken == "" {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
				return
			}

			principal, err := principalFromBearer(tokens, rawToken)
			if err != nil {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid access token")
				return
			}
			active, err := PrincipalIsActive(r.Context(), db, principal, validators...)
			if err != nil {
				httpx.WriteError(w, http.StatusServiceUnavailable, "auth_status_unavailable", "could not verify account status")
				return
			}
			if !active {
				httpx.WriteError(w, http.StatusUnauthorized, "account_inactive", "account is not active")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ContextWithPrincipal(r.Context(), principal), validatedPrincipalKey{}, true)))
		})
	}
}

func OptionalMiddleware(tokens TokenManager, db *sql.DB, validators ...SessionValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken := requestBearerToken(r)
			if rawToken == "" {
				next.ServeHTTP(w, r)
				return
			}
			principal, err := principalFromBearer(tokens, rawToken)
			if err != nil {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "invalid access token")
				return
			}
			active, err := PrincipalIsActive(r.Context(), db, principal, validators...)
			if err != nil {
				httpx.WriteError(w, http.StatusServiceUnavailable, "auth_status_unavailable", "could not verify account status")
				return
			}
			if !active {
				httpx.WriteError(w, http.StatusUnauthorized, "account_inactive", "account is not active")
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ContextWithPrincipal(r.Context(), principal), validatedPrincipalKey{}, true)))
		})
	}
}

func principalIsActive(r *http.Request, db *sql.DB, principal Principal) (bool, error) {
	return PrincipalIsActive(r.Context(), db, principal)
}

// PrincipalIsActive is also used by long-lived realtime streams. Database failures fail closed.
func PrincipalIsActive(ctx context.Context, db *sql.DB, principal Principal, validators ...SessionValidator) (bool, error) {
	if principal.SessionID == uuid.Nil || !time.Now().Before(principal.ExpiresAt) {
		return false, nil
	}
	var active bool
	err := db.QueryRowContext(ctx, activePrincipalSQL, principal.UserID, principal.ActorID, principal.AuthVersion, principal.SessionID).Scan(&active)
	if err != nil || !active {
		return active, err
	}
	for _, validate := range validators {
		if validate != nil {
			ok, err := validate(ctx, principal)
			if err != nil || !ok {
				return ok, err
			}
		}
	}
	return true, nil
}

func principalFromBearer(tokens TokenManager, rawToken string) (Principal, error) {
	claims, err := tokens.VerifyAccess(rawToken)
	if err != nil {
		return Principal{}, err
	}

	userID, err := uuid.Parse(claims.Subject.UserID)
	if err != nil {
		return Principal{}, err
	}
	actorID, err := uuid.Parse(claims.Subject.ActorID)
	if err != nil {
		return Principal{}, err
	}

	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil {
		return Principal{}, err
	}
	return Principal{
		SessionID: sessionID, ExpiresAt: claims.ExpiresAt.Time,
		UserID:      userID,
		ActorID:     actorID,
		Username:    claims.Subject.Username,
		AuthVersion: claims.Subject.Version,
	}, nil
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

func requestBearerToken(r *http.Request) string {
	if token := bearerToken(r.Header.Get("Authorization")); token != "" {
		return token
	}
	if !strings.HasSuffix(r.URL.Path, "/api/ws") && r.URL.Path != "/api/ws" {
		return ""
	}
	for _, protocol := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		protocol = strings.TrimSpace(protocol)
		if strings.HasPrefix(protocol, "bearer.") {
			return strings.TrimPrefix(protocol, "bearer.")
		}
	}
	return ""
}
