package observability

import (
	"context"
	"database/sql"
)

func FederationMetricsProvider(db *sql.DB) func(context.Context) (FederationMetrics, error) {
	return func(ctx context.Context) (FederationMetrics, error) {
		var snapshot FederationMetrics
		err := db.QueryRowContext(ctx, `
SELECT
  (SELECT COUNT(*) FROM inbox_messages WHERE processing_state = 'pending'),
  (SELECT COUNT(*) FROM inbox_messages WHERE processing_state = 'failed'),
  (SELECT COUNT(*) FROM outbox_jobs WHERE state = 'pending'),
  (SELECT COUNT(*) FROM outbox_jobs WHERE state = 'retry'),
  (SELECT COUNT(*) FROM outbox_jobs WHERE state = 'failed'),
  COALESCE((
    SELECT EXTRACT(EPOCH FROM (now() - MIN(created_at)))
    FROM outbox_jobs WHERE state IN ('pending', 'retry')
  ), 0)`).Scan(
			&snapshot.InboxPending, &snapshot.InboxFailed,
			&snapshot.OutboxPending, &snapshot.OutboxRetry, &snapshot.OutboxFailed,
			&snapshot.OldestPendingSeconds,
		)
		return snapshot, err
	}
}
