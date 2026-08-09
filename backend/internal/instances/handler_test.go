package instances

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

func TestJoinPublicInstanceCreatesMemberUpdatesCounterAndPresence(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	instanceID := uuid.New()
	worldID := uuid.New()
	hostID := uuid.New()
	previousInstanceID := uuid.New()
	actorID := testPrincipal.ActorID

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockActorSQL)).
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(actorID))
	mock.ExpectQuery(regexp.QuoteMeta(joinInstanceSelectSQL)).
		WithArgs(instanceID).
		WillReturnRows(joinInstanceRows().AddRow(instanceID, worldID, hostID, "public", 8, 0, "active", nil))
	mock.ExpectQuery(regexp.QuoteMeta(joinedMemberExistsSQL)).
		WithArgs(instanceID, actorID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(regexp.QuoteMeta(leaveOtherInstancesSQL)).
		WithArgs(actorID, instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"instance_id"}).AddRow(previousInstanceID))
	mock.ExpectExec(regexp.QuoteMeta(decrementPreviousInstanceUsersSQL)).
		WithArgs(previousInstanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE instance_members").
		WithArgs(instanceID, actorID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("INSERT INTO instance_members").
		WithArgs(instanceID, actorID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("UPDATE instances SET current_users = current_users").
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO presence_sessions").
		WithArgs(actorID, worldID, instanceID, "online", "friends", false, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()
	mock.ExpectQuery(regexp.QuoteMeta(loadInstanceSQL)).
		WithArgs(instanceID).
		WillReturnRows(instanceRows().AddRow(instanceID, worldID, hostID, "inst-key", "Public Instance", "public", "basis://join/inst-key", 8, 1, "active", nil, []byte(`{}`)))

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID.String()+"/join", strings.NewReader(`{"presenceVisibility":"friends"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestJoinFullInstanceRejectsNewParticipant(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	instanceID := uuid.New()
	worldID := uuid.New()
	hostID := uuid.New()
	actorID := testPrincipal.ActorID

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lockActorSQL)).
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(actorID))
	mock.ExpectQuery(regexp.QuoteMeta(joinInstanceSelectSQL)).
		WithArgs(instanceID).
		WillReturnRows(joinInstanceRows().AddRow(instanceID, worldID, hostID, "public", 1, 1, "active", nil))
	mock.ExpectQuery(regexp.QuoteMeta(joinedMemberExistsSQL)).
		WithArgs(instanceID, actorID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID.String()+"/join", strings.NewReader(`{}`))
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

func TestLeaveInstanceMarksMemberLeftAndClearsPresence(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	instanceID := uuid.New()
	actorID := testPrincipal.ActorID

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE instance_members").
		WithArgs(instanceID, actorID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE instances SET current_users = GREATEST").
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("DELETE FROM presence_sessions").
		WithArgs(actorID, instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID.String()+"/leave", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatExtendsJoinedMemberAndPresence(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	instanceID := uuid.New()
	worldID := uuid.New()
	actorID := testPrincipal.ActorID

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(heartbeatInstanceSelectSQL)).
		WithArgs(instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"world_id", "status", "expires_at"}).
			AddRow(worldID, "active", nil))
	mock.ExpectExec("UPDATE instance_members").
		WithArgs(instanceID, actorID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO presence_sessions").
		WithArgs(actorID, worldID, instanceID, "online", "friends", true, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID.String()+"/heartbeat", strings.NewReader(`{"presenceVisibility":"friends","showExactInstance":true}`))
	req.Header.Set("Content-Type", "application/json")
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

func TestHeartbeatRejectsActorThatHasNotJoined(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	instanceID := uuid.New()
	worldID := uuid.New()
	actorID := testPrincipal.ActorID

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(heartbeatInstanceSelectSQL)).
		WithArgs(instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"world_id", "status", "expires_at"}).
			AddRow(worldID, "active", nil))
	mock.ExpectExec("UPDATE instance_members").
		WithArgs(instanceID, actorID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	req := httptest.NewRequest(http.MethodPost, "/api/instances/"+instanceID.String()+"/heartbeat", strings.NewReader(`{}`))
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

func TestGetPrivateInstanceWithoutViewerReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	instanceID := uuid.New()
	worldID := uuid.New()
	hostID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(loadInstanceSQL)).
		WithArgs(instanceID).
		WillReturnRows(instanceRows().AddRow(instanceID, worldID, hostID, "private-key", "Private Instance", "private", "basis://join/private", 8, 0, "active", nil, []byte(`{}`)))

	req := httptest.NewRequest(http.MethodGet, "/api/instances/"+instanceID.String(), nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListByWorldAppliesVisibilityBeforeCursorPagination(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	worldID := uuid.New()
	hostID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()
	createdAt := time.Now().UTC().Truncate(time.Microsecond)

	mock.ExpectQuery(regexp.QuoteMeta(worldAccessTargetSQL)).
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_actor_id", "visibility"}).AddRow(hostID, "public"))
	mock.ExpectQuery(regexp.QuoteMeta(listWorldInstancesSQL)).
		WithArgs(worldID, nil, nil, uuid.Nil, 2).
		WillReturnRows(listInstanceRows().
			AddRow(firstID, worldID, hostID, "public-one", "Public One", "public", "basis://join/one", 8, 0, "active", nil, []byte(`{}`), createdAt).
			AddRow(secondID, worldID, hostID, "public-two", "Public Two", "public", "basis://join/two", 8, 0, "active", nil, []byte(`{}`), createdAt.Add(-time.Second)))

	req := httptest.NewRequest(http.MethodGet, "/api/worlds/"+worldID.String()+"/instances?limit=1", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body page.Response[InstanceResponse]
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Data) != 1 || body.Data[0].ID != firstID || body.Pagination.NextCursor == nil {
		t.Fatalf("instances = %+v", body)
	}
	cursor, err := page.Decode(*body.Pagination.NextCursor)
	if err != nil || cursor.ID != firstID || !cursor.SortTime.Equal(createdAt) {
		t.Fatalf("cursor = %+v, err = %v", cursor, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestListByWorldDoesNotExposeInstancesFromHiddenWorld(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	worldID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(worldAccessTargetSQL)).
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_actor_id", "visibility"}).AddRow(uuid.New(), "private"))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/worlds/"+worldID.String()+"/instances", nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateInstanceInPrivateWorldWithoutAccessReturnsNotFound(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	worldID := uuid.New()
	ownerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(worldAccessTargetSQL)).
		WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{"owner_actor_id", "visibility"}).AddRow(ownerID, "private"))

	req := httptest.NewRequest(http.MethodPost, "/api/instances", strings.NewReader(`{"worldId":"`+worldID.String()+`","name":"Hidden","visibility":"private"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateInstanceRejectsInvalidVisibility(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	worldID := uuid.New()

	req := httptest.NewRequest(http.MethodPost, "/api/instances", strings.NewReader(`{"worldId":"`+worldID.String()+`","visibility":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), `"code":"invalid_visibility"`) {
		t.Fatalf("body = %s", res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseInstanceClearsMembersPresenceAndCounter(t *testing.T) {
	db, mock := newMockDB(t)
	broker := realtime.NewBroker(realtime.BrokerConfig{BufferSize: 4})
	router := newTestRouterWithBroker(db, broker)
	instanceID := uuid.New()
	memberID := uuid.New()
	events, unsubscribe := broker.Subscribe(context.Background(), memberID)
	defer unsubscribe()

	mock.ExpectQuery("SELECT EXISTS.*FROM instances").
		WithArgs(instanceID, testPrincipal.ActorID).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE instances").
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE instance_members").
		WithArgs(instanceID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("DELETE FROM presence_sessions").
		WithArgs(instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id", "visibility"}).AddRow(memberID, "nobody"))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT host_actor_id AS actor_id").
		WithArgs(instanceID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_id"}))

	req := httptest.NewRequest(http.MethodDelete, "/api/instances/"+instanceID.String(), nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	select {
	case event := <-events:
		if event.Type != "presence.removed" {
			t.Fatalf("event.Type = %q", event.Type)
		}
	default:
		t.Fatal("expected presence.removed event")
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

func newTestRouter(db *sql.DB) http.Handler {
	return newTestRouterWithBroker(db, nil)
}

func newTestRouterWithBroker(db *sql.DB, broker *realtime.Broker) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandler(db, broker), func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), testPrincipal)))
		})
	})
	return r
}

func joinInstanceRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "world_id", "host_actor_id", "visibility", "capacity", "current_users", "status", "expires_at"})
}

func instanceRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "world_id", "host_actor_id", "instance_key", "name", "visibility", "launch_url", "capacity", "current_users", "status", "expires_at", "metadata"})
}

func listInstanceRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{"id", "world_id", "host_actor_id", "instance_key", "name", "visibility", "launch_url", "capacity", "current_users", "status", "expires_at", "metadata", "created_at"})
}
