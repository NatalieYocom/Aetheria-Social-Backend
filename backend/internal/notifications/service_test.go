package notifications

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"basisvr-social-service/internal/realtime"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestCreateAndPublishStoresNotificationAndEmitsRealtimeEvent(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	notificationID := uuid.New()
	createdAt := time.Now().UTC()
	events, unsubscribe := broker.Subscribe(context.Background(), actorID)
	defer unsubscribe()

	mock.ExpectQuery(regexp.QuoteMeta(insertNotificationSQL)).
		WithArgs(actorID, "friend.requested", sqlmock.AnyArg()).
		WillReturnRows(notificationRows().AddRow(
			notificationID,
			actorID,
			"friend.requested",
			[]byte(`{"fromActorId":"`+uuid.NewString()+`"}`),
			nil,
			createdAt,
		))

	notification, err := CreateAndPublish(context.Background(), db, broker, CreateInput{
		ActorID: actorID,
		Type:    "friend.requested",
		Payload: map[string]string{"fromActorId": uuid.NewString()},
	})
	if err != nil {
		t.Fatalf("CreateAndPublish: %v", err)
	}
	if notification.ID != notificationID {
		t.Fatalf("notification.ID = %s", notification.ID)
	}

	select {
	case event := <-events:
		if event.Type != "notification.created" {
			t.Fatalf("event.Type = %q", event.Type)
		}
		if event.ActorID != actorID {
			t.Fatalf("event.ActorID = %s", event.ActorID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for notification.created")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateAndPublishSkipsEmptyActor(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 4})

	if _, err := CreateAndPublish(context.Background(), db, broker, CreateInput{
		Type: "friend.requested",
	}); err == nil {
		t.Fatal("expected validation error")
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

func notificationRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "actor_id", "type", "payload", "read_at", "created_at"})
}
