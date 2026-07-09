package realtime

import (
	"context"

	"github.com/redis/go-redis/v9"
)

type RedisEventBus struct {
	client *redis.Client
}

func NewRedisEventBus(redisURL string) (*RedisEventBus, error) {
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return &RedisEventBus{client: redis.NewClient(options)}, nil
}

func (b *RedisEventBus) Publish(ctx context.Context, topic string, data []byte) error {
	return b.client.Publish(ctx, topic, data).Err()
}

func (b *RedisEventBus) Subscribe(ctx context.Context, topic string) (<-chan []byte, error) {
	pubsub := b.client.Subscribe(ctx, topic)
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, err
	}

	out := make(chan []byte, 128)
	messages := pubsub.Channel()
	go func() {
		defer close(out)
		defer pubsub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-messages:
				if !ok {
					return
				}
				payload := []byte(message.Payload)
				select {
				case out <- payload:
				default:
				}
			}
		}
	}()
	return out, nil
}

func (b *RedisEventBus) Close() error {
	return b.client.Close()
}
