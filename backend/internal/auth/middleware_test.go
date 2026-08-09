package auth

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestOptionalMiddlewareContinuesWithoutBearerToken(t *testing.T) {
	manager := NewTokenManager("secret", time.Minute, time.Hour)
	db, _ := newMiddlewareMockDB(t)
	called := false
	handler := OptionalMiddleware(manager, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	db, mock := newMiddlewareMockDB(t)
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
	mock.ExpectQuery("SELECT EXISTS").WithArgs(userID, actorID, int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	handler := OptionalMiddleware(manager, db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMiddlewareRejectsTokenWhenUserIsSuspended(t *testing.T) {
	manager := NewTokenManager("secret", time.Minute, time.Hour)
	db, mock := newMiddlewareMockDB(t)
	userID := uuid.New()
	actorID := uuid.New()
	pair, err := manager.Issue(TokenSubject{UserID: userID.String(), ActorID: actorID.String(), Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT EXISTS").WithArgs(userID, actorID, int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	handler := Middleware(manager, db)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("suspended principal reached handler")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMiddlewareAcceptsWebSocketBearerSubprotocol(t *testing.T) {
	manager := NewTokenManager("secret", time.Minute, time.Hour)
	db, mock := newMiddlewareMockDB(t)
	userID := uuid.New()
	actorID := uuid.New()
	pair, err := manager.Issue(TokenSubject{UserID: userID.String(), ActorID: actorID.String(), Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT EXISTS").WithArgs(userID, actorID, int64(0)).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	handler := Middleware(manager, db)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/ws", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "basisvr.realtime.v1, bearer."+pair.AccessToken)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

func newMiddlewareMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}
