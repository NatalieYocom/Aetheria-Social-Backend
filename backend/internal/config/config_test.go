package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
)

func TestLoadBuildsConfigFromEnvironment(t *testing.T) {
	t.Setenv("API_BIND", "127.0.0.1:9090")
	t.Setenv("PUBLIC_URL", "https://social.example")
	t.Setenv("HTTP_READ_HEADER_TIMEOUT", "4s")
	t.Setenv("HTTP_READ_TIMEOUT", "25s")
	t.Setenv("HTTP_IDLE_TIMEOUT", "90s")
	t.Setenv("HTTP_MAX_HEADER_BYTES", "524288")
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "12s")
	t.Setenv("DATABASE_URL", "postgres://basis:basis@localhost:5432/basis_social?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/1")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("ACCESS_TOKEN_TTL", "15m")
	t.Setenv("REFRESH_TOKEN_TTL", "720h")
	t.Setenv("REGISTRATION_ENABLED", "false")
	t.Setenv("FEDERATION_MODE", "disabled")
	t.Setenv("ACTIVITYPUB_AUTHORIZED_FETCH", "all")
	t.Setenv("ASSET_CATALOG_ENABLED", "true")
	t.Setenv("ASSET_CATALOG_CODE", "beeba")
	t.Setenv("ASSET_CATALOG_NAME", "BeeBa Asset Catalog")
	t.Setenv("ASSET_CATALOG_KIND", "beeba")
	t.Setenv("ASSET_CATALOG_BASE_URL", "https://catalog.example")
	t.Setenv("ASSET_CATALOG_API_BASE_URL", "https://catalog.example/api/v1")
	t.Setenv("ASSET_CATALOG_API_TOKEN", "catalog-token")
	t.Setenv("ASSET_CATALOG_TIMEOUT", "3s")
	t.Setenv("REALTIME_BUFFER_SIZE", "256")
	t.Setenv("REALTIME_BUS_BUFFER_SIZE", "2048")
	t.Setenv("REALTIME_REDIS_CHANNEL", "basisvr:test:realtime")
	t.Setenv("REALTIME_REDIS_PUBLISH_TIMEOUT", "750ms")
	t.Setenv("REALTIME_REPLAY_ENABLED", "true")
	t.Setenv("REALTIME_REPLAY_RETENTION", "12h")
	t.Setenv("REALTIME_REPLAY_MAX_ENTRIES", "2500")
	t.Setenv("REALTIME_REPLAY_LIMIT", "200")
	t.Setenv("REALTIME_REPLAY_PREFIX", "basisvr:test:history")
	t.Setenv("REALTIME_REPLAY_STORE_TIMEOUT", "400ms")
	t.Setenv("PRESENCE_SWEEP_INTERVAL", "5s")
	t.Setenv("PRESENCE_SWEEP_BATCH_SIZE", "50")
	t.Setenv("MAX_REQUEST_BODY_BYTES", "1048576")
	t.Setenv("RATE_LIMIT_ENABLED", "true")
	t.Setenv("RATE_LIMIT_REQUESTS", "120")
	t.Setenv("RATE_LIMIT_WINDOW", "30s")
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://beeba.example, https://app.example")
	t.Setenv("METRICS_ENABLED", "true")
	t.Setenv("JSON_LOGS_ENABLED", "true")
	t.Setenv("READINESS_TIMEOUT", "1500ms")

	cfg := config.Load()

	if cfg.Server.Bind != "127.0.0.1:9090" {
		t.Fatalf("Server.Bind = %q", cfg.Server.Bind)
	}
	if cfg.Server.PublicURL != "https://social.example" {
		t.Fatalf("Server.PublicURL = %q", cfg.Server.PublicURL)
	}
	if cfg.Server.ReadHeaderTimeout != 4*time.Second || cfg.Server.ReadTimeout != 25*time.Second || cfg.Server.IdleTimeout != 90*time.Second {
		t.Fatalf("HTTP server timeouts = %+v", cfg.Server)
	}
	if cfg.Server.MaxHeaderBytes != 524288 || cfg.Server.ShutdownTimeout != 12*time.Second {
		t.Fatalf("HTTP server limits = %+v", cfg.Server)
	}
	if cfg.Database.URL == "" {
		t.Fatal("Database.URL should be loaded")
	}
	if cfg.Redis.URL != "redis://localhost:6379/1" {
		t.Fatalf("Redis.URL = %q", cfg.Redis.URL)
	}
	if cfg.Auth.JWTSecret != "test-secret" {
		t.Fatalf("Auth.JWTSecret = %q", cfg.Auth.JWTSecret)
	}
	if cfg.Auth.AccessTokenTTL != 15*time.Minute {
		t.Fatalf("Auth.AccessTokenTTL = %s", cfg.Auth.AccessTokenTTL)
	}
	if cfg.Auth.RefreshTokenTTL != 720*time.Hour {
		t.Fatalf("Auth.RefreshTokenTTL = %s", cfg.Auth.RefreshTokenTTL)
	}
	if cfg.Security.RegistrationEnabled {
		t.Fatal("RegistrationEnabled should be false")
	}
	if cfg.Security.FederationMode != "disabled" {
		t.Fatalf("FederationMode = %q", cfg.Security.FederationMode)
	}
	if cfg.Security.MaxRequestBodyBytes != 1048576 {
		t.Fatalf("MaxRequestBodyBytes = %d", cfg.Security.MaxRequestBodyBytes)
	}
	if !cfg.Security.RateLimitEnabled {
		t.Fatal("RateLimitEnabled should be true")
	}
	if cfg.Security.RateLimitRequests != 120 {
		t.Fatalf("RateLimitRequests = %d", cfg.Security.RateLimitRequests)
	}
	if cfg.Security.RateLimitWindow != 30*time.Second {
		t.Fatalf("RateLimitWindow = %s", cfg.Security.RateLimitWindow)
	}
	if len(cfg.Security.CORSAllowedOrigins) != 2 || cfg.Security.CORSAllowedOrigins[0] != "https://beeba.example" {
		t.Fatalf("CORSAllowedOrigins = %#v", cfg.Security.CORSAllowedOrigins)
	}
	if !cfg.Observability.MetricsEnabled {
		t.Fatal("MetricsEnabled should be true")
	}
	if !cfg.Observability.JSONLogsEnabled {
		t.Fatal("JSONLogsEnabled should be true")
	}
	if cfg.Observability.ReadinessTimeout != 1500*time.Millisecond {
		t.Fatalf("ReadinessTimeout = %s", cfg.Observability.ReadinessTimeout)
	}
	if cfg.ActivityPub.Domain != "social.example" {
		t.Fatalf("ActivityPub.Domain = %q", cfg.ActivityPub.Domain)
	}
	if cfg.ActivityPub.AuthorizedFetch != "all" {
		t.Fatalf("ActivityPub.AuthorizedFetch = %q", cfg.ActivityPub.AuthorizedFetch)
	}
	if !cfg.AssetCatalog.Enabled {
		t.Fatal("AssetCatalog.Enabled should be true")
	}
	if cfg.AssetCatalog.Code != "beeba" {
		t.Fatalf("AssetCatalog.Code = %q", cfg.AssetCatalog.Code)
	}
	if cfg.AssetCatalog.Name != "BeeBa Asset Catalog" {
		t.Fatalf("AssetCatalog.Name = %q", cfg.AssetCatalog.Name)
	}
	if cfg.AssetCatalog.Kind != "beeba" {
		t.Fatalf("AssetCatalog.Kind = %q", cfg.AssetCatalog.Kind)
	}
	if cfg.AssetCatalog.BaseURL != "https://catalog.example" {
		t.Fatalf("AssetCatalog.BaseURL = %q", cfg.AssetCatalog.BaseURL)
	}
	if cfg.AssetCatalog.APIBaseURL != "https://catalog.example/api/v1" {
		t.Fatalf("AssetCatalog.APIBaseURL = %q", cfg.AssetCatalog.APIBaseURL)
	}
	if cfg.AssetCatalog.APIToken != "catalog-token" {
		t.Fatalf("AssetCatalog.APIToken = %q", cfg.AssetCatalog.APIToken)
	}
	if cfg.AssetCatalog.Timeout != 3*time.Second {
		t.Fatalf("AssetCatalog.Timeout = %s", cfg.AssetCatalog.Timeout)
	}
	if cfg.Realtime.BufferSize != 256 {
		t.Fatalf("Realtime.BufferSize = %d", cfg.Realtime.BufferSize)
	}
	if cfg.Realtime.BusBufferSize != 2048 {
		t.Fatalf("Realtime.BusBufferSize = %d", cfg.Realtime.BusBufferSize)
	}
	if cfg.Realtime.RedisChannel != "basisvr:test:realtime" {
		t.Fatalf("Realtime.RedisChannel = %q", cfg.Realtime.RedisChannel)
	}
	if cfg.Realtime.RedisPublishTimeout != 750*time.Millisecond {
		t.Fatalf("Realtime.RedisPublishTimeout = %s", cfg.Realtime.RedisPublishTimeout)
	}
	if !cfg.Realtime.ReplayEnabled || cfg.Realtime.ReplayRetention != 12*time.Hour {
		t.Fatalf("Realtime replay enable/retention = %+v", cfg.Realtime)
	}
	if cfg.Realtime.ReplayMaxEntries != 2500 || cfg.Realtime.ReplayLimit != 200 {
		t.Fatalf("Realtime replay limits = %+v", cfg.Realtime)
	}
	if cfg.Realtime.ReplayPrefix != "basisvr:test:history" || cfg.Realtime.ReplayStoreTimeout != 400*time.Millisecond {
		t.Fatalf("Realtime replay storage = %+v", cfg.Realtime)
	}
	if cfg.Presence.SweepInterval != 5*time.Second {
		t.Fatalf("Presence.SweepInterval = %s", cfg.Presence.SweepInterval)
	}
	if cfg.Presence.SweepBatchSize != 50 {
		t.Fatalf("Presence.SweepBatchSize = %d", cfg.Presence.SweepBatchSize)
	}
}

