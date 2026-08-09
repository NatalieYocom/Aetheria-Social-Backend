package page

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

var ErrInvalidCursor = errors.New("cursor is invalid")

type Cursor struct {
	SortTime time.Time `json:"t"`
	ID       uuid.UUID `json:"id"`
}

type Request struct {
	Limit  int
	Cursor *Cursor
}

type Metadata struct {
	NextCursor *string `json:"nextCursor"`
	Limit      int     `json:"limit"`
}

type Response[T any] struct {
	Data       []T      `json:"data"`
	Pagination Metadata `json:"pagination"`
}

func ParseRequest(r *http.Request, defaultLimit, maxLimit int) (Request, error) {
	if defaultLimit <= 0 {
		defaultLimit = 24
	}
	if maxLimit < defaultLimit {
		maxLimit = defaultLimit
	}
	limit := defaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxLimit {
			return Request{}, errors.New("limit is invalid")
		}
		limit = value
	}
	cursor, err := Decode(r.URL.Query().Get("cursor"))
	if err != nil {
		return Request{}, err
	}
	return Request{Limit: limit, Cursor: cursor}, nil
}

func Encode(cursor Cursor) (string, error) {
	if cursor.ID == uuid.Nil || cursor.SortTime.IsZero() {
		return "", ErrInvalidCursor
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func Decode(raw string) (*Cursor, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidCursor
	}
	var cursor Cursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.ID == uuid.Nil || cursor.SortTime.IsZero() {
		return nil, ErrInvalidCursor
	}
	return &cursor, nil
}

func NextCursor(cursor Cursor) *string {
	encoded, err := Encode(cursor)
	if err != nil {
		return nil
	}
	return &encoded
}
