package model

import (
	"encoding/json"
	"io"
	"testing"
)

func TestMessageTextAndToolCallsUseCanonicalParts(t *testing.T) {
	input := json.RawMessage(`{"path":"a.txt"}`)
	message := Message{
		Role: RoleAssistant,
		Parts: []Part{
			NewReasoningPart("think", ReasoningVisible),
			NewTextPart("answer"),
			{Kind: PartToolUse, ToolUse: &ToolCall{ID: "call-1", Name: "READ", Input: input}},
		},
	}

	if got := message.TextContent(); got != "answer" {
		t.Fatalf("TextContent() = %q, want answer", got)
	}
	calls := message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call-1" || string(calls[0].Input) != `{"path":"a.txt"}` {
		t.Fatalf("ToolCalls() = %#v", calls)
	}

	input[0] = '['
	if string(calls[0].Input) != `{"path":"a.txt"}` {
		t.Fatalf("ToolCalls did not clone input: %s", string(calls[0].Input))
	}
}

func TestNewReasoningPartPreservesStreamWhitespace(t *testing.T) {
	part := NewReasoningPart(" user", ReasoningVisible)
	if part.Reasoning == nil || part.Reasoning.VisibleText != " user" {
		t.Fatalf("reasoning part = %#v, want leading whitespace preserved", part)
	}
}

func TestStaticStreamEOF(t *testing.T) {
	stream := &StaticStream{Events: []StreamEvent{{Type: StreamTurnDone}}}
	event, err := stream.Recv()
	if err != nil || event.Type != StreamTurnDone {
		t.Fatalf("first Recv() = %#v, %v", event, err)
	}
	_, err = stream.Recv()
	if err != io.EOF {
		t.Fatalf("second Recv() err = %v, want EOF", err)
	}
}

func TestProviderToolPayloadUsesProviderFallbackAndClones(t *testing.T) {
	openaiPayload := json.RawMessage(`{"type":"web_search_preview"}`)
	defaultPayload := json.RawMessage(`{"type":"generic_tool"}`)
	spec := NewProviderExecutedToolSpec("web_search", map[string]json.RawMessage{
		"openai":  openaiPayload,
		"default": defaultPayload,
	})
	openaiPayload[0] = '['

	raw, ok := ProviderToolPayload(spec, "OPENAI")
	if !ok || string(raw) != `{"type":"web_search_preview"}` {
		t.Fatalf("openai payload = %q ok=%v, want cloned provider payload", string(raw), ok)
	}
	raw[0] = '['
	again, ok := ProviderToolPayload(spec, "openai")
	if !ok || string(again) != `{"type":"web_search_preview"}` {
		t.Fatalf("provider payload was not cloned: %q ok=%v", string(again), ok)
	}
	fallback, ok := ProviderToolPayload(spec, "anthropic")
	if !ok || string(fallback) != `{"type":"generic_tool"}` {
		t.Fatalf("fallback payload = %q ok=%v, want default payload", string(fallback), ok)
	}
}

func TestCloneContentPartsNormalizesAndDetaches(t *testing.T) {
	parts := []ContentPart{{Type: ContentPartText, Text: " hello ", URI: " file://a "}}
	clone := CloneContentParts(parts)
	parts[0].Text = "changed"

	if clone[0].Text != "hello" || clone[0].URI != "file://a" {
		t.Fatalf("clone = %#v, want trimmed detached content", clone[0])
	}
}
