package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func sessionDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BASIS_INTEGRATION_DATABASE_URL")
	if dsn == "" {
		t.Skip("BASIS_INTEGRATION_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := database.Open(ctx, config.DatabaseConfig{URL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	schema := "auth_test_" + uuid.New().String()[:8]
	if _, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	db, err := database.Open(ctx, config.DatabaseConfig{URL: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	_, file, _, _ := runtime.Caller(0)
	path := os.Getenv("BASIS_INTEGRATION_MIGRATIONS_DIR")
	if path == "" {
		path = filepath.Join(filepath.Dir(file), "../../migrations")
	}
	if err = database.ApplyUp(ctx, db, path); err != nil {
		t.Fatal(err)
	}
	return db
}
func sessionUser(t *testing.T, db *sql.DB) TokenSubject {
	t.Helper()
	u, a := uuid.New(), uuid.New()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `INSERT INTO users(id,email,username,password_hash) VALUES($1,$2,$3,'unused')`, u, u.String()+"@example.test", u.String())
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO actors(id,local_user_id,actor_uri,acct,type,preferred_username,domain,inbox_url,outbox_url,followers_url,following_url,is_local) VALUES($1,$2,$3,$3,'Person',$3,'example.test',$3,$3,$3,$3,true)`, a, u, "https://example.test/users/"+u.String())
	if err != nil {
		t.Fatal(err)
	}
	return TokenSubject{UserID: u.String(), ActorID: a.String(), Username: u.String()}
}
func sessionClaims(t *testing.T, m TokenManager, pair TokenPair) *TokenClaims {
	t.Helper()
	c, err := m.VerifyRefresh(pair.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func activePair(t *testing.T, db *sql.DB, m TokenManager, pair TokenPair) bool {
	t.Helper()
	p, err := principalFromBearer(m, pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := PrincipalIsActive(context.Background(), db, p)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestSessionsRotateReplayRevokesOnlyFamily(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	m := NewTokenManager("integration-secret", time.Minute, time.Hour)
	store := NewSessionStore(db, m)
	subject := sessionUser(t, db)
	first, err := store.Start(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Start(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	c := sessionClaims(t, m, first)
	var originalExpiry time.Time
	if err = db.QueryRow(`SELECT expires_at FROM auth_sessions WHERE id=$1`, c.SessionID).Scan(&originalExpiry); err != nil {
		t.Fatal(err)
	}
	rotated, err := store.Rotate(ctx, first.RefreshToken, c, subject)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == first.RefreshToken {
		t.Fatal("refresh token reused")
	}
	var expiry time.Time
	var hashBytes int
	if err = db.QueryRow(`SELECT expires_at,octet_length(refresh_hash) FROM auth_sessions WHERE id=$1`, c.SessionID).Scan(&expiry, &hashBytes); err != nil {
		t.Fatal(err)
	}
	if !expiry.Equal(originalExpiry) || hashBytes != 32 {
		t.Fatal("absolute expiry changed or token not hashed")
	}
	if !activePair(t, db, m, rotated) {
		t.Fatal("rotated session inactive")
	}
	if _, err = store.Rotate(ctx, first.RefreshToken, c, subject); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("replay = %v", err)
	}
	if activePair(t, db, m, rotated) {
		t.Fatal("replay did not revoke family")
	}
	if !activePair(t, db, m, other) {
		t.Fatal("other device revoked")
	}
}
func TestSessionsConcurrentRefreshHasOneWinnerAndRevokesReplay(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	m := NewTokenManager("integration-secret", time.Minute, time.Hour)
	store := NewSessionStore(db, m)
	subject := sessionUser(t, db)
	pair, err := store.Start(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	claims := sessionClaims(t, m, pair)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	start := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := store.Rotate(ctx, pair.RefreshToken, claims, subject)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrSessionInvalid) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful rotations=%d", successes)
	}
	if activePair(t, db, m, pair) {
		t.Fatal("family not revoked after concurrent replay")
	}
}
func TestSessionsRevocationVersionExpiryAndRollback(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	m := NewTokenManager("integration-secret", time.Minute, time.Hour)
	store := NewSessionStore(db, m)
	subject := sessionUser(t, db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	uncommitted, err := store.StartTx(ctx, tx, subject)
	if err != nil {
		t.Fatal(err)
	}
	tx.Rollback()
	if activePair(t, db, m, uncommitted) {
		t.Fatal("rolled back session active")
	}
	for _, change := range []string{"UPDATE users SET auth_version=auth_version+1 WHERE id=$1", "UPDATE users SET status='suspended' WHERE id=$1", "UPDATE users SET status='deleted' WHERE id=$1", "UPDATE auth_sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1"} {
		subject = sessionUser(t, db)
		pair, err := store.Start(ctx, subject)
		if err != nil {
			t.Fatal(err)
		}
		claims := sessionClaims(t, m, pair)
		if _, err = db.Exec(change, subject.UserID); err != nil {
			t.Fatal(err)
		}
		if activePair(t, db, m, pair) {
			t.Fatalf("session survives %s", change)
		}
		if _, err = store.Rotate(ctx, pair.RefreshToken, claims, subject); !errors.Is(err, ErrSessionInvalid) {
			t.Fatalf("refresh survives %s: %v", change, err)
		}
	}
}
func TestSessionsRevokeOwnDeviceAndHideOtherOwners(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	m := NewTokenManager("integration-secret", time.Minute, time.Hour)
	store := NewSessionStore(db, m)
	subject := sessionUser(t, db)
	pair, err := store.Start(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Start(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := store.Start(ctx, sessionUser(t, db))
	if err != nil {
		t.Fatal(err)
	}
	p, err := principalFromBearer(m, pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, config.Config{}, m)
	router := chi.NewRouter()
	router.Delete("/sessions/{id}", h.RevokeSession)
	for _, entry := range []struct {
		pair TokenPair
		want int
	}{{foreign, http.StatusNotFound}, {second, http.StatusNoContent}} {
		request := httptest.NewRequest(http.MethodDelete, "/sessions/"+sessionClaims(t, m, entry.pair).SessionID, nil)
		request = request.WithContext(ContextWithPrincipal(ctx, p))
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != entry.want {
			t.Fatalf("revoke status=%d", response.Code)
		}
	}
	if activePair(t, db, m, second) || !activePair(t, db, m, pair) || !activePair(t, db, m, foreign) {
		t.Fatal("incorrect per-device revoke scope")
	}
	response := httptest.NewRecorder()
	h.Logout(response, httptest.NewRequest(http.MethodPost, "/logout", nil).WithContext(ContextWithPrincipal(ctx, p)))
	if response.Code != 204 || activePair(t, db, m, pair) {
		t.Fatal("legacy logout did not revoke all")
	}
}
