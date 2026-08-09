package worlds

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/page"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestListWorldsReturnsCursorEnvelope(t *testing.T) {
	db, mock := newMockDB(t)
	router := newWorldTestRouter(db, nil)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	worldOne := uuid.New()
	worldTwo := uuid.New()
	ownerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(listWorldsSQL)).
		WithArgs(nil, uuid.Nil, 2).
		WillReturnRows(listWorldRows().
			AddRow(worldOne, "one", "One", "", "", "basis://one", "public", 8, "{}", []byte(`{}`), ownerID, "owner@example.social", "Owner", createdAt).
			AddRow(worldTwo, "two", "Two", "", "", "basis://two", "public", 8, "{}", []byte(`{}`), ownerID, "owner@example.social", "Owner", createdAt.Add(-time.Second)))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/worlds?limit=1", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var response page.Response[WorldResponse]
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Pagination.NextCursor == nil {
		t.Fatalf("response = %+v", response)
	}
	cursor, err := page.Decode(*response.Pagination.NextCursor)
	if err != nil || cursor.ID != worldOne || !cursor.SortTime.Equal(createdAt) {
		t.Fatalf("cursor = %+v, err = %v", cursor, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListWorldsRejectsInvalidCursor(t *testing.T) {
	db, mock := newMockDB(t)
	router := newWorldTestRouter(db, nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/worlds?cursor=invalid", nil))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadByIDAcceptsPostgresTextArrayLiteral(t *testing.T) {
	db, mock := newMockDB(t)
	handler := NewHandler(db, "https://social.example")
	worldID := uuid.New()
	ownerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(`
SELECT w.id, w.slug, w.name, w.description, w.preview_url, w.launch_url, w.visibility, w.capacity, w.tags, w.metadata,
       a.id, a.acct, a.display_name
FROM worlds w
JOIN actors a ON a.id = w.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = a.local_user_id
WHERE w.id = $1
  AND (a.local_user_id IS NULL OR owner_user.status = 'active')`)).
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{
			"id",
			"slug",
			"name",
			"description",
			"preview_url",
			"launch_url",
			"visibility",
			"capacity",
			"tags",
			"metadata",
			"actor_id",
			"acct",
			"display_name",
		}).AddRow(
			worldID,
			"smoke-world",
			"Smoke World",
			"Smoke description",
			"https://cdn.example/preview.webp",
			"basis://launch",
			"public",
			8,
			"{smoke,basis}",
			[]byte(`{"source":"test"}`),
			ownerID,
			"alice@example.social",
			"Alice",
		))

	world, err := handler.loadByID(context.Background(), worldID)
	if err != nil {
		t.Fatalf("loadByID: %v", err)
	}
	if len(world.Tags) != 2 || world.Tags[0] != "smoke" || world.Tags[1] != "basis" {
		t.Fatalf("Tags = %#v", world.Tags)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetPrivateWorldWithoutViewerReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	router := newWorldTestRouter(db, nil)
	worldID := uuid.New()
	ownerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(`
SELECT w.id, w.slug, w.name, w.description, w.preview_url, w.launch_url, w.visibility, w.capacity, w.tags, w.metadata,
       a.id, a.acct, a.display_name
FROM worlds w
JOIN actors a ON a.id = w.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = a.local_user_id
WHERE w.slug = $1
  AND (a.local_user_id IS NULL OR owner_user.status = 'active')`)).
		WithArgs("private-world").
		WillReturnRows(worldRows().AddRow(
			worldID,
			"private-world",
			"Private World",
			"",
			"",
			"basis://private",
			"private",
			8,
			"{}",
			[]byte(`{}`),
			ownerID,
			"owner@example.social",
			"Owner",
		))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/worlds/private-world", nil))

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetFollowersWorldAllowsAcceptedFollower(t *testing.T) {
	db, mock := newMockDB(t)
	router := newWorldTestRouter(db, &testPrincipal)
	worldID := uuid.New()
	ownerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(`
SELECT w.id, w.slug, w.name, w.description, w.preview_url, w.launch_url, w.visibility, w.capacity, w.tags, w.metadata,
       a.id, a.acct, a.display_name
FROM worlds w
JOIN actors a ON a.id = w.owner_actor_id
LEFT JOIN users owner_user ON owner_user.id = a.local_user_id
WHERE w.slug = $1
  AND (a.local_user_id IS NULL OR owner_user.status = 'active')`)).
		WithArgs("followers-world").
		WillReturnRows(worldRows().AddRow(
			worldID,
			"followers-world",
			"Followers World",
			"",
			"",
			"basis://followers",
			"followers",
			8,
			"{social}",
			[]byte(`{}`),
			ownerID,
			"owner@example.social",
			"Owner",
		))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(testPrincipal.ActorID, ownerID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/worlds/followers-world", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"slug":"followers-world"`) {
		t.Fatalf("body = %s", res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFavoriteHiddenWorldWithoutAccessReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	router := newWorldTestRouter(db, &testPrincipal)
	worldID := uuid.New()
	ownerID := uuid.New()

	mock.ExpectQuery("SELECT owner_actor_id, visibility FROM worlds WHERE id").
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_actor_id", "visibility"}).AddRow(ownerID, "private"))

	req := httptest.NewRequest(http.MethodPost, "/api/worlds/"+worldID.String()+"/favorite", nil)
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

var testPrincipal = auth.Principal{
	UserID:   uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
	ActorID:  uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
	Username: "alice",
}

func newWorldTestRouter(db *sql.DB, principal *auth.Principal) http.Handler {
	r := chi.NewRouter()
	if principal != nil {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), *principal)))
			})
		})
	}
	RegisterRoutes(r, NewHandler(db, "https://social.example"), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if principal == nil {
				http.Error(w, "missing test principal", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), *principal)))
		})
	})
	return r
}

func worldRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"slug",
		"name",
		"description",
		"preview_url",
		"launch_url",
		"visibility",
		"capacity",
		"tags",
		"metadata",
		"actor_id",
		"acct",
		"display_name",
	})
}

func listWorldRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "slug", "name", "description", "preview_url", "launch_url",
		"visibility", "capacity", "tags", "metadata", "actor_id", "acct",
		"display_name", "created_at",
	})
}
