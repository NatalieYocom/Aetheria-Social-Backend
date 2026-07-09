package invites

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

func TestCreateInviteCreatesDurableNotification(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 8})
	router := newTestRouter(db, broker)
	toActorID := uuid.New()
	worldID := uuid.New()
	worldOwnerID := uuid.New()
	inviteID := uuid.New()
	notificationID := uuid.New()
	createdAt := time.Now().UTC()
	expiresAt := createdAt.Add(24 * time.Hour)
	toEvents, unsubscribe := broker.Subscribe(context.Background(), toActorID)
	defer unsubscribe()

	mock.ExpectQuery("SELECT owner_actor_id, visibility FROM worlds WHERE id").
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_actor_id", "visibility"}).AddRow(worldOwnerID, "public"))
	mock.ExpectQuery("INSERT INTO invites").
		WithArgs(testPrincipal.ActorID, toActorID, sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), "join me", "direct", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(inviteID))
	mock.ExpectQuery("SELECT i.id").
		WithArgs(inviteID).
		WillReturnRows(inviteRows().AddRow(
			inviteID,
			testPrincipal.ActorID,
			"alice@example.social",
			"Alice",
			toActorID,
			"bob@example.social",
			"Bob",
			worldID,
			nil,
			nil,
			"join me",
			"direct",
			"pending",
			expiresAt,
			createdAt,
		))
	mock.ExpectQuery("INSERT INTO notifications").
		WithArgs(toActorID, "invite.created", sqlmock.AnyArg()).
		WillReturnRows(notificationRows().AddRow(notificationID, toActorID, "invite.created", []byte(`{"state":"pending"}`), nil, createdAt))

	req := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"toActorId":"`+toActorID.String()+`","worldId":"`+worldID.String()+`","message":"join me"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	assertEventEventually(t, toEvents, "notification.created")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateInviteInPrivateWorldWithoutAccessReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 8})
	router := newTestRouter(db, broker)
	toActorID := uuid.New()
	worldID := uuid.New()
	ownerID := uuid.New()

	mock.ExpectQuery("SELECT owner_actor_id, visibility FROM worlds WHERE id").
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_actor_id", "visibility"}).AddRow(ownerID, "private"))

	req := httptest.NewRequest(http.MethodPost, "/api/invites", strings.NewReader(`{"toActorId":"`+toActorID.String()+`","worldId":"`+worldID.String()+`","message":"join me"}`))
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

func TestAcceptExpiredInviteReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 8})
	router := newTestRouter(db, broker)
	inviteID := uuid.New()

	mock.ExpectExec("expires_at > now\\(\\)").
		WithArgs(inviteID, testPrincipal.ActorID, "accepted").
		WillReturnResult(sqlmock.NewResult(0, 0))

	req := httptest.NewRequest(http.MethodPost, "/api/invites/"+inviteID.String()+"/accept", nil)
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

func inviteRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"from_actor_id",
		"from_acct",
		"from_display_name",
		"to_actor_id",
		"to_acct",
		"to_display_name",
		"world_id",
		"instance_id",
		"event_id",
		"message",
		"visibility",
		"state",
		"expires_at",
		"created_at",
	})
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
