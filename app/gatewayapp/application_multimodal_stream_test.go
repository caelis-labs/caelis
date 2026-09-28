package gatewayapp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// largeImageStreamModel finishes the image callback Turn, then holds the next
// Turn's provider stream open after writing two text deltas. No final answer is
// available while the feed assertion runs.
type largeImageStreamModel struct {
	multimodalHTTPModel
	mu      sync.Mutex
	calls   int
	blocked chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *largeImageStreamModel) RoundTrip(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call <= 2 {
		return m.multimodalHTTPModel.RoundTrip(req)
	}
	if call != 3 {
		return nil, fmt.Errorf("unexpected model invocation %d", call)
	}
	if _, err := io.Copy(io.Discard, req.Body); err != nil {
		return nil, err
	}
	reader, writer := io.Pipe()
	go func() {
		defer writer.Close()
		for _, delta := range []string{"STREAM_FIRST_DELTA", "STREAM_SECOND_DELTA"} {
			payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
				"index": 0, "delta": map[string]any{"role": "assistant", "content": delta}, "finish_reason": nil,
			}}})
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", payload); err != nil {
				return
			}
		}
		close(m.blocked) // The provider stream has handed both deltas to its reader.
		select {
		case <-m.release:
		case <-req.Context().Done():
			return
		}
		payload, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
			"index": 0, "delta": map[string]any{}, "finish_reason": "stop",
		}}})
		_, _ = fmt.Fprintf(writer, "data: %s\n\ndata: [DONE]\n\n", payload)
	}()
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader, Request: req}, nil
}

func (m *largeImageStreamModel) unblock() { m.once.Do(func() { close(m.release) }) }

// largeApplicationPNG uses alpha-varying pixels and uncompressed encoding to
// exercise large, fully decodable resource images rather than trailing-byte padding.
func largeApplicationPNG(t *testing.T) []byte {
	t.Helper()
	const side = 1024
	img := image.NewNRGBA(image.Rect(0, 0, side, side))
	state := uint32(0x517cc1b7)
	for i := range img.Pix {
		state ^= state << 13
		state ^= state >> 17
		state ^= state << 5
		img.Pix[i] = byte(state)
	}
	var data bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	if data.Len() <= 3<<20 || data.Len() > application.MaxResourceBytes {
		t.Fatalf("encoded PNG size = %d; need a valid resource image above 3 MiB and within the 8 MiB admission limit", data.Len())
	}
	return data.Bytes()
}

// TestApplicationLargeImageKeepsNextTurnTransientFeed checks the default Host
// spool, not a hand-configured spool or canonical replay. A large content-v1
// resource image must not discard the exact Session feed needed for the next
// Turn's transient assistant deltas.
func TestApplicationLargeImageKeepsNextTurnTransientFeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	provider := &largeImageStreamModel{blocked: make(chan struct{}), release: make(chan struct{})}
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), workspace, provider)
	defer host.close(t)
	defer provider.unblock()

	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	yes := true
	connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-stream-model", ExpectedRevision: &status.Configuration.Revision},
		Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "large-image-stream",
			BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY", ImageInput: &yes},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("ConnectModel: %+v %v", connected, err)
	}
	client, _ := registerApplicationHTTP(t, ctx, host, "large-image-stream", filepath.Join(root, "app.credential"))
	created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "create-large-image-session"}, Profile: multimodalHTTPProfile("large-image-stream"),
	})
	if err != nil || created.Outcome != appserver.OutcomeCommitted || created.SessionID == "" {
		t.Fatalf("CreateApplicationSession: %+v %v", created, err)
	}
	sessionID := created.SessionID
	prompt := func(operation, input string) {
		t.Helper()
		out, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{
			PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: sessionID, OperationID: operation}, Input: input},
			SourceKind:    "user",
		})
		if err != nil || (out.Outcome != appserver.OutcomeAccepted && out.Outcome != appserver.OutcomeCommitted) {
			t.Fatalf("PromptApplication(%s): %+v %v", operation, out, err)
		}
	}
	prompt("image-turn", "Look up the image.")
	calls, err := client.WaitApplicationCalls(ctx, sessionID)
	if err != nil || len(calls) != 1 || calls[0].CallID != "provider-image-call" {
		t.Fatalf("WaitApplicationCalls: %+v %v", calls, err)
	}
	call := calls[0]
	feed, err := client.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	defer feed.Subscription.Close()
	for {
		select {
		case delivery, ok := <-feed.Subscription.Deliveries():
			if !ok {
				t.Fatalf("initial feed closed: %v", feed.Subscription.Err())
			}
			if delivery.Kind == appserver.FeedDeliverySync {
				goto synced
			}
		case <-ctx.Done():
			t.Fatalf("initial feed sync: %v", ctx.Err())
		}
	}
