package history

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// Position binds a backward-only history request to its authorized address and
// immutable source. It is never accepted as a live delivery cursor.
type Position struct {
	SessionID string `json:"session"`
	TaskID    string `json:"task,omitempty"`
	Source    string `json:"source"`
	Before    uint64 `json:"before"`
}

func signature(secret []byte, value string) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(value))
	return m.Sum(nil)
}

// Encode returns an opaque, purpose-separated history token. The empty token
// marks the beginning of history and needs no further request.
func Encode(secret []byte, p Position) string {
	if p.Before == 0 {
		return ""
	}
	raw, _ := json.Marshal(p)
	body := "h1." + base64.RawURLEncoding.EncodeToString(raw)
	return body + "." + base64.RawURLEncoding.EncodeToString(signature(secret, body))
}

// Decode authenticates an exact Session/Task address before any source access.
func Decode(secret []byte, value, sessionID, taskID string) (Position, error) {
	invalid := errorcode.New(errorcode.InvalidArgument, "Invalid history token")
	parts := strings.Split(value, ".")
	if len(value) > 4096 || len(parts) != 3 || parts[0] != "h1" {
		return Position{}, invalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, signature(secret, parts[0]+"."+parts[1])) {
		return Position{}, invalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Position{}, invalid
	}
	var p Position
	if json.Unmarshal(raw, &p) != nil || p.SessionID != sessionID || p.TaskID != taskID || p.Source == "" || p.Before == 0 {
		return Position{}, invalid
	}
	return p, nil
}

// Stale reports a lost cache incarnation. Consumers keep their current view and
// reattach before trying again; they must not splice a different history source.
func Stale() error {
	return errorcode.New(errorcode.Conflict, "History source changed; reconnect to load earlier history")
}

// ValidateRequest rejects mixed traversal directions and unbounded window sizes.
func ValidateRequest(cursor, before string, turns int) error {
	if cursor != "" && before != "" || turns < 0 || turns > MaxTurns {
		return errorcode.New(errorcode.InvalidArgument, "Invalid history window or mixed history and live cursors")
	}
	return nil
}
