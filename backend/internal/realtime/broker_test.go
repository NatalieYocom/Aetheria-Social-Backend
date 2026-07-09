package realtime

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBrokerPublishesEventToActorSubscriber(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 2})
	actorID := uuid.New()
	events, unsubscribe := broker.Subscribe(context.Background(), actorID)
	defer unsubscribe()

	broker.Publish(actorID, Event{
		Type:    "presence.updated",
		ActorID: actorID,
		Payload: map[string]any{"status": "online"},
	})

	select {
	case event := <-events:
		if event.Type != "presence.updated" {
			t.Fatalf("event.Type = %q", event.Type)
		}
		if event.ActorID != actorID {
			t.Fatalf("event.ActorID = %s", event.ActorID)
		}
		if event.ID == "" {
			t.Fatal("event.ID is empty")
		}
		if event.CreatedAt.IsZero() {
			t.Fatal("event.CreatedAt is zero")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}
}

func TestBrokerDoesNotDeliverToDifferentActor(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 2})
	actorID := uuid.New()
	otherActorID := uuid.New()
	events, unsubscribe := broker.Subscribe(context.Background(), actorID)
	defer unsubscribe()

	broker.Publish(otherActorID, Event{
		Type:    "presence.updated",
		ActorID: otherActorID,
	})

	select {
	case event := <-events:
		t.Fatalf("unexpected event delivered: %#v", event)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestBrokerPublishDoesNotBlockSlowSubscriber(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 1})
	actorID := uuid.New()
	_, unsubscribe := broker.Subscribe(context.Background(), actorID)
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			broker.Publish(actorID, Event{Type: "presence.updated", ActorID: actorID})
		}
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Publish blocked behind a slow subscriber")
	}
}

func TestBrokerPublishesThroughBusToAnotherBroker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := newMemoryBus()
	actorID := uuid.New()
	brokerA := NewBroker(BrokerConfig{BufferSize: 4, OriginID: "broker-a"})
	brokerB := NewBroker(BrokerConfig{BufferSize: 4, OriginID: "broker-b"})
	if err := brokerA.AttachBus(ctx, bus); err != nil {
		t.Fatalf("brokerA.AttachBus: %v", err)
	}
	if err := brokerB.AttachBus(ctx, bus); err != nil {
		t.Fatalf("brokerB.AttachBus: %v", err)
	}
	events, unsubscribe := brokerB.Subscribe(ctx, actorID)
	defer unsubscribe()

	brokerA.Publish(actorID, Event{
		Type:    "friend.requested",
		ActorID: uuid.New(),
	})

	select {
	case event := <-events:
		if event.Type != "friend.requested" {
			t.Fatalf("event.Type = %q", event.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for remote broker event")
	}
}

func TestBrokerIgnoresOwnBusEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := newMemoryBus()
	actorID := uuid.New()
	broker := NewBroker(BrokerConfig{BufferSize: 4, OriginID: "broker-a"})
	if err := broker.AttachBus(ctx, bus); err != nil {
		t.Fatalf("AttachBus: %v", err)
	}
	events, unsubscribe := broker.Subscribe(ctx, actorID)
	defer unsubscribe()

	broker.Publish(actorID, Event{
		Type:    "presence.updated",
		ActorID: actorID,
	})

	select {
	case event := <-events:
		if event.Type != "presence.updated" {
			t.Fatalf("event.Type = %q", event.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for local event")
	}
	select {
	case event := <-events:
		t.Fatalf("unexpected duplicate event from own bus echo: %#v", event)
	case <-time.After(40 * time.Millisecond):
	}
}

type memoryBus struct {
	subscribe chan chan []byte
	publish   chan []byte
}

func newMemoryBus() *memoryBus {
	bus := &memoryBus{
		subscribe: make(chan chan []byte, 8),
		publish:   make(chan []byte, 8),
	}
	go bus.run()
	return bus
}

func (b *memoryBus) Publish(ctx context.Context, topic string, data []byte) error {
	select {
	case b.publish <- append([]byte(nil), data...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *memoryBus) Subscribe(ctx context.Context, topic string) (<-chan []byte, error) {
	ch := make(chan []byte, 8)
	select {
	case b.subscribe <- ch:
		return ch, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *memoryBus) Close() error {
	return nil
}

func (b *memoryBus) run() {
	subscribers := []chan []byte{}
	for {
		select {
		case ch := <-b.subscribe:
			subscribers = append(subscribers, ch)
		case data := <-b.publish:
			for _, ch := range subscribers {
				select {
				case ch <- append([]byte(nil), data...):
				default:
				}
			}
		}
	}
}
