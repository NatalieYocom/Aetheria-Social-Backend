package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestPublishAnnounceCreatesDeliveryJobsForRemoteFollowers(t *testing.T) {
	db, mock := newMockDB(t)
	actorID := uuid.New()
	objectID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT actor_uri, followers_url").WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri", "followers_url"}).
			AddRow("https://basis.example/users/alice", "https://basis.example/users/alice/followers"))
	mock.ExpectQuery("SELECT DISTINCT").WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"inbox"}).AddRow("https://remote.example/inbox"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO activities (")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), actorID, objectID, "World", "public", sqlmock.AnyArg(), "outbound").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO outbox_jobs").
		WithArgs(sqlmock.AnyArg(), "https://remote.example/inbox").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := NewService(db, "https://basis.example").PublishAnnounce(context.Background(), AnnounceInput{
		ActorID: actorID, ObjectID: objectID, ObjectType: "World",
		ObjectURI: "https://basis.example/objects/" + objectID.String(), Visibility: "public",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deliveries != 1 {
		t.Fatalf("deliveries = %d", result.Deliveries)
	}
	var activity map[string]any
	if err := json.Unmarshal(result.RawJSON, &activity); err != nil {
		t.Fatal(err)
	}
	if activity["actor"] != "https://basis.example/users/alice" || activity["id"] != result.ActivityURI {
		t.Fatalf("activity = %+v", activity)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPublishAnnounceDoesNotFederatePrivateObject(t *testing.T) {
	db, mock := newMockDB(t)
	actorID := uuid.New()
	objectID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT actor_uri, followers_url").WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri", "followers_url"}).
			AddRow("https://basis.example/users/alice", "https://basis.example/users/alice/followers"))
	mock.ExpectExec("INSERT INTO activities").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), actorID, objectID, "Event", "private", sqlmock.AnyArg(), "local").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	result, err := NewService(db, "https://basis.example").PublishAnnounce(context.Background(), AnnounceInput{
		ActorID: actorID, ObjectID: objectID, ObjectType: "Event",
		ObjectURI: "https://basis.example/objects/" + objectID.String(), Visibility: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Deliveries != 0 {
		t.Fatalf("deliveries = %d", result.Deliveries)
	}
}

func newMockDB(t *testing.T) (*sql.DB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, mock
}
