package activitypub

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/activitypub/delivery"
	"basisvr-social-service/internal/activitypub/messagesig"
)

func TestVerifyHTTPSignatureRequestAcceptsRFC9421Signature(t *testing.T) {
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	privateKey := mustParseTestPrivateKey(t, keyPair.PrivateKeyPEM)
	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/bob"}`)
	req, err := http.NewRequest(http.MethodPost, "https://example.social/users/alice/inbox", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_770_000_000, 0).UTC()
	if err := messagesig.SignRequest(req, body, "https://remote.example/users/bob#main-key", privateKey, now); err != nil {
		t.Fatal(err)
	}

	if err := VerifyHTTPSignatureRequest(req, body, keyPair.PublicKeyPEM, "https://remote.example/users/bob", now, 5*time.Minute); err != nil {
		t.Fatalf("VerifyHTTPSignatureRequest returned error: %v", err)
	}
}

func TestVerifyHTTPSignatureRequestAcceptsLegacySignedGETWithoutDigest(t *testing.T) {
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://example.social/objects/1", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := delivery.SignLegacyRequest(req, "https://remote.example/users/bob", keyPair.PrivateKeyPEM, nil, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyHTTPSignatureRequest(req, nil, keyPair.PublicKeyPEM, "https://remote.example/users/bob", now, 5*time.Minute); err != nil {
		t.Fatalf("VerifyHTTPSignatureRequest returned error: %v", err)
	}
}

func TestVerifyHTTPSignatureRequestAcceptsDeliverySignature(t *testing.T) {
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}
	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/bob"}`)
	req, err := delivery.NewSignedActivityRequest(
		context.Background(),
		"https://example.social/users/alice/inbox",
		"https://remote.example/users/bob",
		keyPair.PrivateKeyPEM,
		body,
	)
	if err != nil {
		t.Fatalf("NewSignedActivityRequest returned error: %v", err)
	}

	if err := VerifyHTTPSignatureRequest(req, body, keyPair.PublicKeyPEM, "https://remote.example/users/bob", time.Now(), 5*time.Minute); err != nil {
		t.Fatalf("VerifyHTTPSignatureRequest returned error: %v", err)
	}
}

func TestVerifyHTTPSignatureRequestRejectsTamperedBody(t *testing.T) {
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}
	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/bob"}`)
	req, err := delivery.NewSignedActivityRequest(
		context.Background(),
		"https://example.social/users/alice/inbox",
		"https://remote.example/users/bob",
		keyPair.PrivateKeyPEM,
		body,
	)
	if err != nil {
		t.Fatalf("NewSignedActivityRequest returned error: %v", err)
	}

	err = VerifyHTTPSignatureRequest(req, []byte(`{"type":"Undo"}`), keyPair.PublicKeyPEM, "https://remote.example/users/bob", time.Now(), 5*time.Minute)
	if err == nil {
		t.Fatal("expected tampered body to be rejected")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "digest") {
		t.Fatalf("expected digest error, got %v", err)
	}
}

func TestVerifyHTTPSignatureRequestRejectsWrongKeyOwner(t *testing.T) {
	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}
	body := []byte(`{"type":"Follow","actor":"https://remote.example/users/bob"}`)
	req, err := delivery.NewSignedActivityRequest(
		context.Background(),
		"https://example.social/users/alice/inbox",
		"https://remote.example/users/bob",
		keyPair.PrivateKeyPEM,
		body,
	)
	if err != nil {
		t.Fatalf("NewSignedActivityRequest returned error: %v", err)
	}

	err = VerifyHTTPSignatureRequest(req, body, keyPair.PublicKeyPEM, "https://remote.example/users/mallory", time.Now(), 5*time.Minute)
	if err == nil {
		t.Fatal("expected wrong key owner to be rejected")
	}
	if !strings.Contains(err.Error(), "keyId") {
		t.Fatalf("expected keyId error, got %v", err)
	}
}

func mustParseTestPrivateKey(t *testing.T, value string) *rsa.PrivateKey {
	t.Helper()
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		t.Fatal("private key PEM is invalid")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
