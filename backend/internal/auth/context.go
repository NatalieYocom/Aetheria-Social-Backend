package auth

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

type contextKey string

const principalContextKey contextKey = "principal"

type Principal struct {
	UserID   uuid.UUID
	ActorID  uuid.UUID
	Username string
}

func ContextWithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalContextKey, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey).(Principal)
	return principal, ok
}

func RequirePrincipal(ctx context.Context) (Principal, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return Principal{}, errors.New("authentication required")
	}
	return principal, nil
}
