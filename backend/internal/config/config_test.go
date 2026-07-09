package config_test

import (
	"testing"
	"time"

	"basisvr-social-service/internal/config"
)

func TestLoadBuildsConfigFromEnvironment(t *testing.T) {
	t.Setenv("API_BIND", "127.0.0.1:9090")
	t.Setenv("PUBLIC_URL", "https://social.example")
	t.Setenv("DATABASE_URL", "postgres://basis:basis@localhost:5432/basis_social?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/1")
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("ACCESS_TOKEN_TTL", "15m")
	t.Setenv("REFRESH_TOKEN_TTL", "720h")
	t.Setenv("REGISTRATION_ENABLED", "false")
	t.Setenv("FEDERATION_MODE", "disabled")
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
	t.Setenv("PRESENCE_SWEEP_INTERVAL", "5s")
	t.Setenv("PRESENCE_SWEEP_BATCH_SIZE", "50")
	t.Setenv("MAX_REQUEST_BODY_BYTES", "1048576")
	t.Setenv("RATE_LIMIT_ENABLED", "true")
	t.Setenv("RATE_LIMIT_REQUESTS", "120")
	t.Setenv("RATE_LIMIT_WINDOW", "30s")
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
	if cfg.Presence.SweepInterval != 5*time.Second {
		t.Fatalf("Presence.SweepInterval = %s", cfg.Presence.SweepInterval)
	}
	if cfg.Presence.SweepBatchSize != 50 {
		t.Fatalf("Presence.SweepBatchSize = %d", cfg.Presence.SweepBatchSize)
	}
}
