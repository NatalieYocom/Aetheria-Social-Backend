package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"basisvr-social-service/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestNewRouterDoesNotPanicWhenMetricsAndOptionalAuthAreEnabled(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func(db *sql.DB) {
		_ = db.Close()
	}(db)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Load()
	cfg.Redis.URL = ""
	cfg.Observability.MetricsEnabled = true
	cfg.Observability.JSONLogsEnabled = false
	cfg.Presence.SweepInterval = time.Hour
	cfg.Security.RateLimitEnabled = false

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("NewRouter panicked: %v", recovered)
		}
	}()
	_ = NewRouter(Deps{DB: db, Config: cfg, Context: ctx})
}
