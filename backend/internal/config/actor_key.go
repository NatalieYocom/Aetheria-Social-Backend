package config

import (
	"os"
	"strings"
)

// An explicitly configured secret file is authoritative; an unreadable file must fail
// validation instead of silently choosing a different environment key.
func loadActorEncryptionKey() string {
	if file := strings.TrimSpace(os.Getenv("ACTOR_KEY_ENCRYPTION_KEY_FILE")); file != "" {
		value, err := os.ReadFile(file)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(value))
	}
	return getEnv("ACTOR_KEY_ENCRYPTION_KEY", "")
}
