package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

// Force deletion to commit between the delegation's nonlocking identity read and
// its profile synchronization. The locked recheck must fence both writes and use.
func TestCommunityDelegationDoesNotRestoreConcurrentlyDeletedProfile(t *testing.T) {
	db := sessionDB(t)
	subject := sessionUser(t, db)
	beebaID := uuid.NewString()
	cfg := config.BeeBaConfig{Enabled: true, PublicURL: "https://beeba.race.test", SharedSecret: strings.Repeat("s", 32), Timeout: 3 * time.Second}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"data": beeBaIdentity{ID: beebaID, Version: 1, Active: true, EmailVerified: true, DisplayName: "Original private identity"}})
	}))
	defer upstream.Close()
	cfg.APIBaseURL = upstream.URL
	if _, err := db.Exec(`INSERT INTO profiles(user_id,display_name) VALUES($1,'Original private identity')`, subject.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO beeba_identity_links(issuer,beeba_user_id,user_id) VALUES($1,$2,$3)`, cfg.PublicURL, beebaID, subject.UserID); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var blocker int
	if err = tx.QueryRow(`SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE users SET status='deleted',auth_version=auth_version+1 WHERE id=$1`, subject.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE profiles SET display_name='Deleted account' WHERE user_id=$1`, subject.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`UPDATE actors SET display_name='Deleted account' WHERE local_user_id=$1`, subject.UserID); err != nil {
		t.Fatal(err)
	}
	reached := false
	handler := BeeBaCommunityMiddleware(db, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true; w.WriteHeader(204) }))
	done := make(chan *httptest.ResponseRecorder, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go func() {
		req := httptest.NewRequest("GET", "/api/internal/beeba/community/friends", nil).WithContext(ctx)
		req.Header.Set("X-BeeBa-Service-Key", cfg.SharedSecret)
		req.Header.Set("X-BeeBa-User-ID", beebaID)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		done <- res
	}()
	// Observe a real PostgreSQL wait, rather than relying on scheduler timing.
	waiting := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !waiting {
		t.Fatal("delegation never reached the concurrent deletion fence")
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var res *httptest.ResponseRecorder
	select {
	case res = <-done:
	case <-ctx.Done():
		t.Fatal("delegation failed to finish")
	}
	if res.Code != 403 || reached {
		t.Fatalf("deleted identity reached delegated handler: status%d reached%v", res.Code, reached)
	}
	var actorName, profileName string
	if err = db.QueryRow(`SELECT a.display_name,p.display_name FROM actors a JOIN profiles p ON p.user_id=a.local_user_id WHERE a.local_user_id=$1`, subject.UserID).Scan(&actorName, &profileName); err != nil {
		t.Fatal(err)
	}
	if actorName != "Deleted account" || profileName != "Deleted account" {
		t.Fatal("profile identity was restored after deletion")
	}
}
