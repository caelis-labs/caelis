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
	"github.com/caelis-labs/caelis/control/streamspool"
)

const (
	resumeCursorVersion = 2
	resumeCursorPrefix  = "c2"
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
	Spool     *spoolCursorPayload      `json:"spool,omitempty"`
}

type spoolCursorPayload struct {
	Epoch       string `json:"epoch"`
	Incarnation string `json:"incarnation"`
	Offset      uint64 `json:"offset"`
}

type sessionSpoolCursor struct {
	Key    streamspool.Key
	Offset streamspool.Offset
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

// EncodeSpool returns a signed cursor for the next record in one exact Session
// partition. Position remains the Envelope semantic position; spool identity
// is transport-only and never becomes Session truth.
func (c *CursorCodec) EncodeSpool(sessionID string, point sessionSpoolCursor, position eventstream.FeedPosition) (string, error) {
	if c == nil || len(c.secret) < sha256.Size || point.Key.Namespace != streamspool.NamespaceSession {
		return "", ErrInvalidCursor
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || position.Validate() != nil || point.Key.Digest != streamspool.DigestStrings(sessionID) {
		return "", ErrInvalidCursor
	}
	payload, err := json.Marshal(resumeCursorPayload{
		Version: resumeCursorVersion, KeyID: c.keyID, SessionID: sessionID, Position: position,
		Spool: &spoolCursorPayload{
			Epoch:       base64.RawURLEncoding.EncodeToString(point.Key.Epoch[:]),
			Incarnation: base64.RawURLEncoding.EncodeToString(point.Key.Incarnation[:]),
			Offset:      uint64(point.Offset),
		},
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidCursor, err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := c.signature([]byte(resumeCursorPrefix + "." + encoded))
	return resumeCursorPrefix + "." + encoded + "." + base64.RawURLEncoding.EncodeToString(signature), nil
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
	decoded, err := c.decode(expectedSessionID, cursor)
	if err != nil {
		return eventstream.FeedPosition{}, err
	}
	return *eventstream.CloneFeedPosition(&decoded.Position), nil
}

func (c *CursorCodec) DecodeSpool(expectedSessionID string, cursor string) (sessionSpoolCursor, eventstream.FeedPosition, error) {
	point, position, err := c.decodeResume(expectedSessionID, cursor)
	if err != nil || point == nil {
		if err == nil {
			err = ErrInvalidCursor
		}
		return sessionSpoolCursor{}, eventstream.FeedPosition{}, err
	}
	return *point, position, nil
}

// decodeResume validates a public cursor and returns its optional cache
// address. A signed position-only cursor is valid and intentionally selects
// authoritative replacement instead of exact spool resume.
func (c *CursorCodec) decodeResume(expectedSessionID string, cursor string) (*sessionSpoolCursor, eventstream.FeedPosition, error) {
	decoded, err := c.decode(expectedSessionID, cursor)
	if err != nil {
		return nil, eventstream.FeedPosition{}, err
	}
	position := *eventstream.CloneFeedPosition(&decoded.Position)
	if decoded.Spool == nil {
		return nil, position, nil
	}
	epoch, err := base64.RawURLEncoding.DecodeString(decoded.Spool.Epoch)
	if err != nil || len(epoch) != len(streamspool.Epoch{}) {
		return nil, eventstream.FeedPosition{}, ErrInvalidCursor
	}
	incarnation, err := base64.RawURLEncoding.DecodeString(decoded.Spool.Incarnation)
	if err != nil || len(incarnation) != len(streamspool.Incarnation{}) {
		return nil, eventstream.FeedPosition{}, ErrInvalidCursor
	}
	point := &sessionSpoolCursor{Key: streamspool.Key{LogicalKey: streamspool.LogicalKey{
		Namespace: streamspool.NamespaceSession, Digest: streamspool.DigestStrings(expectedSessionID),
	}}, Offset: streamspool.Offset(decoded.Spool.Offset)}
	copy(point.Key.Epoch[:], epoch)
	copy(point.Key.Incarnation[:], incarnation)
	return point, position, nil
}

func (c *CursorCodec) decode(expectedSessionID string, cursor string) (resumeCursorPayload, error) {
	if c == nil || len(c.secret) < sha256.Size {
		return resumeCursorPayload{}, ErrInvalidCursor
	}
	parts := strings.Split(strings.TrimSpace(cursor), ".")
	if len(parts) != 3 {
		return resumeCursorPayload{}, ErrInvalidCursor
	}
	if parts[0] != resumeCursorPrefix {
		return resumeCursorPayload{}, ErrCursorVersion
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return resumeCursorPayload{}, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, c.signature([]byte(parts[0]+"."+parts[1]))) {
		return resumeCursorPayload{}, ErrInvalidCursor
	}
	var decoded resumeCursorPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return resumeCursorPayload{}, ErrInvalidCursor
	}
	if decoded.Version != resumeCursorVersion || strings.TrimSpace(decoded.KeyID) != c.keyID {
		return resumeCursorPayload{}, ErrCursorVersion
	}
	if strings.TrimSpace(decoded.SessionID) != strings.TrimSpace(expectedSessionID) {
		return resumeCursorPayload{}, ErrCursorSessionMismatch
	}
	if err := decoded.Position.Validate(); err != nil {
		return resumeCursorPayload{}, ErrInvalidCursor
	}
	return decoded, nil
}

func (c *CursorCodec) signature(value []byte) []byte {
	mac := hmac.New(sha256.New, c.secret)
	_, _ = mac.Write(value)
	return mac.Sum(nil)
}
