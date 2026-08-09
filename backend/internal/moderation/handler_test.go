package moderation

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/common/page"
	"basisvr-social-service/internal/realtime"

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
		WithArgs("open", nil, uuid.Nil, 51).
		WillReturnRows(reportRows().AddRow(reportID, reporterID, targetID, nil, "abuse", "open", createdAt))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/moderation/reports?state=open", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body page.Response[ReportResponse]
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != reportID || body.Pagination.Limit != 50 {
		t.Fatalf("body = %+v", body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestModeratorListsDomainBlocksWithCursorEnvelope(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, moderatorPrincipal)
	blockID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(currentUserRoleSQL)).
		WithArgs(moderatorPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("moderator"))
	mock.ExpectQuery("FROM domain_blocks").
		WithArgs(nil, uuid.Nil, 101).
		WillReturnRows(domainBlockRows().AddRow(blockID, "blocked.example", "suspend", "abuse", time.Now().UTC()))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/moderation/domain-blocks", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body page.Response[DomainBlockResponse]
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != blockID || body.Pagination.Limit != 100 {
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

func TestModeratorSuspendsUserWithAuditAndPresenceCleanup(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 4})
	router := newTestRouterWithBroker(db, moderatorPrincipal, broker)
	targetUserID := uuid.New()
	targetActorID := uuid.New()
	actionID := uuid.New()
	targetEvents, unsubscribe := broker.Subscribe(context.Background(), targetActorID)
	defer unsubscribe()

	mock.ExpectQuery(regexp.QuoteMeta(currentUserRoleSQL)).
		WithArgs(moderatorPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("moderator"))
	mock.ExpectBegin()
	mock.ExpectQuery("FROM users u").WithArgs(targetActorID).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "actor_id", "role", "status"}).
			AddRow(targetUserID, targetActorID, "user", "active"))
	mock.ExpectExec("UPDATE users").WithArgs(targetUserID, "suspended").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM presence_sessions").WithArgs(targetActorID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("UPDATE instance_members").WithArgs(targetActorID).
		WillReturnRows(sqlmock.NewRows([]string{"instance_id"}))
	mock.ExpectExec("UPDATE instance_join_tickets").WithArgs(targetActorID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("INSERT INTO moderation_actions").
		WithArgs(moderatorPrincipal.UserID, targetUserID, targetActorID, "suspend", "abuse", sqlmock.AnyArg()).
		WillReturnRows(moderationActionRows().AddRow(actionID, moderatorPrincipal.UserID, targetUserID, targetActorID, "suspend", "abuse", nil, time.Now().UTC()))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/moderation/users/"+targetActorID.String()+"/actions", strings.NewReader(`{"action":"suspend","reason":"abuse"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	select {
	case event := <-targetEvents:
		if event.Type != "user.suspended" {
			t.Fatalf("event.Type = %q", event.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected user.suspended event")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListModerationActionsReturnsCursorEnvelope(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, moderatorPrincipal)
	createdAt := time.Now().UTC().Truncate(time.Microsecond)
	actionID := uuid.New()
	targetUserID := uuid.New()
	targetActorID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(currentUserRoleSQL)).
		WithArgs(moderatorPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("moderator"))
	mock.ExpectQuery("FROM moderation_actions").
		WithArgs(nil, uuid.Nil, 2).
		WillReturnRows(moderationActionRows().
			AddRow(actionID, moderatorPrincipal.UserID, targetUserID, targetActorID, "suspend", "abuse", nil, createdAt).
			AddRow(uuid.New(), moderatorPrincipal.UserID, targetUserID, targetActorID, "restore", "appeal accepted", nil, createdAt.Add(-time.Second)))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/moderation/actions?limit=1", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var response page.Response[ModerationActionResponse]
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 1 || response.Data[0].ID != actionID || response.Pagination.NextCursor == nil {
		t.Fatalf("response = %+v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepeatedModerationActionReturnsConflict(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, moderatorPrincipal)
	targetUserID := uuid.New()
	targetActorID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(currentUserRoleSQL)).
		WithArgs(moderatorPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("moderator"))
	mock.ExpectBegin()
	mock.ExpectQuery("FROM users u").WithArgs(targetActorID).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "actor_id", "role", "status"}).
			AddRow(targetUserID, targetActorID, "user", "suspended"))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/moderation/users/"+targetActorID.String()+"/actions", strings.NewReader(`{"action":"suspend","reason":"duplicate"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestModerationActionRejectsReportForAnotherActor(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db, moderatorPrincipal)
	targetUserID := uuid.New()
	targetActorID := uuid.New()
	reportID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(currentUserRoleSQL)).WithArgs(moderatorPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("moderator"))
	mock.ExpectBegin()
	mock.ExpectQuery("FROM users u").WithArgs(targetActorID).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "actor_id", "role", "status"}).
			AddRow(targetUserID, targetActorID, "user", "active"))
	mock.ExpectQuery("SELECT EXISTS.*FROM reports").WithArgs(reportID, targetActorID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/moderation/users/"+targetActorID.String()+"/actions",
		strings.NewReader(`{"action":"suspend","reason":"abuse","reportId":"`+reportID.String()+`"}`))
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
	return newTestRouterWithBroker(db, principal, nil)
}

func newTestRouterWithBroker(db *sql.DB, principal auth.Principal, broker *realtime.Broker) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandler(db, broker), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), principal)))
		})
	})
	return r
}

func moderationActionRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "moderator_user_id", "target_user_id", "target_actor_id", "action", "reason", "report_id", "created_at"})
}

func reportRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "reporter_actor_id", "target_actor_id", "target_object_uri", "reason", "state", "created_at"})
}

func domainBlockRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "domain", "severity", "reason", "created_at"})
}
