package profiles

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"basisvr-social-service/internal/common/page"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestSearchUsersReturnsCursorEnvelope(t *testing.T) {
	db, mock := newProfileMockDB(t)
	router := chi.NewRouter()
	RegisterRoutes(router, NewHandler(db), func(next http.Handler) http.Handler { return next })
	firstUserID := uuid.New()
	secondUserID := uuid.New()
	firstActorID := uuid.New()

	mock.ExpectQuery("FROM users u").
		WithArgs("%ali%", nil, uuid.Nil, 2).
		WillReturnRows(profileRows().
			AddRow(firstUserID, firstActorID, "alice", "alice@example.social", "Alice", "", "", "", "", []byte(`[]`)).
			AddRow(secondUserID, uuid.New(), "alicia", "alicia@example.social", "Alicia", "", "", "", "", []byte(`[]`)))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/search/users?q=ali&limit=1", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var response page.Response[publicProfileResponse]
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Pagination.NextCursor == nil {
		t.Fatalf("response = %+v", response)
	}
	cursor, err := page.DecodeText(*response.Pagination.NextCursor)
	if err != nil || cursor.ID != firstUserID || cursor.SortText != "alice" {
		t.Fatalf("cursor = %+v, err = %v", cursor, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func newProfileMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}

func profileRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "actor_id", "username", "acct", "display_name", "bio", "avatar_url", "banner_url", "status_text", "links"})
}
