package activitypub

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
)

type ActorKeyPair struct {
	PublicKeyPEM  string
	PrivateKeyPEM string
}

func GenerateActorKeyPair() (ActorKeyPair, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return ActorKeyPair{}, err
	}

	privateDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateDER,
	})

	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return ActorKeyPair{}, err
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicDER,
	})

	return ActorKeyPair{
		PublicKeyPEM:  string(publicPEM),
		PrivateKeyPEM: string(privatePEM),
	}, nil
}
