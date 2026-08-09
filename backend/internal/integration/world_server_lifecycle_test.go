package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/api"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"

	"github.com/google/uuid"
)

func TestWorldServerLifecycle(t *testing.T) {
	databaseURL := os.Getenv("BASIS_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BASIS_INTEGRATION_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := database.Open(ctx, config.DatabaseConfig{URL: databaseURL})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if err := database.ApplyUp(ctx, db, migrationsPath(t)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	cfg := config.Load()
	cfg.Database.URL = databaseURL
	cfg.Server.PublicURL = "https://social.integration.test"
	cfg.ActivityPub.Domain = "social.integration.test"
	cfg.Auth.JWTSecret = "integration-test-secret-with-sufficient-length"
	cfg.Redis.URL = ""
	cfg.Observability.MetricsEnabled = false
	cfg.Observability.JSONLogsEnabled = false
	cfg.Security.RateLimitEnabled = false
	cfg.Presence.SweepInterval = time.Hour
	router := api.NewRouter(api.Deps{DB: db, Config: cfg, Context: ctx})

	suffix := uuid.NewString()[:8]
	session := registerIntegrationUser(t, router, "integration-"+suffix)
	if _, err := db.ExecContext(ctx, `UPDATE users SET role = 'admin' WHERE id = $1`, session.User.ID); err != nil {
		t.Fatalf("grant admin role: %v", err)
	}

	worldBody := requestJSON(t, router, http.MethodPost, "/api/v1/worlds", session.AccessToken, "", map[string]any{
		"name": "Integration World " + suffix, "slug": "integration-world-" + suffix,
		"visibility": "public", "capacity": 8,
	}, http.StatusCreated)
	var world struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, worldBody, &world)
	worldPage := requestJSON(t, router, http.MethodGet, "/api/v1/worlds?limit=1", "", "", nil, http.StatusOK)
	assertPageContainsID(t, worldPage, world.ID)

	peerOne := registerIntegrationUser(t, router, "page-"+suffix+"-one")
	peerTwo := registerIntegrationUser(t, router, "page-"+suffix+"-two")
	groupBody := requestJSON(t, router, http.MethodPost, "/api/v1/groups", session.AccessToken, "", map[string]any{
		"name": "World Creators " + suffix, "slug": "world-creators-" + suffix, "visibility": "private",
	}, http.StatusCreated)
	var group struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, groupBody, &group)
	groupActorBody := requestJSON(t, router, http.MethodGet, "/groups/world-creators-"+suffix, "", "", nil, http.StatusOK)
	var groupActor struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	decodeResponse(t, groupActorBody, &groupActor)
	if groupActor.Type != "Group" || !strings.HasSuffix(groupActor.ID, "/groups/world-creators-"+suffix) {
		t.Fatalf("group actor = %+v", groupActor)
	}
	requestJSON(t, router, http.MethodGet, "/.well-known/webfinger?resource="+url.QueryEscape("acct:world-creators-"+suffix+"@social.integration.test"), "", "", nil, http.StatusOK)
	requestJSON(t, router, http.MethodGet, "/groups/world-creators-"+suffix+"/followers", "", "", nil, http.StatusOK)
	requestJSON(t, router, http.MethodGet, "/api/v1/groups/world-creators-"+suffix, "", "", nil, http.StatusNotFound)
	joinBody := requestJSON(t, router, http.MethodPost, "/api/v1/groups/"+group.ID.String()+"/join", peerOne.AccessToken, "", nil, http.StatusOK)
	var membership struct {
		State string `json:"state"`
	}
	decodeResponse(t, joinBody, &membership)
	if membership.State != "pending" {
		t.Fatalf("private group membership state = %q, want pending", membership.State)
	}
	requestJSON(t, router, http.MethodPatch, "/api/v1/groups/"+group.ID.String()+"/members/"+peerOne.User.ActorID.String(),
		session.AccessToken, "", map[string]any{"state": "active"}, http.StatusNoContent)
	requestJSON(t, router, http.MethodGet, "/api/v1/groups/world-creators-"+suffix, peerOne.AccessToken, "", nil, http.StatusOK)
	requestJSON(t, router, http.MethodPost, "/api/v1/groups/"+group.ID.String()+"/worlds", session.AccessToken, "",
		map[string]any{"worldId": world.ID}, http.StatusNoContent)
	groupWorlds := requestJSON(t, router, http.MethodGet, "/api/v1/groups/"+group.ID.String()+"/worlds", peerOne.AccessToken, "", nil, http.StatusOK)
	var linkedWorlds []struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, groupWorlds, &linkedWorlds)
	if len(linkedWorlds) != 1 || linkedWorlds[0].ID != world.ID {
		t.Fatalf("linked group worlds = %+v", linkedWorlds)
	}
	requestJSON(t, router, http.MethodPatch, "/api/v1/groups/"+group.ID.String()+"/members/"+peerOne.User.ActorID.String(),
		session.AccessToken, "", map[string]any{"state": "banned"}, http.StatusNoContent)
	requestJSON(t, router, http.MethodPost, "/api/v1/groups/"+group.ID.String()+"/leave", peerOne.AccessToken, "", nil, http.StatusConflict)
	requestJSON(t, router, http.MethodPost, "/api/v1/groups/"+group.ID.String()+"/join", peerOne.AccessToken, "", nil, http.StatusForbidden)
	peerWorldBody := requestJSON(t, router, http.MethodPost, "/api/v1/worlds", peerTwo.AccessToken, "", map[string]any{
		"name": "Suspension World " + suffix, "slug": "suspension-world-" + suffix,
		"visibility": "public", "capacity": 4,
	}, http.StatusCreated)
	var peerWorld struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, peerWorldBody, &peerWorld)
	peerEventBody := requestJSON(t, router, http.MethodPost, "/api/v1/events", peerTwo.AccessToken, "", map[string]any{
		"worldId": peerWorld.ID, "name": "Suspension Event " + suffix, "slug": "suspension-event-" + suffix,
		"startTime": time.Now().UTC().Add(3 * time.Hour), "endTime": time.Now().UTC().Add(4 * time.Hour),
		"visibility": "public",
	}, http.StatusCreated)
	var peerEvent struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, peerEventBody, &peerEvent)
	searchPath := "/api/v1/search/users?q=" + url.QueryEscape("page-"+suffix) + "&limit=1"
	searchCursor := assertCursorPage(t, requestJSON(t, router, http.MethodGet, searchPath, "", "", nil, http.StatusOK))
	assertPageSize(t, requestJSON(t, router, http.MethodGet, searchPath+"&cursor="+url.QueryEscape(searchCursor), "", "", nil, http.StatusOK), 1)
	for _, actorID := range []uuid.UUID{peerOne.User.ActorID, peerTwo.User.ActorID} {
		requestJSON(t, router, http.MethodPost, "/api/v1/relationships/follow", session.AccessToken, "",
			map[string]any{"targetActorId": actorID}, http.StatusOK)
	}
	followingCursor := assertCursorPage(t, requestJSON(t, router, http.MethodGet, "/api/v1/following?limit=1", session.AccessToken, "", nil, http.StatusOK))
	assertPageSize(t, requestJSON(t, router, http.MethodGet, "/api/v1/following?limit=1&cursor="+url.QueryEscape(followingCursor), session.AccessToken, "", nil, http.StatusOK), 1)
	for range 2 {
		requestJSON(t, router, http.MethodPost, "/api/v1/invites", session.AccessToken, "", map[string]any{
			"toActorId": peerOne.User.ActorID, "worldId": world.ID,
		}, http.StatusCreated)
	}
	inviteCursor := assertCursorPage(t, requestJSON(t, router, http.MethodGet, "/api/v1/invites?limit=1", session.AccessToken, "", nil, http.StatusOK))
	assertPageSize(t, requestJSON(t, router, http.MethodGet, "/api/v1/invites?limit=1&cursor="+url.QueryEscape(inviteCursor), session.AccessToken, "", nil, http.StatusOK), 1)
	notificationCursor := assertCursorPage(t, requestJSON(t, router, http.MethodGet, "/api/v1/notifications?limit=1", peerOne.AccessToken, "", nil, http.StatusOK))
	assertPageSize(t, requestJSON(t, router, http.MethodGet, "/api/v1/notifications?limit=1&cursor="+url.QueryEscape(notificationCursor), peerOne.AccessToken, "", nil, http.StatusOK), 1)

	eventBody := requestJSON(t, router, http.MethodPost, "/api/v1/events", session.AccessToken, "", map[string]any{
		"worldId": world.ID, "name": "Integration Event " + suffix, "slug": "integration-event-" + suffix,
		"startTime": time.Now().UTC().Add(time.Hour), "endTime": time.Now().UTC().Add(2 * time.Hour),
		"visibility": "public",
	}, http.StatusCreated)
	var event struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, eventBody, &event)
	eventPage := requestJSON(t, router, http.MethodGet, "/api/v1/events?limit=100", "", "", nil, http.StatusOK)
	assertPageContainsID(t, eventPage, event.ID)

	instanceBody := requestJSON(t, router, http.MethodPost, "/api/v1/instances", session.AccessToken, "", map[string]any{
		"worldId": world.ID, "name": "Integration Instance", "visibility": "public", "capacity": 8,
	}, http.StatusCreated)
	var instance struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, instanceBody, &instance)
	requestJSON(t, router, http.MethodPost, "/api/v1/instances", session.AccessToken, "", map[string]any{
		"worldId": world.ID, "name": "Second Public Instance", "visibility": "public", "capacity": 8,
	}, http.StatusCreated)
	requestJSON(t, router, http.MethodPost, "/api/v1/instances", session.AccessToken, "", map[string]any{
		"worldId": world.ID, "name": "Hidden Instance", "visibility": "private", "capacity": 8,
	}, http.StatusCreated)
	instanceCursor := assertCursorPage(t, requestJSON(t, router, http.MethodGet,
		"/api/v1/worlds/"+world.ID.String()+"/instances?limit=1", "", "", nil, http.StatusOK))
	assertPageSize(t, requestJSON(t, router, http.MethodGet,
		"/api/v1/worlds/"+world.ID.String()+"/instances?limit=1&cursor="+url.QueryEscape(instanceCursor),
		"", "", nil, http.StatusOK), 1)

	credentialBody := requestJSON(t, router, http.MethodPost, "/api/v1/admin/world-server-credentials", session.AccessToken, "", map[string]any{
		"name": "integration-host-" + suffix, "allowedWorldId": world.ID,
	}, http.StatusCreated)
	var credential struct {
		ID    uuid.UUID `json:"id"`
		Token string    `json:"token"`
	}
	decodeResponse(t, credentialBody, &credential)
	requestJSON(t, router, http.MethodPost, "/api/v1/admin/world-server-credentials", session.AccessToken, "", map[string]any{
		"name": "integration-spare-host-" + suffix, "allowedWorldId": world.ID,
	}, http.StatusCreated)
	credentialCursor := assertCursorPage(t, requestJSON(t, router, http.MethodGet,
		"/api/v1/admin/world-server-credentials?limit=1", session.AccessToken, "", nil, http.StatusOK))
	assertPageSize(t, requestJSON(t, router, http.MethodGet,
		"/api/v1/admin/world-server-credentials?limit=1&cursor="+url.QueryEscape(credentialCursor),
		session.AccessToken, "", nil, http.StatusOK), 1)

	requestJSON(t, router, http.MethodPost, "/api/v1/service/instances/"+instance.ID.String()+"/heartbeat", "", credential.Token,
		map[string]any{"ttlSeconds": 120}, http.StatusOK)
	ticketBody := requestJSON(t, router, http.MethodPost, "/api/v1/instances/"+instance.ID.String()+"/join-tickets", session.AccessToken, "",
		map[string]any{"presenceVisibility": "friends", "showExactInstance": true}, http.StatusCreated)
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	decodeResponse(t, ticketBody, &ticket)
	requestJSON(t, router, http.MethodPost, "/api/v1/service/instance-join-tickets/consume", "", credential.Token,
		map[string]any{"ticket": ticket.Ticket}, http.StatusOK)
	secondTicketBody := requestJSON(t, router, http.MethodPost, "/api/v1/instances/"+instance.ID.String()+"/join-tickets", session.AccessToken, "",
		map[string]any{"presenceVisibility": "friends", "showExactInstance": true}, http.StatusCreated)
	decodeResponse(t, secondTicketBody, &ticket)
	requestJSON(t, router, http.MethodPost, "/api/v1/service/instance-join-tickets/consume", "", credential.Token,
		map[string]any{"ticket": ticket.Ticket}, http.StatusOK)
	auditCursor := assertCursorPage(t, requestJSON(t, router, http.MethodGet,
		"/api/v1/admin/instance-join-audit?limit=1", session.AccessToken, "", nil, http.StatusOK))
	assertPageSize(t, requestJSON(t, router, http.MethodGet,
		"/api/v1/admin/instance-join-audit?limit=1&cursor="+url.QueryEscape(auditCursor),
		session.AccessToken, "", nil, http.StatusOK), 1)
	requestJSON(t, router, http.MethodPost,
		"/api/v1/service/instances/"+instance.ID.String()+"/members/"+session.User.ActorID.String()+"/heartbeat",
		"", credential.Token, map[string]any{
			"status": "online", "presenceVisibility": "friends", "showExactInstance": true,
		}, http.StatusOK)
	requestJSON(t, router, http.MethodDelete,
		"/api/v1/service/instances/"+instance.ID.String()+"/members/"+session.User.ActorID.String(),
		"", credential.Token, nil, http.StatusNoContent)

	requestJSON(t, router, http.MethodPost, "/api/v1/moderation/users/"+peerTwo.User.ActorID.String()+"/actions",
		session.AccessToken, "", map[string]any{"action": "suspend", "reason": "integration test"}, http.StatusOK)
	requestJSON(t, router, http.MethodGet, "/api/v1/me", peerTwo.AccessToken, "", nil, http.StatusUnauthorized)
	requestJSON(t, router, http.MethodPost, "/api/v1/auth/refresh", "", "",
		map[string]any{"refreshToken": peerTwo.RefreshToken}, http.StatusUnauthorized)
	requestJSON(t, router, http.MethodGet, "/api/v1/users/page-"+suffix+"-two", "", "", nil, http.StatusNotFound)
	requestJSON(t, router, http.MethodGet, "/users/page-"+suffix+"-two", "", "", nil, http.StatusNotFound)
	requestJSON(t, router, http.MethodGet, "/api/v1/worlds/suspension-world-"+suffix, "", "", nil, http.StatusNotFound)
	requestJSON(t, router, http.MethodGet, "/api/v1/events/suspension-event-"+suffix, "", "", nil, http.StatusNotFound)
	requestJSON(t, router, http.MethodGet, "/api/v1/worlds/"+peerWorld.ID.String()+"/instances", "", "", nil, http.StatusNotFound)
	worldsAfterSuspension := requestJSON(t, router, http.MethodGet, "/api/v1/worlds?limit=100", "", "", nil, http.StatusOK)
	assertPageDoesNotContainID(t, worldsAfterSuspension, peerWorld.ID)
	eventsAfterSuspension := requestJSON(t, router, http.MethodGet, "/api/v1/events?limit=100", "", "", nil, http.StatusOK)
	assertPageDoesNotContainID(t, eventsAfterSuspension, peerEvent.ID)
	followingAfterSuspension := requestJSON(t, router, http.MethodGet, "/api/v1/following?limit=100", session.AccessToken, "", nil, http.StatusOK)
	assertActorPageDoesNotContainID(t, followingAfterSuspension, peerTwo.User.ActorID)
	requestJSON(t, router, http.MethodPost, "/api/v1/moderation/users/"+peerTwo.User.ActorID.String()+"/actions",
		session.AccessToken, "", map[string]any{"action": "restore", "reason": "integration restore"}, http.StatusOK)
	requestJSON(t, router, http.MethodGet, "/api/v1/me", peerTwo.AccessToken, "", nil, http.StatusUnauthorized)
	requestJSON(t, router, http.MethodPost, "/api/v1/auth/refresh", "", "",
		map[string]any{"refreshToken": peerTwo.RefreshToken}, http.StatusUnauthorized)
	loginBody := requestJSON(t, router, http.MethodPost, "/api/v1/auth/login", "", "", map[string]any{
		"login": "page-" + suffix + "-two", "password": "integration-password",
	}, http.StatusOK)
	var restoredSession integrationSession
	decodeResponse(t, loginBody, &restoredSession)
	requestJSON(t, router, http.MethodGet, "/api/v1/me", restoredSession.AccessToken, "", nil, http.StatusOK)
	requestJSON(t, router, http.MethodGet, "/users/page-"+suffix+"-two", "", "", nil, http.StatusOK)

	assertRuntimeState(t, db, instance.ID, session.User.ActorID, credential.ID)
}

