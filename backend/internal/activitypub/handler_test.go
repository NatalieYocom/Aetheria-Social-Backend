package activitypub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/activitypub/delivery"
	"basisvr-social-service/internal/activitypub/messagesig"
	"basisvr-social-service/internal/activitypub/resolver"
	"basisvr-social-service/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestWebFingerResolvesLocalActor(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)

	mock.ExpectQuery("SELECT (.+) FROM actors").
		WithArgs("alice@example.social").
		WillReturnRows(sqlmock.NewRows([]string{"acct", "actor_uri"}).
			AddRow("alice@example.social", "https://example.social/users/alice"))

	req := httptest.NewRequest(http.MethodGet, "/.well-known/webfinger?resource=acct:alice@example.social", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Header().Get("Content-Type"), "application/jrd+json") {
		t.Fatalf("Content-Type = %q", res.Header().Get("Content-Type"))
	}

	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["subject"] != "acct:alice@example.social" {
		t.Fatalf("subject = %v", body["subject"])
	}
	links := body["links"].([]any)
	link := links[0].(map[string]any)
	if link["href"] != "https://example.social/users/alice" {
		t.Fatalf("href = %v", link["href"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPersonActorEndpointReturnsActivityStreamsPerson(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	actorID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(actorByUsernameSelect())).
		WithArgs("alice").
		WillReturnRows(actorRows().
			AddRow(
				actorID,
				"https://example.social/users/alice",
				"alice@example.social",
				"alice",
				"Alice",
				"VR builder",
				"example.social",
				"https://example.social/users/alice/inbox",
				"https://example.social/users/alice/outbox",
				"https://example.social/users/alice/followers",
				"https://example.social/users/alice/following",
				"https://example.social/inbox",
				"public-key",
				"https://cdn.example.social/alice.png",
			))

	req := httptest.NewRequest(http.MethodGet, "/users/alice", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Header().Get("Content-Type"), "application/activity+json") {
		t.Fatalf("Content-Type = %q", res.Header().Get("Content-Type"))
	}

	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["type"] != "Person" {
		t.Fatalf("type = %v", body["type"])
	}
	if body["id"] != "https://example.social/users/alice" {
		t.Fatalf("id = %v", body["id"])
	}
	endpoints := body["endpoints"].(map[string]any)
	if endpoints["sharedInbox"] != "https://example.social/inbox" {
		t.Fatalf("sharedInbox = %v", endpoints["sharedInbox"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestServiceActorEndpointReturnsPublicServiceDocument(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	mock.ExpectQuery("SELECT actor_uri, acct, preferred_username, display_name").
		WithArgs("https://example.social/actor").
		WillReturnRows(sqlmock.NewRows([]string{
			"actor_uri", "acct", "preferred_username", "display_name", "domain", "inbox_url", "outbox_url",
			"followers_url", "following_url", "shared_inbox_url", "public_key_pem",
		}).AddRow(
			"https://example.social/actor", "instance@example.social", "instance", "BasisVR Social", "example.social",
			"https://example.social/actor/inbox", "https://example.social/actor/outbox",
			"https://example.social/actor/followers", "https://example.social/actor/following",
			"https://example.social/inbox", "public-key",
		))

	req := httptest.NewRequest(http.MethodGet, "/actor", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["type"] != "Service" || body["id"] != "https://example.social/actor" {
		t.Fatalf("body = %+v", body)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestStrictAuthorizedFetchRejectsUnsignedActorGET(t *testing.T) {
	db, _ := newMockDB(t)
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandlerWithResolver(db, config.Config{
		Server: config.ServerConfig{PublicURL: "https://example.social"},
		ActivityPub: config.ActivityPubConfig{
			Domain: "example.social", AuthorizedFetch: "all",
		},
	}, fakeResolver{err: errors.New("resolver should not be called without a signature")}))

	res := httptest.NewRecorder()
	r.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/users/alice", nil))

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if res.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("WWW-Authenticate header is missing")
	}
}

func TestFollowersCollectionReturnsOrderedCollection(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	actorID := uuid.New()
	followerID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(actorIDByUsernameSelect())).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_uri"}).
			AddRow(actorID, "https://example.social/users/alice"))
	mock.ExpectQuery("(?s)SELECT COUNT.*JOIN actors target.*LEFT JOIN users target_user").
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("(?s)SELECT target.actor_uri.*LEFT JOIN users target_user").
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri"}).
			AddRow("https://remote.example/users/bob"))
	_ = followerID

	req := httptest.NewRequest(http.MethodGet, "/users/alice/followers", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["type"] != "OrderedCollection" {
		t.Fatalf("type = %v", body["type"])
	}
	if body["totalItems"].(float64) != 1 {
		t.Fatalf("totalItems = %v", body["totalItems"])
	}
	items := body["orderedItems"].([]any)
	if items[0] != "https://remote.example/users/bob" {
		t.Fatalf("orderedItems = %#v", items)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOutboxReturnsStoredActivityObjects(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	actorID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(actorIDByUsernameSelect())).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_uri"}).
			AddRow(actorID, "https://example.social/users/alice"))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT raw_json").
		WithArgs(actorID).
		WillReturnRows(sqlmock.NewRows([]string{"raw_json"}).
			AddRow([]byte(`{"id":"https://example.social/activities/accept-1","type":"Accept"}`)))

	req := httptest.NewRequest(http.MethodGet, "/users/alice/outbox", nil)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	items := body["orderedItems"].([]any)
	item := items[0].(map[string]any)
	if item["type"] != "Accept" {
		t.Fatalf("orderedItems[0].type = %v", item["type"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestActivityEndpointReturnsStoredActivity(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	activityID := uuid.New()
	mock.ExpectQuery("SELECT raw_json").
		WithArgs(activityID).
		WillReturnRows(sqlmock.NewRows([]string{"raw_json"}).AddRow([]byte(`{"id":"https://example.social/activities/1","type":"Create"}`)))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/activities/"+activityID.String(), nil))

	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != activityJSONContentType {
		t.Fatalf("status = %d, content-type = %q, body = %s", res.Code, res.Header().Get("Content-Type"), res.Body.String())
	}
}

func TestObjectEndpointReturnsTombstoneForDeletedObject(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	objectID := uuid.New()
	mock.ExpectQuery("SELECT object_uri, type, raw_json, is_deleted").
		WithArgs(objectID).
		WillReturnRows(sqlmock.NewRows([]string{"object_uri", "type", "raw_json", "is_deleted"}).
			AddRow("https://remote.example/notes/1", "Note", []byte(`{"id":"https://remote.example/notes/1","type":"Note"}`), true))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/objects/"+objectID.String(), nil))

	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"type":"Tombstone"`) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestObjectEndpointDoesNotRehostNonPublicRemoteObject(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	objectID := uuid.New()
	mock.ExpectQuery("SELECT object_uri, type, raw_json, is_deleted").WithArgs(objectID).
		WillReturnRows(sqlmock.NewRows([]string{"object_uri", "type", "raw_json", "is_deleted"}).AddRow(
			"https://remote.example/notes/secret", "Note",
			[]byte(`{"id":"https://remote.example/notes/secret","type":"Note","to":"https://remote.example/users/bob/followers"}`), false,
		))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/objects/"+objectID.String(), nil))
	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestObjectEndpointReturnsPublicLocalWorld(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	worldID := uuid.New()
	ownerID := uuid.New()
	mock.ExpectQuery("SELECT object_uri, type, raw_json, is_deleted").WithArgs(worldID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("FROM worlds w").WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{
			"name", "description", "preview_url", "launch_url", "capacity", "owner_actor_id", "actor_uri", "visibility", "created_at", "updated_at",
		}).AddRow("Moon", "VR world", "https://cdn.example/moon.png", "basis://world/moon", 32,
			ownerID, "https://example.social/users/alice", "public", time.Now(), time.Now()))

	res := httptest.NewRecorder()
	router.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/objects/"+worldID.String(), nil))

	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"type":"Page"`) || !strings.Contains(res.Body.String(), `"basis:objectType":"World"`) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
}

func TestObjectEndpointReturnsFollowersWorldToSignedAcceptedFollower(t *testing.T) {
	db, mock := newMockDB(t)
	worldID := uuid.New()
	ownerID := uuid.New()
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	remoteActorURI := "https://remote.example/users/bob"
	router := newTestRouterWithResolver(db, fakeResolver{actor: resolver.RemoteActor{
		ActorURI: remoteActorURI, PublicKeyPEM: keyPair.PublicKeyPEM, Domain: "remote.example",
	}})
	mock.ExpectQuery("SELECT object_uri, type, raw_json, is_deleted").WithArgs(worldID).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("FROM worlds w").WithArgs(worldID).
		WillReturnRows(sqlmock.NewRows([]string{
			"name", "description", "preview_url", "launch_url", "capacity", "owner_actor_id", "actor_uri", "visibility", "created_at", "updated_at",
		}).AddRow("Followers Moon", "VR world", "", "basis://world/moon", 32, ownerID,
			"https://example.social/users/alice", "followers", time.Now(), time.Now()))
	mock.ExpectQuery(regexp.QuoteMeta(federationBlockedDomainExistsSQL)).WithArgs("remote.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery("SELECT actor_uri, domain, public_key_pem").WithArgs(remoteActorURI).
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri", "domain", "public_key_pem"}).
			AddRow(remoteActorURI, "remote.example", keyPair.PublicKeyPEM))
	mock.ExpectQuery("SELECT EXISTS").WithArgs(ownerID, remoteActorURI).
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	target := "https://example.social/objects/" + worldID.String()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if err := messagesig.SignRequest(req, nil, remoteActorURI+"#main-key", mustParseTestPrivateKey(t, keyPair.PrivateKeyPEM), time.Now()); err != nil {
		t.Fatal(err)
	}
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"name":"Followers Moon"`) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInboxStoresAcceptedActivityEnvelope(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	recipientID := uuid.New()

	mock.ExpectQuery(regexp.QuoteMeta(actorIDByUsernameSelect())).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_uri"}).
			AddRow(recipientID, "https://example.social/users/alice"))
	mock.ExpectQuery(regexp.QuoteMeta(federationBlockedDomainExistsSQL)).
		WithArgs("remote.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("INSERT INTO inbox_messages").
		WithArgs(recipientID, "https://remote.example/activities/flag-1", "Flag", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	req := httptest.NewRequest(
		http.MethodPost,
		"/users/alice/inbox",
		strings.NewReader(`{"id":"https://remote.example/activities/flag-1","type":"Flag","actor":"https://remote.example/users/bob"}`),
	)
	req.Header.Set("Content-Type", "application/activity+json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSharedInboxRoutesActivityToLocalRecipient(t *testing.T) {
	db, mock := newMockDB(t)
	router := newTestRouter(db)
	recipientID := uuid.New()
	localActorURI := "https://example.social/users/alice"

	mock.ExpectQuery("SELECT id, actor_uri").
		WithArgs(localActorURI).
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_uri"}).AddRow(recipientID, localActorURI))
	mock.ExpectQuery(regexp.QuoteMeta(federationBlockedDomainExistsSQL)).
		WithArgs("remote.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("INSERT INTO inbox_messages").
		WithArgs(recipientID, "https://remote.example/activities/flag-2", "Flag", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	req := httptest.NewRequest(http.MethodPost, "/inbox", strings.NewReader(
		`{"id":"https://remote.example/activities/flag-2","type":"Flag","actor":"https://remote.example/users/bob","to":["`+localActorURI+`"]}`,
	))
	req.Header.Set("Content-Type", "application/activity+json")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusAccepted || !strings.Contains(res.Body.String(), `"recipients":1`) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInboxRejectsBlockedActorDomainBeforeResolvingActor(t *testing.T) {
	db, mock := newMockDB(t)
	recipientID := uuid.New()
	remoteResolver := &recordingResolver{err: errors.New("resolver should not be called")}
	router := newTestRouterWithResolver(db, remoteResolver)

	mock.ExpectQuery(regexp.QuoteMeta(actorIDByUsernameSelect())).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_uri"}).
			AddRow(recipientID, "https://example.social/users/alice"))
	mock.ExpectQuery(regexp.QuoteMeta(federationBlockedDomainExistsSQL)).
		WithArgs("blocked.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))

	req := httptest.NewRequest(
		http.MethodPost,
		"/users/alice/inbox",
		strings.NewReader(`{"id":"https://blocked.example/activities/follow-1","type":"Follow","actor":"https://blocked.example/users/bob","object":"https://example.social/users/alice"}`),
	)
	req.Header.Set("Content-Type", "application/activity+json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if remoteResolver.called {
		t.Fatal("remote resolver should not be called for blocked domains")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInboxFollowRejectsMissingHTTPSignatureBeforeResolvingActor(t *testing.T) {
	db, mock := newMockDB(t)
	recipientID := uuid.New()
	remoteResolver := &recordingResolver{err: errors.New("resolver should not be called")}
	router := newTestRouterWithResolver(db, remoteResolver)

	mock.ExpectQuery(regexp.QuoteMeta(actorIDByUsernameSelect())).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_uri"}).
			AddRow(recipientID, "https://example.social/users/alice"))
	mock.ExpectQuery(regexp.QuoteMeta(federationBlockedDomainExistsSQL)).
		WithArgs("remote.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	req := httptest.NewRequest(
		http.MethodPost,
		"/users/alice/inbox",
		strings.NewReader(`{"id":"https://remote.example/activities/follow-1","type":"Follow","actor":"https://remote.example/users/bob","object":"https://example.social/users/alice"}`),
	)
	req.Header.Set("Content-Type", "application/activity+json")
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if remoteResolver.called {
		t.Fatal("remote resolver should not be called without HTTP Signature")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInboxFollowCreatesRemoteActorRelationshipAndAcceptActivity(t *testing.T) {
	db, mock := newMockDB(t)
	localActorID := uuid.New()
	remoteActorID := uuid.New()
	acceptActivityID := uuid.New()
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}
	router := newTestRouterWithResolver(db, fakeResolver{
		actor: resolver.RemoteActor{
			ActorURI:     "https://remote.example/users/bob",
			Acct:         "bob@remote.example",
			Domain:       "remote.example",
			InboxURL:     "https://remote.example/users/bob/inbox",
			OutboxURL:    "https://remote.example/users/bob/outbox",
			FollowersURL: "https://remote.example/users/bob/followers",
			FollowingURL: "https://remote.example/users/bob/following",
			PublicKeyPEM: keyPair.PublicKeyPEM,
			Type:         "Person",
			Name:         "Bob",
		},
	})

	mock.ExpectQuery(regexp.QuoteMeta(actorIDByUsernameSelect())).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_uri"}).
			AddRow(localActorID, "https://example.social/users/alice"))
	mock.ExpectQuery(regexp.QuoteMeta(federationBlockedDomainExistsSQL)).
		WithArgs("remote.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, actor_uri").
		WithArgs("https://remote.example/users/bob").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO actors").
		WithArgs(
			"https://remote.example/users/bob",
			"bob@remote.example",
			"Person",
			"bob",
			"Bob",
			"remote.example",
			"https://remote.example/users/bob/inbox",
			"https://remote.example/users/bob/outbox",
			"https://remote.example/users/bob/followers",
			"https://remote.example/users/bob/following",
			sqlmock.AnyArg(),
			keyPair.PublicKeyPEM,
			sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(remoteActorID))
	mock.ExpectExec("INSERT INTO inbox_messages").
		WithArgs(localActorID, remoteActorID, "https://remote.example/activities/follow-1", sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO relationships").
		WithArgs(localActorID, remoteActorID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("INSERT INTO activities").
		WithArgs(sqlmock.AnyArg(), localActorID, "https://remote.example/activities/follow-1", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(acceptActivityID))
	mock.ExpectExec("INSERT INTO outbox_jobs").
		WithArgs(acceptActivityID, "https://remote.example/users/bob/inbox").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	req := signedActivityRequest(
		t,
		"https://example.social/users/alice/inbox",
		"https://remote.example/users/bob",
		keyPair.PrivateKeyPEM,
		[]byte(`{"id":"https://remote.example/activities/follow-1","type":"Follow","actor":"https://remote.example/users/bob","object":"https://example.social/users/alice"}`),
	)
	res := httptest.NewRecorder()

	router.ServeHTTP(res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["processingState"] != "processed" {
		t.Fatalf("processingState = %v", body["processingState"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRicherInboxActivitiesRequireHTTPSignature(t *testing.T) {
	for _, activityType := range []string{"Create", "Update", "Delete", "Announce", "Like", "Accept", "Reject"} {
		t.Run(activityType, func(t *testing.T) {
			if !requiresInboxHTTPSignature(activityType, map[string]any{"type": activityType}) {
				t.Fatalf("%s must require an HTTP Signature", activityType)
			}
		})
	}
	if !requiresInboxHTTPSignature("Undo", map[string]any{
		"type": "Undo", "object": map[string]any{"type": "Like"},
	}) {
		t.Fatal("Undo Like must require an HTTP Signature")
	}
}

func TestFederatedObjectRejectsAttributionMismatch(t *testing.T) {
	_, err := federatedObjectFromActivity(map[string]any{
		"type":  "Create",
		"actor": "https://remote.example/users/bob",
		"object": map[string]any{
			"id": "https://remote.example/notes/1", "type": "Note",
			"attributedTo": "https://other.example/users/eve",
		},
	}, "https://remote.example/users/bob")
	if !errors.Is(err, errObjectOwnershipMismatch) {
		t.Fatalf("error = %v", err)
	}
}

func TestFederatedObjectRequiresID(t *testing.T) {
	_, err := federatedObjectFromActivity(map[string]any{
		"type": "Create", "object": map[string]any{"type": "Note"},
	}, "https://remote.example/users/bob")
	if !errors.Is(err, errInvalidFederatedObject) {
		t.Fatalf("error = %v", err)
	}
}

func TestInboxStoresSignedCreateAsProcessedFederatedActivity(t *testing.T) {
	db, mock := newMockDB(t)
	localActorID := uuid.New()
	remoteActorID := uuid.New()
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	actorURI := "https://remote.example/users/bob"
	activityURI := "https://remote.example/activities/create-1"
	objectURI := "https://remote.example/notes/1"
	router := newTestRouterWithResolver(db, fakeResolver{err: errors.New("resolver should not be called")})

	mock.ExpectQuery(regexp.QuoteMeta(actorIDByUsernameSelect())).
		WithArgs("alice").
		WillReturnRows(sqlmock.NewRows([]string{"id", "actor_uri"}).AddRow(localActorID, "https://example.social/users/alice"))
	mock.ExpectQuery(regexp.QuoteMeta(federationBlockedDomainExistsSQL)).
		WithArgs("remote.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, actor_uri").
		WithArgs(actorURI).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "actor_uri", "acct", "type", "preferred_username", "display_name", "domain",
			"inbox_url", "outbox_url", "followers_url", "following_url", "shared_inbox_url", "public_key_pem",
		}).AddRow(
			remoteActorID, actorURI, "bob@remote.example", "Person", "bob", "Bob", "remote.example",
			actorURI+"/inbox", actorURI+"/outbox", actorURI+"/followers", actorURI+"/following", "", keyPair.PublicKeyPEM,
		))
	mock.ExpectExec("INSERT INTO inbox_messages").
		WithArgs(localActorID, remoteActorID, activityURI, "Create", sqlmock.AnyArg(), true).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO activitypub_objects").
		WithArgs(objectURI, remoteActorID, "Note", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec("INSERT INTO activities").
		WithArgs(activityURI, remoteActorID, "Create", objectURI, "Note", "public", sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	body := []byte(`{"@context":"https://www.w3.org/ns/activitystreams","id":"` + activityURI + `","type":"Create","actor":"` + actorURI + `","to":["https://www.w3.org/ns/activitystreams#Public"],"object":{"id":"` + objectURI + `","type":"Note","content":"hello"}}`)
	req := signedActivityRequest(t, "https://example.social/users/alice/inbox", actorURI, keyPair.PrivateKeyPEM, body)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)

	if res.Code != http.StatusAccepted || !strings.Contains(res.Body.String(), `"processingState":"processed"`) {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func signedActivityRequest(t *testing.T, inboxURL string, actorURI string, privateKeyPEM string, body []byte) *http.Request {
	t.Helper()
	req, err := delivery.NewSignedActivityRequest(context.Background(), inboxURL, actorURI, privateKeyPEM, body)
	if err != nil {
		t.Fatalf("NewSignedActivityRequest returned error: %v", err)
	}
	return req
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

func actorRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id",
		"actor_uri",
		"acct",
		"preferred_username",
		"display_name",
		"bio",
		"domain",
		"inbox_url",
		"outbox_url",
		"followers_url",
		"following_url",
		"shared_inbox_url",
		"public_key_pem",
		"avatar_url",
	})
}

func newTestRouter(db *sql.DB) http.Handler {
	return newTestRouterWithResolver(db, fakeResolver{err: errors.New("resolver should not be called")})
}

func newTestRouterWithResolver(db *sql.DB, remoteResolver remoteActorResolver) http.Handler {
	r := chi.NewRouter()
	RegisterRoutes(r, NewHandlerWithResolver(db, config.Config{
		Server: config.ServerConfig{
			PublicURL: "https://example.social",
		},
		ActivityPub: config.ActivityPubConfig{
			Domain: "example.social",
		},
	}, remoteResolver))
	return r
}

type fakeResolver struct {
	actor resolver.RemoteActor
	err   error
}

func (r fakeResolver) ResolveActor(_ context.Context, _ string) (resolver.RemoteActor, error) {
	if r.err != nil {
		return resolver.RemoteActor{}, r.err
	}
	return r.actor, nil
}

type recordingResolver struct {
	actor  resolver.RemoteActor
	err    error
	called bool
}

func (r *recordingResolver) ResolveActor(_ context.Context, _ string) (resolver.RemoteActor, error) {
	r.called = true
	if r.err != nil {
		return resolver.RemoteActor{}, r.err
	}
	return r.actor, nil
}
