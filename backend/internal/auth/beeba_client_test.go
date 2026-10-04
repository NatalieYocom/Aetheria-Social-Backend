package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

func TestBeeBaAuthorityBoundary(t *testing.T) {
	id := uuid.NewString()
	var redirected bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer destination.Close()
	for _, tc := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"valid", `{"data":{"id":"` + id + `","identity_version":4,"active":true,"email_verified":true}}`, 200, ""},
		{"mismatch", `{"data":{"id":"` + uuid.NewString() + `","identity_version":4}}`, 200, "invalid"},
		{"oversized", strings.Repeat("x", 65537), 200, "identity_unavailable"},
		{"malformed", `{"data":`, 200, "identity_unavailable"},
		{"private-error", `{"error":{"code":"database_failed","message":"private database secret"}}`, 500, "identity_unavailable"},
		{"pending", `{"error":{"code":"authorization_pending"}}`, 400, "authorization_pending"},
		{"redirect", "", 302, "identity_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/internal/social/identities/"+id || r.Header.Get("Authorization") != "Bearer service-secret" {
					t.Error("incorrect scoped request")
				}
				if tc.status == 302 {
					w.Header().Set("Location", destination.URL)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer server.Close()
			cfg := config.BeeBaConfig{Enabled: true, APIBaseURL: server.URL, SharedSecret: "service-secret", Timeout: time.Second}
			identity, err := loadBeeBaIdentity(context.Background(), cfg, id)
			if tc.want == "" {
				if err != nil || identity.Version != 4 {
					t.Fatalf("valid response failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("unsafe response accepted")
			}
			var upstream *beeBaError
			if tc.want != "invalid" && (!errors.As(err, &upstream) || upstream.Code != tc.want) {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
	if redirected {
		t.Fatal("service credential followed redirect")
	}
}

func TestLinkedIdentityVersionFenceAndOutage(t *testing.T) {
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
	version := int64(1)
	active := true
	unavailable := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"data": beeBaIdentity{ID: id, Version: version, Active: active, EmailVerified: true}})
	}))
	defer server.Close()
	cfg := config.BeeBaConfig{Enabled: true, APIBaseURL: server.URL, PublicURL: "https://identity.example", SharedSecret: "test-secret", Timeout: time.Second}
	validator := LinkedIdentityValidator(db, cfg)
	check := func(want bool, wantErr bool) {
		t.Helper()
		ok, err := validator(ctx, p)
		if ok != want || (err != nil) != wantErr {
			t.Fatalf("active=%v error=%v", ok, err)
		}
	}
	check(true, false) // Legacy local identity has no upstream dependency.
	if _, err = db.Exec(`INSERT INTO beeba_identity_links(issuer,beeba_user_id,user_id) VALUES($1,$2,$3)`, cfg.PublicURL, id, p.UserID); err != nil {
		t.Fatal(err)
	}
	check(false, false) // A pre-link session never becomes a linked session implicitly.
	if _, err = db.Exec(`UPDATE auth_sessions SET beeba_identity_version=1 WHERE id=$1`, p.SessionID); err != nil {
		t.Fatal(err)
	}
	check(true, false)
	version = 2
	check(false, false) // Recovery/logout-all fence old tokens.
	active = false
	check(false, false)
	active = true
	check(false, false) // Restoring account never revives old version.
	version = 1
	unavailable = true
	check(false, true)
	unavailable = false
	check(true, false)
	cfg.Enabled = false
	ok, err := LinkedIdentityValidator(db, cfg)(ctx, p)
	if ok || err != nil {
		t.Fatal("disabled bridge accepted linked identity")
	}
}
