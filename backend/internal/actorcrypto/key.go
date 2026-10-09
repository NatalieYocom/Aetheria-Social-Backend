// Package actorcrypto wraps federation signing keys at rest. The actor URI is authenticated
// as associated data so ciphertext cannot be moved to a different actor.
package actorcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
)

const Prefix = "actor-key:v1:"
const maxPEMBytes = 16 * 1024

var ErrKey = errors.New("ACTOR_KEY_ENCRYPTION_KEY must be base64-encoded 32 random bytes")
var ErrEnvelope = errors.New("actor private key is not a valid authenticated encrypted envelope; check the configured key and migration")
var ErrPrivateKey = errors.New("actor signing key must be a valid RSA private PEM")

type Wrapper struct{ aead cipher.AEAD }

func New(encodedKey string) (*Wrapper, error) {
	key, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(encodedKey))
	if err != nil || len(key) != 32 {
		return nil, ErrKey
	}
	block, err := aes.NewCipher(key)
	clear(key)
	if err != nil {
		return nil, ErrKey
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, ErrKey
	}
	return &Wrapper{aead: aead}, nil
}
func ValidatePrivateKey(value string) error {
	if len(value) > maxPEMBytes {
		return ErrPrivateKey
	}
	block, rest := pem.Decode([]byte(value))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return ErrPrivateKey
	}
	var key *rsa.PrivateKey
	if block.Type == "RSA PRIVATE KEY" {
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return ErrPrivateKey
		}
		key = parsed
	} else if block.Type == "PRIVATE KEY" {
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return ErrPrivateKey
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return ErrPrivateKey
		}
	} else {
		return ErrPrivateKey
	}
	if key.N.BitLen() < 2048 || key.Validate() != nil {
		return ErrPrivateKey
	}
	return nil
}
func (w *Wrapper) Encrypt(actorURI, privatePEM string) (string, error) {
	if actorURI == "" {
		return "", ErrEnvelope
	}
	if err := ValidatePrivateKey(privatePEM); err != nil {
		return "", err
	}
	ciphertext := w.aead.Seal(nil, nil, []byte(privatePEM), []byte(Prefix+actorURI))
	return Prefix + base64.RawStdEncoding.EncodeToString(ciphertext), nil
}
func (w *Wrapper) Decrypt(actorURI, envelope string) (string, error) {
	if actorURI == "" || !strings.HasPrefix(envelope, Prefix) || len(envelope) > 32*1024 {
		return "", ErrEnvelope
	}
	raw, err := base64.RawStdEncoding.Strict().DecodeString(strings.TrimPrefix(envelope, Prefix))
	if err != nil {
		return "", ErrEnvelope
	}
	plain, err := w.aead.Open(nil, nil, raw, []byte(Prefix+actorURI))
	if err != nil {
		return "", ErrEnvelope
	}
	defer clear(plain)
	if err := ValidatePrivateKey(string(plain)); err != nil {
		return "", ErrEnvelope
	}
	return string(plain), nil
}
func Encrypt(key, actorURI, privatePEM string) (string, error) {
	w, err := New(key)
	if err != nil {
		return "", err
	}
	return w.Encrypt(actorURI, privatePEM)
}
func Decrypt(key, actorURI, envelope string) (string, error) {
	w, err := New(key)
	if err != nil {
		return "", err
	}
	return w.Decrypt(actorURI, envelope)
}
