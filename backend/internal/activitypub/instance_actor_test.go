package activitypub

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"

	"basisvr-social-service/internal/config"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestInstanceSignedClientSignsServerGET(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT actor_uri, public_key_pem, private_key_pem_encrypted").
		WithArgs("https://social.example/actor").
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri", "public_key_pem", "private_key_pem_encrypted"}).
			AddRow("https://social.example/actor", keyPair.PublicKeyPEM, keyPair.PrivateKeyPEM))
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Signature-Input") == "" || req.Header.Get("Signature") == "" {
			t.Fatalf("signed headers are missing: %v", req.Header)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`)), Request: req,
		}, nil
	})
	cfg := config.Config{Server: config.ServerConfig{PublicURL: "https://social.example"}}
	client := NewInstanceSignedClient(db, cfg, base)

	res, err := client.Get("https://remote.example/actor")
	if err != nil {
		t.Fatalf("GET returned error: %v", err)
	}
	_ = res.Body.Close()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInstanceSignedClientRetriesUnauthorizedGETWithLegacySignature(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("SELECT actor_uri, public_key_pem, private_key_pem_encrypted").
		WithArgs("https://social.example/actor").
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri", "public_key_pem", "private_key_pem_encrypted"}).
			AddRow("https://social.example/actor", keyPair.PublicKeyPEM, keyPair.PrivateKeyPEM))
	attempts := 0
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			if req.Header.Get("Signature-Input") == "" {
				t.Fatal("first request must use RFC 9421")
			}
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
		}
		if req.Header.Get("Signature-Input") != "" || !strings.Contains(req.Header.Get("Signature"), `keyId="https://social.example/actor#main-key"`) {
			t.Fatalf("legacy retry headers = %v", req.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: http.NoBody, Request: req}, nil
	})
	client := NewInstanceSignedClient(db, config.Config{Server: config.ServerConfig{PublicURL: "https://social.example"}}, base)

	res, err := client.Get("https://remote.example/actor")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestEnsureInstanceActorCreatesMissingServiceActor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Config{Server: config.ServerConfig{PublicURL: "https://social.example"}, ActivityPub: config.ActivityPubConfig{Domain: "social.example"}}

	mock.ExpectQuery("SELECT actor_uri, public_key_pem, private_key_pem_encrypted").
		WithArgs("https://social.example/actor").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("INSERT INTO actors").
		WithArgs(
			"https://social.example/actor", "instance@social.example", "social.example",
			"https://social.example/actor/inbox", "https://social.example/actor/outbox",
			"https://social.example/actor/followers", "https://social.example/actor/following",
			"https://social.example/inbox", sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri", "public_key_pem", "private_key_pem_encrypted"}).
			AddRow("https://social.example/actor", "public-key", "private-key"))

	actor, err := EnsureInstanceActor(context.Background(), db, cfg)
	if err != nil {
		t.Fatalf("EnsureInstanceActor returned error: %v", err)
	}
	if actor.ActorURI != "https://social.example/actor" || actor.PrivateKeyPEM != "private-key" {
		t.Fatalf("actor = %+v", actor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureInstanceActorReturnsExistingActor(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	cfg := config.Config{Server: config.ServerConfig{PublicURL: "https://social.example"}}
	mock.ExpectQuery("SELECT actor_uri, public_key_pem, private_key_pem_encrypted").
		WithArgs("https://social.example/actor").
		WillReturnRows(sqlmock.NewRows([]string{"actor_uri", "public_key_pem", "private_key_pem_encrypted"}).
			AddRow("https://social.example/actor", "public-key", "private-key"))

	actor, err := EnsureInstanceActor(context.Background(), db, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if actor.PublicKeyPEM != "public-key" {
		t.Fatalf("actor = %+v", actor)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
