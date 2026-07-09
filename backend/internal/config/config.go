package config

import (
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	Redis         RedisConfig
	Auth          AuthConfig
	Presence      PresenceConfig
	Realtime      RealtimeConfig
	Observability ObservabilityConfig
	ActivityPub   ActivityPubConfig
	AssetCatalog  AssetCatalogConfig
	Security      SecurityConfig
}

type ServerConfig struct {
	Bind      string
	PublicURL string
}

type DatabaseConfig struct {
	URL string
}

type RedisConfig struct {
	URL string
}

type RealtimeConfig struct {
	BufferSize          int
	BusBufferSize       int
	RedisChannel        string
	RedisPublishTimeout time.Duration
}

type PresenceConfig struct {
	SweepInterval  time.Duration
	SweepBatchSize int
}

type ObservabilityConfig struct {
	MetricsEnabled   bool
	JSONLogsEnabled  bool
	ReadinessTimeout time.Duration
}

type AuthConfig struct {
	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

type ActivityPubConfig struct {
	Enabled                bool
	Domain                 string
	DeliveryConcurrency    int
	MaxDeliveryAttempts    int
	FetchTimeout           time.Duration
	MaxRemoteResponseBytes int64
}

type AssetCatalogConfig struct {
	Enabled    bool
	Code       string
	Name       string
	Kind       string
	BaseURL    string
	APIBaseURL string
	APIToken   string
	Timeout    time.Duration
}

type SecurityConfig struct {
	RegistrationEnabled bool
	FederationMode      string
	MaxRequestBodyBytes int64
	RateLimitEnabled    bool
	RateLimitRequests   int
	RateLimitWindow     time.Duration
}

func Load() Config {
	publicURL := getEnv("PUBLIC_URL", "http://localhost:8080")
	domain := getEnv("ACTIVITYPUB_DOMAIN", domainFromPublicURL(publicURL))

	return Config{
		Server: ServerConfig{
			Bind:      getEnv("API_BIND", "0.0.0.0:8080"),
			PublicURL: strings.TrimRight(publicURL, "/"),
		},
		Database: DatabaseConfig{
			URL: getEnv("DATABASE_URL", "postgres://basis:basis@localhost:5432/basis_social?sslmode=disable"),
		},
		Redis: RedisConfig{
			URL: getEnv("REDIS_URL", ""),
		},
		Realtime: RealtimeConfig{
			BufferSize:          getIntEnv("REALTIME_BUFFER_SIZE", 128),
			BusBufferSize:       getIntEnv("REALTIME_BUS_BUFFER_SIZE", 1024),
			RedisChannel:        getEnv("REALTIME_REDIS_CHANNEL", "basisvr:realtime:v1"),
			RedisPublishTimeout: getDurationEnv("REALTIME_REDIS_PUBLISH_TIMEOUT", 2*time.Second),
		},
		Presence: PresenceConfig{
			SweepInterval:  getDurationEnv("PRESENCE_SWEEP_INTERVAL", 15*time.Second),
			SweepBatchSize: getIntEnv("PRESENCE_SWEEP_BATCH_SIZE", 500),
		},
		Observability: ObservabilityConfig{
			MetricsEnabled:   getBoolEnv("METRICS_ENABLED", true),
			JSONLogsEnabled:  getBoolEnv("JSON_LOGS_ENABLED", true),
			ReadinessTimeout: getDurationEnv("READINESS_TIMEOUT", 2*time.Second),
		},
		Auth: AuthConfig{
			JWTSecret:       getEnv("JWT_SECRET", "dev-secret-change-me"),
			AccessTokenTTL:  getDurationEnv("ACCESS_TOKEN_TTL", 15*time.Minute),
			RefreshTokenTTL: getDurationEnv("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		},
		ActivityPub: ActivityPubConfig{
			Enabled:                getBoolEnv("ACTIVITYPUB_ENABLED", false),
			Domain:                 domain,
			DeliveryConcurrency:    getIntEnv("ACTIVITYPUB_DELIVERY_CONCURRENCY", 16),
			MaxDeliveryAttempts:    getIntEnv("ACTIVITYPUB_MAX_DELIVERY_ATTEMPTS", 6),
			FetchTimeout:           getDurationEnv("ACTIVITYPUB_FETCH_TIMEOUT", 10*time.Second),
			MaxRemoteResponseBytes: int64(getIntEnv("ACTIVITYPUB_MAX_REMOTE_RESPONSE_BYTES", 5*1024*1024)),
		},
		AssetCatalog: AssetCatalogConfig{
			Enabled:    getBoolEnv("ASSET_CATALOG_ENABLED", false),
			Code:       getEnv("ASSET_CATALOG_CODE", "beeba"),
			Name:       getEnv("ASSET_CATALOG_NAME", "BeeBa Asset Catalog"),
			Kind:       getEnv("ASSET_CATALOG_KIND", "beeba"),
			BaseURL:    strings.TrimRight(getEnv("ASSET_CATALOG_BASE_URL", ""), "/"),
			APIBaseURL: strings.TrimRight(getEnv("ASSET_CATALOG_API_BASE_URL", ""), "/"),
			APIToken:   getEnv("ASSET_CATALOG_API_TOKEN", ""),
			Timeout:    getDurationEnv("ASSET_CATALOG_TIMEOUT", 5*time.Second),
		},
		Security: SecurityConfig{
			RegistrationEnabled: getBoolEnv("REGISTRATION_ENABLED", true),
			FederationMode:      getEnv("FEDERATION_MODE", "disabled"),
			MaxRequestBodyBytes: getInt64Env("MAX_REQUEST_BODY_BYTES", 8*1024*1024),
			RateLimitEnabled:    getBoolEnv("RATE_LIMIT_ENABLED", true),
			RateLimitRequests:   getIntEnv("RATE_LIMIT_REQUESTS", 600),
			RateLimitWindow:     getDurationEnv("RATE_LIMIT_WINDOW", time.Minute),
		},
	}
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getBoolEnv(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getIntEnv(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getInt64Env(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func domainFromPublicURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		return "localhost"
	}
	return parsed.Hostname()
}
