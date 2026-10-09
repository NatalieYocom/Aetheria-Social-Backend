package actorcrypto

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"
)

func fixturePEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}
func TestAuthenticatedWrappingRejectsTamperingWrongKeyAndActor(t *testing.T) {
	w, err := New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32))))
	if err != nil {
		t.Fatal(err)
	}
	private := fixturePEM(t)
	encrypted, err := w.Encrypt("https://social.test/users/alice", private)
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.Encrypt("https://social.test/users/alice", private)
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == second || strings.Contains(encrypted, "PRIVATE KEY") {
		t.Fatal("nonce uniqueness or confidentiality failure")
	}
	plain, err := w.Decrypt("https://social.test/users/alice", encrypted)
	if err != nil || plain != private {
		t.Fatal("round trip failed")
	}
	other, _ := New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32))))
	if _, err = other.Decrypt("https://social.test/users/alice", encrypted); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err = w.Decrypt("https://social.test/users/bob", encrypted); err == nil {
		t.Fatal("actor swap accepted")
	}
	raw, _ := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(encrypted, Prefix))
	raw[len(raw)-1] ^= 1
	for _, invalid := range []string{private, Prefix + base64.RawStdEncoding.EncodeToString(raw), "actor-key:v2:xxx", Prefix + "AA", ""} {
		if _, err = w.Decrypt("https://social.test/users/alice", invalid); err == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
}
func TestKeyConfigurationAndPrivatePEMValidation(t *testing.T) {
	for _, key := range []string{"", "not-base64", base64.StdEncoding.EncodeToString(make([]byte, 31)), base64.StdEncoding.EncodeToString(make([]byte, 33))} {
		if _, err := New(key); err == nil {
			t.Fatal("invalid key length accepted")
		}
	}
	w, _ := New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if _, err := w.Encrypt("https://social.test/actor", "not a private key"); err == nil {
		t.Fatal("invalid PEM accepted")
	}
}
