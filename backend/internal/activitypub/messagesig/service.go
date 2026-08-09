package messagesig

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/htmsig"
	"github.com/lestrrat-go/htmsig/component"
	"github.com/lestrrat-go/htmsig/input"
)

const signatureLabel = "sig1"

func SignRequest(req *http.Request, body []byte, keyID string, privateKey *rsa.PrivateKey, now time.Time) error {
	if req == nil || privateKey == nil || strings.TrimSpace(keyID) == "" {
		return errors.New("request, key ID and private key are required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	req.Header.Set("Content-Digest", contentDigest(body))
	definition, err := input.NewDefinitionBuilder().
		Label(signatureLabel).
		Components(component.Method(), component.TargetURI(), component.New("content-digest")).
		CreatedTime(now).
		KeyID(keyID).
		Algorithm(htmsig.AlgorithmRSAV15SHA256).
		Build()
	if err != nil {
		return err
	}
	ctx := component.WithRequestInfoFromHTTP(req.Context(), req)
	return htmsig.SignRequest(ctx, req.Header, input.NewValueBuilder().AddDefinition(definition).MustBuild(), privateKey)
}

func SignRequestPEM(req *http.Request, body []byte, keyID string, privateKeyPEM string, now time.Time) error {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return errors.New("private key PEM is empty or invalid")
	}
	privateKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return fmt.Errorf("parse private key: %w", parseErr)
		}
		var ok bool
		privateKey, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return errors.New("private key is not RSA")
		}
	}
	return SignRequest(req, body, keyID, privateKey, now)
}

func VerifyRequest(req *http.Request, body []byte, publicKey *rsa.PublicKey, expectedKeyID string, now time.Time, maxAge time.Duration) error {
	if req == nil || publicKey == nil || strings.TrimSpace(expectedKeyID) == "" {
		return errors.New("request, expected key ID and public key are required")
	}
	if subtle.ConstantTimeCompare([]byte(req.Header.Get("Content-Digest")), []byte(contentDigest(body))) != 1 {
		return errors.New("Content-Digest does not match request body")
	}
	parsed, err := input.Parse([]byte(req.Header.Get(htmsig.SignatureInputHeader)))
	if err != nil {
		return fmt.Errorf("parse Signature-Input: %w", err)
	}
	if parsed.Len() != 1 {
		return errors.New("exactly one HTTP message signature is required")
	}
	definition := parsed.Definitions()[0]
	if definition.KeyID() != expectedKeyID {
		return fmt.Errorf("signature key ID %q does not match expected key ID", definition.KeyID())
	}
	if definition.Algorithm() != htmsig.AlgorithmRSAV15SHA256 {
		return fmt.Errorf("unsupported signature algorithm %q", definition.Algorithm())
	}
	for _, required := range []string{"@method", "@target-uri", "content-digest"} {
		if !hasComponent(definition, required) {
			return fmt.Errorf("signature must cover %s", required)
		}
	}
	created, ok := definition.Created()
	if !ok {
		return errors.New("signature created parameter is required")
	}
	if now.IsZero() {
		now = time.Now()
	}
	createdAt := time.Unix(created, 0)
	if maxAge > 0 && (createdAt.Before(now.Add(-maxAge)) || createdAt.After(now.Add(maxAge))) {
		return errors.New("signature created parameter is outside allowed clock skew")
	}
	ctx := component.WithRequestInfoFromHTTP(context.Background(), req)
	return htmsig.VerifyRequest(ctx, req.Header, &keyResolver{keyID: expectedKeyID, key: publicKey})
}

func KeyID(req *http.Request) (string, error) {
	if req == nil {
		return "", errors.New("request is required")
	}
	parsed, err := input.Parse([]byte(req.Header.Get(htmsig.SignatureInputHeader)))
	if err != nil {
		return "", fmt.Errorf("parse Signature-Input: %w", err)
	}
	if parsed.Len() != 1 {
		return "", errors.New("exactly one HTTP message signature is required")
	}
	keyID := strings.TrimSpace(parsed.Definitions()[0].KeyID())
	if keyID == "" {
		return "", errors.New("signature keyid parameter is required")
	}
	return keyID, nil
}

type keyResolver struct {
	keyID string
	key   *rsa.PublicKey
}

func (r *keyResolver) ResolveKey(keyID string) (any, error) {
	if keyID != r.keyID {
		return nil, fmt.Errorf("key %q is not allowed", keyID)
	}
	return r.key, nil
}

func hasComponent(definition *input.Definition, expected string) bool {
	for _, identifier := range definition.Components() {
		if strings.EqualFold(identifier.Name(), expected) {
			return true
		}
	}
	return false
}

func contentDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha-256=:" + base64.StdEncoding.EncodeToString(sum[:]) + ":"
}
