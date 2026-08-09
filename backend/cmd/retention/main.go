package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
	"basisvr-social-service/internal/retention"
)

func main() {
	cfg := config.Load()
	openCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := database.Open(openCtx, cfg.Database)
	cancel()
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	retention.NewService(db, cfg.Retention).Run(ctx)
}
