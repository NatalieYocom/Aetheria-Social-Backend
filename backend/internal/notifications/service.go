package notifications

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"basisvr-social-service/internal/common/dbx"
	"basisvr-social-service/internal/realtime"

	"github.com/google/uuid"
)

const insertNotificationSQL = `
INSERT INTO notifications (actor_id, type, payload)
VALUES ($1, $2, $3)
RETURNING id, actor_id, type, payload, read_at, created_at`

const listNotificationsSQL = `
SELECT id, actor_id, type, payload, read_at, created_at
FROM notifications
WHERE actor_id = $1
ORDER BY created_at DESC
LIMIT $2`

const listUnreadNotificationsSQL = `
SELECT id, actor_id, type, payload, read_at, created_at
FROM notifications
WHERE actor_id = $1 AND read_at IS NULL
ORDER BY created_at DESC
LIMIT $2`

const unreadCountSQL = `
SELECT COUNT(*)
FROM notifications
WHERE actor_id = $1 AND read_at IS NULL`

const markReadSQL = `
UPDATE notifications
SET read_at = COALESCE(read_at, now())
WHERE id = $1 AND actor_id = $2
RETURNING id, actor_id, type, payload, read_at, created_at`

const markAllReadSQL = `
UPDATE notifications
SET read_at = now()
WHERE actor_id = $1 AND read_at IS NULL`

type CreateInput struct {
	ActorID uuid.UUID
	Type    string
	Payload any
}

type NotificationResponse struct {
	ID        uuid.UUID      `json:"id"`
	ActorID   uuid.UUID      `json:"actorId"`
	Type      string         `json:"type"`
	Payload   map[string]any `json:"payload"`
	ReadAt    *time.Time     `json:"readAt,omitempty"`
	CreatedAt time.Time      `json:"createdAt"`
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func CreateAndPublish(ctx context.Context, db *sql.DB, broker *realtime.Broker, input CreateInput) (NotificationResponse, error) {
	notification, err := Insert(ctx, db, input)
	if err != nil {
		return NotificationResponse{}, err
	}
	PublishCreated(broker, notification)
	return notification, nil
}

func Insert(ctx context.Context, db queryRower, input CreateInput) (NotificationResponse, error) {
	if input.ActorID == uuid.Nil {
		return NotificationResponse{}, errors.New("actor id is required")
	}
	if input.Type == "" {
		return NotificationResponse{}, errors.New("notification type is required")
	}
	payload, err := dbx.MarshalJSON(input.Payload, "{}")
	if err != nil {
		return NotificationResponse{}, err
	}
	row := db.QueryRowContext(ctx, insertNotificationSQL, input.ActorID, input.Type, payload)
	return scanNotification(row)
}

func PublishCreated(broker *realtime.Broker, notification NotificationResponse) {
	realtime.PublishActorEvent(broker, []uuid.UUID{notification.ActorID}, "notification.created", notification.ActorID, notification)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanNotification(row scanner) (NotificationResponse, error) {
	var notification NotificationResponse
	var payloadRaw []byte
	var readAt sql.NullTime
	if err := row.Scan(
		&notification.ID,
		&notification.ActorID,
		&notification.Type,
		&payloadRaw,
		&readAt,
		&notification.CreatedAt,
	); err != nil {
		return NotificationResponse{}, err
	}
	notification.Payload = dbx.DecodeJSON(payloadRaw, map[string]any{})
	if readAt.Valid {
		notification.ReadAt = &readAt.Time
	}
	return notification, nil
}
