package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type stubReplayStore struct {
	append func(context.Context, uuid.UUID, Event) (Event, error)
	replay func(context.Context, uuid.UUID, string, int) (ReplayResult, error)
}

func (s stubReplayStore) Append(ctx context.Context, actorID uuid.UUID, event Event) (Event, error) {
	return s.append(ctx, actorID, event)
}

func (s stubReplayStore) Replay(ctx context.Context, actorID uuid.UUID, after string, limit int) (ReplayResult, error) {
	return s.replay(ctx, actorID, after, limit)
}

func TestEventSerializesReplayCursor(t *testing.T) {
	data, err := json.Marshal(Event{ID: uuid.NewString(), Cursor: "1710000000000-2", Type: "presence.updated"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"cursor":"1710000000000-2"`) {
		t.Fatalf("event JSON = %s", data)
	}
}

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

func TestBrokerStoresEventBeforeLocalFanout(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 2})
	actorID := uuid.New()
	stored := false
	broker.AttachReplayStore(stubReplayStore{
		append: func(_ context.Context, gotActorID uuid.UUID, event Event) (Event, error) {
			if gotActorID != actorID {
				t.Fatalf("actorID = %s", gotActorID)
			}
			stored = true
			event.Cursor = "1710000000000-1"
			return event, nil
		},
	}, 100*time.Millisecond)
	events, unsubscribe := broker.Subscribe(context.Background(), actorID)
	defer unsubscribe()

	broker.Publish(actorID, Event{Type: "presence.updated"})

	event := <-events
	if !stored {
		t.Fatal("event was delivered before replay persistence")
	}
	if event.Cursor != "1710000000000-1" {
		t.Fatalf("event.Cursor = %q", event.Cursor)
	}
	stats := broker.Stats()
	if stats.ReplayStored != 1 || stats.ReplayStoreFailures != 0 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestBrokerContinuesLiveFanoutWhenReplayStoreFails(t *testing.T) {
	broker := NewBroker(BrokerConfig{BufferSize: 2})
	actorID := uuid.New()
	broker.AttachReplayStore(stubReplayStore{
		append: func(context.Context, uuid.UUID, Event) (Event, error) {
			return Event{}, errors.New("redis unavailable")
		},
	}, 100*time.Millisecond)
	events, unsubscribe := broker.Subscribe(context.Background(), actorID)
	defer unsubscribe()

	broker.Publish(actorID, Event{Type: "presence.updated"})

	select {
	case event := <-events:
		if event.Type != "presence.updated" {
			t.Fatalf("event.Type = %q", event.Type)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("live fanout stopped after replay store failure")
	}
	if broker.Stats().ReplayStoreFailures != 1 {
		t.Fatalf("stats = %+v", broker.Stats())
	}
}

func TestBrokerReplaysStoredEvents(t *testing.T) {
	broker := NewBroker(BrokerConfig{})
	actorID := uuid.New()
	broker.AttachReplayStore(stubReplayStore{
		replay: func(_ context.Context, gotActorID uuid.UUID, after string, limit int) (ReplayResult, error) {
			if gotActorID != actorID || after != "1710000000000-0" || limit != 50 {
				t.Fatalf("Replay(%s, %q, %d)", gotActorID, after, limit)
			}
			return ReplayResult{Events: []Event{{Cursor: "1710000000001-0", Type: "presence.updated"}}}, nil
		},
	}, 100*time.Millisecond)

	result, err := broker.Replay(context.Background(), actorID, "1710000000000-0", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Cursor != "1710000000001-0" {
		t.Fatalf("result = %+v", result)
	}
	if broker.Stats().ReplayDelivered != 1 || broker.Stats().ReplayRequests != 1 {
		t.Fatalf("stats = %+v", broker.Stats())
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
	subscribe chan memorySubscription
	publish   chan []byte
}

type memorySubscription struct {
	messages chan []byte
	ready    chan struct{}
}

func newMemoryBus() *memoryBus {
	bus := &memoryBus{
		subscribe: make(chan memorySubscription, 8),
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
	subscription := memorySubscription{
		messages: ch,
		ready:    make(chan struct{}),
	}
	select {
	case b.subscribe <- subscription:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-subscription.ready:
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
		case subscription := <-b.subscribe:
			subscribers = append(subscribers, subscription.messages)
			close(subscription.ready)
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
