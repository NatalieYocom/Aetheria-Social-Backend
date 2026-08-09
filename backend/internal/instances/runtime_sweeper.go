package instances

import (
	"context"
	"database/sql"
	"log"
	"time"

	"basisvr-social-service/internal/realtime"

	"github.com/google/uuid"
)

const selectExpiredRuntimeInstancesSQL = `
SELECT id
FROM instances
WHERE status = 'active' AND expires_at IS NOT NULL AND expires_at <= now()
ORDER BY expires_at ASC
LIMIT $1
FOR UPDATE SKIP LOCKED`

const removeExpiredInstancePresenceSQL = `
DELETE FROM presence_sessions
WHERE instance_id = $1
RETURNING actor_id`

const leaveExpiredInstanceMembersSQL = `
UPDATE instance_members
SET state = 'left', left_at = now(), last_seen_at = now()
WHERE instance_id = $1 AND state = 'joined'`

const markRuntimeInstanceExpiredSQL = `
UPDATE instances
SET status = 'expired', current_users = 0
WHERE id = $1 AND status = 'active'`

const cleanupRuntimeTicketsSQL = `
DELETE FROM instance_join_tickets
WHERE id IN (
  SELECT id
  FROM instance_join_tickets
  WHERE expires_at < now() - ($1 * interval '1 second')
     OR consumed_at < now() - ($1 * interval '1 second')
  ORDER BY expires_at ASC
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)`

const cleanupRuntimeAuditSQL = `
DELETE FROM instance_join_audit
WHERE id IN (
  SELECT id
  FROM instance_join_audit
  WHERE created_at < now() - ($1 * interval '1 second')
  ORDER BY created_at ASC
  LIMIT $2
  FOR UPDATE SKIP LOCKED
)`

type RuntimeSweeperConfig struct {
	Interval        time.Duration
	BatchSize       int
	TicketRetention time.Duration
	AuditRetention  time.Duration
}

type RuntimeSweeper struct {
	db              *sql.DB
	events          *realtime.Broker
	interval        time.Duration
	batch           int
	ticketRetention time.Duration
	auditRetention  time.Duration
}

type expiredRuntimeInstance struct {
	id       uuid.UUID
	actorIDs []uuid.UUID
}

func NewRuntimeSweeper(db *sql.DB, broker *realtime.Broker, cfg RuntimeSweeperConfig) *RuntimeSweeper {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Minute
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 100
	}
	if cfg.TicketRetention <= 0 {
		cfg.TicketRetention = 24 * time.Hour
	}
	if cfg.AuditRetention <= 0 {
		cfg.AuditRetention = 90 * 24 * time.Hour
	}
	return &RuntimeSweeper{
		db: db, events: broker, interval: cfg.Interval, batch: cfg.BatchSize,
		ticketRetention: cfg.TicketRetention, auditRetention: cfg.AuditRetention,
	}
}

func (s *RuntimeSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.SweepExpiredInstances(ctx); err != nil {
				log.Printf("instance runtime sweep failed: %v", err)
			}
			if _, err := s.CleanupAccessData(ctx); err != nil {
				log.Printf("instance access cleanup failed: %v", err)
			}
		}
	}
}

func (s *RuntimeSweeper) SweepExpiredInstances(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, selectExpiredRuntimeInstancesSQL, s.batch)
	if err != nil {
		return 0, err
	}
	instances := []expiredRuntimeInstance{}
	for rows.Next() {
		var item expiredRuntimeInstance
		if err := rows.Scan(&item.id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		instances = append(instances, item)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for i := range instances {
		presenceRows, err := tx.QueryContext(ctx, removeExpiredInstancePresenceSQL, instances[i].id)
		if err != nil {
			return 0, err
		}
		for presenceRows.Next() {
			var actorID uuid.UUID
			if err := presenceRows.Scan(&actorID); err != nil {
				_ = presenceRows.Close()
				return 0, err
			}
			instances[i].actorIDs = append(instances[i].actorIDs, actorID)
		}
		if err := presenceRows.Close(); err != nil {
			return 0, err
		}
		if err := presenceRows.Err(); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, leaveExpiredInstanceMembersSQL, instances[i].id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, markRuntimeInstanceExpiredSQL, instances[i].id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	for _, item := range instances {
		for _, actorID := range item.actorIDs {
			_ = realtime.PublishPresenceRemoved(ctx, s.db, s.events, actorID)
		}
		_ = realtime.PublishInstanceChanged(ctx, s.db, s.events, item.id, "instance.expired")
	}
	return len(instances), nil
}

func (s *RuntimeSweeper) CleanupAccessData(ctx context.Context) (int64, error) {
	ticketResult, err := s.db.ExecContext(ctx, cleanupRuntimeTicketsSQL, int64(s.ticketRetention/time.Second), s.batch)
	if err != nil {
		return 0, err
	}
	auditResult, err := s.db.ExecContext(ctx, cleanupRuntimeAuditSQL, int64(s.auditRetention/time.Second), s.batch)
	if err != nil {
		return 0, err
	}
	tickets, _ := ticketResult.RowsAffected()
	audits, _ := auditResult.RowsAffected()
	return tickets + audits, nil
}
