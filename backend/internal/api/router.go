package api

import (
	"context"
	"database/sql"
	"log"
	"net/http"

	"basisvr-social-service/internal/activitypub"
	"basisvr-social-service/internal/assetcatalog"
	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/community"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/events"
	"basisvr-social-service/internal/groups"
	"basisvr-social-service/internal/instances"
	"basisvr-social-service/internal/invites"
	"basisvr-social-service/internal/moderation"
	"basisvr-social-service/internal/notifications"
	"basisvr-social-service/internal/observability"
	"basisvr-social-service/internal/presence"
	"basisvr-social-service/internal/profiles"
	"basisvr-social-service/internal/realtime"
	"basisvr-social-service/internal/relationships"
	"basisvr-social-service/internal/security"
	"basisvr-social-service/internal/worlds"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Deps struct {
	DB      *sql.DB
	Config  config.Config
	Context context.Context
}

func NewRouter(deps Deps) http.Handler {
	appCtx := deps.Context
	if appCtx == nil {
		appCtx = context.Background()
	}
	r := chi.NewRouter()
	r.Use(versionedAPI)
	r.Use(middleware.RequestID)
	r.Use(security.TrustedProxy(deps.Config.Security.TrustedProxyCIDRs))
	if deps.Config.Observability.TracingEnabled {
		r.Use(otelhttp.NewMiddleware("basisvr.http", otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/readyz" && r.URL.Path != "/metrics"
		})))
	}
	if deps.Config.Observability.JSONLogsEnabled {
		r.Use(observability.RequestLogger(log.Writer()))
	} else {
		r.Use(middleware.Logger)
	}
	r.Use(middleware.Recoverer)
	r.Use(security.Headers)
	r.Use(security.CORS(deps.Config.Security.CORSAllowedOrigins))
	r.Use(security.BodyLimit(deps.Config.Security.MaxRequestBodyBytes))
	if deps.Config.Security.RateLimitEnabled {
		r.Use(security.RateLimit(security.NewFixedWindowLimiter(security.FixedWindowConfig{
			Limit:   deps.Config.Security.RateLimitRequests,
			Window:  deps.Config.Security.RateLimitWindow,
			MaxKeys: deps.Config.Security.RateLimitMaxKeys,
		})))
		r.Use(security.AuthRateLimit(deps.Config.Security.AuthRateLimitRequests, deps.Config.Security.RateLimitMaxKeys))
	}
	tokens := auth.NewTokenManager(
		deps.Config.Auth.JWTSecret,
		deps.Config.Auth.AccessTokenTTL,
		deps.Config.Auth.RefreshTokenTTL,
	)
	linkedValidator := auth.LinkedIdentityValidator(deps.DB, deps.Config.BeeBa)
	authMiddleware := auth.Middleware(tokens, deps.DB, linkedValidator)
	r.Use(auth.OptionalMiddleware(tokens, deps.DB, linkedValidator))
	var metrics *observability.Metrics
	if deps.Config.Observability.MetricsEnabled {
		metrics = observability.NewMetrics()
		r.Use(metrics.Middleware)
		r.Get("/metrics", metrics.Handler().ServeHTTP)
	}

	realtimeBroker := realtime.NewBroker(realtime.BrokerConfig{
		BufferSize:     deps.Config.Realtime.BufferSize,
		BusBufferSize:  deps.Config.Realtime.BusBufferSize,
		BusTopic:       deps.Config.Realtime.RedisChannel,
		PublishTimeout: deps.Config.Realtime.RedisPublishTimeout,
	})
	if metrics != nil {
		metrics.SetRealtimeProvider(func() observability.RealtimeMetrics {
			stats := realtimeBroker.Stats()
			return observability.RealtimeMetrics{
				ReplayStored: stats.ReplayStored, ReplayStoreFailures: stats.ReplayStoreFailures,
				ReplayRequests: stats.ReplayRequests, ReplayDelivered: stats.ReplayDelivered,
				ReplayResyncs: stats.ReplayResyncs, ActiveSubscribers: stats.ActiveSubscribers,
			}
		})
		metrics.SetFederationProvider(observability.FederationMetricsProvider(deps.DB))
	}
	if deps.Config.Redis.URL != "" {
		if deps.Config.Realtime.ReplayEnabled {
			store, err := realtime.NewRedisReplayStore(deps.Config.Redis.URL, realtime.ReplayStoreConfig{
				Prefix:     deps.Config.Realtime.ReplayPrefix,
				Retention:  deps.Config.Realtime.ReplayRetention,
				MaxEntries: deps.Config.Realtime.ReplayMaxEntries,
			})
			if err != nil {
				log.Printf("realtime replay disabled: %v", err)
			} else {
				realtimeBroker.AttachReplayStore(store, deps.Config.Realtime.ReplayStoreTimeout)
				go func() {
					<-appCtx.Done()
					_ = store.Close()
				}()
				log.Printf("realtime replay enabled with retention %s", deps.Config.Realtime.ReplayRetention)
			}
		}
		if bus, err := realtime.NewRedisEventBus(deps.Config.Redis.URL); err != nil {
			log.Printf("realtime redis disabled: %v", err)
		} else if err := realtimeBroker.AttachBus(appCtx, bus); err != nil {
			_ = bus.Close()
			log.Printf("realtime redis disabled: %v", err)
		} else {
			log.Printf("realtime redis enabled on channel %s", deps.Config.Realtime.RedisChannel)
		}
	}
	go presence.NewSweeper(deps.DB, realtimeBroker, presence.SweeperConfig{
		Interval:  deps.Config.Presence.SweepInterval,
		BatchSize: deps.Config.Presence.SweepBatchSize,
	}).Run(appCtx)
	go instances.NewRuntimeSweeper(deps.DB, realtimeBroker, instances.RuntimeSweeperConfig{
		Interval:  deps.Config.Presence.SweepInterval,
		BatchSize: deps.Config.Presence.SweepBatchSize,
	}).Run(appCtx)

	readinessChecks := []observability.Check{observability.DatabaseCheck(deps.DB)}
	if deps.Config.Redis.URL != "" {
		check, closeRedisCheck, err := observability.NewRedisCheck(deps.Config.Redis.URL)
		if err != nil {
			readinessChecks = append(readinessChecks, observability.Check{
				Name: "redis",
				Check: func(ctx context.Context) error {
					return err
				},
			})
		} else {
			readinessChecks = append(readinessChecks, check)
			go func() {
				<-appCtx.Done()
				_ = closeRedisCheck()
			}()
		}
	}

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	r.Get("/openapi.json", serveOpenAPI)
	r.Get("/readyz", observability.ReadinessHandler(observability.ReadinessConfig{
		Timeout: deps.Config.Observability.ReadinessTimeout,
		Checks:  readinessChecks,
	}).ServeHTTP)

	authHandler := auth.NewHandler(deps.DB, deps.Config, tokens)
	auth.RegisterRoutes(r, authHandler, authMiddleware)
	auth.RegisterBeeBaRoutes(r, authHandler)
	community.RegisterRoutes(r, deps.DB, deps.Config, realtimeBroker)
	profiles.RegisterRoutes(r, profiles.NewHandler(deps.DB), authMiddleware)
	relationships.RegisterRoutes(r, relationships.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	worlds.RegisterRoutes(r, worlds.NewHandler(deps.DB, deps.Config.Server.PublicURL), authMiddleware)
	events.RegisterRoutes(r, events.NewHandler(deps.DB, deps.Config.Server.PublicURL), authMiddleware)
	groups.RegisterRoutes(r, groups.NewHandler(deps.DB, deps.Config.Server.PublicURL, realtimeBroker, deps.Config.ActivityPub.ActorKeyEncryptionKey), authMiddleware)
	instances.RegisterRoutes(r, instances.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	invites.RegisterRoutes(r, invites.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	moderation.RegisterRoutes(r, moderation.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	notifications.RegisterRoutes(r, notifications.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	presence.RegisterRoutes(r, presence.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	realtimeHandler := realtime.NewHandler(realtimeBroker, deps.Config.Security.CORSAllowedOrigins...).
		SetReplayLimit(deps.Config.Realtime.ReplayLimit).
		SetPrivacyDB(deps.DB).
		SetSessionValidator(func(ctx context.Context, p auth.Principal) (bool, error) {
			return auth.PrincipalIsActive(ctx, deps.DB, p, linkedValidator)
		})
	realtime.RegisterRoutes(r, realtimeHandler, authMiddleware)
	assetcatalog.RegisterRoutes(r, assetcatalog.NewHandler(deps.DB, deps.Config.AssetCatalog), authMiddleware)
	activitypub.RegisterRoutes(r, activitypub.NewHandler(deps.DB, deps.Config))

	return r
}
