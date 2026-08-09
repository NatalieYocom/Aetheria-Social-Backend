package retention

import (
	"context"
	"database/sql"
	"log"
	"time"

	"basisvr-social-service/internal/config"
)

type Service struct {
	db  *sql.DB
	cfg config.RetentionConfig
}

func NewService(db *sql.DB, cfg config.RetentionConfig) *Service {
	if cfg.Interval <= 0 {
		cfg.Interval = time.Hour
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = 1000
	}
	if cfg.InboxAge <= 0 {
		cfg.InboxAge = 30 * 24 * time.Hour
	}
	if cfg.OutboxAge <= 0 {
		cfg.OutboxAge = 30 * 24 * time.Hour
	}
	if cfg.NotificationsAge <= 0 {
		cfg.NotificationsAge = 180 * 24 * time.Hour
	}
	if cfg.InvitesAge <= 0 {
		cfg.InvitesAge = 90 * 24 * time.Hour
	}
	if cfg.InboundActivityAge <= 0 {
		cfg.InboundActivityAge = 180 * 24 * time.Hour
	}
	return &Service{db: db, cfg: cfg}
}

func (s *Service) Run(ctx context.Context) {
	s.runAndLog(ctx)
	ticker := time.NewTicker(s.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runAndLog(ctx)
		}
	}
}

func (s *Service) runAndLog(ctx context.Context) {
	deleted, err := s.RunOnce(ctx, time.Now().UTC())
	if err != nil {
		log.Printf("retention cleanup failed: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("retention cleanup deleted %d rows", deleted)
	}
}

func (s *Service) RunOnce(ctx context.Context, now time.Time) (int64, error) {
	policies := []struct {
		query  string
		cutoff time.Time
	}{
		{`DELETE FROM inbox_messages WHERE id IN (
           SELECT id FROM inbox_messages WHERE processing_state IN ('processed','ignored') AND created_at < $1 ORDER BY created_at LIMIT $2
         )`, now.Add(-s.cfg.InboxAge)},
		{`DELETE FROM outbox_jobs WHERE id IN (
           SELECT id FROM outbox_jobs WHERE state IN ('delivered','failed') AND updated_at < $1 ORDER BY updated_at LIMIT $2
         )`, now.Add(-s.cfg.OutboxAge)},
		{`DELETE FROM notifications WHERE id IN (
           SELECT id FROM notifications WHERE read_at IS NOT NULL AND read_at < $1 ORDER BY read_at LIMIT $2
         )`, now.Add(-s.cfg.NotificationsAge)},
		{`DELETE FROM invites WHERE id IN (
           SELECT id FROM invites WHERE state IN ('accepted','declined','expired') AND updated_at < $1 ORDER BY updated_at LIMIT $2
         )`, now.Add(-s.cfg.InvitesAge)},
		{`DELETE FROM activities WHERE id IN (
           SELECT activity.id FROM activities activity
           WHERE activity.direction = 'inbound' AND activity.created_at < $1
             AND NOT EXISTS (SELECT 1 FROM outbox_jobs job WHERE job.activity_id = activity.id)
           ORDER BY activity.created_at LIMIT $2
         )`, now.Add(-s.cfg.InboundActivityAge)},
	}
	var deleted int64
	for _, policy := range policies {
		result, err := s.db.ExecContext(ctx, policy.query, policy.cutoff, s.cfg.BatchSize)
		if err != nil {
			return deleted, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return deleted, err
		}
		deleted += rows
	}
	return deleted, nil
}
