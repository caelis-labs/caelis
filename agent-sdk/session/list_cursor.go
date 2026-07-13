package session

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const sessionListCursorVersion = 1

// SessionListCursor is the stable keyset position for UpdatedAt-descending
// Session listings. Store implementations use it to provide opaque paging.
type SessionListCursor struct {
	UpdatedAt time.Time
	SessionID string
}

type sessionListCursorPayload struct {
	Version     int    `json:"v"`
	UpdatedAtNS int64  `json:"updated_at_ns"`
	SessionID   string `json:"session_id"`
}

// EncodeSessionListCursor encodes one opaque Session list keyset position.
func EncodeSessionListCursor(cursor SessionListCursor) (string, error) {
	sessionID := strings.TrimSpace(cursor.SessionID)
	if sessionID == "" {
		return "", fmt.Errorf("agent-sdk/session: list cursor session id is required")
	}
	raw, err := json.Marshal(sessionListCursorPayload{
		Version: sessionListCursorVersion, UpdatedAtNS: cursor.UpdatedAt.UnixNano(), SessionID: sessionID,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeSessionListCursor decodes and validates one opaque Session list cursor.
func DecodeSessionListCursor(encoded string) (SessionListCursor, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return SessionListCursor{}, fmt.Errorf("agent-sdk/session: list cursor is required")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return SessionListCursor{}, fmt.Errorf("agent-sdk/session: decode list cursor: %w", err)
	}
	var payload sessionListCursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return SessionListCursor{}, fmt.Errorf("agent-sdk/session: decode list cursor payload: %w", err)
	}
	if payload.Version != sessionListCursorVersion || strings.TrimSpace(payload.SessionID) == "" {
		return SessionListCursor{}, fmt.Errorf("agent-sdk/session: invalid list cursor")
	}
	return SessionListCursor{UpdatedAt: time.Unix(0, payload.UpdatedAtNS), SessionID: strings.TrimSpace(payload.SessionID)}, nil
}
