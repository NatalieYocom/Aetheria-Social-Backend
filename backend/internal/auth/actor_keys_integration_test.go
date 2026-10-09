package auth

import (
	"basisvr-social-service/internal/activitypub"
	"basisvr-social-service/internal/actorcrypto"
	"basisvr-social-service/internal/config"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestActorKeyMigrationDryRunRepeatAndRollback(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	subject := sessionUser(t, db)
	uri := "https://example.test/users/" + subject.UserID
	pair, err := activitypub.GenerateActorKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE actors SET private_key_pem_encrypted=$2 WHERE id=$1`, subject.ActorID, pair.PrivateKeyPEM); err != nil {
		t.Fatal(err)
	}
	result, err := actorcrypto.Migrate(ctx, db, key, false)
	if err != nil || result.Legacy != 1 || result.Encrypted != 0 {
		t.Fatalf("dry run=%+v %v", result, err)
	}
	var stored string
	db.QueryRow(`SELECT private_key_pem_encrypted FROM actors WHERE id=$1`, subject.ActorID).Scan(&stored)
	if stored != pair.PrivateKeyPEM {
		t.Fatal("dry run changed key")
	}
	result, err = actorcrypto.Migrate(ctx, db, key, true)
	if err != nil || result.Encrypted != 1 {
		t.Fatalf("apply=%+v %v", result, err)
	}
	db.QueryRow(`SELECT private_key_pem_encrypted FROM actors WHERE id=$1`, subject.ActorID).Scan(&stored)
	decrypted, err := actorcrypto.Decrypt(key, uri, stored)
	if err != nil || decrypted != pair.PrivateKeyPEM {
		t.Fatal("migration did not preserve signing key")
	}
	result, err = actorcrypto.Migrate(ctx, db, key, true)
	if err != nil || result.Encrypted != 0 || result.Legacy != 0 {
		t.Fatalf("repeat=%+v %v", result, err)
	}
	if _, err = actorcrypto.Migrate(ctx, db, base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32))), true); err == nil {
		t.Fatal("wrong migration key accepted")
	}
	// Put a valid legacy row before malformed input in the same ordered batch: no partial commit.
	if _, err = db.Exec(`UPDATE actors SET private_key_pem_encrypted=$2 WHERE id=$1`, subject.ActorID, pair.PrivateKeyPEM); err != nil {
		t.Fatal(err)
	}
	other := sessionUser(t, db)
	if _, err = db.Exec(`UPDATE actors SET id='ffffffff-ffff-ffff-ffff-ffffffffffff', private_key_pem_encrypted='corrupt-value' WHERE id=$1`, other.ActorID); err != nil {
		t.Fatal(err)
	}
	if _, err = actorcrypto.Migrate(ctx, db, key, true); err == nil {
		t.Fatal("malformed key accepted")
	}
	db.QueryRow(`SELECT private_key_pem_encrypted FROM actors WHERE id=$1`, subject.ActorID).Scan(&stored)
	if stored != pair.PrivateKeyPEM {
		t.Fatal("failed batch partially committed")
	}
}

func TestLocalActorKeyEncryptionAndFailureRollback(t *testing.T) {
	db := sessionDB(t)
	ctx := context.Background()
	key := "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	cfg := config.Config{Server: config.ServerConfig{PublicURL: "https://social.example.test"}, ActivityPub: config.ActivityPubConfig{ActorKeyEncryptionKey: key}}
	h := NewHandler(db, cfg, NewTokenManager("test", time.Minute, time.Hour))
	user, err := h.createLocalUser(ctx, "fixture@example.test", "fixture", "Fixture", "fixture-only-password-hash")
	if err != nil {
		t.Fatal(err)
	}
	var uri, encrypted string
	if err = db.QueryRow(`SELECT actor_uri,private_key_pem_encrypted FROM actors WHERE local_user_id=$1`, user.ID).Scan(&uri, &encrypted); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(encrypted, actorcrypto.Prefix) || strings.Contains(encrypted, "PRIVATE KEY") {
		t.Fatal("plaintext signing key stored")
	}
	if _, err = actorcrypto.Decrypt(key, uri, encrypted); err != nil {
		t.Fatal("stored actor key cannot decrypt")
	}
	h.cfg.ActivityPub.ActorKeyEncryptionKey = "invalid"
	if _, err = h.createLocalUser(ctx, "failure@example.test", "failure", "Failure", "fixture-only-password-hash"); err == nil {
		t.Fatal("bad wrapping key accepted")
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM users WHERE username='failure'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed key wrapping left partial account")
	}
}
