package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

func TestLinkedIdentityDoesNotRestoreDeletedProfileAfterUpstreamWait(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	subject := sessionUser(t, db)
	m := NewTokenManager("test-secret", time.Minute, time.Hour)
	pair, err := NewSessionStore(db, m).Start(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	p, err := principalFromBearer(m, pair.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	requested, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requested)
		<-release
		json.NewEncoder(w).Encode(map[string]any{"data": beeBaIdentity{ID: id, Version: 1, Active: true, EmailVerified: true, DisplayName: "Private original name"}})
	}))
	defer server.Close()
	cfg := config.BeeBaConfig{Enabled: true, APIBaseURL: server.URL, PublicURL: "https://identity.example", SharedSecret: "test-secret", Timeout: 3 * time.Second}
	if _, err = db.Exec(`INSERT INTO profiles(user_id,display_name) VALUES($1,'Private original name')`, p.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO beeba_identity_links(issuer,beeba_user_id,user_id) VALUES($1,$2,$3)`, cfg.PublicURL, id, p.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE auth_sessions SET beeba_identity_version=1 WHERE id=$1`, p.SessionID); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		active bool
		err    error
	}
	result := make(chan outcome, 1)
	go func() { active, err := LinkedIdentityValidator(db, cfg)(ctx, p); result <- outcome{active, err} }()
	select {
	case <-requested:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("upstream request did not begin")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, query := range []string{
		`UPDATE users SET status='deleted',auth_version=auth_version+1 WHERE id=$1`,
		`UPDATE profiles SET display_name='Deleted user',avatar_url='' WHERE user_id=$1`,
		`UPDATE actors SET display_name='Deleted user',raw_json='{"name":"Deleted user"}'::jsonb WHERE local_user_id=$1`,
	} {
		if _, err = tx.ExecContext(ctx, query, p.UserID); err != nil {
			close(release)
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case got := <-result:
		if got.err != nil || got.active {
			t.Fatalf("deleted identity accepted: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("validation stalled")
	}
	var profileName, actorName, documentName string
	if err = db.QueryRow(`SELECT p.display_name,a.display_name,a.raw_json->>'name' FROM profiles p JOIN actors a ON a.local_user_id=p.user_id WHERE p.user_id=$1`, p.UserID).Scan(&profileName, &actorName, &documentName); err != nil {
		t.Fatal(err)
	}
	if profileName != "Deleted user" || actorName != "Deleted user" || documentName != "Deleted user" {
		t.Fatal("deleted profile was deanonymized")
	}
}

func TestLinkedIdentityMissingSessionFailsClosed(t *testing.T) {
	db := sessionDB(t)
	subject := sessionUser(t, db)
	id := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO beeba_identity_links(issuer,beeba_user_id,user_id) VALUES('https://identity.example',$1,$2)`, id, subject.UserID); err != nil {
		t.Fatal(err)
	}
	p := Principal{UserID: uuid.MustParse(subject.UserID), SessionID: uuid.New(), AuthVersion: subject.Version, ExpiresAt: time.Now().Add(time.Minute)}
	active, err := LinkedIdentityValidator(db, config.BeeBaConfig{Enabled: true, PublicURL: "https://identity.example"})(context.Background(), p)
	if err != nil || active {
		t.Fatalf("missing linked session accepted: active=%v error=%v", active, err)
	}
}
