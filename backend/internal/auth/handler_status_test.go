package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestRefreshRejectsSuspendedUser(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := NewTokenManager("test-secret", time.Minute, time.Hour)
	userID := uuid.New()
	actorID := uuid.New()
	pair, err := manager.Issue(TokenSubject{UserID: userID.String(), ActorID: actorID.String(), Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("FROM users u").WithArgs(userID).
		WillReturnRows(meRows().AddRow(
			userID, "alice@example.test", "alice", "suspended", int64(0),
			"Alice", "", "", "", "", []byte(`[]`), []byte(`{}`),
			actorID, "alice@example.test", "https://example.test/users/alice",
			"https://example.test/users/alice/inbox", "https://example.test/users/alice/outbox",
			"https://example.test/users/alice/followers", "https://example.test/users/alice/following",
		))
	handler := NewHandler(db, config.Config{}, manager)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", strings.NewReader(`{"refreshToken":"`+pair.RefreshToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	handler.Refresh(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteAccountRequiresPassword(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := Principal{UserID: uuid.New(), ActorID: uuid.New(), Username: "alice"}
	handler := NewHandler(db, config.Config{}, NewTokenManager("test-secret", time.Minute, time.Hour))
	req := httptest.NewRequest(http.MethodDelete, "/api/me", strings.NewReader(`{"password":""}`))
	req = req.WithContext(ContextWithPrincipal(req.Context(), principal))
	res := httptest.NewRecorder()

	handler.DeleteAccount(res, req)

	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "password_required") {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestExportAccountReturnsDownloadableJSON(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := Principal{UserID: uuid.New(), ActorID: uuid.New(), Username: "alice"}
	mock.ExpectQuery("SELECT jsonb_build_object").WithArgs(principal.UserID, principal.ActorID).
		WillReturnRows(sqlmock.NewRows([]string{"export"}).AddRow([]byte(`{"schemaVersion":1,"account":{"username":"alice"}}`)))
	handler := NewHandler(db, config.Config{}, NewTokenManager("test-secret", time.Minute, time.Hour))
	req := httptest.NewRequest(http.MethodGet, "/api/me/export", nil)
	req = req.WithContext(ContextWithPrincipal(req.Context(), principal))
	res := httptest.NewRecorder()

	handler.ExportAccount(res, req)

	if res.Code != http.StatusOK || !strings.Contains(res.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("status = %d, headers = %v, body = %s", res.Code, res.Header(), res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"schemaVersion":1`) {
		t.Fatalf("body = %s", res.Body.String())
	}
}

func TestRefreshRejectsRevokedTokenVersion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := NewTokenManager("test-secret", time.Minute, time.Hour)
	userID := uuid.New()
	actorID := uuid.New()
	pair, err := manager.Issue(TokenSubject{UserID: userID.String(), ActorID: actorID.String(), Username: "alice", Version: 0})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("FROM users u").WithArgs(userID).
		WillReturnRows(meRows().AddRow(
			userID, "alice@example.test", "alice", "active", int64(1),
			"Alice", "", "", "", "", []byte(`[]`), []byte(`{}`),
			actorID, "alice@example.test", "https://example.test/users/alice",
			"https://example.test/users/alice/inbox", "https://example.test/users/alice/outbox",
			"https://example.test/users/alice/followers", "https://example.test/users/alice/following",
		))
	handler := NewHandler(db, config.Config{}, manager)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", strings.NewReader(`{"refreshToken":"`+pair.RefreshToken+`"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	handler.Refresh(res, req)
	if res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "token_revoked") {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLogoutRevokesAllCurrentTokens(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	principal := Principal{UserID: uuid.New(), ActorID: uuid.New(), Username: "alice"}
	mock.ExpectExec("UPDATE users SET auth_version = auth_version \\+ 1").
		WithArgs(principal.UserID).
		WillReturnResult(sqlmock.NewResult(0, 1))

	handler := NewHandler(db, config.Config{}, NewTokenManager("test-secret", time.Minute, time.Hour))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req = req.WithContext(ContextWithPrincipal(req.Context(), principal))
	res := httptest.NewRecorder()
	handler.Logout(res, req)
	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func meRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "email", "username", "status", "auth_version", "display_name", "bio", "avatar_url", "banner_url",
		"status_text", "links", "privacy_settings", "actor_id", "acct", "actor_uri", "inbox_url",
		"outbox_url", "followers_url", "following_url",
	})
}
