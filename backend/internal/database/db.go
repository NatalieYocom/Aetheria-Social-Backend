package database

import (
	"context"
	"database/sql"
	"time"

	"basisvr-social-service/internal/config"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func Open(ctx context.Context, cfg config.DatabaseConfig) (*sql.DB, error) {
	db, err := sql.Open("pgx", cfg.URL)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(25)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
