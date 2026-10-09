package activitypub

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"basisvr-social-service/internal/activitypub/legacysig"
	"basisvr-social-service/internal/activitypub/messagesig"
	"basisvr-social-service/internal/actorcrypto"
	"basisvr-social-service/internal/config"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type InstanceActor struct {
	ActorURI      string
	PublicKeyPEM  string
	PrivateKeyPEM string
}

func NewInstanceSignedClient(db *sql.DB, cfg config.Config, base http.RoundTripper) *http.Client {
	if base == nil {
		base = otelhttp.NewTransport(http.DefaultTransport)
	}
	timeout := cfg.ActivityPub.FetchTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &instanceSigningTransport{db: db, cfg: cfg, base: base},
	}
}

type instanceSigningTransport struct {
	db   *sql.DB
	cfg  config.Config
	base http.RoundTripper
}

func (t *instanceSigningTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		return nil, fmt.Errorf("instance signing transport only supports GET and HEAD, got %s", req.Method)
	}
	actorURI := strings.TrimRight(t.cfg.Server.PublicURL, "/") + "/actor"
	actor, err := loadInstanceActor(req.Context(), t.db, actorURI, t.cfg.ActivityPub.ActorKeyEncryptionKey)
	if err != nil {
		return nil, fmt.Errorf("load instance actor: %w", err)
	}
	privateKey, err := parseInstancePrivateKey(actor.PrivateKeyPEM)
	if err != nil {
		return nil, err
	}
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	if err := messagesig.SignRequest(cloned, nil, actor.ActorURI+"#main-key", privateKey, time.Now()); err != nil {
		return nil, fmt.Errorf("sign instance request: %w", err)
	}
	response, err := t.base.RoundTrip(cloned)
	if err != nil || response == nil || (response.StatusCode != http.StatusBadRequest && response.StatusCode != http.StatusUnauthorized) {
		return response, err
	}
	_ = response.Body.Close()
	legacy := req.Clone(req.Context())
	legacy.Header = req.Header.Clone()
	if err := legacysig.SignRequest(legacy, actor.ActorURI, actor.PrivateKeyPEM, nil, time.Now()); err != nil {
		return nil, fmt.Errorf("sign legacy instance request: %w", err)
	}
	return t.base.RoundTrip(legacy)
}

func parseInstancePrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("instance private key PEM is invalid")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse instance private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("instance private key is not RSA")
	}
	return key, nil
}

func EnsureInstanceActor(ctx context.Context, db *sql.DB, cfg config.Config) (InstanceActor, error) {
	baseURL := strings.TrimRight(cfg.Server.PublicURL, "/")
	actorURI := baseURL + "/actor"
	actor, err := loadInstanceActor(ctx, db, actorURI, cfg.ActivityPub.ActorKeyEncryptionKey)
	if err == nil {
		return actor, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return InstanceActor{}, err
	}

	keyPair, err := GenerateActorKeyPair()
	if err != nil {
		return InstanceActor{}, fmt.Errorf("generate instance actor key: %w", err)
	}
	encrypted, err := actorcrypto.Encrypt(cfg.ActivityPub.ActorKeyEncryptionKey, actorURI, keyPair.PrivateKeyPEM)
	if err != nil {
		return InstanceActor{}, err
	}
	domain := strings.TrimSpace(cfg.ActivityPub.Domain)
	if domain == "" {
		domain = hostFromURL(baseURL)
	}
	err = db.QueryRowContext(ctx, `
INSERT INTO actors (
  actor_uri, acct, type, preferred_username, display_name, domain,
  inbox_url, outbox_url, followers_url, following_url, shared_inbox_url,
  public_key_pem, private_key_pem_encrypted, is_local, raw_json
)
VALUES ($1, $2, 'Service', 'instance', 'BasisVR Social', $3, $4, $5, $6, $7, $8, $9, $10, true, '{}'::jsonb)
ON CONFLICT (actor_uri) DO UPDATE SET actor_uri = EXCLUDED.actor_uri
RETURNING actor_uri, public_key_pem, private_key_pem_encrypted`,
		actorURI, "instance@"+domain, domain,
		actorURI+"/inbox", actorURI+"/outbox", actorURI+"/followers", actorURI+"/following",
		baseURL+"/inbox", keyPair.PublicKeyPEM, encrypted,
	).Scan(&actor.ActorURI, &actor.PublicKeyPEM, &actor.PrivateKeyPEM)
	if err != nil {
		return InstanceActor{}, fmt.Errorf("create instance actor: %w", err)
	}
	actor.PrivateKeyPEM, err = actorcrypto.Decrypt(cfg.ActivityPub.ActorKeyEncryptionKey, actor.ActorURI, actor.PrivateKeyPEM)
	if err != nil {
		return InstanceActor{}, err
	}
	return actor, nil
}

func loadInstanceActor(ctx context.Context, db *sql.DB, actorURI, encryptionKey string) (InstanceActor, error) {
	var actor InstanceActor
	err := db.QueryRowContext(ctx, `
SELECT actor_uri, public_key_pem, private_key_pem_encrypted
FROM actors
WHERE actor_uri = $1 AND is_local = true AND type = 'Service'`, actorURI).
		Scan(&actor.ActorURI, &actor.PublicKeyPEM, &actor.PrivateKeyPEM)
	if err != nil {
		return InstanceActor{}, err
	}
	actor.PrivateKeyPEM, err = actorcrypto.Decrypt(encryptionKey, actor.ActorURI, actor.PrivateKeyPEM)
	if err != nil {
		return InstanceActor{}, err
	}
	return actor, nil
}
