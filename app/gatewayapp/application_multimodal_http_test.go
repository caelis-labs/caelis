package gatewayapp_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// multimodalHTTPModel observes real adapter requests. In Chat Completions an
// image from a tool is bridged into a user message after its tool_call_id; the
// fixture terminates when it sees the tool message, not when the last role is tool.
type multimodalHTTPModel struct {
	mu       sync.Mutex
	requests [][]byte
}

func (m *multimodalHTTPModel) RoundTrip(req *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	var body struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.requests = append(m.requests, append([]byte(nil), raw...))
	m.mu.Unlock()
	seenTool := false
	for _, message := range body.Messages {
		seenTool = seenTool || message.Role == "tool"
	}
	message := map[string]any{"role": "assistant", "content": "MULTIMODAL_FINAL_SENTINEL"}
	finish := "stop"
	if !seenTool {
		message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{
			"id": "provider-image-call", "index": 0, "type": "function", "function": map[string]any{
				"name": "ApplicationLookup", "arguments": `{"key":"image"}`,
			},
		}}}
		finish = "tool_calls"
	}
	payload, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": finish}}})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("data: " + string(payload) + "\n\ndata: [DONE]\n\n")), Request: req}, nil
}

func (m *multimodalHTTPModel) snapshot() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([][]byte, len(m.requests))
	for i := range m.requests {
		out[i] = append([]byte(nil), m.requests[i]...)
	}
	return out
}

func tinyApplicationPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{B: 255, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func multimodalHTTPProfile(model string) application.Profile {
	profile := applicationHTTPProfile()
	profile.Model = "openai-compatible/" + model
	profile.Tools[0].ResultFormat = application.ResultFormatContentV1
	profile.Tools[0].OutputSchema = map[string]any{
		"type": "object", "properties": map[string]any{"score": map[string]any{"type": "integer"}, "identifier": map[string]any{"type": "integer"}},
		"required": []any{"score"}, "additionalProperties": false,
	}
	return profile
}

func multimodalHTTPImageRequest(t *testing.T, raw, pngBytes []byte, callID string) {
	t.Helper()
	var request struct {
		Messages []struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"tool_call_id"`
			Content    json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	wantURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	for i, message := range request.Messages {
		if message.Role != "tool" || message.ToolCallID != callID {
			continue
		}
		if !bytes.Contains(message.Content, []byte("outcome")) || !bytes.Contains(message.Content, []byte("structuredContent")) ||
			!bytes.Contains(message.Content, []byte("score")) || !bytes.Contains(message.Content, []byte("9007199254740991")) {
			t.Fatalf("tool_call_id %q lost structured receipt: %s", callID, message.Content)
		}
		if i+1 >= len(request.Messages) || request.Messages[i+1].Role != "user" {
			t.Fatalf("tool_call_id %q lacks adjacent image-bearing input: %s", callID, raw)
		}
		var parts []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		}
		if err := json.Unmarshal(request.Messages[i+1].Content, &parts); err != nil {
			t.Fatalf("image-bearing request is not native content: %v; %s", err, request.Messages[i+1].Content)
		}
		first, imageAt, last, receiptAt := -1, -1, -1, -1
		for j, part := range parts {
			switch {
			case part.Type == "image_url" && part.ImageURL.URL == wantURL:
				imageAt = j
			case part.Type == "text" && part.Text == "first text":
				first = j
			case part.Type == "text" && part.Text == "last text":
				last = j
			case part.Type == "text" && strings.Contains(part.Text, `"structuredContent"`) && strings.Contains(part.Text, `"outcome":"succeeded"`):
				receiptAt = j
			}
		}
		if first < 0 || first >= imageAt || imageAt >= last || last >= receiptAt {
			t.Fatalf("provider lost ordered text/image/text/receipt: %+v", parts)
		}
		return
	}
	t.Fatalf("provider request lacks tool_call_id %q: %s", callID, raw)
}

// TestApplicationMultimodalHTTP exercises callback admission, inline image
// validation, canonical replay, and the actual model provider request through
// the authenticated Host. Image-unsupported or unknown models still retain the
// completed callback and historical image but receive no image model request.
func TestApplicationMultimodalHTTP(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name       string
		imageInput *bool
		resource   bool
	}{
		{"supported", &yes, false},
		{"unsupported", &no, false},
		{"unknown", nil, false},
		{"resource", &yes, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 35*time.Second)
			defer cancel()
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			provider := &multimodalHTTPModel{}
			store := filepath.Join(root, "store")
			host := startApplicationHTTPHost(t, store, workspace, provider)
			defer func() { host.close(t) }()
			status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
			if err != nil {
				t.Fatal(err)
			}
			connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
				WriteBase: appserver.WriteBase{OperationID: "connect-model", ExpectedRevision: &status.Configuration.Revision},
				Config: appserver.ConnectConfig{Provider: "openai-compatible", Model: "multimodal-" + tc.name,
					BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY", ImageInput: tc.imageInput},
			})
			if err != nil || connected.Outcome != appserver.OutcomeCommitted {
				t.Fatalf("ConnectModel: %+v %v", connected, err)
			}
			credentialPath := filepath.Join(root, "app.credential")
			client, _ := registerApplicationHTTP(t, ctx, host, "multimodal-"+tc.name, credentialPath)
			profile := multimodalHTTPProfile("multimodal-" + tc.name)
			created, err := client.CreateApplicationSession(ctx, appserver.CreateApplicationSessionRequest{
				WriteBase: appserver.WriteBase{OperationID: "create-image-session"}, Profile: profile,
			})
			if err != nil || created.Outcome != appserver.OutcomeCommitted || created.SessionID == "" {
				t.Fatalf("CreateApplicationSession: %+v %v", created, err)
			}
			session := created.SessionID
			capabilities, err := client.ApplicationModelCapabilities(ctx, session)
			if err != nil || capabilities.Model != profile.Model || !reflect.DeepEqual(capabilities.ImageInput, tc.imageInput) {
				t.Fatalf("selected model image capability: %+v %v", capabilities, err)
			}
			admitted, err := client.PromptApplication(ctx, appserver.ApplicationPromptRequest{
				PromptRequest: appserver.PromptRequest{WriteBase: appserver.WriteBase{SessionID: session, OperationID: "image-turn"}, Input: "Look up the image."},
				SourceKind:    "user",
			})
			if err != nil || (admitted.Outcome != appserver.OutcomeAccepted && admitted.Outcome != appserver.OutcomeCommitted) {
				t.Fatalf("PromptApplication: %+v %v", admitted, err)
			}
			calls, err := client.WaitApplicationCalls(ctx, session)
			if err != nil || len(calls) != 1 || calls[0].CallID != "provider-image-call" {
				t.Fatalf("WaitApplicationCalls: %+v %v", calls, err)
			}
			call := calls[0]
			if _, err := client.ClaimApplicationCall(ctx, session, call.ID); err != nil {
				t.Fatal(err)
			}
			pngBytes := tinyApplicationPNG(t)
			imageBlock := application.ContentBlock{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(pngBytes)}
			if tc.resource {
				// A decodable PNG larger than the inline limit must take the
				// authenticated upload/reference path, never a local path or URL.
				pngBytes = append(pngBytes, make([]byte, application.MaxInlineImageBytes+1-len(pngBytes))...)
				digest := sha256.Sum256(pngBytes)
				expires := time.Now().Add(5 * time.Minute)
				resource, err := client.UploadApplicationResource(ctx, appserver.ApplicationResourceRequest{
					WriteBase: appserver.WriteBase{SessionID: session, OperationID: "upload-image"},
					Name:      "screen.png", MediaType: "image/png", Data: pngBytes,
					SHA256: hex.EncodeToString(digest[:]), ExpiresAt: &expires,
				})
				if err != nil {
					t.Fatal(err)
				}
				imageBlock = application.ContentBlock{Type: "resource_link", MIMEType: resource.MediaType,
					URI: application.ResourceURIPrefix + resource.ID, Name: resource.Name, SHA256: resource.SHA256}
			}
			first, last := "first text", "last text"
			content, err := json.Marshal([]application.ContentBlock{
				{Type: "text", Text: &first}, imageBlock, {Type: "text", Text: &last},
			})
			if err != nil {
				t.Fatal(err)
			}
			result := application.CallResult{Outcome: "succeeded", ResultFormat: application.ResultFormatContentV1,
				Content: content, StructuredContent: map[string]any{"score": 7, "identifier": json.Number("9007199254740991")}}
			if err := client.CompleteApplicationCall(ctx, session, call.ID, result); err != nil {
				t.Fatalf("CompleteApplicationCall: %v", err)
			}
			completed, err := client.ApplicationCall(ctx, session, call.ID)
			if err != nil || completed.State != "completed" || completed.Result == nil || completed.Result.ResultFormat != application.ResultFormatContentV1 ||
				!bytes.Equal(completed.Result.Content, content) || !reflect.DeepEqual(completed.Result.StructuredContent, map[string]any{"score": json.Number("7"), "identifier": json.Number("9007199254740991")}) {
				t.Fatalf("completed content-v1 receipt: %+v %v", completed, err)
			}
			waitApplicationHTTPIdle(t, ctx, client, session)
			requests := provider.snapshot()
			if tc.imageInput != nil && *tc.imageInput {
				if len(requests) != 2 {
					t.Fatalf("supported model requests = %d, want tool call and image response", len(requests))
				}
				multimodalHTTPImageRequest(t, requests[1], pngBytes, call.CallID)
			} else if len(requests) != 1 {
				t.Fatalf("image-unsupported model got %d requests, want only initial tool call", len(requests))
			}
			history := applicationHTTPHistory(t, ctx, client, session)
			var toolContent []eventstream.ToolCallContent
			for _, envelope := range history {
				if update, ok := envelope.Update.(eventstream.ToolCallUpdate); ok && update.ToolCallID == call.CallID && len(update.Content) != 0 {
					toolContent = update.Content
				}
			}
			encoded, err := json.Marshal(toolContent)
			if err != nil {
				t.Fatal(err)
			}
			firstAt := bytes.Index(encoded, []byte("first text"))
			imageAt := bytes.Index(encoded, []byte(base64.StdEncoding.EncodeToString(pngBytes)))
			lastAt := bytes.Index(encoded, []byte("last text"))
			receiptAt := bytes.Index(encoded, []byte("structuredContent"))
			if len(toolContent) == 0 || firstAt < 0 || firstAt >= imageAt || imageAt >= lastAt || lastAt >= receiptAt {
				t.Fatalf("canonical history lost ordered text/image/text/structured receipt: %s", encoded)
			}
			// Replace the Host and Store, then recover both the callback receipt
			// and the canonical media snapshot without redispatch or provider I/O.
			host.close(t)
			host = startApplicationHTTPHost(t, store, workspace, provider)
			secret, err := os.ReadFile(credentialPath)
			if err != nil {
				t.Fatal(err)
			}
			client = host.app(string(secret))
			recovered, err := client.ApplicationCall(ctx, session, call.ID)
			if err != nil || !reflect.DeepEqual(recovered.Result, completed.Result) || recovered.State != "completed" {
				t.Fatalf("Host restart changed callback receipt: %+v %v", recovered, err)
			}
			replayed := applicationHTTPHistory(t, ctx, client, session)
			replayedIDs := applicationHTTPCanonicalIDs(replayed)
			for id := range applicationHTTPCanonicalIDs(history) {
				if !replayedIDs[id] {
					t.Fatalf("Host restart lost canonical event %s", id)
				}
			}
			var replayedContent []eventstream.ToolCallContent
			for _, envelope := range replayed {
				if update, ok := envelope.Update.(eventstream.ToolCallUpdate); ok && update.ToolCallID == call.CallID && len(update.Content) != 0 {
					replayedContent = update.Content
				}
			}
			if !reflect.DeepEqual(replayedContent, toolContent) || len(provider.snapshot()) != len(requests) {
				t.Fatalf("Host restart lost canonical image or re-invoked provider: before=%+v after=%+v", toolContent, replayedContent)
			}
		})
	}
}
