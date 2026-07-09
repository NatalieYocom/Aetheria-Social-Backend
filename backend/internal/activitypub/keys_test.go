package activitypub

import "testing"

func TestGenerateActorKeyPairReturnsPEMEncodedKeys(t *testing.T) {
	pair, err := GenerateActorKeyPair()
	if err != nil {
		t.Fatalf("GenerateActorKeyPair returned error: %v", err)
	}
	if pair.PublicKeyPEM == "" || pair.PrivateKeyPEM == "" {
		t.Fatal("expected both public and private PEM values")
	}
	if pair.PublicKeyPEM[:26] != "-----BEGIN PUBLIC KEY-----" {
		t.Fatalf("unexpected public PEM prefix: %q", pair.PublicKeyPEM[:26])
	}
	if pair.PrivateKeyPEM[:31] != "-----BEGIN RSA PRIVATE KEY-----" {
		t.Fatalf("unexpected private PEM prefix: %q", pair.PrivateKeyPEM[:31])
	}
}
