package taskstream

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/control/streamspool"
)

const taskCursorVersion = 2

var errInvalidCursor = errors.New("taskstream: invalid cursor")

type cursorPoint struct {
	Key      streamspool.Key
	Offset   streamspool.Offset
	Sequence uint64
}

type cursorPayload struct {
	Version     int    `json:"v"`
	SessionID   string `json:"sid"`
	TaskID      string `json:"tid"`
	Epoch       string `json:"epoch"`
	Incarnation string `json:"inc"`
	Offset      uint64 `json:"offset"`
	Sequence    uint64 `json:"seq"`
}

type cursorCodec struct{ secret []byte }

func (c cursorCodec) encode(sessionID, taskID string, point cursorPoint) (string, error) {
	payload, err := json.Marshal(cursorPayload{
		Version: taskCursorVersion, SessionID: strings.TrimSpace(sessionID), TaskID: strings.TrimSpace(taskID),
		Epoch:       base64.RawURLEncoding.EncodeToString(point.Key.Epoch[:]),
		Incarnation: base64.RawURLEncoding.EncodeToString(point.Key.Incarnation[:]),
		Offset:      uint64(point.Offset), Sequence: point.Sequence,
	})
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := c.signature([]byte("t2." + encoded))
	return "t2." + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func (c cursorCodec) decode(sessionID, taskID, value string) (cursorPoint, bool, error) {
	if strings.TrimSpace(value) == "" {
		return cursorPoint{}, false, nil
	}
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 3 || parts[0] != "t2" {
		return cursorPoint{}, true, errInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cursorPoint{}, true, errInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, c.signature([]byte(parts[0]+"."+parts[1]))) {
		return cursorPoint{}, true, errInvalidCursor
	}
	var decoded cursorPayload
	if json.Unmarshal(payload, &decoded) != nil || decoded.Version != taskCursorVersion ||
		strings.TrimSpace(decoded.SessionID) != strings.TrimSpace(sessionID) ||
		strings.TrimSpace(decoded.TaskID) != strings.TrimSpace(taskID) {
		return cursorPoint{}, true, errInvalidCursor
	}
	epoch, err := base64.RawURLEncoding.DecodeString(decoded.Epoch)
	if err != nil || len(epoch) != len(streamspool.Epoch{}) {
		return cursorPoint{}, true, errInvalidCursor
	}
	incarnation, err := base64.RawURLEncoding.DecodeString(decoded.Incarnation)
	if err != nil || len(incarnation) != len(streamspool.Incarnation{}) {
		return cursorPoint{}, true, errInvalidCursor
	}
	point := cursorPoint{
		Key: streamspool.Key{LogicalKey: streamspool.LogicalKey{
			Namespace: streamspool.NamespaceTask,
			Digest:    streamspool.DigestStrings(sessionID, taskID),
		}},
		Offset: streamspool.Offset(decoded.Offset), Sequence: decoded.Sequence,
	}
	copy(point.Key.Epoch[:], epoch)
	copy(point.Key.Incarnation[:], incarnation)
	return point, true, nil
}

func (c cursorCodec) signature(value []byte) []byte {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}
