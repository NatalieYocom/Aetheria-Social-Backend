package messagesig

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignAndVerifyRequest(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"type":"Create"}`)
	req, err := http.NewRequest(http.MethodPost, "https://remote.example/inbox?source=test", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_770_000_000, 0).UTC()
	keyID := "https://local.example/actor#main-key"

	if err := SignRequest(req, body, keyID, privateKey, now); err != nil {
		t.Fatalf("SignRequest returned error: %v", err)
	}
	if req.Header.Get("Signature-Input") == "" {
		t.Fatal("Signature-Input header is missing")
	}
	if req.Header.Get("Signature") == "" {
		t.Fatal("Signature header is missing")
	}
	if req.Header.Get("Content-Digest") == "" {
		t.Fatal("Content-Digest header is missing")
	}
	if err := VerifyRequest(req, body, &privateKey.PublicKey, keyID, now.Add(time.Minute), 5*time.Minute); err != nil {
		t.Fatalf("VerifyRequest returned error: %v", err)
	}
}

func TestVerifyRequestRejectsTamperedBody(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"type":"Create"}`)
	req, err := http.NewRequest(http.MethodPost, "https://remote.example/inbox", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_770_000_000, 0).UTC()
	keyID := "https://local.example/actor#main-key"
	if err := SignRequest(req, body, keyID, privateKey, now); err != nil {
		t.Fatal(err)
	}

	err = VerifyRequest(req, []byte(`{"type":"Delete"}`), &privateKey.PublicKey, keyID, now, 5*time.Minute)
	if err == nil {
		t.Fatal("VerifyRequest accepted a tampered body")
	}
}

func TestVerifyRequestRejectsStaleSignature(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://remote.example/actor", nil)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Unix(1_770_000_000, 0).UTC()
	keyID := "https://local.example/actor#main-key"
	if err := SignRequest(req, nil, keyID, privateKey, created); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRequest(req, nil, &privateKey.PublicKey, keyID, created.Add(6*time.Minute), 5*time.Minute); err == nil {
		t.Fatal("VerifyRequest accepted a stale signature")
	}
}

func TestVerifyRequestRejectsChangedTargetURI(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, "https://remote.example/actor", nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	keyID := "https://local.example/actor#main-key"
	if err := SignRequest(req, nil, keyID, privateKey, now); err != nil {
		t.Fatal(err)
	}
	req.URL.Path = "/other-actor"
	if err := VerifyRequest(req, nil, &privateKey.PublicKey, keyID, now, 5*time.Minute); err == nil {
		t.Fatal("VerifyRequest accepted a changed target URI")
	}
}
