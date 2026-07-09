package dbx

import (
	"database/sql"
	"encoding/json"

	"github.com/google/uuid"
)

func MarshalJSON(value any, fallback string) ([]byte, error) {
	if value == nil {
		return []byte(fallback), nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func DecodeJSON[T any](data []byte, fallback T) T {
	if len(data) == 0 {
		return fallback
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return fallback
	}
	return value
}

func NullUUID(id *uuid.UUID) uuid.NullUUID {
	if id == nil || *id == uuid.Nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *id, Valid: true}
}

func UUIDPtr(value uuid.NullUUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	id := value.UUID
	return &id
}

func NullString(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}
