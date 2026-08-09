package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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
	cfg.Auth.JWTSecret = "integration-test-secret-with-sufficient-length"
	cfg.Redis.URL = ""
	cfg.Observability.MetricsEnabled = false
	cfg.Observability.JSONLogsEnabled = false
	cfg.Security.RateLimitEnabled = false
	cfg.Presence.SweepInterval = time.Hour
	router := api.NewRouter(api.Deps{DB: db, Config: cfg, Context: ctx})

	suffix := uuid.NewString()[:8]
	register := requestJSON(t, router, http.MethodPost, "/api/v1/auth/register", "", "", map[string]any{
		"email": "integration-" + suffix + "@example.test", "username": "integration-" + suffix,
		"password": "integration-password", "displayName": "Integration User",
	}, http.StatusCreated)
	var session struct {
		AccessToken string `json:"accessToken"`
		User        struct {
			ID      uuid.UUID `json:"id"`
			ActorID uuid.UUID `json:"actorId"`
		} `json:"user"`
	}
	decodeResponse(t, register, &session)
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

	eventBody := requestJSON(t, router, http.MethodPost, "/api/v1/events", session.AccessToken, "", map[string]any{
		"worldId": world.ID, "name": "Integration Event " + suffix, "slug": "integration-event-" + suffix,
		"startTime": time.Now().UTC().Add(time.Hour), "endTime": time.Now().UTC().Add(2 * time.Hour),
		"visibility": "public",
	}, http.StatusCreated)
	var event struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, eventBody, &event)
	eventPage := requestJSON(t, router, http.MethodGet, "/api/v1/events?limit=1", "", "", nil, http.StatusOK)
	assertPageContainsID(t, eventPage, event.ID)

	instanceBody := requestJSON(t, router, http.MethodPost, "/api/v1/instances", session.AccessToken, "", map[string]any{
		"worldId": world.ID, "name": "Integration Instance", "visibility": "public", "capacity": 8,
	}, http.StatusCreated)
	var instance struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, instanceBody, &instance)

	credentialBody := requestJSON(t, router, http.MethodPost, "/api/v1/admin/world-server-credentials", session.AccessToken, "", map[string]any{
		"name": "integration-host-" + suffix, "allowedWorldId": world.ID,
	}, http.StatusCreated)
	var credential struct {
		ID    uuid.UUID `json:"id"`
		Token string    `json:"token"`
	}
	decodeResponse(t, credentialBody, &credential)

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
	requestJSON(t, router, http.MethodPost,
		"/api/v1/service/instances/"+instance.ID.String()+"/members/"+session.User.ActorID.String()+"/heartbeat",
		"", credential.Token, map[string]any{
			"status": "online", "presenceVisibility": "friends", "showExactInstance": true,
		}, http.StatusOK)
	requestJSON(t, router, http.MethodDelete,
		"/api/v1/service/instances/"+instance.ID.String()+"/members/"+session.User.ActorID.String(),
		"", credential.Token, nil, http.StatusNoContent)

	assertRuntimeState(t, db, instance.ID, session.User.ActorID, credential.ID)
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
	if len(response.Data) != 1 || response.Data[0].ID != wantID || response.Pagination.Limit != 1 {
		t.Fatalf("unexpected page: %+v, want id %s", response, wantID)
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
