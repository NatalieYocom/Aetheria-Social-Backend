// actor-keys is an explicit operator migration, never run automatically by API startup.
package main

import (
	"basisvr-social-service/internal/actorcrypto"
	"basisvr-social-service/internal/config"
	"basisvr-social-service/internal/database"
	"context"
	"flag"
	"fmt"
	"log"
	"time"
)

func main() {
	apply := flag.Bool("apply", false, "encrypt legacy actor keys; default verifies and reports only")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected arguments; use -apply to migrate")
	}
	cfg := config.Load()
	if _, err := actorcrypto.New(cfg.ActivityPub.ActorKeyEncryptionKey); err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	db, err := database.Open(ctx, cfg.Database)
	if err != nil {
		log.Fatal("could not open actor-key migration database")
	}
	defer db.Close()
	result, err := actorcrypto.Migrate(ctx, db, cfg.ActivityPub.ActorKeyEncryptionKey, *apply)
	if err != nil {
		log.Fatalf("actor-key migration failed (checked=%d encrypted=%d): %v", result.Checked, result.Encrypted, err)
	}
	fmt.Printf("actor keys checked=%d legacy=%d encrypted=%d apply=%t\n", result.Checked, result.Legacy, result.Encrypted, *apply)
}
