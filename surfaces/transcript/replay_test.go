package transcript

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestProjectReplayEventsKeepsFinalAssistantChunksOnly(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "partial"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentMessage,
				Content:       schema.TextContent{Type: "text", Text: "final"},
			},
			Final: true,
		},
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one final replay event", events)
	}
	if events[0].Kind != EventNarrative || events[0].NarrativeKind != NarrativeAssistant || events[0].Text != "final" || !events[0].Final {
		t.Fatalf("event = %#v, want final assistant narrative", events[0])
	}
}

func TestProjectReplayEventsSynthesizesParticipantUserPrompt(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeParticipant,
		Meta:  map[string]any{"mention": "claude"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "summarize"},
		},
	}}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want synthesized participant prompt", events)
	}
	if events[0].Kind != EventNarrative || events[0].Scope != ScopeMain || events[0].NarrativeKind != NarrativeUser {
		t.Fatalf("event = %#v, want main user narrative", events[0])
	}
	if events[0].Text != "User to @claude: summarize" {
		t.Fatalf("event text = %q, want participant prompt label", events[0].Text)
	}
}
