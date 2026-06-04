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
