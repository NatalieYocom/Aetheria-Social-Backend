package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment   string
	Server        ServerConfig
	Database      DatabaseConfig
	Redis         RedisConfig
	Auth          AuthConfig
	Presence      PresenceConfig
	Realtime      RealtimeConfig
	Observability ObservabilityConfig
	ActivityPub   ActivityPubConfig
	Retention     RetentionConfig
	AssetCatalog  AssetCatalogConfig
	Security      SecurityConfig
}

type ServerConfig struct {
	Bind              string
	PublicURL         string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	ShutdownTimeout   time.Duration
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
	ReplayEnabled       bool
	ReplayRetention     time.Duration
	ReplayMaxEntries    int64
	ReplayLimit         int
	ReplayPrefix        string
	ReplayStoreTimeout  time.Duration
}

type PresenceConfig struct {
	SweepInterval  time.Duration
	SweepBatchSize int
}

type ObservabilityConfig struct {
	MetricsEnabled     bool
	JSONLogsEnabled    bool
	ReadinessTimeout   time.Duration
	TracingEnabled     bool
	TracingService     string
	TracingEndpoint    string
	TracingSampleRatio float64
}

type AuthConfig struct {
	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

type ActivityPubConfig struct {
	Enabled                bool
	Domain                 string
	AuthorizedFetch        string
	DeliveryConcurrency    int
	MaxDeliveryAttempts    int
	FetchTimeout           time.Duration
	MaxRemoteResponseBytes int64
}

type RetentionConfig struct {
	Interval           time.Duration
	BatchSize          int
	InboxAge           time.Duration
	OutboxAge          time.Duration
	NotificationsAge   time.Duration
	InvitesAge         time.Duration
	InboundActivityAge time.Duration
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
	CORSAllowedOrigins  []string
}

func Load() Config {
	publicURL := getEnv("PUBLIC_URL", "http://localhost:8080")
	domain := getEnv("ACTIVITYPUB_DOMAIN", domainFromPublicURL(publicURL))
	redisURL := getSecretEnv("REDIS_URL", "")

	return Config{
		Environment: getEnv("APP_ENV", "development"),
		Server: ServerConfig{
			Bind:              getEnv("API_BIND", "0.0.0.0:8080"),
			PublicURL:         strings.TrimRight(publicURL, "/"),
			ReadHeaderTimeout: getDurationEnv("HTTP_READ_HEADER_TIMEOUT", 5*time.Second),
			ReadTimeout:       getDurationEnv("HTTP_READ_TIMEOUT", 30*time.Second),
			IdleTimeout:       getDurationEnv("HTTP_IDLE_TIMEOUT", 2*time.Minute),
			MaxHeaderBytes:    getIntEnv("HTTP_MAX_HEADER_BYTES", 1<<20),
			ShutdownTimeout:   getDurationEnv("HTTP_SHUTDOWN_TIMEOUT", 15*time.Second),
		},
		Database: DatabaseConfig{
			URL: getSecretEnv("DATABASE_URL", "postgres://basis:basis@localhost:5432/basis_social?sslmode=disable"),
		},
		Redis: RedisConfig{
			URL: redisURL,
		},
		Realtime: RealtimeConfig{
			BufferSize:          getIntEnv("REALTIME_BUFFER_SIZE", 128),
			BusBufferSize:       getIntEnv("REALTIME_BUS_BUFFER_SIZE", 1024),
			RedisChannel:        getEnv("REALTIME_REDIS_CHANNEL", "basisvr:realtime:v1"),
			RedisPublishTimeout: getDurationEnv("REALTIME_REDIS_PUBLISH_TIMEOUT", 2*time.Second),
			ReplayEnabled:       getBoolEnv("REALTIME_REPLAY_ENABLED", redisURL != ""),
			ReplayRetention:     getDurationEnv("REALTIME_REPLAY_RETENTION", 24*time.Hour),
			ReplayMaxEntries:    getInt64Env("REALTIME_REPLAY_MAX_ENTRIES", 10000),
			ReplayLimit:         getIntEnv("REALTIME_REPLAY_LIMIT", 500),
			ReplayPrefix:        getEnv("REALTIME_REPLAY_PREFIX", "basisvr:realtime"),
			ReplayStoreTimeout:  getDurationEnv("REALTIME_REPLAY_STORE_TIMEOUT", 250*time.Millisecond),
		},
		Presence: PresenceConfig{
			SweepInterval:  getDurationEnv("PRESENCE_SWEEP_INTERVAL", 15*time.Second),
			SweepBatchSize: getIntEnv("PRESENCE_SWEEP_BATCH_SIZE", 500),
		},
		Observability: ObservabilityConfig{
			MetricsEnabled:     getBoolEnv("METRICS_ENABLED", true),
			JSONLogsEnabled:    getBoolEnv("JSON_LOGS_ENABLED", true),
			ReadinessTimeout:   getDurationEnv("READINESS_TIMEOUT", 2*time.Second),
			TracingEnabled:     getBoolEnv("OTEL_TRACING_ENABLED", false),
			TracingService:     getEnv("OTEL_SERVICE_NAME", "basisvr-social-api"),
			TracingEndpoint:    getEnv("OTEL_EXPORTER_OTLP_ENDPOINT", ""),
			TracingSampleRatio: getFloatEnv("OTEL_TRACES_SAMPLER_RATIO", 0.1),
		},
		Auth: AuthConfig{
			JWTSecret:       getSecretEnv("JWT_SECRET", "dev-secret-change-me"),
			AccessTokenTTL:  getDurationEnv("ACCESS_TOKEN_TTL", 15*time.Minute),
			RefreshTokenTTL: getDurationEnv("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		},
		ActivityPub: ActivityPubConfig{
			Enabled:                getBoolEnv("ACTIVITYPUB_ENABLED", false),
			Domain:                 domain,
			AuthorizedFetch:        strings.ToLower(getEnv("ACTIVITYPUB_AUTHORIZED_FETCH", "protected")),
			DeliveryConcurrency:    getIntEnv("ACTIVITYPUB_DELIVERY_CONCURRENCY", 16),
			MaxDeliveryAttempts:    getIntEnv("ACTIVITYPUB_MAX_DELIVERY_ATTEMPTS", 6),
			FetchTimeout:           getDurationEnv("ACTIVITYPUB_FETCH_TIMEOUT", 10*time.Second),
			MaxRemoteResponseBytes: int64(getIntEnv("ACTIVITYPUB_MAX_REMOTE_RESPONSE_BYTES", 5*1024*1024)),
		},
		Retention: RetentionConfig{
			Interval:           getDurationEnv("RETENTION_INTERVAL", time.Hour),
			BatchSize:          getIntEnv("RETENTION_BATCH_SIZE", 1000),
			InboxAge:           getDurationEnv("RETENTION_INBOX_AGE", 30*24*time.Hour),
			OutboxAge:          getDurationEnv("RETENTION_OUTBOX_AGE", 30*24*time.Hour),
			NotificationsAge:   getDurationEnv("RETENTION_NOTIFICATIONS_AGE", 180*24*time.Hour),
			InvitesAge:         getDurationEnv("RETENTION_INVITES_AGE", 90*24*time.Hour),
			InboundActivityAge: getDurationEnv("RETENTION_INBOUND_ACTIVITY_AGE", 180*24*time.Hour),
		},
		AssetCatalog: AssetCatalogConfig{
			Enabled:    getBoolEnv("ASSET_CATALOG_ENABLED", false),
			Code:       getEnv("ASSET_CATALOG_CODE", "beeba"),
			Name:       getEnv("ASSET_CATALOG_NAME", "BeeBa Asset Catalog"),
			Kind:       getEnv("ASSET_CATALOG_KIND", "beeba"),
			BaseURL:    strings.TrimRight(getEnv("ASSET_CATALOG_BASE_URL", ""), "/"),
			APIBaseURL: strings.TrimRight(getEnv("ASSET_CATALOG_API_BASE_URL", ""), "/"),
			APIToken:   getSecretEnv("ASSET_CATALOG_API_TOKEN", ""),
			Timeout:    getDurationEnv("ASSET_CATALOG_TIMEOUT", 5*time.Second),
		},
		Security: SecurityConfig{
			RegistrationEnabled: getBoolEnv("REGISTRATION_ENABLED", true),
			FederationMode:      getEnv("FEDERATION_MODE", "disabled"),
			MaxRequestBodyBytes: getInt64Env("MAX_REQUEST_BODY_BYTES", 8*1024*1024),
			RateLimitEnabled:    getBoolEnv("RATE_LIMIT_ENABLED", true),
			RateLimitRequests:   getIntEnv("RATE_LIMIT_REQUESTS", 600),
			RateLimitWindow:     getDurationEnv("RATE_LIMIT_WINDOW", time.Minute),
			CORSAllowedOrigins:  getCSVEnv("CORS_ALLOWED_ORIGINS"),
		},
	}
}

func (c Config) Validate() error {
	if strings.ToLower(strings.TrimSpace(c.Environment)) != "production" {
		return nil
	}
	var problems []error
	publicURL, err := url.Parse(c.Server.PublicURL)
	if err != nil || publicURL.Scheme != "https" || publicURL.Hostname() == "" {
		problems = append(problems, errors.New("PUBLIC_URL must be an absolute HTTPS URL in production"))
	}
	secret := strings.TrimSpace(c.Auth.JWTSecret)
	if len(secret) < 32 || secret == "dev-secret-change-me" || secret == "change-me-in-production" {
		problems = append(problems, errors.New("JWT_SECRET must contain at least 32 non-default characters in production"))
	}
	databaseURL, err := url.Parse(c.Database.URL)
	if err != nil || databaseURL.Scheme == "" || databaseURL.Hostname() == "" {
		problems = append(problems, errors.New("DATABASE_URL must be a valid network URL in production"))
	} else if strings.EqualFold(databaseURL.Query().Get("sslmode"), "disable") {
		problems = append(problems, errors.New("DATABASE_URL must not use sslmode=disable in production"))
	}
	if c.Realtime.ReplayEnabled {
		redisURL, err := url.Parse(c.Redis.URL)
		if err != nil || (redisURL.Scheme != "redis" && redisURL.Scheme != "rediss") || redisURL.Hostname() == "" {
			problems = append(problems, errors.New("REDIS_URL is required when realtime replay is enabled in production"))
		}
	}
	if c.ActivityPub.Enabled {
		domain := strings.ToLower(strings.TrimSpace(c.ActivityPub.Domain))
		if domain == "" || domain == "localhost" || strings.HasSuffix(domain, ".local") {
			problems = append(problems, errors.New("ACTIVITYPUB_DOMAIN must be a public domain in production"))
		}
		mode := strings.ToLower(strings.TrimSpace(c.ActivityPub.AuthorizedFetch))
		if mode != "disabled" && mode != "protected" && mode != "all" {
			problems = append(problems, errors.New("ACTIVITYPUB_AUTHORIZED_FETCH must be disabled, protected or all"))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid production configuration: %w", errors.Join(problems...))
	}
	return nil
}

func getEnv(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func getSecretEnv(key, fallback string) string {
	if file := strings.TrimSpace(os.Getenv(key + "_FILE")); file != "" {
		data, err := os.ReadFile(file)
		if err == nil {
			if value := strings.TrimSpace(string(data)); value != "" {
				return value
			}
		}
	}
	return getEnv(key, fallback)
}

func getCSVEnv(key string) []string {
	parts := strings.Split(os.Getenv(key), ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
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

func getFloatEnv(key string, fallback float64) float64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
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
