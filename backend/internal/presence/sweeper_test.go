package presence

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

func TestSweeperDeletesExpiredPresenceAndPublishesRemoval(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	friendID := uuid.New()
	instanceID := uuid.New()
	selfEvents, unsubscribeSelf := broker.Subscribe(context.Background(), actorID)
	defer unsubscribeSelf()
	friendEvents, unsubscribeFriend := broker.Subscribe(context.Background(), friendID)
	defer unsubscribeFriend()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(expiredPresenceDeleteBatchSQL)).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "visibility", "instance_id"}).AddRow(actorID, "friends", instanceID))
	mock.ExpectExec(regexp.QuoteMeta(expireInstanceMemberSQL)).
		WithArgs(instanceID, actorID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(decrementInstanceUsersSQL)).
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT actor_id").
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id"}).AddRow(friendID))
	mock.ExpectQuery("SELECT host_actor_id AS actor_id").
		WithArgs(instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id"}))

	count, err := NewSweeper(db, broker, SweeperConfig{BatchSize: 10}).SweepExpired(context.Background())
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	assertPresenceEventType(t, selfEvents, "presence.removed")
	assertPresenceEventType(t, friendEvents, "presence.removed")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSweeperDoesNotNotifyFriendsForPrivateExpiredPresence(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	friendID := uuid.New()
	selfEvents, unsubscribeSelf := broker.Subscribe(context.Background(), actorID)
	defer unsubscribeSelf()
	friendEvents, unsubscribeFriend := broker.Subscribe(context.Background(), friendID)
	defer unsubscribeFriend()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(expiredPresenceDeleteBatchSQL)).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "visibility", "instance_id"}).AddRow(actorID, "nobody", nil))
	mock.ExpectCommit()

	count, err := NewSweeper(db, broker, SweeperConfig{BatchSize: 10}).SweepExpired(context.Background())
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	assertPresenceEventType(t, selfEvents, "presence.removed")
	assertNoPresenceEvent(t, friendEvents)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSweeperSkipsPublishWhenExpiredPresenceWasAlreadyDeleted(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	selfEvents, unsubscribeSelf := broker.Subscribe(context.Background(), actorID)
	defer unsubscribeSelf()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(expiredPresenceDeleteBatchSQL)).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "visibility", "instance_id"}))
	mock.ExpectCommit()

	count, err := NewSweeper(db, broker, SweeperConfig{BatchSize: 10}).SweepExpired(context.Background())
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d", count)
	}
	assertNoPresenceEvent(t, selfEvents)
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

func assertPresenceEventType(t *testing.T, events <-chan realtime.Event, eventType string) {
	t.Helper()
	select {
	case event := <-events:
		if event.Type != eventType {
			t.Fatalf("event.Type = %q, want %q", event.Type, eventType)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("timed out waiting for %s", eventType)
	}
}

func assertNoPresenceEvent(t *testing.T, events <-chan realtime.Event) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected event delivered: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}
