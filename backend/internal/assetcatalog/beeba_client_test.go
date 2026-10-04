package assetcatalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
					"id": "3d194549-02a0-4966-8583-59254c19f64b",
					"slug": "moon-hub",
					"title": "Moon Hub",
					"description": "Social VR world",
					"status": "published",
					"visibility": "public",
					"nsfw": false,
					"category": {"slug": "worlds", "name": "Worlds"},
					"author": {"id": "author-1", "username": "alice", "display_name": "Alice"},
					"tags": [{"slug": "social", "name": "Social"}],
					"preview_image_id": "4e786f8d-7914-4418-947c-723a55395f0c",
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
	if item.ExternalID != "3d194549-02a0-4966-8583-59254c19f64b" {
		t.Fatalf("ExternalID = %q", item.ExternalID)
	}
	if item.ContentType != AssetTypeWorld {
		t.Fatalf("ContentType = %q", item.ContentType)
	}
	if item.PreviewURL != server.URL+"/api/v1/media/4e786f8d-7914-4418-947c-723a55395f0c" {
		t.Fatalf("PreviewURL = %q", item.PreviewURL)
	}
	if item.DownloadURL != "" {
		t.Fatalf("DownloadURL = %q", item.DownloadURL)
	}
	if item.Metadata["downloadsCount"].(float64) != 12 {
		t.Fatalf("Metadata = %#v", item.Metadata)
	}
}

func TestBeeBaClientCheckHealthUsesReadyzContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/readyz" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ready","service":"beeba"}`))
	}))
	defer server.Close()
	client := NewBeeBaClient(BeeBaClientConfig{
		Catalog:    Catalog{BaseURL: "https://public-client-origin.example", APIBaseURL: server.URL + "/api/v1"},
		HTTPClient: server.Client(),
	})
	if err := client.CheckHealth(context.Background()); err != nil {
		t.Fatalf("CheckHealth: %v", err)
	}
}

func TestBeeBaClientCheckHealthRejectsNotReadyStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
	}))
	defer server.Close()
	client := NewBeeBaClient(BeeBaClientConfig{
		Catalog: Catalog{BaseURL: server.URL}, HTTPClient: server.Client(),
	})
	if err := client.CheckHealth(context.Background()); err == nil {
		t.Fatal("expected unavailable error")
	}
}