func TestAccountExportAndDeletionLifecycle(t *testing.T) {
	databaseURL := os.Getenv("BASIS_INTEGRATION_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("BASIS_INTEGRATION_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db, err := database.Open(ctx, config.DatabaseConfig{URL: databaseURL})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.ApplyUp(ctx, db, migrationsPath(t)); err != nil {
		t.Fatal(err)
	}
	cfg := config.Load()
	cfg.Database.URL = databaseURL
	cfg.Server.PublicURL = "https://social.integration.test"
	cfg.ActivityPub.Domain = "social.integration.test"
	cfg.Auth.JWTSecret = "integration-test-secret-with-sufficient-length"
	cfg.Redis.URL = ""
	cfg.Observability.MetricsEnabled = false
	cfg.Observability.JSONLogsEnabled = false
	cfg.Security.RateLimitEnabled = false
	cfg.Presence.SweepInterval = time.Hour
	router := api.NewRouter(api.Deps{DB: db, Config: cfg, Context: ctx})

	suffix := uuid.NewString()[:8]
	session := registerIntegrationUser(t, router, "delete-"+suffix)
	worldBody := requestJSON(t, router, http.MethodPost, "/api/v1/worlds", session.AccessToken, "", map[string]any{
		"name": "Delete me", "slug": "delete-world-" + suffix, "visibility": "public", "capacity": 4,
	}, http.StatusCreated)
	var world struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, worldBody, &world)
	export := requestJSON(t, router, http.MethodGet, "/api/v1/me/export", session.AccessToken, "", nil, http.StatusOK)
	if !bytes.Contains(export, []byte(`"schemaVersion": 1`)) && !bytes.Contains(export, []byte(`"schemaVersion":1`)) {
		t.Fatalf("export missing schema version: %s", export)
	}
	requestJSON(t, router, http.MethodDelete, "/api/v1/me", session.AccessToken, "", map[string]any{
		"password": "integration-password",
	}, http.StatusNoContent)
	requestJSON(t, router, http.MethodGet, "/api/v1/me", session.AccessToken, "", nil, http.StatusUnauthorized)

	var status, email, displayName string
	if err := db.QueryRowContext(ctx, `
SELECT user_account.status, user_account.email, profile.display_name
FROM users user_account JOIN profiles profile ON profile.user_id = user_account.id
WHERE user_account.id = $1`, session.User.ID).Scan(&status, &email, &displayName); err != nil {
		t.Fatal(err)
	}
	if status != "deleted" || !strings.HasSuffix(email, "@deleted.invalid") || displayName != "Deleted user" {
		t.Fatalf("deleted account state = status:%s email:%s display:%s", status, email, displayName)
	}
	var worldCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM worlds WHERE id = $1`, world.ID).Scan(&worldCount); err != nil {
		t.Fatal(err)
	}
	if worldCount != 0 {
		t.Fatalf("owned world remained after deletion: %d", worldCount)
	}
}

type integrationSession struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	User         struct {
		ID      uuid.UUID `json:"id"`
		ActorID uuid.UUID `json:"actorId"`
	} `json:"user"`
}

func registerIntegrationUser(t *testing.T, handler http.Handler, username string) integrationSession {
	t.Helper()
	body := requestJSON(t, handler, http.MethodPost, "/api/v1/auth/register", "", "", map[string]any{
		"email": username + "@example.test", "username": username,
		"password": "integration-password", "displayName": username,
	}, http.StatusCreated)
	var session integrationSession
	decodeResponse(t, body, &session)
	return session
}

func requestJSON(t *testing.T, handler http.Handler, method, path, bearerToken, serviceToken string, body any, wantStatus int) []byte {
	t.Helper()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal %s %s: %v", method, path, err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	if serviceToken != "" {
		req.Header.Set("X-Basis-Service-Token", serviceToken)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, res.Code, wantStatus, res.Body.String())
	}
	return res.Body.Bytes()
}

func decodeResponse(t *testing.T, payload []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(payload, target); err != nil {
		t.Fatalf("decode response %s: %v", payload, err)
	}
}

func assertPageContainsID(t *testing.T, payload []byte, wantID uuid.UUID) {
	t.Helper()
	var response struct {
		Data []struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
		Pagination struct {
			Limit int `json:"limit"`
		} `json:"pagination"`
	}
	decodeResponse(t, payload, &response)
	for _, item := range response.Data {
		if item.ID == wantID {
			return
		}
	}
	t.Fatalf("page does not contain id %s: %+v", wantID, response)
}

func assertPageDoesNotContainID(t *testing.T, payload []byte, unwantedID uuid.UUID) {
	t.Helper()
	var response struct {
		Data []struct {
			ID uuid.UUID `json:"id"`
		} `json:"data"`
	}
	decodeResponse(t, payload, &response)
	for _, item := range response.Data {
		if item.ID == unwantedID {
			t.Fatalf("page unexpectedly contains id %s", unwantedID)
		}
	}
}

func assertActorPageDoesNotContainID(t *testing.T, payload []byte, unwantedID uuid.UUID) {
	t.Helper()
	var response struct {
		Data []struct {
			ActorID uuid.UUID `json:"actorId"`
		} `json:"data"`
	}
	decodeResponse(t, payload, &response)
	for _, item := range response.Data {
		if item.ActorID == unwantedID {
			t.Fatalf("actor page unexpectedly contains id %s", unwantedID)
		}
	}
}

func assertCursorPage(t *testing.T, payload []byte) string {
	t.Helper()
	var response struct {
		Data       []json.RawMessage `json:"data"`
		Pagination struct {
			NextCursor *string `json:"nextCursor"`
			Limit      int     `json:"limit"`
		} `json:"pagination"`
	}
	decodeResponse(t, payload, &response)
	if len(response.Data) != 1 || response.Pagination.Limit != 1 || response.Pagination.NextCursor == nil {
		t.Fatalf("unexpected cursor page: %+v", response)
	}
	return *response.Pagination.NextCursor
}

func assertPageSize(t *testing.T, payload []byte, want int) {
	t.Helper()
	var response struct {
		Data []json.RawMessage `json:"data"`
	}
	decodeResponse(t, payload, &response)
	if len(response.Data) != want {
		t.Fatalf("page size = %d, want %d", len(response.Data), want)
	}
}

func assertRuntimeState(t *testing.T, db *sql.DB, instanceID, actorID, credentialID uuid.UUID) {
	t.Helper()
	var currentUsers int
	var ownerID uuid.NullUUID
	if err := db.QueryRow(`SELECT current_users, world_server_credential_id FROM instances WHERE id = $1`, instanceID).Scan(&currentUsers, &ownerID); err != nil {
		t.Fatalf("load instance state: %v", err)
	}
	if currentUsers != 0 || !ownerID.Valid || ownerID.UUID != credentialID {
		t.Fatalf("instance currentUsers=%d owner=%v, want 0/%s", currentUsers, ownerID, credentialID)
	}
	var memberState string
	if err := db.QueryRow(`SELECT state FROM instance_members WHERE instance_id = $1 AND actor_id = $2`, instanceID, actorID).Scan(&memberState); err != nil {
		t.Fatalf("load member state: %v", err)
	}
	if memberState != "left" {
		t.Fatalf("member state = %q", memberState)
	}
	var presenceCount int
	if err := db.QueryRow(`SELECT count(*) FROM presence_sessions WHERE actor_id = $1`, actorID).Scan(&presenceCount); err != nil {
		t.Fatalf("count presence: %v", err)
	}
	if presenceCount != 0 {
		t.Fatalf("presence count = %d", presenceCount)
	}
}

func migrationsPath(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate integration test source")
	}
	path := filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..", "migrations"))
	if stat, err := os.Stat(path); err != nil || !stat.IsDir() {
		t.Fatalf("migration directory %s is unavailable: %v", path, err)
	}
	return path
}
