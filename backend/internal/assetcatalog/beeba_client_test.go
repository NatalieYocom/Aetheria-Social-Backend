package assetcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBeeBaClientResolveAssetMapsPublicContentDetail(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"id": "3d194549-02a0-4966-8583-59254c19f64b",
				"slug": "moon-hub",
				"title": "Moon Hub",
				"description": "BasisVR social world",
				"status": "published",
				"visibility": "public",
				"nsfw": false,
				"category": {"slug": "worlds", "name": "Worlds"},
				"author": {"id": "913d0966-f4c7-4622-8503-14fb6bdb1b1b", "username": "alice", "display_name": "Alice"},
				"tags": [{"slug": "social", "name": "Social"}, {"slug": "space", "name": "Space"}],
				"preview_image_id": "4e786f8d-7914-4418-947c-723a55395f0c",
				"gallery": [
					{"id": "4e786f8d-7914-4418-947c-723a55395f0c", "url": "/api/v1/media/4e786f8d-7914-4418-947c-723a55395f0c", "alt_text": "Moon Hub", "width": 1200, "height": 675, "is_primary": true, "sort_order": 0}
				],
				"file": {"file_size": 4096, "file_hash_sha256": "sha256-value", "original_filename": "moon-hub.bee"}
			}
		}`))
	}))
	defer server.Close()

	client := NewBeeBaClient(BeeBaClientConfig{
		Catalog: Catalog{
			Code:       "beeba",
			Kind:       CatalogKindBeeBa,
			BaseURL:    server.URL,
			APIBaseURL: server.URL + "/api/v1",
		},
		APIToken:   "test-token",
		HTTPClient: server.Client(),
	})

	asset, err := client.ResolveAsset(context.Background(), "3d194549-02a0-4966-8583-59254c19f64b")
	if err != nil {
		t.Fatalf("ResolveAsset: %v", err)
	}

	if requestedPath != "/api/v1/content/3d194549-02a0-4966-8583-59254c19f64b" {
		t.Fatalf("requested path = %q", requestedPath)
	}
	if asset.ExternalID != "3d194549-02a0-4966-8583-59254c19f64b" {
		t.Fatalf("ExternalID = %q", asset.ExternalID)
	}
	if asset.ContentType != AssetTypeWorld {
		t.Fatalf("ContentType = %q", asset.ContentType)
	}
	if asset.Title != "Moon Hub" {
		t.Fatalf("Title = %q", asset.Title)
	}
	if asset.AuthorName != "alice" {
		t.Fatalf("AuthorName = %q", asset.AuthorName)
	}
	if asset.PreviewURL != server.URL+"/api/v1/media/4e786f8d-7914-4418-947c-723a55395f0c" {
		t.Fatalf("PreviewURL = %q", asset.PreviewURL)
	}
	if asset.DownloadURL != server.URL+"/api/v1/content/3d194549-02a0-4966-8583-59254c19f64b/download" {
		t.Fatalf("DownloadURL = %q", asset.DownloadURL)
	}
	if len(asset.Tags) != 2 || asset.Tags[0] != "social" || asset.Tags[1] != "space" {
		t.Fatalf("Tags = %#v", asset.Tags)
	}
	if asset.Metadata["fileHashSHA256"] != "sha256-value" {
		t.Fatalf("Metadata = %#v", asset.Metadata)
	}
}

func TestBeeBaClientSearchAssetsMapsPublicContentPage(t *testing.T) {
	var requestedPath string
	var requestedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		requestedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": [
				{
					"id": "world-1",
					"slug": "moon-hub",
					"title": "Moon Hub",
					"description": "Social VR world",
					"status": "published",
					"visibility": "public",
					"nsfw": false,
					"category": {"slug": "worlds", "name": "Worlds"},
					"author": {"id": "author-1", "username": "alice", "display_name": "Alice"},
					"tags": [{"slug": "social", "name": "Social"}],
					"preview_image_id": "preview-1",
					"likes_count": 7,
					"downloads_count": 12,
					"comments_count": 2
				}
			],
			"pagination": {"next_cursor": "cursor-2"}
		}`))
	}))
	defer server.Close()

	client := NewBeeBaClient(BeeBaClientConfig{
		Catalog: Catalog{
			Code:       "beeba",
			Kind:       CatalogKindBeeBa,
			BaseURL:    server.URL,
			APIBaseURL: server.URL + "/api/v1",
		},
		HTTPClient: server.Client(),
	})

	page, err := client.SearchAssets(context.Background(), SearchQuery{
		Query:       "moon",
		ContentType: AssetTypeWorld,
		Tags:        []string{"social", "featured"},
		IncludeNSFW: true,
		Sort:        "downloads",
		Limit:       25,
		Cursor:      "cursor-1",
	})
	if err != nil {
		t.Fatalf("SearchAssets: %v", err)
	}

	if requestedPath != "/api/v1/search" {
		t.Fatalf("requested path = %q", requestedPath)
	}
	if requestedQuery != "category=worlds&cursor=cursor-1&include_nsfw=true&limit=25&q=moon&sort=downloads&tags=social%2Cfeatured" {
		t.Fatalf("requested query = %q", requestedQuery)
	}
	if page.NextCursor != "cursor-2" {
		t.Fatalf("NextCursor = %q", page.NextCursor)
	}
	if len(page.Items) != 1 {
		t.Fatalf("len(Items) = %d", len(page.Items))
	}
	item := page.Items[0]
	if item.ExternalID != "world-1" {
		t.Fatalf("ExternalID = %q", item.ExternalID)
	}
	if item.ContentType != AssetTypeWorld {
		t.Fatalf("ContentType = %q", item.ContentType)
	}
	if item.PreviewURL != server.URL+"/api/v1/media/preview-1" {
		t.Fatalf("PreviewURL = %q", item.PreviewURL)
	}
	if item.DownloadURL != server.URL+"/api/v1/content/world-1/download" {
		t.Fatalf("DownloadURL = %q", item.DownloadURL)
	}
	if item.Metadata["downloadsCount"].(float64) != 12 {
		t.Fatalf("Metadata = %#v", item.Metadata)
	}
}
