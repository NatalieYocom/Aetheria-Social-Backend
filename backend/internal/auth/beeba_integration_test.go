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

func TestBeeBaExplicitLinkPreservesActorAndFencesCredentials(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	subject := sessionUser(t, db)
	if _, err := db.Exec(`INSERT INTO profiles(user_id,display_name,bio) VALUES($1,'Local display','Preserved bio')`, subject.UserID); err != nil {
		t.Fatal(err)
	}
	m := NewTokenManager("integration-secret", time.Minute, time.Hour)
	store := NewSessionStore(db, m)
	original, err := store.Start(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Start(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	targetClaims := sessionClaims(t, m, original)
	identity := beeBaIdentity{ID: uuid.NewString(), Version: 3, Active: true, EmailVerified: true, DisplayName: "Canonical name", Email: "canonical@example.test"}
	targetUser, targetSession := subject.UserID, targetClaims.SessionID
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/redeem") {
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"identity": identity, "target_user_id": targetUser, "target_session_id": targetSession}})
		} else {
			json.NewEncoder(w).Encode(map[string]any{"data": identity})
		}
	}))
	defer server.Close()
	cfg := config.Config{BeeBa: config.BeeBaConfig{Enabled: true, APIBaseURL: server.URL, PublicURL: "https://identity.example", SharedSecret: strings.Repeat("s", 32), Timeout: time.Second}}
	h := NewHandler(db, cfg, m)
	complete := func(expected int) authResponse {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/auth/beeba/complete", strings.NewReader(`{"deviceCode":"`+strings.Repeat("d", 43)+`","codeVerifier":"`+strings.Repeat("v", 43)+`"}`))
		h.BeeBaComplete(w, r)
		if w.Code != expected {
			t.Fatalf("complete: expected%d got%d", expected, w.Code)
		}
		var result authResponse
		if expected == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
		}
		return result
	}
	linked := complete(200)
	if linked.User.ID.String() != subject.UserID || linked.User.ActorID.String() != subject.ActorID || linked.User.Profile.Bio != "Preserved bio" || linked.User.Profile.DisplayName != "Canonical name" || linked.User.Email != identity.Email {
		t.Fatal("link changed immutable data or lost canonical fields")
	}
	if activePair(t, db, m, original) || activePair(t, db, m, other) {
		t.Fatal("pre-link sessions survived")
	}
	p, err := principalFromBearer(m, linked.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := PrincipalIsActive(ctx, db, p, LinkedIdentityValidator(db, cfg.BeeBa)); err != nil || !ok {
		t.Fatalf("new linked session inactive %v", err)
	}
	identity.DisplayName = "Updated BeeBa name"
	if ok, err := PrincipalIsActive(ctx, db, p, LinkedIdentityValidator(db, cfg.BeeBa)); err != nil || !ok {
		t.Fatalf("identity sync failed: %v", err)
	}
	var profileName, actorName, documentName string
	if err = db.QueryRow(`SELECT p.display_name,a.display_name,a.raw_json->>'name' FROM profiles p JOIN actors a ON a.local_user_id=p.user_id WHERE p.user_id=$1`, subject.UserID).Scan(&profileName, &actorName, &documentName); err != nil {
		t.Fatal(err)
	}
	if profileName != identity.DisplayName || actorName != identity.DisplayName || documentName != identity.DisplayName {
		t.Fatal("identity snapshots diverged")
	}
	var hash string
	if err = db.QueryRow(`SELECT password_hash FROM users WHERE id=$1`, subject.UserID).Scan(&hash); err != nil || hash != "!beeba-identity-only" {
		t.Fatal("legacy password not retired")
	}
	if _, _, err = h.findLoginUser(ctx, subject.Username); err == nil {
		t.Fatal("local credential lookup accepted linked user")
	}
	// A consumed approval cannot be aimed at a different actor by the client.
	foreign := sessionUser(t, db)
	foreignPair, err := store.Start(ctx, foreign)
	if err != nil {
		t.Fatal(err)
	}
	targetUser = foreign.UserID
	targetSession = sessionClaims(t, m, foreignPair).SessionID
	complete(409)
	// A second authority identity cannot replace an already-linked Social account.
	identity.ID = uuid.NewString()
	targetUser = subject.UserID
	targetSession = sessionClaims(t, m, linked.TokenPair).SessionID
	complete(409)
	// The source session must still exist when linking completes.
	if _, err = db.Exec(`UPDATE auth_sessions SET revoked_at=now() WHERE id=$1`, sessionClaims(t, m, foreignPair).SessionID); err != nil {
		t.Fatal(err)
	}
	targetUser = foreign.UserID
	targetSession = sessionClaims(t, m, foreignPair).SessionID
	complete(401)
}
