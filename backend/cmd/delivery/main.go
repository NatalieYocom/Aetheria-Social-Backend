package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"basisvr-social-service/internal/activitypub/delivery"
	"basisvr-social-service/internal/actorcrypto"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
	"basisvr-social-service/internal/observability"
)

func main() {
	cfg := config.Load()
	if _, err := actorcrypto.New(cfg.ActivityPub.ActorKeyEncryptionKey); err != nil {
		log.Fatal(err)
	}
	if cfg.Observability.TracingService == "basisvr-social-api" {
		cfg.Observability.TracingService = "basisvr-social-delivery"
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := database.Open(ctx, cfg.Database)
	cancel()
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	worker := delivery.NewWorker(
		db,
		&http.Client{Timeout: cfg.ActivityPub.FetchTimeout},
		cfg.ActivityPub.MaxDeliveryAttempts,
		cfg.ActivityPub.ActorKeyEncryptionKey,
	)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	log.Print("ActivityPub delivery worker started")
	for {
		select {
		case <-stop:
			log.Print("ActivityPub delivery worker stopped")
			return
		case <-ticker.C:
			deliverUntilIdle(context.Background(), worker)
		}
	}
}

func deliverUntilIdle(ctx context.Context, worker delivery.Worker) {
	for {
		delivered, err := worker.DeliverDueOne(ctx)
		if err != nil {
			log.Printf("deliver due job: %v", err)
			return
		}
		if !delivered {
			return
		}
	}
}
