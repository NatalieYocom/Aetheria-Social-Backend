package observability

import (
	"context"
	"testing"

	"basisvr-social-service/internal/config"
)

func TestInitTracingDisabledIsNoOp(t *testing.T) {
	shutdown, err := InitTracing(context.Background(), config.ObservabilityConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInitTracingRejectsInvalidSampleRatio(t *testing.T) {
	_, err := InitTracing(context.Background(), config.ObservabilityConfig{
		TracingEnabled: true, TracingSampleRatio: 1.5,
	})
	if err == nil {
		t.Fatal("expected invalid sample ratio error")
	}
}
