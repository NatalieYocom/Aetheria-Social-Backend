package realtime

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestPublishPresenceChangedFansOutToSelfAndFriends(t *testing.T) {
	db, mock := newMockDB(t)
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	friendID := uuid.New()
	strangerID := uuid.New()
	presenceID := uuid.New()
	worldID := uuid.New()
	instanceID := uuid.New()
	expiresAt := time.Now().UTC().Add(time.Minute)
	updatedAt := time.Now().UTC()
	selfEvents, unsubscribeSelf := broker.Subscribe(context.Background(), actorID)
	defer unsubscribeSelf()
	friendEvents, unsubscribeFriend := broker.Subscribe(context.Background(), friendID)
	defer unsubscribeFriend()
	strangerEvents, unsubscribeStranger := broker.Subscribe(context.Background(), strangerID)
	defer unsubscribeStranger()

	mock.ExpectQuery(regexp.QuoteMeta(loadPresenceForRealtimeSQL)).
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows(presenceRealtimeColumns()).
			AddRow(presenceID, actorID, "alice@example.social", "Alice", worldID, instanceID, "online", "friends", true, expiresAt, updatedAt))
	mock.ExpectQuery(regexp.QuoteMeta(presenceFriendWatchersSQL)).
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id"}).AddRow(friendID))

	if err := PublishPresenceChanged(context.Background(), db, broker, actorID); err != nil {
		t.Fatalf("PublishPresenceChanged: %v", err)
	}

	assertEventType(t, selfEvents, "presence.updated")
	assertEventType(t, friendEvents, "presence.updated")
	assertNoEvent(t, strangerEvents)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishPresenceChangedDoesNotFanOutPrivatePresenceToFriends(t *testing.T) {
	db, mock := newMockDB(t)
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	friendID := uuid.New()
	presenceID := uuid.New()
	selfEvents, unsubscribeSelf := broker.Subscribe(context.Background(), actorID)
	defer unsubscribeSelf()
	friendEvents, unsubscribeFriend := broker.Subscribe(context.Background(), friendID)
	defer unsubscribeFriend()

	mock.ExpectQuery(regexp.QuoteMeta(loadPresenceForRealtimeSQL)).
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows(presenceRealtimeColumns()).
			AddRow(presenceID, actorID, "alice@example.social", "Alice", nil, nil, "online", "nobody", false, time.Now().UTC().Add(time.Minute), time.Now().UTC()))

	if err := PublishPresenceChanged(context.Background(), db, broker, actorID); err != nil {
		t.Fatalf("PublishPresenceChanged: %v", err)
	}

	assertEventType(t, selfEvents, "presence.updated")
	assertNoEvent(t, friendEvents)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishPresenceRemovedFansOutToSelfAndFriends(t *testing.T) {
	db, mock := newMockDB(t)
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	friendID := uuid.New()
	selfEvents, unsubscribeSelf := broker.Subscribe(context.Background(), actorID)
	defer unsubscribeSelf()
	friendEvents, unsubscribeFriend := broker.Subscribe(context.Background(), friendID)
	defer unsubscribeFriend()

	mock.ExpectQuery(regexp.QuoteMeta(presenceFriendWatchersSQL)).
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id"}).AddRow(friendID))

	if err := PublishPresenceRemoved(context.Background(), db, broker, actorID); err != nil {
		t.Fatalf("PublishPresenceRemoved: %v", err)
	}

	assertEventType(t, selfEvents, "presence.removed")
	assertEventType(t, friendEvents, "presence.removed")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishInstanceChangedFansOutToHostAndJoinedMembers(t *testing.T) {
	db, mock := newMockDB(t)
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	instanceID := uuid.New()
	hostID := uuid.New()
	memberID := uuid.New()
	hostEvents, unsubscribeHost := broker.Subscribe(context.Background(), hostID)
	defer unsubscribeHost()
	memberEvents, unsubscribeMember := broker.Subscribe(context.Background(), memberID)
	defer unsubscribeMember()

	mock.ExpectQuery(regexp.QuoteMeta(instanceWatchersSQL)).
		WithArgs(instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id"}).AddRow(hostID).AddRow(memberID))

	if err := PublishInstanceChanged(context.Background(), db, broker, instanceID, "instance.updated"); err != nil {
		t.Fatalf("PublishInstanceChanged: %v", err)
	}

	assertEventType(t, hostEvents, "instance.updated")
	assertEventType(t, memberEvents, "instance.updated")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishActorEventFansOutToRecipients(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	targetID := uuid.New()
	strangerID := uuid.New()
	actorEvents, unsubscribeActor := broker.Subscribe(context.Background(), actorID)
	defer unsubscribeActor()
	targetEvents, unsubscribeTarget := broker.Subscribe(context.Background(), targetID)
	defer unsubscribeTarget()
	strangerEvents, unsubscribeStranger := broker.Subscribe(context.Background(), strangerID)
	defer unsubscribeStranger()

	PublishActorEvent(broker, []uuid.UUID{actorID, targetID}, "friend.requested", actorID, map[string]string{
		"targetActorId": targetID.String(),
	})

	assertEventType(t, actorEvents, "friend.requested")
	assertEventType(t, targetEvents, "friend.requested")
	assertNoEvent(t, strangerEvents)
}

func TestPublishActorEventDeduplicatesRecipients(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 4})
	actorID := uuid.New()
	events, unsubscribe := broker.Subscribe(context.Background(), actorID)
	defer unsubscribe()

	PublishActorEvent(broker, []uuid.UUID{actorID, actorID}, "invite.created", actorID, map[string]string{
		"inviteId": uuid.NewString(),
	})

	assertEventType(t, events, "invite.created")
	assertNoEvent(t, events)
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

func presenceRealtimeColumns() []string {
	return []string{"id", "actor_id", "acct", "display_name", "world_id", "instance_id", "status", "visibility", "show_exact_instance", "expires_at", "updated_at"}
}

func assertEventType(t *testing.T, events <-chan Event, eventType string) {
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

func assertNoEvent(t *testing.T, events <-chan Event) {
	t.Helper()
	select {
	case event := <-events:
		t.Fatalf("unexpected event delivered: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}
