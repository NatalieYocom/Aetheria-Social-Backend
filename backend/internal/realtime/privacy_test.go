package realtime

import (
	"context"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"regexp"
	"testing"
	"time"
)

func TestDeliveryRevalidatesOldPresenceAgainstCurrentPrivacy(t *testing.T) {
	for _, test := range []struct {
		name, status, visibility string
		blocked, exact           bool
		want                     string
	}{
		{"now-invisible", "invisible", "friends", false, true, "presence.removed"},
		{"now-private", "online", "nobody", false, true, "presence.removed"},
		{"now-blocked", "online", "friends", true, true, "presence.removed"},
		{"now-hides-instance", "online", "friends", false, false, "presence.updated"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newMockDB(t)
			h := NewHandler(nil).SetPrivacyDB(db)
			subject, viewer, instance := uuid.New(), uuid.New(), uuid.New()
			mock.ExpectQuery(regexp.QuoteMeta(loadPresenceForRealtimeSQL)).WithArgs(subject).WillReturnRows(sqlmock.NewRows(presenceRealtimeColumns()).AddRow(uuid.New(), subject, "actor@example.test", "Name", uuid.New(), instance, test.status, test.visibility, test.exact, time.Now().Add(time.Minute), time.Now()))
			mock.ExpectQuery("SELECT EXISTS.*type='friend'").WithArgs(viewer, subject).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			mock.ExpectQuery("SELECT EXISTS.*type = 'block'").WithArgs(viewer, subject).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(test.blocked))
			old := Event{ID: "old-event", Cursor: "100-0", Type: "presence.updated", ActorID: subject, Payload: map[string]any{"instanceId": uuid.NewString(), "visibility": "public"}}
			actual := h.privateEvent(context.Background(), viewer, old)
			if actual.Type != test.want || actual.Cursor != old.Cursor {
				t.Fatalf("event type/cursor changed incorrectly: %+v", actual)
			}
			if test.want == "presence.updated" {
				payload, ok := actual.Payload.(PresenceEventPayload)
				if !ok || payload.InstanceID != nil {
					t.Fatalf("historical exact instance escaped: %+v", actual.Payload)
				}
			} else {
				payload, ok := actual.Payload.(map[string]string)
				if !ok || len(payload) != 1 || payload["actorId"] != subject.String() {
					t.Fatalf("removed event leaked fields: %+v", actual.Payload)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
