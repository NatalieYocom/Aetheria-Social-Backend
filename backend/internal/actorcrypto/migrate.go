package actorcrypto

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type MigrationResult struct{ Checked, Legacy, Encrypted int }
type actorRow struct{ id, uri, value string }

// Migrate verifies every local nonempty key and optionally wraps legacy PEM values.
// Each batch commits atomically. Restarting after interruption is safe: encrypted rows
// are authenticated with the supplied key, never encrypted a second time.
func Migrate(ctx context.Context, db *sql.DB, key string, apply bool) (MigrationResult, error) {
	wrapper, err := New(key)
	if err != nil {
		return MigrationResult{}, err
	}
	var result MigrationResult
	var cursor any
	for {
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !apply})
		if err != nil {
			return result, err
		}
		query := `SELECT id,actor_uri,private_key_pem_encrypted FROM actors WHERE is_local=true AND private_key_pem_encrypted IS NOT NULL AND private_key_pem_encrypted<>'' AND ($1::uuid IS NULL OR id>$1) ORDER BY id LIMIT 100`
		if apply {
			query += " FOR UPDATE"
		}
		rows, err := tx.QueryContext(ctx, query, cursor)
		if err != nil {
			tx.Rollback()
			return result, err
		}
		batch := []actorRow{}
		for rows.Next() {
			var row actorRow
			if err = rows.Scan(&row.id, &row.uri, &row.value); err != nil {
				break
			}
			batch = append(batch, row)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			tx.Rollback()
			return result, err
		}
		legacy, changed := 0, 0
		for _, row := range batch {
			if strings.HasPrefix(row.value, Prefix) {
				_, err = wrapper.Decrypt(row.uri, row.value)
			} else {
				var encrypted string
				encrypted, err = wrapper.Encrypt(row.uri, row.value)
				if err == nil {
					legacy++
					if apply {
						_, err = tx.ExecContext(ctx, `UPDATE actors SET private_key_pem_encrypted=$2 WHERE id=$1`, row.id, encrypted)
						if err == nil {
							changed++
						}
					}
				}
			}
			if err != nil {
				tx.Rollback()
				return result, errors.New("actor-key migration refused an invalid key or ciphertext; no changes from the current batch were committed")
			}
		}
		if err = tx.Commit(); err != nil {
			return result, err
		}
		result.Checked += len(batch)
		result.Legacy += legacy
		result.Encrypted += changed
		if len(batch) < 100 {
			return result, nil
		}
		cursor = batch[len(batch)-1].id
	}
}
