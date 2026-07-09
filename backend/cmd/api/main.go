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

	"basisvr-social-service/internal/api"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
)

func main() {
	cfg := config.Load()
	appCtx, appCancel := context.WithCancel(context.Background())
	defer appCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	db, err := database.Open(ctx, cfg.Database)
	cancel()
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	server := &http.Server{
		Addr:              cfg.Server.Bind,
		Handler:           api.NewRouter(api.Deps{DB: db, Config: cfg, Context: appCtx}),
		ReadHeaderTimeout: 5 * time.Second,
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
