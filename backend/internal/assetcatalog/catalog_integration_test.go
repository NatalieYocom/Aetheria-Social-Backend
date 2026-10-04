package assetcatalog

import (
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
	"sync"
	"testing"

	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
	"github.com/google/uuid"
)

func catalogDB(t *testing.T) *sql.DB {
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
	schema := "catalog_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
func catalogWorld(t *testing.T, db *sql.DB) (uuid.UUID, uuid.UUID) {
	t.Helper()
	actor, world := uuid.New(), uuid.New()
	uri := "https://fixture.example/" + actor.String()
	if _, err := db.Exec(`INSERT INTO actors(id,actor_uri,acct,type,preferred_username,domain,inbox_url,outbox_url,followers_url,following_url,is_local) VALUES($1,$2,$2,'Person',$2,'fixture.example',$2,$2,$2,$2,false)`, actor, uri); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO worlds(id,owner_actor_id,slug,name) VALUES($1,$2,$3,'Fixture')`, world, actor, world.String()); err != nil {
		t.Fatal(err)
	}
	return world, actor
}

func TestCatalogHTTPRechecksExistingReferenceAndPins(t *testing.T) {
	db := catalogDB(t)
	world, actor := catalogWorld(t, db)
	testActorIDFromDBExpectation = actor
	id, version := uuid.NewString(), uuid.NewString()
	status := "available"
	nsfw := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs         []string `json:"ids"`
			IncludeNSFW bool     `json:"include_nsfw"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if status == "outage" {
			w.WriteHeader(503)
			return
		}
		if status == "unavailable" || (nsfw && !req.IncludeNSFW) {
			writeManifests(w, map[string]any{"id": id, "status": "unavailable"})
			return
		}
		entry := manifestFixture(id, version)
		entry["content"].(map[string]any)["nsfw"] = nsfw
		writeManifests(w, entry)
	}))
	defer server.Close()
	cfg := config.AssetCatalogConfig{Enabled: true, Code: "beeba", Kind: "beeba", BaseURL: "https://public.example", APIBaseURL: server.URL + "/api/v1"}
	router := newTestRouter(db, cfg, DefaultClientFactory{})
	request := func(method, path, body string, statusCode int) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		if w.Code != statusCode {
			t.Fatalf("%s expected %d got %d: %s", path, statusCode, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	raw := request("POST", "/api/assets/resolve", `{"catalog":"beeba","externalId":"`+id+`"}`, 200)
	var ref AssetRefResponse
	if err := json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	attachPath := "/api/worlds/" + world.String() + "/assets"
	body := `{"assetRefId":"` + ref.ID.String() + `","role":"primary"}`
	request("POST", attachPath, body, 200)
	var pinned string
	if err := db.QueryRow(`SELECT pinned_version_id::text FROM world_asset_refs WHERE world_id=$1`, world).Scan(&pinned); err != nil || pinned != version {
		t.Fatal("pin was not persisted")
	}
	status = "unavailable"
	request("POST", attachPath, body, 404)
	raw = request("GET", attachPath, "", 200)
	if strings.Contains(string(raw), "Latest") || strings.Contains(string(raw), "public-package-key") || !strings.Contains(string(raw), `"availability":"unavailable"`) {
		t.Fatal("stale snapshot leaked")
	}
	status = "available"
	version = uuid.NewString()
	raw = request("GET", attachPath, "", 200)
	if !strings.Contains(string(raw), `"availability":"update_available"`) || strings.Contains(string(raw), "public-package-key") {
		t.Fatal("changed version silently adopted")
	}
	request("POST", attachPath, body, 200)
	if err := db.QueryRow(`SELECT pinned_version_id::text FROM world_asset_refs WHERE world_id=$1`, world).Scan(&pinned); err != nil || pinned != version {
		t.Fatal("explicit update not persisted")
	}
	nsfw = true
	raw = request("GET", attachPath, "", 200)
	if !strings.Contains(string(raw), `"availability":"unavailable"`) {
		t.Fatal("NSFW shown without opt-in")
	}
	raw = request("GET", attachPath+"?includeNsfw=true", "", 200)
	if !strings.Contains(string(raw), `"availability":"available"`) {
		t.Fatal("NSFW explicit opt-in failed")
	}
	status = "outage"
	request("GET", attachPath, "", 503)
	// A different authenticated actor cannot attach even an already-resolved public asset.
	testActorIDFromDBExpectation = uuid.New()
	request("POST", attachPath, body, 403)
}

func TestCatalogConcurrentAttachMaintainsPrimaryAndLimit(t *testing.T) {
	db := catalogDB(t)
	world, actor := catalogWorld(t, db)
	repo := newRepository(db)
	ctx := context.Background()
	catalog, err := repo.upsertCatalog(ctx, Catalog{Code: "beeba", Name: "BeeBa", Kind: "beeba", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	refs := make([]AssetRef, 55)
	for i := range refs {
		refs[i], err = repo.upsertAssetRef(ctx, catalog, ResolvedAsset{ExternalID: uuid.NewString(), VersionID: uuid.NewString(), ContentType: "world"})
		if err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- repo.attachWorldAsset(ctx, world, actor, refs[i], "primary", 0, nil)
		}(i)
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("primary winners=%d", winners)
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM world_asset_refs WHERE world_id=$1 AND role='primary'`, world).Scan(&n); err != nil || n != 1 {
		t.Fatal("multiple primary links")
	}
	for i := 0; i < 48; i++ {
		if err = repo.attachWorldAsset(ctx, world, actor, refs[i], "dependency", 0, nil); err != nil {
			t.Fatal(err)
		}
	}
	results = make(chan error, 7)
	for i := 48; i < 55; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- repo.attachWorldAsset(ctx, world, actor, refs[i], "dependency", 0, nil)
		}(i)
	}
	wg.Wait()
	close(results)
	winners = 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("last-slot winners=%d", winners)
	}
	if err = db.QueryRow(`SELECT count(*) FROM world_asset_refs WHERE world_id=$1`, world).Scan(&n); err != nil || n != 50 {
		t.Fatalf("bound failed %d %v", n, err)
	}
	if err = repo.attachWorldAsset(ctx, world, uuid.New(), refs[0], "dependency", 0, nil); err == nil {
		t.Fatal("wrong owner accepted")
	}
}
