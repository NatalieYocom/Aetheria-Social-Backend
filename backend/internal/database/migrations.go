package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var migrationFilePattern = regexp.MustCompile(`^(\d+)_(.+)\.(up|down)\.sql$`)

type Migration struct {
	Version  int64
	Name     string
	UpPath   string
	DownPath string
}

func ApplyUp(ctx context.Context, db *sql.DB, dir string) error {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return err
	}

	migrations, err := LoadMigrations(dir)
	if err != nil {
		return err
	}

	applied, err := appliedVersions(ctx, db)
	if err != nil {
		return err
	}

	for _, migration := range migrations {
		if applied[migration.Version] {
			continue
		}
		if migration.UpPath == "" {
			return fmt.Errorf("migration %d has no up file", migration.Version)
		}
		if err := applyMigration(ctx, db, migration.Version, migration.Name, migration.UpPath, true); err != nil {
			return err
		}
	}

	return nil
}

func ApplyDown(ctx context.Context, db *sql.DB, dir string) error {
	if err := ensureMigrationsTable(ctx, db); err != nil {
		return err
	}

	last, err := lastAppliedMigration(ctx, db)
	if err != nil {
		return err
	}
	if last.Version == 0 {
		return nil
	}

	migrations, err := LoadMigrations(dir)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if migration.Version == last.Version {
			if migration.DownPath == "" {
				return fmt.Errorf("migration %d has no down file", migration.Version)
			}
			return applyMigration(ctx, db, migration.Version, migration.Name, migration.DownPath, false)
		}
	}

	return fmt.Errorf("down file for migration %d not found", last.Version)
}

func LoadMigrations(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	byVersion := map[int64]Migration{}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		matches := migrationFilePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}
		version, err := strconv.ParseInt(matches[1], 10, 64)
		if err != nil {
			return nil, err
		}
		migration := byVersion[version]
		migration.Version = version
		migration.Name = matches[2]
		path := filepath.Join(dir, entry.Name())
		switch matches[3] {
		case "up":
			migration.UpPath = path
		case "down":
			migration.DownPath = path
		}
		byVersion[version] = migration
	}

	migrations := make([]Migration, 0, len(byVersion))
	for _, migration := range byVersion {
		migrations = append(migrations, migration)
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

type appliedMigration struct {
	Version int64
	Name    string
}

func ensureMigrationsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version BIGINT PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`)
	return err
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[int64]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	versions := map[int64]bool{}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		versions[version] = true
	}
	return versions, rows.Err()
}

func lastAppliedMigration(ctx context.Context, db *sql.DB) (appliedMigration, error) {
	row := db.QueryRowContext(ctx, `SELECT version, name FROM schema_migrations ORDER BY version DESC LIMIT 1`)
	var migration appliedMigration
	if err := row.Scan(&migration.Version, &migration.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return appliedMigration{}, nil
		}
		return appliedMigration{}, err
	}
	return migration, nil
}

func applyMigration(ctx context.Context, db *sql.DB, version int64, name string, path string, up bool) error {
	sqlBytes, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	statements := splitSQLStatements(string(sqlBytes))
	if len(statements) == 0 {
		return fmt.Errorf("migration %s is empty", path)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply migration %s: %w", path, err)
		}
	}

	if up {
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3)`, version, name, time.Now().UTC()); err != nil {
			return err
		}
	} else {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = $1`, version); err != nil {
			return err
		}
	}

	return tx.Commit()
}

func splitSQLStatements(input string) []string {
	statements := []string{}
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	dollarTag := ""

	for i := 0; i < len(input); i++ {
		if dollarTag != "" {
			if strings.HasPrefix(input[i:], dollarTag) {
				current.WriteString(dollarTag)
				i += len(dollarTag) - 1
				dollarTag = ""
				continue
			}
			current.WriteByte(input[i])
			continue
		}

		if inSingleQuote {
			current.WriteByte(input[i])
			if input[i] == '\'' {
				if i+1 < len(input) && input[i+1] == '\'' {
					i++
					current.WriteByte(input[i])
					continue
				}
				inSingleQuote = false
			}
			continue
		}

		if inDoubleQuote {
			current.WriteByte(input[i])
			if input[i] == '"' {
				if i+1 < len(input) && input[i+1] == '"' {
					i++
					current.WriteByte(input[i])
					continue
				}
				inDoubleQuote = false
			}
			continue
		}

		switch input[i] {
		case '\'':
			inSingleQuote = true
			current.WriteByte(input[i])
		case '"':
			inDoubleQuote = true
			current.WriteByte(input[i])
		case '$':
			if tag, ok := readDollarTag(input[i:]); ok {
				dollarTag = tag
				current.WriteString(tag)
				i += len(tag) - 1
				continue
			}
			current.WriteByte(input[i])
		case ';':
			statement := strings.TrimSpace(current.String())
			if statement != "" {
				statements = append(statements, statement)
			}
			current.Reset()
		default:
			current.WriteByte(input[i])
		}
	}

	statement := strings.TrimSpace(current.String())
	if statement != "" {
		statements = append(statements, statement)
	}
	return statements
}

func readDollarTag(input string) (string, bool) {
	if input == "" || input[0] != '$' {
		return "", false
	}
	for i := 1; i < len(input); i++ {
		if input[i] == '$' {
			return input[:i+1], true
		}
		if !isDollarTagChar(input[i]) {
			return "", false
		}
	}
	return "", false
}

func isDollarTagChar(value byte) bool {
	return value == '_' ||
		(value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9')
}
