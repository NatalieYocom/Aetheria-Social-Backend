package auth

import (
	"net/http"
	"strings"

	"basisvr-social-service/internal/common/httpx"

	"github.com/google/uuid"
)

func Middleware(tokens TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken := bearerToken(r.Header.Get("Authorization"))
			if rawToken == "" {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
				return
			}

			principal, err := principalFromBearer(tokens, rawToken)
			if err != nil {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
		})
	}
}

func OptionalMiddleware(tokens TokenManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawToken := bearerToken(r.Header.Get("Authorization"))
			if rawToken == "" {
				next.ServeHTTP(w, r)
				return
			}
			principal, err := principalFromBearer(tokens, rawToken)
			if err != nil {
				httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", err.Error())
				return
			}
			next.ServeHTTP(w, r.WithContext(ContextWithPrincipal(r.Context(), principal)))
		})
	}
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
		UserID:   userID,
		ActorID:  actorID,
		Username: claims.Subject.Username,
	}, nil
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
