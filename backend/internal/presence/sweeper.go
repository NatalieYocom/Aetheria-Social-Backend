package presence

import (
	"context"
	"database/sql"
	"log"
	"time"

	"basisvr-social-service/internal/realtime"

	"github.com/google/uuid"
)

const expiredPresenceSelectSQL = `
SELECT actor_id, visibility
FROM presence_sessions
WHERE expires_at <= now()
ORDER BY expires_at ASC
LIMIT $1`

const expiredPresenceDeleteSQL = `
DELETE FROM presence_sessions
WHERE actor_id = $1 AND expires_at <= now()`

type SweeperConfig struct {
	Interval  time.Duration
	BatchSize int
}

type Sweeper struct {
	db       *sql.DB
	events   *realtime.Broker
	interval time.Duration
	batch    int
}

type expiredPresence struct {
	actorID    uuid.UUID
	visibility string
}

func NewSweeper(db *sql.DB, broker *realtime.Broker, config SweeperConfig) *Sweeper {
	interval := config.Interval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	batch := config.BatchSize
	if batch <= 0 {
		batch = 500
	}
	return &Sweeper{
		db:       db,
		events:   broker,
		interval: interval,
		batch:    batch,
	}
}

func (s *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.SweepExpired(ctx); err != nil {
				log.Printf("presence sweep failed: %v", err)
			}
		}
	}
}

func (s *Sweeper) SweepExpired(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, expiredPresenceSelectSQL, s.batch)
	if err != nil {
		return 0, err
	}
	expired := []expiredPresence{}
	for rows.Next() {
		var item expiredPresence
		if err := rows.Scan(&item.actorID, &item.visibility); err != nil {
			_ = rows.Close()
			return 0, err
		}
		expired = append(expired, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	deleted := 0
	for _, item := range expired {
		result, err := s.db.ExecContext(ctx, expiredPresenceDeleteSQL, item.actorID)
		if err != nil {
			return deleted, err
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			continue
		}
		deleted++
		s.publishRemoval(ctx, item)
	}
	return deleted, nil
}

func (s *Sweeper) publishRemoval(ctx context.Context, item expiredPresence) {
	if item.visibility == "friends" || item.visibility == "public" {
		_ = realtime.PublishPresenceRemoved(ctx, s.db, s.events, item.actorID)
		return
	}
	realtime.PublishActorEvent(s.events, []uuid.UUID{item.actorID}, "presence.removed", item.actorID, map[string]string{
		"actorId": item.actorID.String(),
	})
}
