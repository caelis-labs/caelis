package appserver

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestCursorCodecRoundTripAndSessionBinding(t *testing.T) {
	codec := newTestCursorCodec(t)
	want := eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: 42, ProjectionIndex: 3}}
	cursor, err := codec.Encode("session-a", want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cursor, "session-a") {
		t.Fatalf("cursor leaks session id: %q", cursor)
	}
	got, err := codec.Decode("session-a", cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got.Durable == nil || *got.Durable != *want.Durable {
		t.Fatalf("decoded position = %#v, want %#v", got, want)
	}
	if _, err := codec.Decode("session-b", cursor); !errors.Is(err, ErrCursorSessionMismatch) {
		t.Fatalf("cross-session Decode error = %v", err)
	}
}

func TestCursorCodecRejectsForgeryAndVersion(t *testing.T) {
	codec := newTestCursorCodec(t)
	cursor, err := codec.Encode("session-a", eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
		Anchor: eventstream.DurableFeedPosition{Seq: 7, ProjectionIndex: 1}, Generation: "generation-a", Sequence: 2,
	}})
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(cursor, ".")
	replacement := byte('A')
	if parts[2][0] == replacement {
		replacement = 'B'
	}
	parts[2] = string(replacement) + parts[2][1:]
	forged := strings.Join(parts, ".")
	if _, err := codec.Decode("session-a", forged); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("forged Decode error = %v", err)
	}

	payload, err := json.Marshal(resumeCursorPayload{
		Version: resumeCursorVersion + 1, KeyID: codec.keyID, SessionID: "session-a",
		Position: eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{Seq: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	message := resumeCursorPrefix + "." + encoded
	mac := hmac.New(sha256.New, codec.secret)
	_, _ = mac.Write([]byte(message))
	versioned := message + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if _, err := codec.Decode("session-a", versioned); !errors.Is(err, ErrCursorVersion) {
		t.Fatalf("version Decode error = %v", err)
	}
}

func newTestCursorCodec(t testing.TB) *CursorCodec {
	t.Helper()
	codec, err := NewCursorCodec(CursorCodecConfig{Secret: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	return codec
}
