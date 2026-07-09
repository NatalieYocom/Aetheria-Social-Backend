package notifications

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
	"basisvr-social-service/internal/realtime"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestListNotificationsReturnsActorNotifications(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, nil)
	notificationID := uuid.New()
	createdAt := time.Now().UTC()

	mock.ExpectQuery(regexp.QuoteMeta(listUnreadNotificationsSQL)).
		WithArgs(testPrincipal.ActorID, 25).
		WillReturnRows(notificationRows().AddRow(
			notificationID,
			testPrincipal.ActorID,
			"friend.requested",
			[]byte(`{"fromActorId":"actor-1"}`),
			nil,
			createdAt,
		))

	req := httptest.NewRequest(http.MethodGet, "/api/notifications?unreadOnly=true&limit=25", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body []NotificationResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != 1 {
		t.Fatalf("len(body) = %d", len(body))
	}
	if body[0].ID != notificationID {
		t.Fatalf("body[0].ID = %s", body[0].ID)
	}
	if body[0].Type != "friend.requested" {
		t.Fatalf("body[0].Type = %q", body[0].Type)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestUnreadCountReturnsCount(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, nil)

	mock.ExpectQuery(regexp.QuoteMeta(unreadCountSQL)).
		WithArgs(testPrincipal.ActorID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))

	req := httptest.NewRequest(http.MethodGet, "/api/notifications/unread-count", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"count":3`) {
		t.Fatalf("body = %s", res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkReadMarksOnlyActorNotification(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, nil)
	notificationID := uuid.New()
	readAt := time.Now().UTC()
	createdAt := readAt.Add(-time.Minute)

	mock.ExpectQuery(regexp.QuoteMeta(markReadSQL)).
		WithArgs(notificationID, testPrincipal.ActorID).
		WillReturnRows(notificationRows().AddRow(
			notificationID,
			testPrincipal.ActorID,
			"invite.created",
			[]byte(`{"inviteId":"invite-1"}`),
			readAt,
			createdAt,
		))

	req := httptest.NewRequest(http.MethodPost, "/api/notifications/"+notificationID.String()+"/read", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body NotificationResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ReadAt == nil {
		t.Fatal("ReadAt is nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestMarkAllReadReturnsUpdatedCount(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, nil)

	mock.ExpectExec(regexp.QuoteMeta(markAllReadSQL)).
		WithArgs(testPrincipal.ActorID).
		WillReturnResult(sqlmock.NewResult(0, 4))

	req := httptest.NewRequest(http.MethodPost, "/api/notifications/read-all", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"updated":4`) {
		t.Fatalf("body = %s", res.Body.String())
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

func newTestRouter(db *sql.DB, broker *realtime.Broker) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandler(db, broker), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), testPrincipal)))
		})
	})
	return r
}
