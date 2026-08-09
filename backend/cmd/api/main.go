package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"basisvr-social-service/internal/activitypub"
	"basisvr-social-service/internal/api"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
	"basisvr-social-service/internal/observability"
)

func main() {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("configuration: %v", err)
	}
	tracingShutdown, err := observability.InitTracing(context.Background(), cfg.Observability)
	if err != nil {
		log.Fatalf("initialize tracing: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tracingShutdown(ctx); err != nil {
			log.Printf("shutdown tracing: %v", err)
		}
	}()
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := database.Open(ctx, cfg.Database)
	cancel()
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	if cfg.ActivityPub.Enabled {
		actorCtx, actorCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, err = activitypub.EnsureInstanceActor(actorCtx, db, cfg)
		actorCancel()
		if err != nil {
			log.Fatalf("ensure ActivityPub instance actor: %v", err)
		}
	}

	server := &http.Server{
		Addr:              cfg.Server.Bind,
		Handler:           api.NewRouter(api.Deps{DB: db, Config: cfg, Context: appCtx}),
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.MaxHeaderBytes,
	}

	go func() {
		log.Printf("Basis Social API listening on %s", cfg.Server.Bind)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	appCancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
