package privacy

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestCanViewAllowsPublicWithoutViewer(t *testing.T) {
	db, mock := newMockDB(t)
	ownerID := uuid.New()

	allowed, err := CanView(context.Background(), db, ViewInput{
		OwnerActorID: ownerID,
		Visibility:   "public",
	})
	if err != nil {
		t.Fatalf("CanView: %v", err)
	}
	if !allowed {
		t.Fatal("public object should be visible without viewer")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCanViewAllowsOwnerForPrivateObject(t *testing.T) {
	db, mock := newMockDB(t)
	ownerID := uuid.New()

	allowed, err := CanView(context.Background(), db, ViewInput{
		OwnerActorID:  ownerID,
		ViewerActorID: uuid.NullUUID{UUID: ownerID, Valid: true},
		Visibility:    "private",
	})
	if err != nil {
		t.Fatalf("CanView: %v", err)
	}
	if !allowed {
		t.Fatal("owner should be able to view private object")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCanViewAllowsFollowersObjectForAcceptedFollower(t *testing.T) {
	db, mock := newMockDB(t)
	ownerID := uuid.New()
	viewerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(followerExistsSQL)).
		WithArgs(viewerID, ownerID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	allowed, err := CanView(context.Background(), db, ViewInput{
		OwnerActorID:  ownerID,
		ViewerActorID: uuid.NullUUID{UUID: viewerID, Valid: true},
		Visibility:    "followers",
	})
	if err != nil {
		t.Fatalf("CanView: %v", err)
	}
	if !allowed {
		t.Fatal("accepted follower should be able to view followers-only object")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCanViewRejectsFollowersObjectForAnonymousViewer(t *testing.T) {
	db, mock := newMockDB(t)
	ownerID := uuid.New()

	allowed, err := CanView(context.Background(), db, ViewInput{
		OwnerActorID: ownerID,
		Visibility:   "followers",
	})
	if err != nil {
		t.Fatalf("CanView: %v", err)
	}
	if allowed {
		t.Fatal("anonymous viewer should not see followers-only object")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCanViewAllowsFriendsObjectForAcceptedFriend(t *testing.T) {
	db, mock := newMockDB(t)
	ownerID := uuid.New()
	viewerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(friendExistsSQL)).
		WithArgs(viewerID, ownerID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	allowed, err := CanView(context.Background(), db, ViewInput{
		OwnerActorID:  ownerID,
		ViewerActorID: uuid.NullUUID{UUID: viewerID, Valid: true},
		Visibility:    "friends",
	})
	if err != nil {
		t.Fatalf("CanView: %v", err)
	}
	if !allowed {
		t.Fatal("accepted friend should be able to view friends-only object")
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
