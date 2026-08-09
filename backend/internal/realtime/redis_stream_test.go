package realtime

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRedisReplayStoreAppendsAndReplaysAfterCursor(t *testing.T) {
	redisURL := os.Getenv("BASIS_INTEGRATION_REDIS_URL")
	if redisURL == "" {
		t.Skip("BASIS_INTEGRATION_REDIS_URL is not set")
	}
	store, err := NewRedisReplayStore(redisURL, ReplayStoreConfig{
		Prefix: "basisvr:test:replay:" + uuid.NewString(), Retention: time.Hour, MaxEntries: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	actorID := uuid.New()
	first, err := store.Append(ctx, actorID, Event{ID: uuid.NewString(), Type: "presence.updated"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(ctx, actorID, Event{ID: uuid.NewString(), Type: "invite.created"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Cursor == "" || second.Cursor == "" || first.Cursor == second.Cursor {
		t.Fatalf("cursors = %q, %q", first.Cursor, second.Cursor)
	}

	replay, err := store.Replay(ctx, actorID, first.Cursor, 10)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Truncated || len(replay.Events) != 1 || replay.Events[0].ID != second.ID || replay.Events[0].Cursor != second.Cursor {
		t.Fatalf("replay = %+v", replay)
	}
	other, err := store.Replay(ctx, uuid.New(), "0-0", 10)
	if err != nil || len(other.Events) != 0 {
		t.Fatalf("other actor replay = %+v, err = %v", other, err)
	}
}

func TestRedisReplayStoreRejectsMalformedCursor(t *testing.T) {
	redisURL := os.Getenv("BASIS_INTEGRATION_REDIS_URL")
	if redisURL == "" {
		t.Skip("BASIS_INTEGRATION_REDIS_URL is not set")
	}
	store, err := NewRedisReplayStore(redisURL, ReplayStoreConfig{Prefix: "basisvr:test:replay:" + uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.Replay(context.Background(), uuid.New(), "not-a-stream-id", 10)
	if !errors.Is(err, ErrInvalidReplayCursor) {
		t.Fatalf("err = %v", err)
	}
}
