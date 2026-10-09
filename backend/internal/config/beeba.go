package config

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

type BeeBaConfig struct {
	Enabled      bool
	APIBaseURL   string
	PublicURL    string
	SharedSecret string
	Timeout      time.Duration
}

func loadBeeBa() BeeBaConfig {
	return BeeBaConfig{Enabled: getBoolEnv("BEEBA_IDENTITY_ENABLED", false), APIBaseURL: strings.TrimRight(getEnv("BEEBA_IDENTITY_API_URL", ""), "/"), PublicURL: strings.TrimRight(getEnv("BEEBA_IDENTITY_PUBLIC_URL", ""), "/"), SharedSecret: getSecretEnv("BEEBA_IDENTITY_SECRET", ""), Timeout: getDurationEnv("BEEBA_IDENTITY_TIMEOUT", 3*time.Second)}
}
func (c BeeBaConfig) Validate(environment string) error {
	if !c.Enabled {
		return nil
	}
	if len(c.SharedSecret) < 32 {
		return errors.New("BEEBA_IDENTITY_SECRET must contain at least 32 bytes")
	}
	if c.Timeout <= 0 || c.Timeout > 10*time.Second {
		return errors.New("BEEBA_IDENTITY_TIMEOUT must be between 0 and 10 seconds")
	}
	for _, raw := range []string{c.APIBaseURL, c.PublicURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("BeeBa identity URLs must be absolute HTTP(S) origins")
		}
		if u.Scheme != "https" && !(environment == "development" && u.Scheme == "http") {
			return errors.New("BeeBa identity requires HTTPS outside development")
		}
	}
	return nil
}
