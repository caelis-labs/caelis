package appserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

const (
	resumeCursorVersion = 1
	resumeCursorPrefix  = "c1"
)

var (
	ErrInvalidCursor         = errors.New("appserver: invalid resume cursor")
	ErrCursorSessionMismatch = errors.New("appserver: resume cursor belongs to another session")
	ErrCursorVersion         = errors.New("appserver: unsupported resume cursor version")
)

// CursorCodecConfig configures one persistent signed resume-token codec.
type CursorCodecConfig struct {
	Secret []byte
	KeyID  string
}

// CursorCodec signs and verifies the sole public client resume token issued by
// the Control Session feed.
type CursorCodec struct {
	secret []byte
	keyID  string
}

type resumeCursorPayload struct {
	Version   int                      `json:"v"`
	KeyID     string                   `json:"kid"`
	SessionID string                   `json:"sid"`
	Position  eventstream.FeedPosition `json:"pos"`
}

// NewCursorCodec constructs a signed cursor codec. A 256-bit secret is
// required so accidentally using a user-visible identifier fails closed.
func NewCursorCodec(cfg CursorCodecConfig) (*CursorCodec, error) {
	if len(cfg.Secret) < sha256.Size {
		return nil, fmt.Errorf("%w: signing secret must be at least %d bytes", ErrInvalidCursor, sha256.Size)
	}
	keyID := strings.TrimSpace(cfg.KeyID)
	if keyID == "" {
		keyID = "default"
	}
	return &CursorCodec{secret: append([]byte(nil), cfg.Secret...), keyID: keyID}, nil
}

// Encode returns a signed opaque Cursor bound to one Session and position.
func (c *CursorCodec) Encode(sessionID string, position eventstream.FeedPosition) (string, error) {
	if c == nil || len(c.secret) < sha256.Size {
		return "", ErrInvalidCursor
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || position.Validate() != nil {
		return "", ErrInvalidCursor
	}
	payload, err := json.Marshal(resumeCursorPayload{
		Version: resumeCursorVersion, KeyID: c.keyID, SessionID: sessionID, Position: position,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := c.signature([]byte(resumeCursorPrefix + "." + encoded))
	return resumeCursorPrefix + "." + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// Decode verifies and returns one Cursor position for the expected Session.
func (c *CursorCodec) Decode(expectedSessionID string, cursor string) (eventstream.FeedPosition, error) {
	if c == nil || len(c.secret) < sha256.Size {
		return eventstream.FeedPosition{}, ErrInvalidCursor
	}
	parts := strings.Split(strings.TrimSpace(cursor), ".")
	if len(parts) != 3 {
		return eventstream.FeedPosition{}, ErrInvalidCursor
	}
	if parts[0] != resumeCursorPrefix {
		return eventstream.FeedPosition{}, ErrCursorVersion
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return eventstream.FeedPosition{}, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, c.signature([]byte(parts[0]+"."+parts[1]))) {
		return eventstream.FeedPosition{}, ErrInvalidCursor
	}
	var decoded resumeCursorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return eventstream.FeedPosition{}, ErrInvalidCursor
	}
	if decoded.Version != resumeCursorVersion || strings.TrimSpace(decoded.KeyID) != c.keyID {
		return eventstream.FeedPosition{}, ErrCursorVersion
	}
	if strings.TrimSpace(decoded.SessionID) != strings.TrimSpace(expectedSessionID) {
		return eventstream.FeedPosition{}, ErrCursorSessionMismatch
	}
	if err := decoded.Position.Validate(); err != nil {
		return eventstream.FeedPosition{}, ErrInvalidCursor
	}
	return *eventstream.CloneFeedPosition(&decoded.Position), nil
}

func (c *CursorCodec) signature(value []byte) []byte {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}
