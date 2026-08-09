package legacysig

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func SignRequest(req *http.Request, actorURI string, privateKeyPEM string, body []byte, now time.Time) error {
	privateKey, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return err
	}
	if now.IsZero() {
		now = time.Now()
	}
	req.Header.Set("Date", now.UTC().Format(http.TimeFormat))
	headers := []string{"(request-target)", "host", "date"}
	signingLines := []string{
		"(request-target): " + strings.ToLower(req.Method) + " " + req.URL.RequestURI(),
		"host: " + req.URL.Host,
		"date: " + req.Header.Get("Date"),
	}
	if (req.Method != http.MethodGet && req.Method != http.MethodHead) || len(body) > 0 {
		req.Header.Set("Digest", digestHeader(body))
		headers = append(headers, "digest")
		signingLines = append(signingLines, "digest: "+req.Header.Get("Digest"))
	} else {
		req.Header.Del("Digest")
	}

	hashed := sha256.Sum256([]byte(strings.Join(signingLines, "\n")))
	signatureBytes, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return err
	}
	req.Header.Set("Signature", fmt.Sprintf(
		`keyId="%s#main-key",algorithm="rsa-sha256",headers="%s",signature="%s"`,
		actorURI, strings.Join(headers, " "), base64.StdEncoding.EncodeToString(signatureBytes),
	))
	return nil
}

func digestHeader(body []byte) string {
	sum := sha256.Sum256(body)
	return "SHA-256=" + base64.StdEncoding.EncodeToString(sum[:])
}

func parseRSAPrivateKey(value string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(value))
	if block == nil {
		return nil, errors.New("private key PEM is empty or invalid")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}
