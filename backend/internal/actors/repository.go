package actors

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type Actor struct {
	ID                     uuid.UUID
	LocalUserID            uuid.NullUUID
	ActorURI               string
	Acct                   string
	Type                   string
	PreferredUsername      string
	DisplayName            string
	Domain                 string
	InboxURL               string
	OutboxURL              string
	FollowersURL           string
	FollowingURL           string
	SharedInboxURL         sql.NullString
	PublicKeyPEM           string
	PrivateKeyPEMEncrypted sql.NullString
	IsLocal                bool
	RawJSON                []byte
	LastFetchedAt          sql.NullTime
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository {
	return Repository{db: db}
}

func (r Repository) GetByID(ctx context.Context, id uuid.UUID) (Actor, error) {
	row := r.db.QueryRowContext(ctx, actorSelect()+` WHERE id = $1`, id)
	return scanActor(row)
}

func (r Repository) GetByAcct(ctx context.Context, acct string) (Actor, error) {
	row := r.db.QueryRowContext(ctx, actorSelect()+` WHERE lower(acct) = lower($1)`, acct)
	return scanActor(row)
}

func actorSelect() string {
	return `
SELECT id, local_user_id, actor_uri, acct, type, preferred_username, display_name, domain,
       inbox_url, outbox_url, followers_url, following_url, shared_inbox_url,
       public_key_pem, private_key_pem_encrypted, is_local, raw_json, last_fetched_at,
       created_at, updated_at
FROM actors`
}

type scanner interface {
	Scan(dest ...any) error
}

func scanActor(row scanner) (Actor, error) {
	var actor Actor
	if err := row.Scan(
		&actor.ID,
		&actor.LocalUserID,
		&actor.ActorURI,
		&actor.Acct,
		&actor.Type,
		&actor.PreferredUsername,
		&actor.DisplayName,
		&actor.Domain,
		&actor.InboxURL,
		&actor.OutboxURL,
		&actor.FollowersURL,
		&actor.FollowingURL,
		&actor.SharedInboxURL,
		&actor.PublicKeyPEM,
		&actor.PrivateKeyPEMEncrypted,
		&actor.IsLocal,
		&actor.RawJSON,
		&actor.LastFetchedAt,
		&actor.CreatedAt,
		&actor.UpdatedAt,
	); err != nil {
		return Actor{}, err
	}
	return actor, nil
}
