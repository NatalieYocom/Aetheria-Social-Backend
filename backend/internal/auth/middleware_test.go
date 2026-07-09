package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOptionalMiddlewareContinuesWithoutBearerToken(t *testing.T) {
	manager := NewTokenManager("secret", time.Minute, time.Hour)
	called := false
	handler := OptionalMiddleware(manager)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if _, ok := PrincipalFromContext(r.Context()); ok {
			t.Fatal("principal should not be present without bearer token")
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Fatal("next handler was not called")
	}
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d", res.Code)
	}
}

func TestOptionalMiddlewareAttachesPrincipalWhenBearerTokenIsValid(t *testing.T) {
	manager := NewTokenManager("secret", time.Minute, time.Hour)
	userID := uuid.New()
	actorID := uuid.New()
	pair, err := manager.Issue(TokenSubject{
		UserID:   userID.String(),
		ActorID:  actorID.String(),
		Username: "alice",
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	handler := OptionalMiddleware(manager)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("principal missing from context")
		}
		if principal.UserID != userID || principal.ActorID != actorID || principal.Username != "alice" {
			t.Fatalf("principal = %+v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}
