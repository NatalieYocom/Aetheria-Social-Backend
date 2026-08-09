package api

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
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

func TestVersionedAPIRoutesToLegacyHandlers(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Load()
	cfg.Redis.URL = ""
	cfg.Observability.MetricsEnabled = false
	cfg.Observability.JSONLogsEnabled = false
	cfg.Presence.SweepInterval = time.Hour
	cfg.Security.RateLimitEnabled = false

	router := NewRouter(Deps{DB: db, Config: cfg, Context: ctx})
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("X-Basis-API-Version"); got != currentAPIVersion {
		t.Fatalf("X-Basis-API-Version = %q", got)
	}
}
