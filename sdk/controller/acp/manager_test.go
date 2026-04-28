package acp

import (
	"encoding/json"
	"testing"
	"time"

	sdkacpclient "github.com/OnslaughtSnail/caelis/acp/client"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func TestContentChunkTextPreservesStreamWhitespace(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(sdkacpclient.TextChunk{Type: "text", Text: "hello "})
	if err != nil {
		t.Fatal(err)
	}

	got := contentChunkText(sdkacpclient.ContentChunk{
		SessionUpdate: sdkacpclient.UpdateAgentMessage,
		Content:       raw,
	})
	if got != "hello " {
		t.Fatalf("contentChunkText() = %q, want trailing space preserved", got)
	}
}

func TestNormalizeACPUpdateEventKeepsCodexWebSearchToolIdentity(t *testing.T) {
	t.Parallel()

	event := normalizeACPUpdateEvent(func() time.Time { return time.Unix(0, 0) }, sdksession.ControllerBinding{
		Kind:         sdksession.ControllerKindACP,
		ControllerID: "codex",
		Label:        "codex",
	}, "remote-1", "turn-1", sdkacpclient.ToolCallUpdate{
		SessionUpdate: sdkacpclient.UpdateToolCallState,
		ToolCallID:    "ws_1",
		Kind:          testStringPtr("fetch"),
		Title:         testStringPtr("Searching for: weather: Shanghai, China"),
		Status:        testStringPtr("in_progress"),
		RawInput:      map[string]any{"query": "weather: Shanghai, China"},
	})
	if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		t.Fatalf("event = %#v, want structured tool update", event)
	}
	if got := event.Protocol.ToolCall.Name; got != "Searching for: weather: Shanghai, China" {
		t.Fatalf("tool name = %q, want ACP title", got)
	}
	if got := event.Protocol.ToolCall.Kind; got != "fetch" {
		t.Fatalf("tool kind = %q, want fetch", got)
	}
	if got := event.Protocol.ToolCall.RawInput["query"]; got != "weather: Shanghai, China" {
		t.Fatalf("raw input query = %#v", got)
	}
}

func testStringPtr(value string) *string {
	return &value
}
