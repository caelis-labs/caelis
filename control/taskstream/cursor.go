package taskstream

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

const taskCursorVersion = 1

var errInvalidCursor = errors.New("taskstream: invalid cursor")

type cursorPoint struct {
	Cursor   stream.Cursor
	Sequence uint64
}

type cursorPayload struct {
	Version    int           `json:"v"`
	SessionID  string        `json:"sid"`
	TaskID     string        `json:"tid"`
	Generation string        `json:"gen"`
	Cursor     stream.Cursor `json:"cursor"`
	Sequence   uint64        `json:"seq"`
}

type cursorCodec struct {
	secret     []byte
	generation string
}

func (c cursorCodec) encode(sessionID, taskID string, point cursorPoint) (string, error) {
	payload, err := json.Marshal(cursorPayload{
		Version: taskCursorVersion, SessionID: strings.TrimSpace(sessionID), TaskID: strings.TrimSpace(taskID),
		Generation: c.generation, Cursor: stream.CloneCursor(point.Cursor), Sequence: point.Sequence,
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := c.signature([]byte("t1." + encoded))
	return "t1." + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c cursorCodec) decode(sessionID, taskID, value string) (cursorPoint, bool, error) {
	if strings.TrimSpace(value) == "" {
		return cursorPoint{}, true, nil
	}
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 3 || parts[0] != "t1" {
		return cursorPoint{}, false, errInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cursorPoint{}, false, errInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, c.signature([]byte(parts[0]+"."+parts[1]))) {
		return cursorPoint{}, false, errInvalidCursor
	}
	var decoded cursorPayload
	if json.Unmarshal(payload, &decoded) != nil || decoded.Version != taskCursorVersion ||
		strings.TrimSpace(decoded.SessionID) != strings.TrimSpace(sessionID) ||
		strings.TrimSpace(decoded.TaskID) != strings.TrimSpace(taskID) {
		return cursorPoint{}, false, errInvalidCursor
	}
	return cursorPoint{Cursor: stream.CloneCursor(decoded.Cursor), Sequence: decoded.Sequence}, decoded.Generation == c.generation, nil
}

func (c cursorCodec) signature(value []byte) []byte {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}
