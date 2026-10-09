package community

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func mappingDB(t *testing.T) *sql.DB {
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
	schema := "community_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema+",public")
	parsed.RawQuery = query.Encode()
	db, err := database.Open(ctx, config.DatabaseConfig{URL: parsed.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close(); admin.ExecContext(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	migrationPath := os.Getenv("BASIS_INTEGRATION_MIGRATIONS_DIR")
	if migrationPath == "" {
		_, file, _, _ := runtime.Caller(0)
		migrationPath = filepath.Join(filepath.Dir(file), "../../migrations")
	}
	if err = database.ApplyUp(ctx, db, migrationPath); err != nil {
		t.Fatal(err)
	}
	return db
}
func TestServerWorldMappingRequiresBothOwnersAndLiveVisibility(t *testing.T) {
	db := mappingDB(t)
	issuer := "https://beeba.mapping.test"
	secret := strings.Repeat("m", 32)
	type identity struct{ beeba, user, actor uuid.UUID }
	newIdentity := func() identity {
		t.Helper()
		i := identity{uuid.New(), uuid.New(), uuid.New()}
		uri := "https://social.mapping.test/users/" + i.user.String()
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO users(id,email,username,password_hash) VALUES($1,$2,$3,'unused')`, []any{i.user, i.user.String() + "@example.test", i.user.String()}},
			{`INSERT INTO profiles(user_id,display_name) VALUES($1,'Fixture')`, []any{i.user}},
			{`INSERT INTO actors(id,local_user_id,actor_uri,acct,type,preferred_username,domain,inbox_url,outbox_url,followers_url,following_url,is_local) VALUES($1,$2,$3,$3,'Person',$3,'example.test',$3,$3,$3,$3,true)`, []any{i.actor, i.user, uri}},
			{`INSERT INTO beeba_identity_links(issuer,beeba_user_id,user_id) VALUES($1,$2,$3)`, []any{issuer, i.beeba, i.user}},
		} {
			if _, err := db.Exec(statement.sql, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		return i
	}
	owner, other := newIdentity(), newIdentity()
	upstreamCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing backend service credential")
			w.WriteHeader(401)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/internal/social/identities/")
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"id": id, "identity_version": 1, "active": true, "email_verified": true, "display_name": "Fixture"}})
	}))
	defer upstream.Close()
	cfg := config.Config{Server: config.ServerConfig{PublicURL: "https://social.mapping.test"}, BeeBa: config.BeeBaConfig{Enabled: true, APIBaseURL: upstream.URL, PublicURL: issuer, SharedSecret: secret, Timeout: time.Second}}
	router := chi.NewRouter()
	RegisterRoutes(router, db, cfg, nil)
	world, foreign, server := uuid.New(), uuid.New(), uuid.New()
	for _, entry := range []struct {
		id, actor uuid.UUID
		name      string
	}{{world, owner.actor, "Owned secret world"}, {foreign, other.actor, "Foreign secret world"}} {
		if _, err := db.Exec(`INSERT INTO worlds(id,owner_actor_id,slug,name,visibility) VALUES($1,$2,$3,$4,'private')`, entry.id, entry.actor, entry.id.String(), entry.name); err != nil {
			t.Fatal(err)
		}
	}
	call := func(method, path string, viewer, serverOwner identity, key string, body any, want int) []byte {
		t.Helper()
		var payload []byte
		if body != nil {
			payload, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, "/api/internal/beeba/community"+path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-BeeBa-Service-Key", key)
		req.Header.Set("X-BeeBa-User-ID", viewer.beeba.String())
		req.Header.Set("X-BeeBa-Server-Owner", serverOwner.beeba.String())
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s%s status%d want%d: %s", method, path, res.Code, want, res.Body.String())
		}
		if want != 404 && res.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private bridge response cacheable")
		}
		if want >= 400 && (strings.Contains(res.Body.String(), "Owned secret") || strings.Contains(res.Body.String(), world.String()) || strings.Contains(res.Body.String(), "Foreign secret")) {
			t.Fatal("mapping denial leaked world identity")
		}
		return res.Body.Bytes()
	}
	path := "/server-worlds/" + server.String()
	target := map[string]any{"worldId": world}
	call("PUT", path, owner, owner, "", target, 401)
	if upstreamCalls != 0 {
		t.Fatal("missing serviceauth reached identity lookup")
	}
	call("PUT", path, owner, owner, secret, map[string]any{"worldId": foreign}, 404)
	call("PUT", path, other, owner, secret, target, 403)
	call("PUT", path, owner, owner, secret, target, 204)
	result := call("GET", path, owner, owner, secret, nil, 200)
	if !bytes.Contains(result, []byte(world.String())) {
		t.Fatal("owned privateworld mapping missing")
	}
	call("GET", path, other, owner, secret, nil, 404)
	owned := call("GET", "/owned-worlds?limit=1", owner, owner, secret, nil, 200)
	if !bytes.Contains(owned, []byte(world.String())) || bytes.Contains(owned, []byte(foreign.String())) {
		t.Fatal("ownedworld listing leaks or omits")
	}
	if _, err := db.Exec(`UPDATE worlds SET visibility='public' WHERE id=$1`, world); err != nil {
		t.Fatal(err)
	}
	call("GET", path, other, owner, secret, nil, 200)
	if _, err := db.Exec(`UPDATE worlds SET visibility='private' WHERE id=$1`, world); err != nil {
		t.Fatal(err)
	}
	call("GET", path, other, owner, secret, nil, 404)
	// BeeBa transfer passes its current owner; old link must immediately disappear.
	call("GET", path, other, other, secret, nil, 404)
	if _, err := db.Exec(`UPDATE worlds SET owner_actor_id=$2 WHERE id=$1`, world, other.actor); err != nil {
		t.Fatal(err)
	}
	call("GET", path, owner, owner, secret, nil, 404)
	call("PUT", path, other, other, secret, target, 204)
	call("GET", path, other, other, secret, nil, 200)
	call("DELETE", path, owner, other, secret, nil, 403)
	call("DELETE", path, other, other, secret, nil, 204)
	call("GET", path, other, other, secret, nil, 404)
	// Delegation surface cannot mint admission tickets or server credentials.
	call("POST", "/instances/"+uuid.NewString()+"/join-tickets", owner, owner, secret, map[string]any{}, 404)
	call("GET", "/admin/world-server-credentials", owner, owner, secret, nil, 404)
}
