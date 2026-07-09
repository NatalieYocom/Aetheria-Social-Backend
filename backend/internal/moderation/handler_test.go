package moderation

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/auth"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestCreateReportPersistsReporterAndTarget(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, testPrincipal)
	targetID := uuid.New()
	reportID := uuid.New()
	createdAt := time.Now().UTC()

	mock.ExpectQuery("INSERT INTO reports").
		WithArgs(testPrincipal.ActorID, targetID, sqlmock.AnyArg(), "spam").
		WillReturnRows(reportRows().AddRow(reportID, testPrincipal.ActorID, targetID, nil, "spam", "open", createdAt))

	req := httptest.NewRequest(http.MethodPost, "/api/reports", strings.NewReader(`{"targetActorId":"`+targetID.String()+`","reason":"spam"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body ReportResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ID != reportID || body.State != "open" || body.Reason != "spam" {
		t.Fatalf("body = %+v", body)
	}
	if body.TargetActorID == nil || *body.TargetActorID != targetID {
		t.Fatalf("TargetActorID = %v", body.TargetActorID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateReportRequiresTarget(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, testPrincipal)

	req := httptest.NewRequest(http.MethodPost, "/api/reports", strings.NewReader(`{"reason":"spam"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestModerationReportsRequiresModeratorRole(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, testPrincipal)

	mock.ExpectQuery(regexp.QuoteMeta(currentUserRoleSQL)).
		WithArgs(testPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("user"))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/moderation/reports", nil))

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestModeratorListsOpenReports(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, moderatorPrincipal)
	reportID := uuid.New()
	reporterID := uuid.New()
	targetID := uuid.New()
	createdAt := time.Now().UTC()

	mock.ExpectQuery(regexp.QuoteMeta(currentUserRoleSQL)).
		WithArgs(moderatorPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("moderator"))
	mock.ExpectQuery("FROM reports r").
		WithArgs("open", 50).
		WillReturnRows(reportRows().AddRow(reportID, reporterID, targetID, nil, "abuse", "open", createdAt))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/moderation/reports?state=open", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body []ReportResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != 1 || body[0].ID != reportID {
		t.Fatalf("body = %+v", body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestModeratorUpsertsDomainBlock(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, adminPrincipal)
	blockID := uuid.New()
	createdAt := time.Now().UTC()

	mock.ExpectQuery(regexp.QuoteMeta(currentUserRoleSQL)).
		WithArgs(adminPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectQuery("INSERT INTO domain_blocks").
		WithArgs("bad.example", "suspend", "spam farm").
		WillReturnRows(domainBlockRows().AddRow(blockID, "bad.example", "suspend", "spam farm", createdAt))

	req := httptest.NewRequest(http.MethodPost, "/api/moderation/domain-blocks", strings.NewReader(`{"domain":"Bad.Example","severity":"suspend","reason":"spam farm"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body DomainBlockResponse
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Domain != "bad.example" || body.Severity != "suspend" {
		t.Fatalf("body = %+v", body)
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

var moderatorPrincipal = auth.Principal{
	UserID:   uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc"),
	ActorID:  uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd"),
	Username: "mod",
}

var adminPrincipal = auth.Principal{
	UserID:   uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"),
	ActorID:  uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"),
	Username: "admin",
}

func newTestRouter(db *sql.DB, principal auth.Principal) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandler(db), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), principal)))
		})
	})
	return r
}

func reportRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "reporter_actor_id", "target_actor_id", "target_object_uri", "reason", "state", "created_at"})
}

func domainBlockRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "domain", "severity", "reason", "created_at"})
}
