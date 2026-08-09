package instances

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestOpaqueTokenUsesPrefixAndStableHash(t *testing.T) {
	token, err := newOpaqueToken(joinTicketPrefix)
	if err != nil {
		t.Fatalf("newOpaqueToken: %v", err)
	}
	if !strings.HasPrefix(token, joinTicketPrefix) || len(token) < 40 {
		t.Fatalf("unexpected token shape: %q", token)
	}
	if hashOpaqueToken(token) == token || len(hashOpaqueToken(token)) != 64 {
		t.Fatal("token hash must be a 64-character SHA-256 hex value")
	}
}

func TestIssueJoinTicketReturnsSecretOnce(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	instanceID := uuid.New()
	worldID := uuid.New()
	hostID := uuid.New()
	ticketID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(joinInstanceSelectSQL)).
		WithArgs(instanceID).
		WillReturnRows(joinInstanceRows().AddRow(instanceID, worldID, hostID, "public", 16, 0, "active", nil))
	mock.ExpectQuery(regexp.QuoteMeta(joinedMemberExistsSQL)).
		WithArgs(instanceID, testPrincipal.ActorID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("UPDATE instance_join_tickets").
		WithArgs(testPrincipal.ActorID, instanceID).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(insertJoinTicketSQL)).
		WithArgs(sqlmock.AnyArg(), testPrincipal.ActorID, instanceID, "friends", true, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(ticketID))
	mock.ExpectCommit()
	mock.ExpectQuery(regexp.QuoteMeta(loadInstanceSQL)).
		WithArgs(instanceID).
		WillReturnRows(instanceRows().AddRow(instanceID, worldID, hostID, "instance-key", "World", "public", "basis://join", 16, 0, "active", nil, []byte(`{}`)))

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID.String()+"/join-tickets", strings.NewReader(`{"presenceVisibility":"friends","showExactInstance":true}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var response JoinTicketResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(response.Ticket, joinTicketPrefix) {
		t.Fatalf("ticket = %q", response.Ticket)
	}
	if time.Until(response.ExpiresAt) <= 0 || time.Until(response.ExpiresAt) > joinTicketTTL+time.Second {
		t.Fatalf("expiresAt = %v", response.ExpiresAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeJoinTicketRequiresWorldServerCredential(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	req := httptest.NewRequest(http.MethodPost, "/api/service/instance-join-tickets/consume", strings.NewReader(`{"ticket":"bvr_jt_test"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeJoinTicketCreatesMembershipPresenceAndConsumesTicket(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	credentialID := uuid.New()
	ticketID := uuid.New()
	instanceID := uuid.New()
	worldID := uuid.New()
	hostID := uuid.New()
	actorID := testPrincipal.ActorID
	token := "bvr_jt_test-success"
	serviceToken := "bvr_ws_test-service"
	expiresAt := time.Now().UTC().Add(time.Minute)

	mock.ExpectQuery(regexp.QuoteMeta(authenticateWorldServerSQL)).
		WithArgs(hashOpaqueToken(serviceToken)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name", "allowed_world_id"}).AddRow(credentialID, "world-server", worldID))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(loadJoinTicketForConsumeSQL)).
		WithArgs(hashOpaqueToken(token)).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "actor_id", "instance_id", "presence_visibility", "show_exact_instance",
			"metadata", "expires_at", "consumed_at", "acct", "status",
		}).AddRow(ticketID, actorID, instanceID, "friends", true, []byte(`{"client":"test"}`), expiresAt, nil, "alice@example.test", "active"))
	mock.ExpectQuery(regexp.QuoteMeta(lockActorSQL)).
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(actorID))
	mock.ExpectQuery(regexp.QuoteMeta(joinInstanceSelectSQL)).
		WithArgs(instanceID).
		WillReturnRows(joinInstanceRows().AddRow(instanceID, worldID, hostID, "public", 16, 0, "active", nil))
	mock.ExpectExec(regexp.QuoteMeta(claimRuntimeInstanceSQL)).
		WithArgs(instanceID, credentialID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta(joinedMemberExistsSQL)).
		WithArgs(instanceID, actorID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(leaveOtherInstancesSQL)).
		WithArgs(actorID, instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"instance_id"}))
	mock.ExpectExec("UPDATE instance_members").
		WithArgs(instanceID, actorID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO instance_members").
		WithArgs(instanceID, actorID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE instances SET current_users").
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO presence_sessions").
		WithArgs(actorID, worldID, instanceID, "friends", true, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE instance_join_tickets").
		WithArgs(ticketID, credentialID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO instance_join_audit").
		WithArgs(sqlmock.AnyArg(), credentialID, sqlmock.AnyArg(), sqlmock.AnyArg(), "accepted", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(regexp.QuoteMeta(loadInstanceSQL)).
		WithArgs(instanceID).
		WillReturnRows(instanceRows().AddRow(instanceID, worldID, hostID, "instance-key", "World", "public", "basis://join", 16, 1, "active", nil, []byte(`{}`)))

	req := httptest.NewRequest(http.MethodPost, "/api/service/instance-join-tickets/consume", strings.NewReader(`{"ticket":"`+token+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Basis-Service-Token", serviceToken)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"state":"joined"`) {
		t.Fatalf("body = %s", res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateWorldServerCredentialReturnsSecretButStoresHash(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	credentialID := uuid.New()
	worldID := uuid.New()
	now := time.Now().UTC()

	mock.ExpectQuery("SELECT role FROM users").
		WithArgs(testPrincipal.UserID).
		WillReturnRows(sqlmock.NewRows([]string{"role"}).AddRow("admin"))
	mock.ExpectQuery("INSERT INTO world_server_credentials").
		WithArgs("eu-world-host", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), testPrincipal.UserID, sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "name", "token_prefix", "allowed_world_id", "status", "metadata",
			"last_used_at", "revoked_at", "created_at",
		}).AddRow(credentialID, "eu-world-host", "bvr_ws_visible", worldID, "active", []byte(`{}`), nil, nil, now))

	req := httptest.NewRequest(http.MethodPost, "/api/admin/world-server-credentials", strings.NewReader(`{"name":"eu-world-host","allowedWorldId":"`+worldID.String()+`"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var response WorldServerCredentialResponse
	if err := json.Unmarshal(res.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(response.Token, serverCredentialPrefix) {
		t.Fatalf("token = %q", response.Token)
	}
	if response.ID != credentialID || response.AllowedWorldID == nil || *response.AllowedWorldID != worldID {
		t.Fatalf("response = %+v", response)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
