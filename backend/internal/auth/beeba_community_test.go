package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

func TestBeeBaCommunityRequiresExplicitActiveLinkAndLiveIdentity(t *testing.T) {
	db := sessionDB(t)
	subject := sessionUser(t, db)
	identity := beeBaIdentity{ID: uuid.NewString(), Version: 2, Active: true, EmailVerified: true, DisplayName: "Canonical"}
	unavailable := false
	upstreamCalls := 0
	handlerCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if unavailable {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": identity})
	}))
	defer upstream.Close()
	cfg := config.BeeBaConfig{Enabled: true, APIBaseURL: upstream.URL, PublicURL: "https://beeba.test", SharedSecret: strings.Repeat("x", 32), Timeout: time.Second}
	h := BeeBaCommunityMiddleware(db, cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalls++
		p, ok := PrincipalFromContext(r.Context())
		if !ok || p.UserID.String() != subject.UserID || p.ActorID.String() != subject.ActorID || p.SessionID != uuid.Nil {
			t.Fatal("unscoped identity delegation")
		}
		w.WriteHeader(204)
	}))
	call := func(secret, id string, want int) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/api/internal/beeba/community/friends", nil)
		r.Header.Set("X-BeeBa-Service-Key", secret)
		r.Header.Set("X-BeeBa-User-ID", id)
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("want%d got%d: %s", want, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private response cached")
		}
	}
	call("", identity.ID, 401)
	call("wrong", identity.ID, 401)
	call(cfg.SharedSecret, "invalid", 400)
	if upstreamCalls != 0 {
		t.Fatal("unauthenticated request called identity provider")
	}
	call(cfg.SharedSecret, identity.ID, 403)
	if _, err := db.Exec(`INSERT INTO beeba_identity_links(issuer,beeba_user_id,user_id) VALUES($1,$2,$3)`, cfg.PublicURL, identity.ID, subject.UserID); err != nil {
		t.Fatal(err)
	}
	call(cfg.SharedSecret, identity.ID, 204)
	identity.EmailVerified = false
	call(cfg.SharedSecret, identity.ID, 403)
	identity.EmailVerified = true
	identity.Active = false
	call(cfg.SharedSecret, identity.ID, 403)
	identity.Active = true
	unavailable = true
	call(cfg.SharedSecret, identity.ID, 503)
	unavailable = false
	if _, err := db.Exec(`UPDATE users SET status='suspended' WHERE id=$1`, subject.UserID); err != nil {
		t.Fatal(err)
	}
	call(cfg.SharedSecret, identity.ID, 403)
	if handlerCalls != 1 {
		t.Fatal("inactive/unlinked delegation reached domain", handlerCalls)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM auth_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatal("bridge minted session", count, err)
	}
}
