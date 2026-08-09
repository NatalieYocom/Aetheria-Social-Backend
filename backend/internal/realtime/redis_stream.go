package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

var ErrInvalidReplayCursor = errors.New("invalid realtime replay cursor")

var replayCursorPattern = regexp.MustCompile(`^[0-9]+-[0-9]+$`)

type ReplayStoreConfig struct {
	Prefix     string
	Retention  time.Duration
	MaxEntries int64
}

type ReplayResult struct {
	Events    []Event
	Truncated bool
}

type ReplayStore interface {
	Append(ctx context.Context, actorID uuid.UUID, event Event) (Event, error)
	Replay(ctx context.Context, actorID uuid.UUID, after string, limit int) (ReplayResult, error)
}

type RedisReplayStore struct {
	client     *redis.Client
	prefix     string
	retention  time.Duration
	maxEntries int64
}

func NewRedisReplayStore(redisURL string, cfg ReplayStoreConfig) (*RedisReplayStore, error) {
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(strings.TrimSpace(cfg.Prefix), ":")
	if prefix == "" {
		prefix = "basisvr:realtime"
	}
	retention := cfg.Retention
	if retention <= 0 {
		retention = 24 * time.Hour
	}
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = 10000
	}
	return &RedisReplayStore{
		client: redis.NewClient(options), prefix: prefix, retention: retention, maxEntries: maxEntries,
	}, nil
}

func (s *RedisReplayStore) Append(ctx context.Context, actorID uuid.UUID, event Event) (Event, error) {
	if actorID == uuid.Nil {
		return Event{}, errors.New("replay actor id is required")
	}
	event = normalizeEvent(actorID, event)
	event.Cursor = ""
	raw, err := json.Marshal(event)
	if err != nil {
		return Event{}, err
	}
	key := s.actorKey(actorID)
	minimumID := fmt.Sprintf("%d-0", time.Now().UTC().Add(-s.retention).UnixMilli())
	var add *redis.StringCmd
	_, err = s.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		add = pipe.XAdd(ctx, &redis.XAddArgs{
			Stream: key, MaxLen: s.maxEntries, Approx: true, ID: "*",
			Values: map[string]any{"event": string(raw)},
		})
		pipe.XTrimMinIDApprox(ctx, key, minimumID, 0)
		pipe.Expire(ctx, key, s.retention)
		return nil
	})
	if err != nil {
		return Event{}, err
	}
	event.Cursor = add.Val()
	return event, nil
}

func (s *RedisReplayStore) Replay(ctx context.Context, actorID uuid.UUID, after string, limit int) (ReplayResult, error) {
	if !validReplayCursor(after) {
		return ReplayResult{}, ErrInvalidReplayCursor
	}
	if limit <= 0 {
		limit = 500
	}
	key := s.actorKey(actorID)
	first, err := s.client.XRangeN(ctx, key, "-", "+", 1).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return ReplayResult{}, err
	}
	if len(first) > 0 && after != "0-0" && compareStreamIDs(after, first[0].ID) < 0 {
		return ReplayResult{Events: []Event{}, Truncated: true}, nil
	}
	messages, err := s.client.XRangeN(ctx, key, "("+after, "+", int64(limit)).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return ReplayResult{}, err
	}
	events := make([]Event, 0, len(messages))
	for _, message := range messages {
		raw, ok := message.Values["event"]
		if !ok {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(fmt.Sprint(raw)), &event); err != nil {
			continue
		}
		event.Cursor = message.ID
		events = append(events, event)
	}
	return ReplayResult{Events: events}, nil
}

func (s *RedisReplayStore) Close() error {
	return s.client.Close()
}

func (s *RedisReplayStore) actorKey(actorID uuid.UUID) string {
	return s.prefix + ":{" + actorID.String() + "}"
}

func validReplayCursor(value string) bool {
	return replayCursorPattern.MatchString(strings.TrimSpace(value))
}

func compareStreamIDs(left, right string) int {
	leftParts := strings.SplitN(left, "-", 2)
	rightParts := strings.SplitN(right, "-", 2)
	leftMS, _ := strconv.ParseUint(leftParts[0], 10, 64)
	rightMS, _ := strconv.ParseUint(rightParts[0], 10, 64)
	if leftMS < rightMS {
		return -1
	}
	if leftMS > rightMS {
		return 1
	}
	leftSequence, _ := strconv.ParseUint(leftParts[1], 10, 64)
	rightSequence, _ := strconv.ParseUint(rightParts[1], 10, 64)
	if leftSequence < rightSequence {
		return -1
	}
	if leftSequence > rightSequence {
		return 1
	}
	return 0
}
