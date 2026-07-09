package realtime

import "testing"

func TestNewRedisEventBusAcceptsRedisURL(t *testing.T) {
	bus, err := NewRedisEventBus("redis://localhost:6379/0")
	if err != nil {
		t.Fatalf("NewRedisEventBus returned error: %v", err)
	}
	if bus == nil {
		t.Fatal("NewRedisEventBus returned nil bus")
	}
	if err := bus.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}

func TestNewRedisEventBusRejectsInvalidURL(t *testing.T) {
	if _, err := NewRedisEventBus("://bad"); err == nil {
		t.Fatal("expected invalid URL error")
	}
}
