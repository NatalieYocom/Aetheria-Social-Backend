package config

import (
	"strings"
	"testing"
	"time"
)

func TestBeeBaConfigurationRequiresExplicitTrustedOrigins(t *testing.T) {
	base := BeeBaConfig{Enabled: true, APIBaseURL: "https://identity.example", PublicURL: "https://identity.example", SharedSecret: strings.Repeat("s", 32), Timeout: 3 * time.Second}
	if err := base.Validate("production"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://identity.example", "https://user:secret@identity.example", "https://identity.example/path", "https://identity.example?secret=x", "https://identity.example#fragment", "//identity.example"} {
		cfg := base
		cfg.APIBaseURL = raw
		if cfg.Validate("production") == nil {
			t.Errorf("accepted unsafe origin %s", raw)
		}
	}
	cfg := base
	cfg.APIBaseURL = "http://backend:8080"
	cfg.PublicURL = "http://127.0.0.1:18088"
	if err := cfg.Validate("development"); err != nil {
		t.Fatal(err)
	}
	cfg = base
	cfg.SharedSecret = "short"
	if cfg.Validate("production") == nil {
		t.Fatal("accepted short secret")
	}
	cfg = base
	cfg.Timeout = 11 * time.Second
	if cfg.Validate("production") == nil {
		t.Fatal("accepted unbounded timeout")
	}
}
