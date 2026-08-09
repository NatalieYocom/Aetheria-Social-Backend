package events

import (
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

func TestListEventsReturnsCursorEnvelope(t *testing.T) {
	db, mock := newMockDB(t)
	router := newEventTestRouter(db, nil)
	start := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)
	eventOne := uuid.New()
	eventTwo := uuid.New()
	worldID := uuid.New()
	ownerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(listEventsSQL)).
		WithArgs(nil, uuid.Nil, 2).
		WillReturnRows(eventRows().
			AddRow(eventOne, "one", "One", "", start, start.Add(time.Hour), "basis://one", "public", []byte(`{}`), ownerID, "owner@example.social", "Owner", worldID, "world", "World").
			AddRow(eventTwo, "two", "Two", "", start.Add(time.Hour), start.Add(2*time.Hour), "basis://two", "public", []byte(`{}`), ownerID, "owner@example.social", "Owner", worldID, "world", "World"))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/events?limit=1", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var response page.Response[EventResponse]
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Pagination.NextCursor == nil {
		t.Fatalf("response = %+v", response)
	}
	cursor, err := page.Decode(*response.Pagination.NextCursor)
	if err != nil || cursor.ID != eventOne || !cursor.SortTime.Equal(start) {
		t.Fatalf("cursor = %+v, err = %v", cursor, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetPrivateEventWithoutViewerReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	router := newEventTestRouter(db, nil)
	eventID := uuid.New()
	worldID := uuid.New()
	ownerID := uuid.New()
	start := time.Now().UTC().Add(time.Hour)
	end := start.Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta(eventQuery() + ` WHERE e.slug = $1
  AND (a.local_user_id IS NULL OR event_owner_user.status = 'active')
  AND (world_owner.local_user_id IS NULL OR world_owner_user.status = 'active')`)).
		WithArgs("private-event").
		WillReturnRows(eventRows().AddRow(
			eventID,
			"private-event",
			"Private Event",
			"",
			start,
			end,
			"basis://event/private",
			"private",
			[]byte(`{}`),
			ownerID,
			"owner@example.social",
			"Owner",
			worldID,
			"private-world",
			"Private World",
		))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/events/private-event", nil))

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestGetFriendsEventAllowsAcceptedFriend(t *testing.T) {
	db, mock := newMockDB(t)
	router := newEventTestRouter(db, &testPrincipal)
	eventID := uuid.New()
	worldID := uuid.New()
	ownerID := uuid.New()
	start := time.Now().UTC().Add(time.Hour)
	end := start.Add(time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta(eventQuery() + ` WHERE e.slug = $1
  AND (a.local_user_id IS NULL OR event_owner_user.status = 'active')
  AND (world_owner.local_user_id IS NULL OR world_owner_user.status = 'active')`)).
		WithArgs("friends-event").
		WillReturnRows(eventRows().AddRow(
			eventID,
			"friends-event",
			"Friends Event",
			"",
			start,
			end,
			"basis://event/friends",
			"friends",
			[]byte(`{}`),
			ownerID,
			"owner@example.social",
			"Owner",
			worldID,
			"friends-world",
			"Friends World",
		))
	mock.ExpectQuery("SELECT EXISTS").
		WithArgs(testPrincipal.ActorID, ownerID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/events/friends-event", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"slug":"friends-event"`) {
		t.Fatalf("body = %s", res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateEventInHiddenWorldWithoutAccessReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	router := newEventTestRouter(db, &testPrincipal)
	worldID := uuid.New()
	ownerID := uuid.New()
	start := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	end := time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339)

	mock.ExpectQuery("SELECT owner_actor_id, visibility FROM worlds WHERE id").
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_actor_id", "visibility"}).AddRow(ownerID, "private"))

	body := `{"worldId":"` + worldID.String() + `","name":"Hidden Event","startTime":"` + start + `","endTime":"` + end + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/events", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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

func newEventTestRouter(db *sql.DB, principal *auth.Principal) http.Handler {
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

func eventRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"slug",
		"name",
		"description",
		"start_time",
		"end_time",
		"launch_url",
		"visibility",
		"metadata",
		"actor_id",
		"acct",
		"display_name",
		"world_id",
		"world_slug",
		"world_name",
	})
}
