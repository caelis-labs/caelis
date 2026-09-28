package httpclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"net/http"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestReconnectReceivesLargeImageCanonicalReplacement(t *testing.T) {
	// An owned resource may contain 8 MiB of image bytes. Its base64 ACP tool
	// content occupies over 11 MiB on one SSE data line, above the old 8 MiB
	// client scanner bound. Use a decodable 1x1 PNG with trailing bytes so the
	// transport test exercises the maximum resource size without large pixels.
	pngBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC")
	if err != nil {
		t.Fatal(err)
	}
	imageBytes := append(pngBytes, make([]byte, 8<<20-len(pngBytes))...)
	if _, err := png.Decode(bytes.NewReader(imageBytes)); err != nil {
		t.Fatalf("fixture PNG: %v", err)
	}
	imageData := base64.StdEncoding.EncodeToString(imageBytes)
	if len(imageData) <= 8<<20 {
		t.Fatalf("fixture is too small for old scanner: %d", len(imageData))
	}
	envelope := eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1",
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "call-1",
			Content: []eventstream.ToolCallContent{{Type: "content", Content: map[string]any{
				"type": "image", "mimeType": "image/png", "data": imageData,
			}}},
		},
	}
	state := appserver.SessionState{
		ProtocolVersion: acpsdk.ProtocolVersionNumber, EnvelopeVersion: appserver.EnvelopeVersion,
		APIVersion: appserver.HTTPAPIVersion, SessionID: "session-1", BoundaryCursor: "boundary-1",
	}
	client, closeServer := newFixtureClient(t, reconnectFixture(t, state, []eventstream.Envelope{envelope}, nil, false))
	defer closeServer()
	if client.maxEventBytes < 32<<20+1 {
		t.Fatalf("scanner max %d cannot hold a 32 MiB replacement Envelope plus framing", client.maxEventBytes)
	}
	result, err := client.Reconnect(context.Background(), appserver.ReconnectRequest{SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	assembler := &appserver.FeedDeliveryAssembler{}
	var replacement []eventstream.Envelope
	for delivery := range result.Subscription.Deliveries() {
		events, replaced, err := assembler.Accept(delivery)
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Kind == appserver.FeedDeliveryReplacePage && (len(events) != 0 || replaced) {
			t.Fatal("replacement page published before ReplaceEnd")
		}
		if replaced {
			replacement = events
		}
	}
	if err := result.Subscription.Err(); err != nil {
		t.Fatalf("read large replacement SSE frame: %v", err)
	}
	if len(replacement) != 1 || replacement[0].Cursor != "" {
		t.Fatalf("canonical replacement count = %d, cursorless = %t", len(replacement), len(replacement) == 1 && replacement[0].Cursor == "")
	}
	update, ok := replacement[0].Update.(eventstream.ToolCallUpdate)
	if !ok || len(update.Content) != 1 {
		t.Fatalf("replaced update = %T, content count = %d", replacement[0].Update, len(update.Content))
	}
	image, ok := update.Content[0].Content.(map[string]any)
	if !ok || image["data"] != imageData {
		t.Fatalf("replacement image missing or truncated (expected data bytes %d)", len(imageData))
	}
}

func TestReconnectRetainsConfiguredSmallerSSELimit(t *testing.T) {
	client, closeServer := newFixtureClientWithConfig(t, Config{MaxEventBytes: 1 << 20}, func(_ http.ResponseWriter, _ *http.Request) {})
	defer closeServer()
	if client.maxEventBytes != 1<<20 {
		t.Fatalf("configured scanner limit = %d, want 1 MiB", client.maxEventBytes)
	}
}
