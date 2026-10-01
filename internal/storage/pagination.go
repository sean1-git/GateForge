package storage

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"gateforge/internal/security"
	"time"
)

type KeyQuery struct {
	Limit      int
	BeforeTime time.Time
	BeforeID   string
}
type KeyPage struct {
	Keys       []security.Key `json:"keys"`
	NextCursor string         `json:"next_cursor,omitempty"`
}
type keyCursor struct {
	Created time.Time `json:"t"`
	ID      string    `json:"id"`
}

func EncodeCursor(created time.Time, id string) string {
	b, _ := json.Marshal(keyCursor{created, id})
	return base64.RawURLEncoding.EncodeToString(b)
}
func ParseKeyQuery(limit int, cursor string) (KeyQuery, error) {
	q := KeyQuery{Limit: limit}
	if limit < 1 || limit > 200 {
		return q, errors.New("limit must be between 1 and 200")
	}
	if cursor == "" {
		return q, nil
	}
	invalid := errors.New("invalid key cursor")
	if len(cursor) > 256 {
		return q, invalid
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return q, invalid
	}
	var c keyCursor
	if json.Unmarshal(b, &c) != nil || c.Created.IsZero() || len(c.ID) != 24 {
		return q, invalid
	}
	for _, ch := range c.ID {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return q, invalid
		}
	}
	q.BeforeTime, q.BeforeID = c.Created, c.ID
	return q, nil
}
