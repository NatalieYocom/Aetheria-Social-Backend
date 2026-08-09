package delivery

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	activitypub "basisvr-social-service/internal/activitypub"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestSignActivityRequestAddsRequiredActivityPubHeaders(t *testing.T) {
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}

	req, err := NewSignedActivityRequest(
		context.Background(),
		"https://remote.example/users/alice/inbox",
		"https://basis.example/users/bob",
		keyPair.PrivateKeyPEM,
		[]byte(`{"type":"Accept"}`),
	)
	if err != nil {
		t.Fatalf("NewSignedActivityRequest returned error: %v", err)
	}

	if req.Method != http.MethodPost {
		t.Fatalf("method = %s", req.Method)
	}
	if req.Header.Get("Content-Type") != "application/activity+json" {
		t.Fatalf("Content-Type = %q", req.Header.Get("Content-Type"))
	}
	if req.Header.Get("Date") == "" {
		t.Fatal("Date header is required")
	}
	if !strings.HasPrefix(req.Header.Get("Digest"), "SHA-256=") {
		t.Fatalf("Digest = %q", req.Header.Get("Digest"))
	}
	signature := req.Header.Get("Signature")
	if !strings.Contains(signature, `keyId="https://basis.example/users/bob#main-key"`) {
		t.Fatalf("Signature missing keyId: %s", signature)
	}
	if !strings.Contains(signature, `headers="(request-target) host date digest"`) {
		t.Fatalf("Signature missing signed headers: %s", signature)
	}
}

func TestSignLegacyGETCoversTargetWithoutDigest(t *testing.T) {
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://remote.example/users/alice?view=full", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SignLegacyRequest(req, "https://local.example/actor", keyPair.PrivateKeyPEM, nil, time.Now()); err != nil {
		t.Fatalf("SignLegacyRequest returned error: %v", err)
	}
	if req.Header.Get("Digest") != "" {
		t.Fatalf("Digest = %q", req.Header.Get("Digest"))
	}
	if signature := req.Header.Get("Signature"); !strings.Contains(signature, `headers="(request-target) host date"`) {
		t.Fatalf("Signature = %q", signature)
	}
}

func TestSignActivityRequestRejectsUnsafeInboxURL(t *testing.T) {
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}

	_, err = NewSignedActivityRequest(
		context.Background(),
		"http://127.0.0.1/users/alice/inbox",
		"https://basis.example/users/bob",
		keyPair.PrivateKeyPEM,
		[]byte(`{"type":"Accept"}`),
	)
	if err == nil {
		t.Fatal("expected unsafe inbox URL to be rejected")
	}
}

func TestWorkerDeliversDueJobAndMarksDelivered(t *testing.T) {
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}

	var received map[string]any
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.URL.String() != "https://remote.example/inbox" {
			t.Fatalf("URL = %s", r.URL.String())
		}
		if r.Header.Get("Signature") == "" {
			t.Fatal("missing Signature header")
		}
		if r.Header.Get("Digest") == "" {
			t.Fatal("missing Digest header")
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Body:       http.NoBody,
			Header:     make(http.Header),
		}, nil
	})}

	db, mock := newMockDB(t)
	jobID := uuid.New()
	activityID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT j.id, j.activity_id").
		WillReturnRows(sqlmock.NewRows([]string{
			"job_id",
			"activity_id",
			"target_inbox_url",
			"attempts",
			"raw_json",
			"activity_uri",
			"actor_uri",
			"private_key_pem_encrypted",
		}).AddRow(
			jobID,
			activityID,
			"https://remote.example/inbox",
			0,
			[]byte(`{"id":"https://basis.example/activities/accept-1","type":"Accept"}`),
			"https://basis.example/activities/accept-1",
			"https://basis.example/users/bob",
			keyPair.PrivateKeyPEM,
		))
	mock.ExpectQuery(regexp.QuoteMeta(blockedDomainExistsSQL)).
		WithArgs("remote.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("UPDATE outbox_jobs").
		WithArgs(jobID).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	worker := NewWorker(db, client, 6)
	delivered, err := worker.DeliverDueOne(context.Background())
	if err != nil {
		t.Fatalf("DeliverDueOne returned error: %v", err)
	}
	if !delivered {
		t.Fatal("expected one job to be delivered")
	}
	if received["type"] != "Accept" {
		t.Fatalf("received activity type = %v", received["type"])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerRetriesRejectedLegacyDeliveryWithRFC9421(t *testing.T) {
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			if r.Header.Get("Signature-Input") != "" {
				t.Fatal("first delivery must use the legacy signature")
			}
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody, Header: make(http.Header)}, nil
		}
		if r.Header.Get("Signature-Input") == "" || r.Header.Get("Content-Digest") == "" {
			t.Fatalf("RFC 9421 headers are missing: %v", r.Header)
		}
		return &http.Response{StatusCode: http.StatusAccepted, Body: http.NoBody, Header: make(http.Header)}, nil
	})}
	db, mock := newMockDB(t)
	jobID := uuid.New()
	activityID := uuid.New()
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT j.id, j.activity_id").WillReturnRows(sqlmock.NewRows([]string{
		"job_id", "activity_id", "target_inbox_url", "attempts", "raw_json", "activity_uri", "actor_uri", "private_key_pem_encrypted",
	}).AddRow(jobID, activityID, "https://remote.example/inbox", 0,
		[]byte(`{"id":"https://basis.example/activities/1","type":"Create"}`),
		"https://basis.example/activities/1", "https://basis.example/users/bob", keyPair.PrivateKeyPEM))
	mock.ExpectQuery(regexp.QuoteMeta(blockedDomainExistsSQL)).WithArgs("remote.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectExec("UPDATE outbox_jobs").WithArgs(jobID).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	delivered, err := NewWorker(db, client, 6).DeliverDueOne(context.Background())
	if err != nil || !delivered {
		t.Fatalf("delivered = %v, error = %v", delivered, err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d", attempts)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestWorkerMarksBlockedDomainJobFailedWithoutSending(t *testing.T) {
	keyPair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}

	db, mock := newMockDB(t)
	jobID := uuid.New()
	activityID := uuid.New()
	clientCalled := false

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT j.id, j.activity_id").
		WillReturnRows(sqlmock.NewRows([]string{
			"job_id",
			"activity_id",
			"target_inbox_url",
			"attempts",
			"raw_json",
			"activity_uri",
			"actor_uri",
			"private_key_pem_encrypted",
		}).AddRow(
			jobID,
			activityID,
			"https://blocked.example/inbox",
			0,
			[]byte(`{"id":"https://basis.example/activities/accept-1","type":"Accept"}`),
			"https://basis.example/activities/accept-1",
			"https://basis.example/users/bob",
			keyPair.PrivateKeyPEM,
		))
	mock.ExpectQuery(regexp.QuoteMeta(blockedDomainExistsSQL)).
		WithArgs("blocked.example").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("UPDATE outbox_jobs").
		WithArgs(jobID, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	worker := NewWorker(db, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		clientCalled = true
		return nil, nil
	})}, 6)
	delivered, err := worker.DeliverDueOne(context.Background())
	if err != nil {
		t.Fatalf("DeliverDueOne returned error: %v", err)
	}
	if !delivered {
		t.Fatal("expected one job to be consumed")
	}
	if clientCalled {
		t.Fatal("HTTP client should not be called for blocked domain")
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
