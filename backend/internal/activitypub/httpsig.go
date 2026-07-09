package activitypub

import (
	"crypto"
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

func VerifyHTTPSignatureRequest(r *http.Request, body []byte, publicKeyPEM string, expectedActorURI string, now time.Time, maxSkew time.Duration) error {
	params, err := parseHTTPSignatureHeader(r.Header.Get("Signature"))
	if err != nil {
		return err
	}

	keyID := strings.TrimSpace(params["keyId"])
	if keyID == "" {
		return errors.New("Signature keyId is required")
	}
	if expectedActorURI != "" && keyID != expectedActorURI && !strings.HasPrefix(keyID, expectedActorURI+"#") {
		return fmt.Errorf("Signature keyId %q does not belong to actor %q", keyID, expectedActorURI)
	}

	if strings.ToLower(strings.TrimSpace(params["algorithm"])) != "rsa-sha256" {
		return errors.New("Signature algorithm must be rsa-sha256")
	}

	headers := strings.Fields(strings.ToLower(strings.TrimSpace(params["headers"])))
	if len(headers) == 0 {
		return errors.New("Signature headers are required")
	}
	for _, required := range []string{"(request-target)", "host", "date", "digest"} {
		if !containsHeader(headers, required) {
			return fmt.Errorf("Signature headers must include %s", required)
		}
	}

	if err := verifyDigestHeader(r.Header.Get("Digest"), body); err != nil {
		return err
	}
	if err := verifyDateHeader(r.Header.Get("Date"), now, maxSkew); err != nil {
		return err
	}

	signature, err := base64.StdEncoding.DecodeString(params["signature"])
	if err != nil || len(signature) == 0 {
		return errors.New("Signature signature value is invalid")
	}
	publicKey, err := parseRSAPublicKey(publicKeyPEM)
	if err != nil {
		return err
	}

	signingString, err := signingStringForRequest(r, headers)
	if err != nil {
		return err
	}
	hashed := sha256.Sum256([]byte(signingString))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, hashed[:], signature); err != nil {
		return fmt.Errorf("Signature verification failed: %w", err)
	}
	return nil
}

func parseHTTPSignatureHeader(header string) (map[string]string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, errors.New("Signature header is required")
	}
	params := map[string]string{}
	for _, part := range splitSignatureHeader(header) {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid Signature parameter %q", part)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"`)
		if key == "" || value == "" {
			return nil, fmt.Errorf("invalid Signature parameter %q", part)
		}
		params[key] = value
	}
	if params["signature"] == "" {
		return nil, errors.New("Signature signature value is required")
	}
	return params, nil
}

func splitSignatureHeader(header string) []string {
	parts := []string{}
	var current strings.Builder
	inQuote := false
	for _, r := range header {
		switch r {
		case '"':
			inQuote = !inQuote
			current.WriteRune(r)
		case ',':
			if inQuote {
				current.WriteRune(r)
				continue
			}
			if part := strings.TrimSpace(current.String()); part != "" {
				parts = append(parts, part)
			}
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if part := strings.TrimSpace(current.String()); part != "" {
		parts = append(parts, part)
	}
	return parts
}

func verifyDigestHeader(header string, body []byte) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return errors.New("Digest header is required")
	}
	expected := activityDigestHeader(body)
	if header != expected {
		return errors.New("Digest header does not match request body")
	}
	return nil
}

func verifyDateHeader(header string, now time.Time, maxSkew time.Duration) error {
	header = strings.TrimSpace(header)
	if header == "" {
		return errors.New("Date header is required")
	}
	parsed, err := http.ParseTime(header)
	if err != nil {
		return fmt.Errorf("Date header is invalid: %w", err)
	}
	if maxSkew <= 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if parsed.Before(now.Add(-maxSkew)) || parsed.After(now.Add(maxSkew)) {
		return errors.New("Date header is outside allowed clock skew")
	}
	return nil
}

func signingStringForRequest(r *http.Request, headers []string) (string, error) {
	lines := make([]string, 0, len(headers))
	for _, header := range headers {
		switch header {
		case "(request-target)":
			lines = append(lines, "(request-target): "+strings.ToLower(r.Method)+" "+r.URL.RequestURI())
		case "host":
			host := r.Host
			if host == "" {
				host = r.URL.Host
			}
			if host == "" {
				return "", errors.New("Host header is required")
			}
			lines = append(lines, "host: "+host)
		default:
			value := r.Header.Get(header)
			if value == "" {
				return "", fmt.Errorf("%s header is required", header)
			}
			lines = append(lines, header+": "+value)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func parseRSAPublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, errors.New("public key PEM is empty or invalid")
	}
	if key, err := x509.ParsePKIXPublicKey(block.Bytes); err == nil {
		publicKey, ok := key.(*rsa.PublicKey)
		if !ok {
			return nil, errors.New("public key is not RSA")
		}
		return publicKey, nil
	}
	publicKey, err := x509.ParsePKCS1PublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	return publicKey, nil
}

func activityDigestHeader(body []byte) string {
	sum := sha256.Sum256(body)
	return "SHA-256=" + base64.StdEncoding.EncodeToString(sum[:])
}

func containsHeader(headers []string, expected string) bool {
	for _, header := range headers {
		if header == expected {
			return true
		}
	}
	return false
}
