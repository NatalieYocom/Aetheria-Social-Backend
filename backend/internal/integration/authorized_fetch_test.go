package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"basisvr-social-service/internal/activitypub"
	"basisvr-social-service/internal/activitypub/messagesig"
	"basisvr-social-service/internal/api"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"

	"github.com/google/uuid"
)

func TestAuthorizedFetchReturnsFollowersWorldOnlyToSignedFollower(t *testing.T) {
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
	cfg.ActivityPub.ActorKeyEncryptionKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	cfg.Database.URL = databaseURL
	cfg.Server.PublicURL = "https://social.integration.test"
	cfg.ActivityPub.Domain = "social.integration.test"
	cfg.ActivityPub.AuthorizedFetch = "protected"
	cfg.Auth.JWTSecret = "integration-test-secret-with-sufficient-length"
	cfg.Observability.MetricsEnabled = false
	cfg.Observability.JSONLogsEnabled = false
	cfg.Security.RateLimitEnabled = false
	router := api.NewRouter(api.Deps{DB: db, Config: cfg, Context: ctx})

	suffix := uuid.NewString()[:8]
	owner := registerIntegrationUser(t, router, "fetch-owner-"+suffix)
	worldBody := requestJSON(t, router, http.MethodPost, "/api/v1/worlds", owner.AccessToken, "", map[string]any{
		"name": "Followers World " + suffix, "slug": "followers-world-" + suffix,
		"visibility": "followers", "capacity": 8,
	}, http.StatusCreated)
	var world struct {
		ID uuid.UUID `json:"id"`
	}
	decodeResponse(t, worldBody, &world)

	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	remoteActorURI := "https://remote-" + suffix + ".example/users/bob"
	var remoteActorID uuid.UUID
	err = db.QueryRowContext(ctx, `
INSERT INTO actors (
  actor_uri, acct, type, preferred_username, display_name, domain,
  inbox_url, outbox_url, followers_url, following_url, public_key_pem, is_local
)
VALUES ($1, $2, 'Person', 'bob', 'Bob', $3, $4, $5, $6, $7, $8, false)
RETURNING id`, remoteActorURI, "bob@remote-"+suffix+".example", "remote-"+suffix+".example",
		remoteActorURI+"/inbox", remoteActorURI+"/outbox", remoteActorURI+"/followers", remoteActorURI+"/following",
		keyPair.PublicKeyPEM).Scan(&remoteActorID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO relationships (actor_id, target_actor_id, type, direction, state)
VALUES ($1, $2, 'follow', 'incoming', 'accepted')`, owner.User.ActorID, remoteActorID); err != nil {
		t.Fatal(err)
	}

	target := "https://social.integration.test/objects/" + world.ID.String()
	signed := httptest.NewRequest(http.MethodGet, target, nil)
	if err := messagesig.SignRequestPEM(signed, nil, remoteActorURI+"#main-key", keyPair.PrivateKeyPEM, time.Now()); err != nil {
		t.Fatal(err)
	}
	signedResponse := httptest.NewRecorder()
	router.ServeHTTP(signedResponse, signed)
	if signedResponse.Code != http.StatusOK {
		t.Fatalf("signed fetch status=%d body=%s", signedResponse.Code, signedResponse.Body.String())
	}

	unsignedResponse := httptest.NewRecorder()
	router.ServeHTTP(unsignedResponse, httptest.NewRequest(http.MethodGet, target, nil))
	if unsignedResponse.Code != http.StatusNotFound {
		t.Fatalf("unsigned fetch status=%d body=%s", unsignedResponse.Code, unsignedResponse.Body.String())
	}
}
