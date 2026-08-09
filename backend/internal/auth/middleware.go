package auth

import (
	"database/sql"
	"net/http"
	"strings"

	"basisvr-social-service/internal/common/httpx"

	"github.com/google/uuid"
)

const activePrincipalSQL = `
SELECT EXISTS (
  SELECT 1
  FROM users u
  JOIN actors a ON a.local_user_id = u.id
  WHERE u.id = $1 AND a.id = $2 AND u.status = 'active' AND u.auth_version = $3
)`

func Middleware(tokens TokenManager, db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken := requestBearerToken(r)
			if rawToken == "" {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
				return
			}

			principal, err := principalFromBearer(tokens, rawToken)
			if err != nil {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			active, err := principalIsActive(r, db, principal)
			if err != nil {
				httpx.WriteError(w, http.StatusServiceUnavailable, "auth_status_unavailable", "could not verify account status")
				return
			}
			if !active {
				httpx.WriteError(w, http.StatusUnauthorized, "account_inactive", "account is not active")
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
		})
	}
}

func OptionalMiddleware(tokens TokenManager, db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken := requestBearerToken(r)
			if rawToken == "" {
				next.ServeHTTP(w, r)
				return
			}
			principal, err := principalFromBearer(tokens, rawToken)
			if err != nil {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			active, err := principalIsActive(r, db, principal)
			if err != nil {
				httpx.WriteError(w, http.StatusServiceUnavailable, "auth_status_unavailable", "could not verify account status")
				return
			}
			if !active {
				httpx.WriteError(w, http.StatusUnauthorized, "account_inactive", "account is not active")
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
		})
	}
}

func principalIsActive(r *http.Request, db *sql.DB, principal Principal) (bool, error) {
	var active bool
	err := db.QueryRowContext(r.Context(), activePrincipalSQL, principal.UserID, principal.ActorID, principal.AuthVersion).Scan(&active)
	return active, err
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

	return Principal{
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
