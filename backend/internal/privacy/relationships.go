package privacy

import (
	"context"
	"database/sql"
	"github.com/google/uuid"
)

type Querier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// HasBlock is symmetric: neither side may regain contact through another API.
func HasBlock(ctx context.Context, db Querier, first, second uuid.UUID) (bool, error) {
	var blocked bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM relationships WHERE type = 'block' AND state = 'accepted' AND ((actor_id = $1 AND target_actor_id = $2) OR (actor_id = $2 AND target_actor_id = $1)))`, first, second).Scan(&blocked)
	return blocked, err
}

// LockPair serializes reciprocal friendship decisions and blocks in UUID order.
func LockPair(ctx context.Context, tx *sql.Tx, first, second uuid.UUID) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM actors WHERE id IN ($1,$2) ORDER BY id FOR UPDATE`, first, second)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}
