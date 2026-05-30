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

func TestCloneContentPartsNormalizesAndDetaches(t *testing.T) {
	parts := []ContentPart{{Type: ContentPartText, Text: " hello ", URI: " file://a "}}
	clone := CloneContentParts(parts)
	parts[0].Text = "changed"

	if clone[0].Text != "hello" || clone[0].URI != "file://a" {
		t.Fatalf("clone = %#v, want trimmed detached content", clone[0])
	}
}
