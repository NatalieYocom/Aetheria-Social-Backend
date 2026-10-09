package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"basisvr-social-service/internal/config"
	"github.com/google/uuid"
)

func TestBeeBaIdentityTimeoutDoesNotLeakTransportDetails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
			w.WriteHeader(200)
		}
	}))
	defer server.Close()
	start := time.Now()
	_, err := loadBeeBaIdentity(context.Background(), config.BeeBaConfig{Enabled: true, APIBaseURL: server.URL, SharedSecret: "sensitive-fixture-secret", Timeout: 20 * time.Millisecond}, uuid.NewString())
	var upstream *beeBaError
	if !errors.As(err, &upstream) || upstream.Status != 503 || upstream.Code != "identity_unavailable" {
		t.Fatalf("unexpected timeout error=%v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("identity request ignored timeout")
	}
}
