package privacy

import (
	"context"
	"database/sql"
	"strings"

	"github.com/google/uuid"
)

const followerExistsSQL = `
SELECT EXISTS (
  SELECT 1
  FROM relationships
  WHERE type = 'follow'
    AND state = 'accepted'
    AND (
      (actor_id = $1 AND target_actor_id = $2)
      OR (actor_id = $2 AND target_actor_id = $1 AND direction = 'incoming')
    )
)`

const friendExistsSQL = `
SELECT EXISTS (
  SELECT 1
  FROM relationships
  WHERE actor_id = $1
    AND target_actor_id = $2
    AND type = 'friend'
    AND state = 'accepted'
)`

type ViewInput struct {
	OwnerActorID  uuid.UUID
	ViewerActorID uuid.NullUUID
	Visibility    string
}

func CanView(ctx context.Context, db *sql.DB, input ViewInput) (bool, error) {
	visibility := strings.ToLower(strings.TrimSpace(input.Visibility))
	if visibility == "" || visibility == "public" {
		return true, nil
	}
	if input.ViewerActorID.Valid && input.ViewerActorID.UUID == input.OwnerActorID {
		return true, nil
	}
	if !input.ViewerActorID.Valid {
		return false, nil
	}

	switch visibility {
	case "followers":
		return exists(ctx, db, followerExistsSQL, input.ViewerActorID.UUID, input.OwnerActorID)
	case "friends":
		return exists(ctx, db, friendExistsSQL, input.ViewerActorID.UUID, input.OwnerActorID)
	case "private":
		return false, nil
	default:
		return false, nil
	}
}

func exists(ctx context.Context, db *sql.DB, query string, args ...any) (bool, error) {
	var ok bool
	err := db.QueryRowContext(ctx, query, args...).Scan(&ok)
	return ok, err
}
