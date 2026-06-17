package tuiapp

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestProjectACPEventToTranscriptEventsUsesEnvelopeScope(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:    eventstream.KindSessionUpdate,
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		Actor:   "copilot",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "subagent output"},
		},
		Final: true,
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].Scope != ACPProjectionSubagent || events[0].ScopeID != "task-1" || events[0].Actor != "copilot" {
		t.Fatalf("event scope = %#v, want subagent/task-1/copilot", events[0])
	}
}

func TestProjectACPEventToTranscriptEventsProjectsNotice(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind:   eventstream.KindNotice,
		Notice: "gateway notice",
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].Kind != TranscriptEventNotice || events[0].Text != "gateway notice" {
		t.Fatalf("event = %#v, want notice text", events[0])
	}
}

func TestProjectACPEventToTranscriptEventsDisplaysStandardRawTerminalOutput(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	kind := schema.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "side acp output\n"},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "side acp output\n" {
		t.Fatalf("ToolOutput = %q, want standard raw terminal output", events[0].ToolOutput)
	}
}

func TestProjectACPEventToTranscriptEventsDoesNotDisplayGatewayProjectedRawTerminalOutput(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	kind := schema.ToolKindExecute
	events := ProjectACPEventToTranscriptEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Meta: map[string]any{
			"caelis": map[string]any{
				"bridge": map[string]any{"source": "gateway_projection"},
			},
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "hidden raw output\n"},
		},
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolOutput != "" {
		t.Fatalf("ToolOutput = %q, want gateway-projected raw terminal output hidden without content", events[0].ToolOutput)
	}
}
