package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

var ErrActorNotFound = errors.New("activitypub actor not found")

type Service struct {
	db        *sql.DB
	publicURL string
}

type AnnounceInput struct {
	ActorID    uuid.UUID
	ObjectID   uuid.UUID
	ObjectType string
	ObjectURI  string
	Visibility string
}

type PublishResult struct {
	ActivityID  uuid.UUID
	ActivityURI string
	Deliveries  int
	RawJSON     []byte
}

func NewService(db *sql.DB, publicURL string) *Service {
	return &Service{db: db, publicURL: strings.TrimRight(publicURL, "/")}
}

func (s *Service) PublishAnnounce(ctx context.Context, input AnnounceInput) (PublishResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PublishResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := s.PublishAnnounceTx(ctx, tx, input)
	if err != nil {
		return PublishResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

func (s *Service) PublishAnnounceTx(ctx context.Context, tx *sql.Tx, input AnnounceInput) (PublishResult, error) {
	if input.ActorID == uuid.Nil || input.ObjectID == uuid.Nil || strings.TrimSpace(input.ObjectURI) == "" {
		return PublishResult{}, errors.New("announce actor, object and object URI are required")
	}

	var actorURI, followersURL string
	if err := tx.QueryRowContext(ctx, `
SELECT actor_uri, followers_url
FROM actors
WHERE id = $1 AND is_local = true`, input.ActorID).Scan(&actorURI, &followersURL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PublishResult{}, ErrActorNotFound
		}
		return PublishResult{}, err
	}

	activityID := uuid.New()
	activityURI := s.publicURL + "/activities/" + activityID.String()
	to := []string{followersURL}
	if input.Visibility == "public" {
		to = append(to, "https://www.w3.org/ns/activitystreams#Public")
	}
	rawJSON, err := json.Marshal(map[string]any{
		"@context": "https://www.w3.org/ns/activitystreams",
		"id":       activityURI, "type": "Announce", "actor": actorURI,
		"object": input.ObjectURI, "to": to,
	})
	if err != nil {
		return PublishResult{}, err
	}

	targets := []string{}
	if input.Visibility == "public" {
		rows, err := tx.QueryContext(ctx, `
SELECT DISTINCT COALESCE(NULLIF(target.shared_inbox_url, ''), target.inbox_url)
FROM relationships rel
JOIN actors target ON target.id = rel.target_actor_id
WHERE rel.actor_id = $1
  AND rel.type = 'follow' AND rel.direction = 'incoming' AND rel.state = 'accepted'
  AND target.is_local = false
  AND COALESCE(NULLIF(target.shared_inbox_url, ''), target.inbox_url) <> ''`, input.ActorID)
		if err != nil {
			return PublishResult{}, err
		}
		for rows.Next() {
			var target string
			if err := rows.Scan(&target); err != nil {
				_ = rows.Close()
				return PublishResult{}, err
			}
			targets = append(targets, target)
		}
		if err := rows.Close(); err != nil {
			return PublishResult{}, err
		}
		if err := rows.Err(); err != nil {
			return PublishResult{}, err
		}
	}

	direction := "local"
	if len(targets) > 0 {
		direction = "outbound"
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO activities (
  id, activity_uri, actor_id, type, object_id, object_type,
  visibility, raw_json, direction
)
VALUES ($1, $2, $3, 'Announce', $4, $5, $6, $7, $8)`,
		activityID, activityURI, input.ActorID, input.ObjectID, input.ObjectType,
		input.Visibility, rawJSON, direction,
	); err != nil {
		return PublishResult{}, err
	}
	for _, target := range targets {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO outbox_jobs (activity_id, target_inbox_url)
VALUES ($1, $2)
ON CONFLICT (activity_id, target_inbox_url) DO NOTHING`, activityID, target); err != nil {
			return PublishResult{}, err
		}
	}
	return PublishResult{
		ActivityID: activityID, ActivityURI: activityURI,
		Deliveries: len(targets), RawJSON: rawJSON,
	}, nil
}
