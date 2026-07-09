package users

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Username     string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository {
	return Repository{db: db}
}

func (r Repository) GetByID(ctx context.Context, id uuid.UUID) (User, error) {
	row := r.db.QueryRowContext(ctx, userSelect()+` WHERE id = $1`, id)
	return scanUser(row)
}

func (r Repository) GetByLogin(ctx context.Context, login string) (User, error) {
	row := r.db.QueryRowContext(ctx, userSelect()+` WHERE lower(email) = lower($1) OR lower(username) = lower($1)`, login)
	return scanUser(row)
}

func userSelect() string {
	return `
SELECT id, email, password_hash, username, status, created_at, updated_at
FROM users`
}

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (User, error) {
	var user User
	if err := row.Scan(
		&user.ID,
		&user.Email,
		&user.PasswordHash,
		&user.Username,
		&user.Status,
		&user.CreatedAt,
		&user.UpdatedAt,
	); err != nil {
		return User{}, err
	}
	return user, nil
}
