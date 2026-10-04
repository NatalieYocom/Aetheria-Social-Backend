package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"basisvr-social-service/internal/actorcrypto"
	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

func TestBeeBaProvisionNeverMergesMatchingEmailOrUsername(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	manager := NewTokenManager("fixture-secret", time.Minute, time.Hour)
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	identity := beeBaIdentity{ID: uuid.NewString(), Version: 7, Active: true, EmailVerified: true, Email: "already-owned@example.test", Username: "already_owned", DisplayName: "BeeBa display"}
	var mu sync.Mutex
	consumed := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("s", 32) {
			t.Error("service authentication omitted")
			w.WriteHeader(401)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/redeem") {
			var input struct {
				DeviceCode   string `json:"device_code"`
				CodeVerifier string `json:"code_verifier"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			used := consumed[input.DeviceCode]
			consumed[input.DeviceCode] = true
			mu.Unlock()
			if used {
				w.WriteHeader(400)
				json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": "invalid_grant"}})
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": func() any {
			if strings.HasSuffix(r.URL.Path, "/redeem") {
				return map[string]any{"identity": identity}
			}
			return identity
		}()})
	}))
	defer server.Close()
	cfg := config.Config{Server: config.ServerConfig{PublicURL: "https://social.example.test"}, ActivityPub: config.ActivityPubConfig{ActorKeyEncryptionKey: key}, BeeBa: config.BeeBaConfig{Enabled: true, APIBaseURL: server.URL, PublicURL: "https://beeba.example.test", SharedSecret: strings.Repeat("s", 32), Timeout: time.Second}}
	handler := NewHandler(db, cfg, manager)
	hash, err := HashPassword("existing account password")
	if err != nil {
		t.Fatal(err)
	}
	existing, err := handler.createLocalUser(ctx, identity.Email, identity.Username, "Existing social display", hash)
	if err != nil {
		t.Fatal(err)
	}
	oldSession, err := NewSessionStore(db, manager).Start(ctx, TokenSubject{UserID: existing.ID.String(), ActorID: existing.ActorID.String(), Username: existing.Username})
	if err != nil {
		t.Fatal(err)
	}
	complete := func(code string, want int) authResponse {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/auth/beeba/complete", strings.NewReader(`{"deviceCode":"`+code+`","codeVerifier":"`+strings.Repeat("v", 43)+`"}`))
		handler.BeeBaComplete(w, r)
		if w.Code != want {
			t.Fatalf("completion status=%d expected%d body=%s", w.Code, want, w.Body.String())
		}
		var out authResponse
		if want == 200 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}
	created := complete(strings.Repeat("a", 43), 200)
	if created.User.ID == existing.ID || created.User.ActorID == existing.ActorID || created.User.Username == existing.Username {
		t.Fatal("implicit account merge by matching profile fields")
	}
	if created.User.Email != identity.Email || created.User.Profile.DisplayName != identity.DisplayName {
		t.Fatal("canonical display identity missing")
	}
	var internalEmail, password, envelope, uri string
	if err = db.QueryRow(`SELECT u.email,u.password_hash,a.private_key_pem_encrypted,a.actor_uri FROM users u JOIN actors a ON a.local_user_id=u.id WHERE u.id=$1`, created.User.ID).Scan(&internalEmail, &password, &envelope, &uri); err != nil {
		t.Fatal(err)
	}
	if internalEmail == identity.Email || !strings.HasSuffix(internalEmail, "@identity.invalid") || password != "!beeba-identity-only" {
		t.Fatal("provisioned account has unsafe local credential/email")
	}
	if _, err = actorcrypto.Decrypt(key, uri, envelope); err != nil {
		t.Fatal("provisioned actor key not encrypted")
	}
	original, currentHash, err := handler.findLoginUser(ctx, identity.Username)
	if err != nil || original.ID != existing.ID || currentHash != hash {
		t.Fatal("unrelated local credentials changed")
	}
	if !activePair(t, db, manager, oldSession) {
		t.Fatal("unrelated local session revoked")
	}
	if _, _, err = handler.findLoginUser(ctx, created.User.Username); err == nil {
		t.Fatal("provisioned account accessible via local password route")
	}
	again := complete(strings.Repeat("b", 43), 200)
	if again.User.ID != created.User.ID || again.User.ActorID != created.User.ActorID {
		t.Fatal("same immutable identity created duplicate social actor")
	}
	complete(strings.Repeat("b", 43), 400)
	var users, links, sessions int
	if err = db.QueryRow(`SELECT count(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM beeba_identity_links`).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM auth_sessions WHERE user_id=$1`, created.User.ID).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if users != 2 || links != 1 || sessions != 2 {
		t.Fatalf("unexpected provisioning counts users=%d links=%d sessions=%d", users, links, sessions)
	}
	// Additional client fields cannot choose or overwrite an existing Social identity.
	w := httptest.NewRecorder()
	handler.BeeBaComplete(w, httptest.NewRequest("POST", "/api/auth/beeba/complete", strings.NewReader(`{"deviceCode":"`+strings.Repeat("c", 43)+`","codeVerifier":"`+strings.Repeat("v", 43)+`","targetUserId":"`+existing.ID.String()+`"}`)))
	if w.Code != 400 {
		t.Fatal("client-supplied target account accepted")
	}
}
