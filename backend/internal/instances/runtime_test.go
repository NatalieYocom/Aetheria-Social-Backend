package instances

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestServiceInstanceHeartbeatClaimsAndRenewsLease(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	credentialID := uuid.New()
	instanceID := uuid.New()
	worldID := uuid.New()
	token := "bvr_ws_runtime-test"

	expectServiceAuthentication(mock, token, credentialID, worldID)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockRuntimeInstanceSQL)).
		WithArgs(instanceID).
		WillReturnRows(runtimeInstanceRows().AddRow(worldID, "active", time.Now().Add(-time.Minute), nil))
	mock.ExpectExec(regexp.QuoteMeta(claimRuntimeInstanceSQL)).
		WithArgs(instanceID, credentialID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE instances").
		WithArgs(instanceID, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/service/instances/"+instanceID.String()+"/heartbeat", strings.NewReader(`{"ttlSeconds":120}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Basis-Service-Token", token)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"state":"active"`) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceMemberHeartbeatRefreshesMembershipAndPresence(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	credentialID := uuid.New()
	instanceID := uuid.New()
	worldID := uuid.New()
	actorID := uuid.New()
	token := "bvr_ws_member-heartbeat"

	expectServiceAuthentication(mock, token, credentialID, worldID)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockRuntimeInstanceSQL)).
		WithArgs(instanceID).
		WillReturnRows(runtimeInstanceRows().AddRow(worldID, "active", time.Now().Add(time.Minute), credentialID))
	mock.ExpectExec(regexp.QuoteMeta(claimRuntimeInstanceSQL)).
		WithArgs(instanceID, credentialID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE instance_members").
		WithArgs(instanceID, actorID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO presence_sessions").
		WithArgs(actorID, worldID, instanceID, "online", "friends", true, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost,
		"/api/service/instances/"+instanceID.String()+"/members/"+actorID.String()+"/heartbeat",
		strings.NewReader(`{"status":"online","presenceVisibility":"friends","showExactInstance":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Basis-Service-Token", token)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), actorID.String()) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceMemberLeaveReleasesCapacity(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	credentialID := uuid.New()
	instanceID := uuid.New()
	worldID := uuid.New()
	actorID := uuid.New()
	token := "bvr_ws_member-leave"

	expectServiceAuthentication(mock, token, credentialID, worldID)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockRuntimeInstanceSQL)).
		WithArgs(instanceID).
		WillReturnRows(runtimeInstanceRows().AddRow(worldID, "active", time.Now().Add(time.Minute), credentialID))
	mock.ExpectExec(regexp.QuoteMeta(claimRuntimeInstanceSQL)).
		WithArgs(instanceID, credentialID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE instance_members").
		WithArgs(instanceID, actorID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(decrementRuntimeInstanceUsersSQL)).
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM presence_sessions").
		WithArgs(actorID, instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO instance_join_audit").
		WithArgs(credentialID, actorID, instanceID, "member_left", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodDelete,
		"/api/service/instances/"+instanceID.String()+"/members/"+actorID.String(), nil)
	req.Header.Set("X-Basis-Service-Token", token)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSweeperExpiresInstancesAndCleansAccessData(t *testing.T) {
	db, mock := newMockDB(t)
	instanceID := uuid.New()
	actorID := uuid.New()
	sweeper := NewRuntimeSweeper(db, nil, RuntimeSweeperConfig{BatchSize: 10})

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(selectExpiredRuntimeInstancesSQL)).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(instanceID))
	mock.ExpectQuery(regexp.QuoteMeta(removeExpiredInstancePresenceSQL)).
		WithArgs(instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id"}).AddRow(actorID))
	mock.ExpectExec(regexp.QuoteMeta(leaveExpiredInstanceMembersSQL)).
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(markRuntimeInstanceExpiredSQL)).
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(regexp.QuoteMeta(cleanupRuntimeTicketsSQL)).
		WithArgs(int64(24*time.Hour/time.Second), 10).
		WillReturnResult(sqlmock.NewResult(0, 3))
	mock.ExpectExec(regexp.QuoteMeta(cleanupRuntimeAuditSQL)).
		WithArgs(int64(90*24*time.Hour/time.Second), 10).
		WillReturnResult(sqlmock.NewResult(0, 2))

	count, err := sweeper.SweepExpiredInstances(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("SweepExpiredInstances count=%d err=%v", count, err)
	}
	cleaned, err := sweeper.CleanupAccessData(context.Background())
	if err != nil || cleaned != 5 {
		t.Fatalf("CleanupAccessData count=%d err=%v", cleaned, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectServiceAuthentication(mock sqlmock.Sqlmock, token string, credentialID, worldID uuid.UUID) {
	mock.ExpectQuery(regexp.QuoteMeta(authenticateWorldServerSQL)).
		WithArgs(hashOpaqueToken(token)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "allowed_world_id"}).AddRow(credentialID, "world-server", worldID))
}

func runtimeInstanceRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"world_id", "status", "expires_at", "world_server_credential_id"})
}
