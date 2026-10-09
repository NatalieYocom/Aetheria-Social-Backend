package config_test

import (
	"basisvr-social-service/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActorKeyIsRequiredAndSupportsSecretFile(t *testing.T) {
	cfg := config.Config{}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ACTOR_KEY_ENCRYPTION_KEY") {
		t.Fatalf("missing key error=%v", err)
	}
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	path := filepath.Join(t.TempDir(), "actor-key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACTOR_KEY_ENCRYPTION_KEY_FILE", path)
	cfg = config.Load()
	if cfg.ActivityPub.ActorKeyEncryptionKey != key {
		t.Fatal("secret file key not loaded")
	}
	cfg.ActivityPub.ActorKeyEncryptionKey = "malformed"
	if err := cfg.Validate(); err == nil {
		t.Fatal("invalid key accepted")
	}
}

func TestUnreadableActorKeyFileDoesNotFallBackToEnvironment(t *testing.T) {
	t.Setenv("ACTOR_KEY_ENCRYPTION_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	t.Setenv("ACTOR_KEY_ENCRYPTION_KEY_FILE", filepath.Join(t.TempDir(), "missing"))
	cfg := config.Load()
	if cfg.ActivityPub.ActorKeyEncryptionKey != "" {
		t.Fatal("unexpected environment fallback")
	}
}