synced:
	if _, err := client.ClaimApplicationCall(ctx, sessionID, call.ID); err != nil {
		t.Fatal(err)
	}
	image := largeApplicationPNG(t)
	digest := sha256.Sum256(image)
	expires := time.Now().Add(5 * time.Minute)
	resource, err := client.UploadApplicationResource(ctx, appserver.ApplicationResourceRequest{
		WriteBase: appserver.WriteBase{SessionID: sessionID, OperationID: "upload-large-image"},
		Name:      "screen.png", MediaType: "image/png", Data: image,
		SHA256: hex.EncodeToString(digest[:]), ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatalf("UploadApplicationResource(%d bytes): %v", len(image), err)
	}
	first, last := "first text", "last text"
	content, err := json.Marshal([]application.ContentBlock{
		{Type: "text", Text: &first},
		{Type: "resource_link", MIMEType: resource.MediaType, URI: application.ResourceURIPrefix + resource.ID, Name: resource.Name, SHA256: resource.SHA256},
		{Type: "text", Text: &last},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CompleteApplicationCall(ctx, sessionID, call.ID, application.CallResult{
		Outcome: "succeeded", ResultFormat: application.ResultFormatContentV1, Content: content,
		StructuredContent: map[string]any{"score": 7, "identifier": json.Number("9007199254740991")},
	}); err != nil {
		t.Fatalf("CompleteApplicationCall: %v", err)
	}
	wantBase64 := []byte(base64.StdEncoding.EncodeToString(image))
	nextExactPage := func() appserver.FeedDelivery {
		t.Helper()
		for {
			select {
			case delivery, ok := <-feed.Subscription.Deliveries():
				if !ok {
					t.Fatalf("Session feed ended: %v", feed.Subscription.Err())
				}
				if delivery.Kind == appserver.FeedDeliveryReplaceBegin || delivery.Kind == appserver.FeedDeliveryReplacePage || delivery.Kind == appserver.FeedDeliveryReplaceEnd {
					t.Fatalf("large image invalidated spool; canonical replacement cannot preserve subsequent transient deltas: delivery=%s", delivery.Kind)
				}
				if delivery.Kind == appserver.FeedDeliveryAppendPage && delivery.Source == appserver.FeedSourceExact {
					return delivery
				}
			case <-ctx.Done():
				t.Fatalf("waiting for exact Session feed: %v", ctx.Err())
			}
		}
	}
	imageExact, firstTurnComplete := false, false
	for !firstTurnComplete {
		for _, event := range nextExactPage().Events {
			if update, ok := event.Update.(eventstream.ToolCallUpdate); ok && update.ToolCallID == call.CallID && len(update.Content) != 0 {
				encoded, err := json.Marshal(update.Content)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(encoded, wantBase64) || event.Cursor == "" || event.Position == nil {
					t.Fatalf("image callback lacks exact spool media (%d PNG bytes, cursor=%q)", len(image), event.Cursor)
				}
				imageExact = true
			}
			if event.TurnID == call.TurnID && eventstream.IsTurnTerminalLifecycle(event) {
				if event.Lifecycle.State != eventstream.LifecycleStateCompleted {
					t.Fatalf("image Turn terminated: %+v", event.Lifecycle)
				}
				firstTurnComplete = true
			}
		}
	}
	if !imageExact {
		t.Fatal("large resource image did not arrive through the exact Session spool")
	}
	waitApplicationHTTPIdle(t, ctx, client, sessionID)
	requests := provider.snapshot()
	if len(requests) != 2 {
		t.Fatalf("model requests before next Turn = %d, want callback and image response", len(requests))
	}
	multimodalHTTPImageRequest(t, requests[1], image, call.CallID)
	// Keep reading the previously attached observer across the Turn boundary:
	// neither image replay nor a durable final may stand in for the second Turn's
	// exact transient provider deltas.
	prompt("following-turn", "Reply with streamed text.")
	select {
	case <-provider.blocked:
	case <-ctx.Done():
		t.Fatalf("waiting for provider deltas before final: %v", ctx.Err())
	}
	want := []string{"STREAM_FIRST_DELTA", "STREAM_SECOND_DELTA"}
	var got []string
	for len(got) < len(want) {
		for _, event := range nextExactPage().Events {
			chunk, ok := event.Update.(eventstream.ContentChunk)
			if !ok || chunk.SessionUpdate != eventstream.UpdateAgentMessage || event.TurnID == call.TurnID {
				continue
			}
			raw, err := json.Marshal(chunk.Content)
			if err != nil {
				t.Fatal(err)
			}
			var text eventstream.TextContent
			if err := json.Unmarshal(raw, &text); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(text.Text, "STREAM_") {
				continue
			}
			if event.Delivery == nil || event.Delivery.Mode != eventstream.DeliveryTransient || event.Final || event.EventID != "" || event.Cursor == "" || event.Position == nil || event.Position.Transient == nil {
				t.Fatalf("provider delta was not an exact transient append: %+v", event)
			}
			got = append(got, text.Text)
		}
	}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("transient feed emitted %q, want exact ordered deltas %q", got, want)
	}
	state, err := client.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	if err != nil || !state.Run.Active {
		t.Fatalf("provider must remain blocked before durable final: active=%v err=%v", state.Run.Active, err)
	}
	provider.unblock()
	waitApplicationHTTPIdle(t, ctx, client, sessionID)
}
