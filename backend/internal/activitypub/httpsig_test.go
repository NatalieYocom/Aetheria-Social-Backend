package activitypub

import (
	"context"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/activitypub/delivery"
)

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
