package relationships

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/realtime"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestFriendRequestCreatesDurableNotification(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 8})
	router := newTestRouter(db, broker)
	targetID := uuid.New()
	notificationID := uuid.New()
	targetEvents, unsubscribe := broker.Subscribe(context.Background(), targetID)
	defer unsubscribe()

	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO relationships").
		WithArgs(testPrincipal.ActorID, targetID, "friend", "outgoing", "pending").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO relationships").
		WithArgs(targetID, testPrincipal.ActorID, "friend", "incoming", "pending").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("INSERT INTO notifications").
		WithArgs(targetID, "friend.requested", sqlmock.AnyArg()).
		WillReturnRows(notificationRows().AddRow(notificationID, targetID, "friend.requested", []byte(`{"state":"pending"}`), nil, time.Now().UTC()))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/friends/request", strings.NewReader(`{"targetActorId":"`+targetID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	assertEventEventually(t, targetEvents, "notification.created")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFriendAcceptRequiresIncomingPendingRequest(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 8})
	router := newTestRouter(db, broker)
	targetID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("actor_id = \\$1 AND target_actor_id = \\$2 AND direction = 'incoming'").
		WithArgs(testPrincipal.ActorID, targetID, "accepted").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/friends/accept", strings.NewReader(`{"targetActorId":"`+targetID.String()+`"}`))
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

func newTestRouter(db *sql.DB, broker *realtime.Broker) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandler(db, broker), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), testPrincipal)))
		})
	})
	return r
}

func notificationRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "actor_id", "type", "payload", "read_at", "created_at"})
}

func assertEventEventually(t *testing.T, events <-chan realtime.Event, eventType string) {
	t.Helper()
	deadline := time.After(100 * time.Millisecond)
	for {
		select {
		case event := <-events:
			if event.Type == eventType {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", eventType)
		}
	}
}
