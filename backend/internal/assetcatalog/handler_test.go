package assetcatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestCatalogStatusReturnsAvailable(t *testing.T) {
	db, mock := newMockDB(t)
	catalogID := uuid.New()
	mock.ExpectQuery(regexp.QuoteMeta(selectCatalogByCodeSQL)).
		WithArgs("beeba").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "code", "name", "kind", "base_url", "api_base_url", "enabled", "metadata",
		}).AddRow(catalogID, "beeba", "BeeBa", "beeba", "https://catalog.test", "https://catalog.test/api/v1", true, []byte(`{}`)))
	router := newTestRouter(db, config.AssetCatalogConfig{}, healthClientFactory{client: healthyCatalogClient{}})
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/assets/catalogs/beeba/status", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"status":"available"`) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAssetFetchesFromCatalogAndPersistsSnapshot(t *testing.T) {
	db, mock := newMockDB(t)
	catalogID := uuid.New()
	assetID := uuid.New()
	router := newTestRouter(db, config.AssetCatalogConfig{}, fakeClientFactory{
		client: fakeCatalogClient{
			asset: ResolvedAsset{
				ExternalID:  "bee-asset-1",
				ExternalURL: "https://catalog.example/api/v1/content/bee-asset-1",
				ContentType: AssetTypeWorld,
				Title:       "Bee World",
				Description: "Imported world",
				PreviewURL:  "https://catalog.example/api/v1/media/preview",
				DownloadURL: "https://catalog.example/api/v1/content/bee-asset-1/download",
				AuthorName:  "alice",
				NSFW:        false,
				Tags:        []string{"social"},
				Metadata:    map[string]any{"source": "beeba"},
			},
		},
	})

	mock.ExpectQuery(regexp.QuoteMeta(selectCatalogByCodeSQL)).
		WithArgs("beeba").
		WillReturnRows(catalogRows().AddRow(
			catalogID,
			"beeba",
			"BeeBa",
			CatalogKindBeeBa,
			"https://catalog.example",
			"https://catalog.example/api/v1",
			true,
			[]byte(`{}`),
		))
	mock.ExpectQuery("INSERT INTO asset_refs").
		WithArgs(
			catalogID,
			"bee-asset-1",
			"https://catalog.example/api/v1/content/bee-asset-1",
			AssetTypeWorld,
			"Bee World",
			"Imported world",
			"https://catalog.example/api/v1/media/preview",
			"https://catalog.example/api/v1/content/bee-asset-1/download",
			"alice",
			"",
			false,
			"{social}",
			sqlmock.AnyArg(),
		).
		WillReturnRows(assetRows().AddRow(
			assetID,
			catalogID,
			"bee-asset-1",
			"https://catalog.example/api/v1/content/bee-asset-1",
			AssetTypeWorld,
			"Bee World",
			"Imported world",
			"https://catalog.example/api/v1/media/preview",
			"https://catalog.example/api/v1/content/bee-asset-1/download",
			"alice",
			"",
			false,
			"{social}",
			[]byte(`{"source":"beeba"}`),
		))

	req := httptest.NewRequest(http.MethodPost, "/api/assets/resolve", strings.NewReader(`{"catalog":"beeba","externalId":"bee-asset-1"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body AssetRefResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != assetID {
		t.Fatalf("ID = %s", body.ID)
	}
	if body.Catalog.Code != "beeba" {
		t.Fatalf("Catalog.Code = %q", body.Catalog.Code)
	}
	if body.ContentType != AssetTypeWorld {
		t.Fatalf("ContentType = %q", body.ContentType)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSearchAssetsLoadsCatalogAndReturnsCatalogPage(t *testing.T) {
	db, mock := newMockDB(t)
	catalogID := uuid.New()
	router := newTestRouter(db, config.AssetCatalogConfig{}, fakeClientFactory{
		client: fakeCatalogClient{
			searchPage: SearchPage{
				Items: []ResolvedAsset{
					{
						ExternalID:  "bee-asset-1",
						ExternalURL: "https://catalog.example/api/v1/content/bee-asset-1",
						ContentType: AssetTypeWorld,
						Title:       "Bee World",
						Description: "Catalog world",
						PreviewURL:  "https://catalog.example/api/v1/media/preview",
						DownloadURL: "https://catalog.example/api/v1/content/bee-asset-1/download",
						AuthorName:  "alice",
						Tags:        []string{"social"},
						Metadata:    map[string]any{"source": "beeba"},
					},
				},
				NextCursor: "cursor-2",
			},
		},
	})

	mock.ExpectQuery(regexp.QuoteMeta(selectCatalogByCodeSQL)).
		WithArgs("beeba").
		WillReturnRows(catalogRows().AddRow(
			catalogID,
			"beeba",
			"BeeBa",
			CatalogKindBeeBa,
			"https://catalog.example",
			"https://catalog.example/api/v1",
			true,
			[]byte(`{}`),
		))

	req := httptest.NewRequest(http.MethodGet, "/api/assets/search?catalog=beeba&q=bee&type=world&tags=social&includeNsfw=true&sort=downloads&limit=10&cursor=cursor-1", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body SearchResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Catalog.Code != "beeba" {
		t.Fatalf("Catalog.Code = %q", body.Catalog.Code)
	}
	if body.NextCursor != "cursor-2" {
		t.Fatalf("NextCursor = %q", body.NextCursor)
	}
	if len(body.Items) != 1 {
		t.Fatalf("len(Items) = %d", len(body.Items))
	}
	if body.Items[0].ExternalID != "bee-asset-1" {
		t.Fatalf("ExternalID = %q", body.Items[0].ExternalID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListWorldAssetsForPrivateWorldWithoutAccessReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	worldID := uuid.New()
	ownerID := uuid.New()
	router := newTestRouter(db, config.AssetCatalogConfig{}, fakeClientFactory{})

	mock.ExpectQuery("(?s)SELECT owner_actor_id, visibility.*FROM worlds w.*owner_user.status = 'active'").
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_actor_id", "visibility"}).AddRow(ownerID, "private"))

	req := httptest.NewRequest(http.MethodGet, "/api/worlds/"+worldID.String()+"/assets", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db, mock
}

func catalogRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"code",
		"name",
		"kind",
		"base_url",
		"api_base_url",
		"enabled",
		"metadata",
	})
}

func assetRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"catalog_id",
		"external_id",
		"external_url",
		"content_type",
		"title",
		"description",
		"preview_url",
		"download_url",
		"author_name",
		"license",
		"nsfw",
		"tags",
		"metadata",
	})
}

func worldAssetRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"catalog_id",
		"external_id",
		"external_url",
		"content_type",
		"title",
		"description",
		"preview_url",
		"download_url",
		"author_name",
		"license",
		"nsfw",
		"tags",
		"asset_metadata",
		"code",
		"name",
		"kind",
		"base_url",
		"api_base_url",
		"role",
		"sort_order",
		"link_metadata",
		"enabled",
		"pinned_version_id",
	})
}

func newTestRouter(db *sql.DB, cfg config.AssetCatalogConfig, factory ClientFactory) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandlerWithClientFactory(db, cfg, factory), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal := auth.Principal{
				UserID:   uuid.New(),
				ActorID:  testActorIDFromDBExpectation,
				Username: "alice",
			}
			next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), principal)))
		})
	})
	return r
}

var testActorIDFromDBExpectation uuid.UUID

type fakeClientFactory struct {
	client CatalogClient
	err    error
}

func (f fakeClientFactory) NewClient(_ Catalog) (CatalogClient, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
}

type fakeCatalogClient struct {
	asset      ResolvedAsset
	searchPage SearchPage
	err        error
}

type healthClientFactory struct {
	client CatalogClient
}

func (f healthClientFactory) NewClient(_ Catalog) (CatalogClient, error) {
	return f.client, nil
}

type healthyCatalogClient struct{}

func (healthyCatalogClient) ResolveAsset(context.Context, string) (ResolvedAsset, error) {
	return ResolvedAsset{}, nil
}

func (healthyCatalogClient) SearchAssets(context.Context, SearchQuery) (SearchPage, error) {
	return SearchPage{}, nil
}

func (healthyCatalogClient) CheckHealth(context.Context) error {
	return nil
}

func (c fakeCatalogClient) ResolveAsset(_ context.Context, _ string) (ResolvedAsset, error) {
	if c.err != nil {
		return ResolvedAsset{}, c.err
	}
	return c.asset, nil
}

func (c fakeCatalogClient) SearchAssets(_ context.Context, _ SearchQuery) (SearchPage, error) {
	if c.err != nil {
		return SearchPage{}, c.err
	}
	return c.searchPage, nil
}
