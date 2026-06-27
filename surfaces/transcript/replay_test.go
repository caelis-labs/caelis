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

func TestProjectReplayEventsKeepsFinalThoughtChunksOnly(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentThought,
				Content:       schema.TextContent{Type: "text", Text: "partial thought"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ContentChunk{
				SessionUpdate: schema.UpdateAgentThought,
				Content:       schema.TextContent{Type: "text", Text: "final thought"},
			},
			Final: true,
		},
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one final replay event", events)
	}
	if events[0].Kind != EventNarrative || events[0].NarrativeKind != NarrativeReasoning || events[0].Text != "final thought" || !events[0].Final {
		t.Fatalf("event = %#v, want final reasoning narrative", events[0])
	}
}

func TestProjectReplayEventsProjectsMainDurableTrace(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	kind := schema.ToolKindExecute
	events := ProjectReplayEvents([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ToolCall{
				SessionUpdate: schema.UpdateToolCall,
				ToolCallID:    "call-1",
				Kind:          "RUN_COMMAND",
				Status:        schema.ToolStatusInProgress,
				RawInput:      map[string]any{"command": "go test ./..."},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          &kind,
				Status:        &status,
				RawOutput:     map[string]any{"stdout": "ok"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: schema.PlanUpdate{
				SessionUpdate: schema.UpdatePlan,
				Entries:       []schema.PlanEntry{{Content: "run tests", Status: "in_progress"}},
			},
		},
		{
			Kind:      eventstream.KindLifecycle,
			Lifecycle: &eventstream.Lifecycle{State: "interrupted", Reason: "user interrupt"},
		},
	}, testSurfaceProjector{toolName: "RUN_COMMAND"})
	if len(events) != 4 {
		t.Fatalf("events = %#v, want tool call, tool result, plan, lifecycle", events)
	}
	if events[0].Kind != EventTool || events[1].Kind != EventTool || events[1].ToolCallID != "call-1" {
		t.Fatalf("tool events = %#v", events[:2])
	}
	if events[2].Kind != EventPlan || len(events[2].PlanEntries) != 1 || events[2].PlanEntries[0].Content != "run tests" {
		t.Fatalf("plan event = %#v", events[2])
	}
	if events[3].Kind != EventLifecycle || events[3].State != "interrupted" {
		t.Fatalf("lifecycle event = %#v", events[3])
	}
}

func TestProjectReplayEventsProjectsUsageUpdate(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{
			PromptTokens: 12,
			TotalTokens:  17,
		}, nil),
	}}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one usage replay event", events)
	}
	if events[0].Kind != EventUsage || events[0].Usage == nil || events[0].Usage.PromptTokens != 12 || events[0].Usage.TotalTokens != 17 {
		t.Fatalf("event = %#v, want usage replay event", events[0])
	}
}

func TestProjectReplayEventsSkipsSideACPProcessTrace(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	events := ProjectReplayEvents([]eventstream.Envelope{{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeParticipant,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "side-call",
			Status:        &status,
		},
	}}, testSurfaceProjector{toolName: "RUN_COMMAND"})
	if len(events) != 0 {
		t.Fatalf("events = %#v, want side ACP process trace skipped", events)
	}
}

func TestProjectReplayEventsProjectsParticipantUserPrompt(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeParticipant,
		Meta:  map[string]any{"mention": "@claude"},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateUserMessage,
			Content:       schema.TextContent{Type: "text", Text: "summarize"},
		},
	}}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want participant prompt", events)
	}
	if events[0].Kind != EventNarrative || events[0].Scope != ScopeParticipant || events[0].NarrativeKind != NarrativeUser {
		t.Fatalf("event = %#v, want participant user narrative", events[0])
	}
	if events[0].Text != "summarize" || MetaString(events[0].Meta, "mention") != "@claude" {
		t.Fatalf("event = %#v, want participant prompt text and label metadata", events[0])
	}
}
