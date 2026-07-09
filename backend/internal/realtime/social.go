package realtime

import (
	"context"
	"database/sql"
	"time"

	"basisvr-social-service/internal/common/dbx"

	"github.com/google/uuid"
)

const loadPresenceForRealtimeSQL = `
SELECT ps.id, ps.actor_id, a.acct, a.display_name, ps.world_id, ps.instance_id,
       ps.status, ps.visibility, ps.show_exact_instance, ps.expires_at, ps.updated_at
FROM presence_sessions ps
JOIN actors a ON a.id = ps.actor_id
WHERE ps.actor_id = $1`

const presenceFriendWatchersSQL = `
SELECT actor_id
FROM relationships
WHERE target_actor_id = $1
  AND type = 'friend'
  AND state = 'accepted'`

const instanceWatchersSQL = `
SELECT host_actor_id AS actor_id
FROM instances
WHERE id = $1
UNION
SELECT actor_id
FROM instance_members
WHERE instance_id = $1 AND state = 'joined'`

type PresenceEventPayload struct {
	ID                uuid.UUID  `json:"id"`
	ActorID           uuid.UUID  `json:"actorId"`
	Acct              string     `json:"acct"`
	DisplayName       string     `json:"displayName"`
	WorldID           *uuid.UUID `json:"worldId,omitempty"`
	InstanceID        *uuid.UUID `json:"instanceId,omitempty"`
	Status            string     `json:"status"`
	Visibility        string     `json:"visibility"`
	ShowExactInstance bool       `json:"showExactInstance"`
	ExpiresAt         time.Time  `json:"expiresAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

func PublishPresenceChanged(ctx context.Context, db *sql.DB, broker *Broker, actorID uuid.UUID) error {
	if broker == nil || db == nil || actorID == uuid.Nil {
		return nil
	}

	payload, err := loadPresenceForRealtime(ctx, db, actorID)
	if err != nil {
		return err
	}
	broker.Publish(actorID, Event{
		Type:    "presence.updated",
		ActorID: actorID,
		Payload: payload,
	})

	if !payload.visibleToFriends() {
		return nil
	}
	watchers, err := loadPresenceFriendWatchers(ctx, db, actorID)
	if err != nil {
		return err
	}
	friendPayload := payload.publicView()
	for _, watcherID := range watchers {
		if watcherID == actorID {
			continue
		}
		broker.Publish(watcherID, Event{
			Type:    "presence.updated",
			ActorID: actorID,
			Payload: friendPayload,
		})
	}
	return nil
}

func PublishPresenceRemoved(ctx context.Context, db *sql.DB, broker *Broker, actorID uuid.UUID) error {
	if broker == nil || db == nil || actorID == uuid.Nil {
		return nil
	}
	payload := map[string]string{"actorId": actorID.String()}
	broker.Publish(actorID, Event{
		Type:    "presence.removed",
		ActorID: actorID,
		Payload: payload,
	})

	watchers, err := loadPresenceFriendWatchers(ctx, db, actorID)
	if err != nil {
		return err
	}
	for _, watcherID := range watchers {
		if watcherID == actorID {
			continue
		}
		broker.Publish(watcherID, Event{
			Type:    "presence.removed",
			ActorID: actorID,
			Payload: payload,
		})
	}
	return nil
}

func PublishInstanceChanged(ctx context.Context, db *sql.DB, broker *Broker, instanceID uuid.UUID, eventType string) error {
	if broker == nil || db == nil || instanceID == uuid.Nil || eventType == "" {
		return nil
	}
	rows, err := db.QueryContext(ctx, instanceWatchersSQL, instanceID)
	if err != nil {
		return err
	}
	defer rows.Close()

	payload := map[string]string{"instanceId": instanceID.String()}
	published := map[uuid.UUID]struct{}{}
	for rows.Next() {
		var actorID uuid.UUID
		if err := rows.Scan(&actorID); err != nil {
			return err
		}
		if actorID == uuid.Nil {
			continue
		}
		if _, ok := published[actorID]; ok {
			continue
		}
		published[actorID] = struct{}{}
		broker.Publish(actorID, Event{
			Type:    eventType,
			ActorID: actorID,
			Payload: payload,
		})
	}
	return rows.Err()
}

func PublishActorEvent(broker *Broker, recipients []uuid.UUID, eventType string, actorID uuid.UUID, payload any) {
	if broker == nil || eventType == "" {
		return
	}
	published := map[uuid.UUID]struct{}{}
	for _, recipientID := range recipients {
		if recipientID == uuid.Nil {
			continue
		}
		if _, ok := published[recipientID]; ok {
			continue
		}
		published[recipientID] = struct{}{}
		broker.Publish(recipientID, Event{
			Type:    eventType,
			ActorID: actorID,
			Payload: payload,
		})
	}
}

func loadPresenceForRealtime(ctx context.Context, db *sql.DB, actorID uuid.UUID) (PresenceEventPayload, error) {
	row := db.QueryRowContext(ctx, loadPresenceForRealtimeSQL, actorID)
	var payload PresenceEventPayload
	var worldID uuid.NullUUID
	var instanceID uuid.NullUUID
	if err := row.Scan(
		&payload.ID,
		&payload.ActorID,
		&payload.Acct,
		&payload.DisplayName,
		&worldID,
		&instanceID,
		&payload.Status,
		&payload.Visibility,
		&payload.ShowExactInstance,
		&payload.ExpiresAt,
		&payload.UpdatedAt,
	); err != nil {
		return PresenceEventPayload{}, err
	}
	payload.WorldID = dbx.UUIDPtr(worldID)
	payload.InstanceID = dbx.UUIDPtr(instanceID)
	return payload, nil
}

func loadPresenceFriendWatchers(ctx context.Context, db *sql.DB, actorID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := db.QueryContext(ctx, presenceFriendWatchersSQL, actorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	watchers := []uuid.UUID{}
	for rows.Next() {
		var watcherID uuid.UUID
		if err := rows.Scan(&watcherID); err != nil {
			return nil, err
		}
		watchers = append(watchers, watcherID)
	}
	return watchers, rows.Err()
}

func (p PresenceEventPayload) visibleToFriends() bool {
	return p.Status != "invisible" && (p.Visibility == "friends" || p.Visibility == "public")
}

func (p PresenceEventPayload) publicView() PresenceEventPayload {
	if !p.ShowExactInstance {
		p.InstanceID = nil
	}
	return p
}
