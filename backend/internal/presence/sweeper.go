package presence

import (
	"context"
	"database/sql"
	"log"
	"time"

	"basisvr-social-service/internal/realtime"

	"github.com/google/uuid"
)

const expiredPresenceDeleteBatchSQL = `
DELETE FROM presence_sessions
WHERE id IN (
  SELECT id
  FROM presence_sessions
  WHERE expires_at <= now()
  ORDER BY expires_at ASC
  LIMIT $1
  FOR UPDATE SKIP LOCKED
)
RETURNING actor_id, visibility, instance_id`

const expireInstanceMemberSQL = `
UPDATE instance_members
SET state = 'left', left_at = now(), last_seen_at = now()
WHERE instance_id = $1 AND actor_id = $2 AND state = 'joined'`

const decrementInstanceUsersSQL = `
UPDATE instances
SET current_users = GREATEST(current_users - 1, 0)
WHERE id = $1`

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
	instanceID uuid.NullUUID
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	rows, err := tx.QueryContext(ctx, expiredPresenceDeleteBatchSQL, s.batch)
	if err != nil {
		return 0, err
	}
	expired := []expiredPresence{}
	for rows.Next() {
		var item expiredPresence
		if err := rows.Scan(&item.actorID, &item.visibility, &item.instanceID); err != nil {
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

	changedInstances := map[uuid.UUID]struct{}{}
	for _, item := range expired {
		if !item.instanceID.Valid {
			continue
		}
		result, err := tx.ExecContext(ctx, expireInstanceMemberSQL, item.instanceID.UUID, item.actorID)
		if err != nil {
			return 0, err
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, decrementInstanceUsersSQL, item.instanceID.UUID); err != nil {
			return 0, err
		}
		changedInstances[item.instanceID.UUID] = struct{}{}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true

	for _, item := range expired {
		s.publishRemoval(ctx, item)
	}
	for instanceID := range changedInstances {
		_ = realtime.PublishInstanceChanged(ctx, s.db, s.events, instanceID, "instance.updated")
	}
	return len(expired), nil
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
