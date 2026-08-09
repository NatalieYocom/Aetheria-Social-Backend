package retention

import (
	"context"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/DATA-DOG/go-sqlmock"
)

func TestRunOnceDeletesBoundedRetentionBatches(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for range 5 {
		mock.ExpectExec("DELETE FROM").WithArgs(sqlmock.AnyArg(), 250).
			WillReturnResult(sqlmock.NewResult(0, 2))
	}
	service := NewService(db, config.RetentionConfig{
		BatchSize: 250, InboxAge: time.Hour, OutboxAge: time.Hour,
		NotificationsAge: time.Hour, InvitesAge: time.Hour, InboundActivityAge: time.Hour,
	})
	deleted, err := service.RunOnce(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 10 {
		t.Fatalf("deleted = %d", deleted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
