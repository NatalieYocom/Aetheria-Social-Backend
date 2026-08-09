package api

import (
	"context"
	"database/sql"
	"log"
	"net/http"

	"basisvr-social-service/internal/activitypub"
	"basisvr-social-service/internal/assetcatalog"
	"basisvr-social-service/internal/auth"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/events"
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
	r.Use(middleware.RealIP)
	if deps.Config.Observability.JSONLogsEnabled {
		r.Use(observability.RequestLogger(log.Writer()))
	} else {
		r.Use(middleware.Logger)
	}
	r.Use(middleware.Recoverer)
	r.Use(security.Headers)
	r.Use(security.BodyLimit(deps.Config.Security.MaxRequestBodyBytes))
	if deps.Config.Security.RateLimitEnabled {
		r.Use(security.RateLimit(security.NewFixedWindowLimiter(security.FixedWindowConfig{
			Limit:  deps.Config.Security.RateLimitRequests,
			Window: deps.Config.Security.RateLimitWindow,
		})))
	}
	tokens := auth.NewTokenManager(
		deps.Config.Auth.JWTSecret,
		deps.Config.Auth.AccessTokenTTL,
		deps.Config.Auth.RefreshTokenTTL,
	)
	authMiddleware := auth.Middleware(tokens)
	r.Use(auth.OptionalMiddleware(tokens))
	if deps.Config.Observability.MetricsEnabled {
		metrics := observability.NewMetrics()
		r.Use(metrics.Middleware)
		r.Get("/metrics", metrics.Handler().ServeHTTP)
	}

	realtimeBroker := realtime.NewBroker(realtime.BrokerConfig{
		BufferSize:     deps.Config.Realtime.BufferSize,
		BusBufferSize:  deps.Config.Realtime.BusBufferSize,
		BusTopic:       deps.Config.Realtime.RedisChannel,
		PublishTimeout: deps.Config.Realtime.RedisPublishTimeout,
	})
	if deps.Config.Redis.URL != "" {
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

	auth.RegisterRoutes(r, auth.NewHandler(deps.DB, deps.Config, tokens), authMiddleware)
	profiles.RegisterRoutes(r, profiles.NewHandler(deps.DB), authMiddleware)
	relationships.RegisterRoutes(r, relationships.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	worlds.RegisterRoutes(r, worlds.NewHandler(deps.DB, deps.Config.Server.PublicURL), authMiddleware)
	events.RegisterRoutes(r, events.NewHandler(deps.DB, deps.Config.Server.PublicURL), authMiddleware)
	instances.RegisterRoutes(r, instances.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	invites.RegisterRoutes(r, invites.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	moderation.RegisterRoutes(r, moderation.NewHandler(deps.DB), authMiddleware)
	notifications.RegisterRoutes(r, notifications.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	presence.RegisterRoutes(r, presence.NewHandler(deps.DB, realtimeBroker), authMiddleware)
	realtime.RegisterRoutes(r, realtime.NewHandler(realtimeBroker), authMiddleware)
	assetcatalog.RegisterRoutes(r, assetcatalog.NewHandler(deps.DB, deps.Config.AssetCatalog), authMiddleware)
	activitypub.RegisterRoutes(r, activitypub.NewHandler(deps.DB, deps.Config))

	return r
}
