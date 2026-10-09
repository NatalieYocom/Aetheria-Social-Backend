package auth

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

func TestSessionRevokeSerializesWithIdentityAuthority(t *testing.T) {
	for _, changeVersion := range []bool{false, true} {
		name := "linking_authority_lock"
		if changeVersion {
			name = "authority_revoked_while_waiting"
		}
		t.Run(name, func(t *testing.T) {
			db := sessionDB(t)
			ctx := context.Background()
			subject := sessionUser(t, db)
			manager := NewTokenManager("integration-secret", time.Minute, time.Hour)
			pair, err := NewSessionStore(db, manager).Start(ctx, subject)
			if err != nil {
				t.Fatal(err)
			}
			principal, err := principalFromBearer(manager, pair.AccessToken)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			var locked uuid.UUID
			if err = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, subject.UserID).Scan(&locked); err != nil {
				t.Fatal(err)
			}
			if changeVersion {
				if _, err = tx.ExecContext(ctx, `UPDATE users SET auth_version=auth_version+1 WHERE id=$1`, subject.UserID); err != nil {
					t.Fatal(err)
				}
			}
			handler := NewHandler(db, config.Config{}, manager)
			result := make(chan int, 1)
			go func() {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("DELETE", "/api/me/sessions/"+principal.SessionID.String(), nil)
				handler.revokeSession(w, r, principal.SessionID, principal)
				result <- w.Code
			}()
			select {
			case status := <-result:
				t.Fatalf("revoke bypassed identity authority lock: %d", status)
			case <-time.After(100 * time.Millisecond):
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			want := 204
			if changeVersion {
				want = 401
			}
			select {
			case status := <-result:
				if status != want {
					t.Fatalf("status%d want%d", status, want)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("revoke stalled")
			}
			var revoked bool
			if err = db.QueryRowContext(ctx, `SELECT revoked_at IS NOT NULL FROM auth_sessions WHERE id=$1`, principal.SessionID).Scan(&revoked); err != nil {
				t.Fatal(err)
			}
			if revoked == changeVersion {
				t.Fatal("revocation committed with wrong authority state", revoked)
			}
		})
	}
}
