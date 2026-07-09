package realtime

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/google/uuid"
)

type BrokerConfig struct {
	BufferSize     int
	BusBufferSize  int
	BusTopic       string
	OriginID       string
	PublishTimeout time.Duration
}

type EventBus interface {
	Publish(ctx context.Context, topic string, data []byte) error
	Subscribe(ctx context.Context, topic string) (<-chan []byte, error)
	Close() error
}

type Event struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	ActorID   uuid.UUID `json:"actorId"`
	Payload   any       `json:"payload,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type Broker struct {
	mu          sync.RWMutex
	nextSubID   uint64
	bufferSize  int
	busTopic    string
	originID    string
	busTimeout  time.Duration
	busQueue    chan []byte
	busEnabled  bool
	subscribers map[uuid.UUID]map[uint64]chan Event
}

type busEnvelope struct {
	OriginID    string    `json:"originId"`
	RecipientID uuid.UUID `json:"recipientId"`
	Event       Event     `json:"event"`
}

func NewBroker(config BrokerConfig) *Broker {
	bufferSize := config.BufferSize
	if bufferSize <= 0 {
		bufferSize = 64
	}
	busBufferSize := config.BusBufferSize
	if busBufferSize <= 0 {
		busBufferSize = bufferSize * 4
	}
	busTopic := config.BusTopic
	if busTopic == "" {
		busTopic = "basisvr:realtime:v1"
	}
	originID := config.OriginID
	if originID == "" {
		originID = uuid.NewString()
	}
	publishTimeout := config.PublishTimeout
	if publishTimeout <= 0 {
		publishTimeout = 2 * time.Second
	}
	return &Broker{
		bufferSize:  bufferSize,
		busTopic:    busTopic,
		originID:    originID,
		busTimeout:  publishTimeout,
		busQueue:    make(chan []byte, busBufferSize),
		subscribers: make(map[uuid.UUID]map[uint64]chan Event),
	}
}

func (b *Broker) Subscribe(ctx context.Context, actorID uuid.UUID) (<-chan Event, func()) {
	if b == nil {
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}

	ch := make(chan Event, b.bufferSize)
	b.mu.Lock()
	b.nextSubID++
	subID := b.nextSubID
	if b.subscribers[actorID] == nil {
		b.subscribers[actorID] = make(map[uint64]chan Event)
	}
	b.subscribers[actorID][subID] = ch
	b.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			if actorSubscribers := b.subscribers[actorID]; actorSubscribers != nil {
				if existing, ok := actorSubscribers[subID]; ok {
					delete(actorSubscribers, subID)
					close(existing)
				}
				if len(actorSubscribers) == 0 {
					delete(b.subscribers, actorID)
				}
			}
			b.mu.Unlock()
		})
	}

	go func() {
		<-ctx.Done()
		unsubscribe()
	}()

	return ch, unsubscribe
}

func (b *Broker) Publish(actorID uuid.UUID, event Event) {
	if b == nil || actorID == uuid.Nil {
		return
	}
	event = normalizeEvent(actorID, event)
	b.publishLocal(actorID, event)
	b.queueBusPublish(actorID, event)
}

func (b *Broker) AttachBus(ctx context.Context, bus EventBus) error {
	if b == nil || bus == nil {
		return nil
	}
	incoming, err := bus.Subscribe(ctx, b.busTopic)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.busEnabled = true
	b.mu.Unlock()
	go b.runBusPublisher(ctx, bus)
	go b.runBusSubscriber(ctx, incoming)
	return nil
}

func normalizeEvent(actorID uuid.UUID, event Event) Event {
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.ActorID == uuid.Nil {
		event.ActorID = actorID
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	return event
}

func (b *Broker) publishLocal(actorID uuid.UUID, event Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers[actorID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func (b *Broker) queueBusPublish(actorID uuid.UUID, event Event) {
	b.mu.RLock()
	enabled := b.busEnabled
	b.mu.RUnlock()
	if !enabled {
		return
	}
	envelope := busEnvelope{
		OriginID:    b.originID,
		RecipientID: actorID,
		Event:       event,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return
	}
	select {
	case b.busQueue <- data:
	default:
	}
}

func (b *Broker) runBusPublisher(ctx context.Context, bus EventBus) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-b.busQueue:
			publishCtx, cancel := context.WithTimeout(ctx, b.busTimeout)
			_ = bus.Publish(publishCtx, b.busTopic, data)
			cancel()
		}
	}
}

func (b *Broker) runBusSubscriber(ctx context.Context, incoming <-chan []byte) {
	for {
		select {
		case <-ctx.Done():
			return
		case data, ok := <-incoming:
			if !ok {
				return
			}
			var envelope busEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				continue
			}
			if envelope.OriginID == b.originID || envelope.RecipientID == uuid.Nil {
				continue
			}
			b.publishLocal(envelope.RecipientID, normalizeEvent(envelope.RecipientID, envelope.Event))
		}
	}
}
