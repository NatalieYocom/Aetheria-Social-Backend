package assetcatalog

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

func manifestFixture(id, version string) map[string]any {
	return map[string]any{"id": id, "status": "available", "revision": "2026-09-17T12:00:00Z", "download_path": "/api/v1/content/" + id + "/download?version=" + version, "content": map[string]any{"id": id, "title": "Latest public title", "unlock_password": "public-package-key", "description": "Latest", "status": "published", "visibility": "public", "category": map[string]string{"slug": "worlds"}, "preview_image_id": uuid.NewString(), "file": map[string]any{"id": version, "file_size": 2048, "file_hash_sha256": strings.Repeat("a", 64)}}}
}
func writeManifests(w http.ResponseWriter, entries ...map[string]any) {
	json.NewEncoder(w).Encode(map[string]any{"data": entries, "checked_at": time.Now().UTC()})
}

func TestSnapshotsUsePublicIdentityAndOrigins(t *testing.T) {
	id, version := uuid.NewString(), uuid.NewString()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/content/snapshots" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Errorf("unexpected upstream request")
		}
		writeManifests(w, manifestFixture(id, version))
	}))
	defer server.Close()
	client := NewBeeBaClient(BeeBaClientConfig{Catalog: Catalog{BaseURL: "https://public.example", APIBaseURL: server.URL + "/api/v1"}, APIToken: "must-never-be-used", HTTPClient: server.Client()})
	asset, err := client.ResolveAsset(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if asset.VersionID != version || asset.SHA256 != strings.Repeat("a", 64) || asset.SizeBytes != 2048 || asset.UnlockPassword == "" || !strings.HasPrefix(asset.DownloadURL, "https://public.example/") || strings.Contains(asset.ExternalURL, server.URL) {
		t.Fatalf("bad manifest: %#v", asset)
	}
}

func TestSnapshotsRejectMalformedAndNonPublicResponses(t *testing.T) {
	id, version := uuid.NewString(), uuid.NewString()
	cases := map[string]func(map[string]any){
		"private":          func(e map[string]any) { e["content"].(map[string]any)["visibility"] = "private" },
		"hidden":           func(e map[string]any) { e["content"].(map[string]any)["status"] = "hidden" },
		"nsfw":             func(e map[string]any) { e["content"].(map[string]any)["nsfw"] = true },
		"wrongID":          func(e map[string]any) { e["id"] = uuid.NewString() },
		"wrongVersionPath": func(e map[string]any) { e["download_path"] = "https://evil.example/file" },
		"badHash": func(e map[string]any) {
			e["content"].(map[string]any)["file"].(map[string]any)["file_hash_sha256"] = "bad"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				e := manifestFixture(id, version)
				mutate(e)
				writeManifests(w, e)
			}))
			defer server.Close()
			client := NewBeeBaClient(BeeBaClientConfig{Catalog: Catalog{BaseURL: server.URL, APIBaseURL: server.URL + "/api/v1"}})
			if _, err := client.ResolveAsset(context.Background(), id); err == nil {
				t.Fatal("unsafe manifest accepted")
			}
		})
	}
}

func TestSnapshotsDoNotFollowRedirectsAndBoundBody(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer target.Close()
	for _, mode := range []string{"redirect", "large", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			id, version := uuid.NewString(), uuid.NewString()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, target.URL, 307)
				case "large":
					w.Write([]byte(strings.Repeat(" ", 4<<20+1)))
				case "duplicate":
					writeManifests(w, manifestFixture(id, version), manifestFixture(id, version))
				}
			}))
			defer server.Close()
			c := NewBeeBaClient(BeeBaClientConfig{Catalog: Catalog{BaseURL: server.URL, APIBaseURL: server.URL + "/api/v1"}})
			if _, err := c.ResolveAsset(context.Background(), id); err == nil {
				t.Fatal("unsafe response accepted")
			}
		})
	}
	if calls != 0 {
		t.Fatal("followed redirect")
	}
	c := NewBeeBaClient(BeeBaClientConfig{})
	for _, id := range []string{"../auth/me", "x?secret=1", "", uuid.Nil.String()} {
		if _, err := c.ResolveAsset(context.Background(), id); err == nil {
			t.Fatal("invalid id accepted")
		}
	}
}

func TestWorldRevalidationFencesChangesAndClearsSnapshots(t *testing.T) {
	id, version := uuid.NewString(), uuid.NewString()
	status := "available"
	nextVersion := version
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status == "outage" {
			w.WriteHeader(503)
			return
		}
		if status == "unavailable" {
			writeManifests(w, map[string]any{"id": id, "status": "unavailable"})
			return
		}
		writeManifests(w, manifestFixture(id, nextVersion))
	}))
	defer server.Close()
	catalog := Catalog{ID: uuid.New(), Code: "beeba", Kind: "beeba", BaseURL: "https://public.example", APIBaseURL: server.URL + "/api/v1", Enabled: true}
	h := NewHandler(nil, config.AssetCatalogConfig{Enabled: true, Code: "beeba", Kind: "beeba", BaseURL: catalog.BaseURL, APIBaseURL: catalog.APIBaseURL})
	original := func() []WorldAsset {
		return []WorldAsset{{PinnedVersionID: version, Asset: AssetRef{ID: uuid.New(), Catalog: catalog, Asset: ResolvedAsset{ExternalID: id, Title: "stale secret", DownloadURL: "old-url", UnlockPassword: "stale key", Metadata: map[string]any{"secret": "old"}}}}}
	}
	got, err := h.revalidateWorldAssets(context.Background(), original(), false)
	if err != nil || got[0].Asset.Asset.Title != "Latest public title" || got[0].Asset.Asset.Availability != "available" {
		t.Fatalf("initial live resolution failed %v", err)
	}
	nextVersion = uuid.NewString()
	got, err = h.revalidateWorldAssets(context.Background(), original(), false)
	if err != nil || got[0].Asset.Asset.Availability != "update_available" || got[0].Asset.Asset.VersionID != version || got[0].Asset.Asset.DownloadURL != "" || got[0].Asset.Asset.UnlockPassword != "" {
		t.Fatal("version silently advanced")
	}
	status = "unavailable"
	got, err = h.revalidateWorldAssets(context.Background(), original(), false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "stale") || strings.Contains(string(encoded), "old-url") || got[0].Asset.Asset.Availability != "unavailable" {
		t.Fatal("stale data leaked")
	}
	status = "outage"
	if _, err = h.revalidateWorldAssets(context.Background(), original(), false); err == nil {
		t.Fatal("outage returned stale snapshot")
	}
	h.cfg.Enabled = false
	got, err = h.revalidateWorldAssets(context.Background(), original(), false)
	if err != nil || got[0].Asset.Asset.Availability != "unavailable" {
		t.Fatal("persisted catalog bypassed runtime disable")
	}
}