func TestValidateRejectsUnsafeProductionConfiguration(t *testing.T) {
	cfg := config.Config{
		Environment: "production",
		Server:      config.ServerConfig{PublicURL: "http://social.example"},
		Database:    config.DatabaseConfig{URL: "postgres://basis:basis@db/basis?sslmode=disable"},
		Auth:        config.AuthConfig{JWTSecret: "dev-secret-change-me"},
		Redis:       config.RedisConfig{URL: ""},
		Realtime:    config.RealtimeConfig{ReplayEnabled: true},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected unsafe production config to be rejected")
	}
	for _, expected := range []string{"PUBLIC_URL", "JWT_SECRET", "DATABASE_URL", "REDIS_URL"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("error %q does not mention %s", err, expected)
		}
	}
}

func TestValidateAcceptsProductionConfiguration(t *testing.T) {
	cfg := config.Config{
		Environment: "production",
		Server:      config.ServerConfig{PublicURL: "https://social.example"},
		Database:    config.DatabaseConfig{URL: "postgres://basis:secret@db/basis?sslmode=require"},
		Auth:        config.AuthConfig{JWTSecret: strings.Repeat("a", 48)},
		Redis:       config.RedisConfig{URL: "rediss://redis.example:6380/0"},
		Realtime:    config.RealtimeConfig{ReplayEnabled: true},
		ActivityPub: config.ActivityPubConfig{ActorKeyEncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", Enabled: true, Domain: "social.example", AuthorizedFetch: "protected"},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsUnknownAuthorizedFetchModeInProduction(t *testing.T) {
	cfg := config.Config{
		Environment: "production",
		Server:      config.ServerConfig{PublicURL: "https://social.example"},
		Database:    config.DatabaseConfig{URL: "postgres://basis:secret@db/basis?sslmode=require"},
		Auth:        config.AuthConfig{JWTSecret: strings.Repeat("a", 48)},
		ActivityPub: config.ActivityPubConfig{ActorKeyEncryptionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", Enabled: true, Domain: "social.example", AuthorizedFetch: "sometimes"},
	}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "ACTIVITYPUB_AUTHORIZED_FETCH") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestLoadReadsSensitiveValuesFromFiles(t *testing.T) {
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "jwt")
	if err := os.WriteFile(secretFile, []byte("file-backed-secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_SECRET", "environment-value")
	t.Setenv("JWT_SECRET_FILE", secretFile)
	if got := config.Load().Auth.JWTSecret; got != "file-backed-secret-value" {
		t.Fatalf("JWT secret = %q", got)
	}
}
