package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
)

func main() {
	action := "up"
	if len(os.Args) > 1 {
		action = os.Args[1]
	}

	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	switch action {
	case "up":
		err = database.ApplyUp(ctx, db, "migrations")
	case "down":
		err = database.ApplyDown(ctx, db, "migrations")
	default:
		err = fmt.Errorf("unknown migration action %q; expected up or down", action)
	}
	if err != nil {
		log.Fatal(err)
	}
}
