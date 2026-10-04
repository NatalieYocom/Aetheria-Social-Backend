package presence

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"basisvr-social-service/internal/auth"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestUpsertRejectsInvalidStatus(t *testing.T) {
	db, mock := newMockDB(t)
	router := newPresenceTestRouter(db)

	req := httptest.NewRequest(http.MethodPost, "/api/presence", strings.NewReader(`{"status":"teleporting"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUpsertRejectsInvalidVisibility(t *testing.T) {
	db, mock := newMockDB(t)
	router := newPresenceTestRouter(db)

	req := httptest.NewRequest(http.MethodPost, "/api/presence", strings.NewReader(`{"visibility":"everyone"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFriendsPresenceQueryExcludesInvisibleStatus(t *testing.T) {
	db, mock := newMockDB(t)
	router := newPresenceTestRouter(db)

	mock.ExpectQuery("ps.status <> 'invisible'").
		WithArgs(testPrincipal.ActorID, nil, uuid.Nil, 51).
		WillReturnRows(sqlmock.NewRows([]string{
			"id",
			"actor_id",
			"acct",
			"display_name",
			"world_id",
			"instance_id",
			"status",
			"visibility",
			"show_exact_instance",
			"expires_at",
			"updated_at",
		}))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/presence/friends", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

var testPrincipal = auth.Principal{
	UserID:   uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"),
	ActorID:  uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
	Username: "alice",
}

func newPresenceTestRouter(db *sql.DB) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandler(db), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), testPrincipal)))
		})
	})
	return r
}
