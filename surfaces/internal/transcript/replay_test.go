package transcript

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestProjectReplayEventsKeepsFinalAssistantChunksOnly(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "partial"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "final"},
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
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentThought,
				Content:       eventstream.TextContent{Type: "text", Text: "partial thought"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentThought,
				Content:       eventstream.TextContent{Type: "text", Text: "final thought"},
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

func TestProjectReplayEventsSkipsCompactNotice(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateCompact,
			Content:       eventstream.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
		},
		Final: true,
	}}, nil)
	if len(events) != 0 {
		t.Fatalf("events = %#v, want compact notice skipped during replay", events)
	}
}

func TestProjectReplayEventsSkipsTransientEnvelope(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{{
		Kind:      eventstream.KindLifecycle,
		Scope:     eventstream.ScopeMain,
		Delivery:  &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Lifecycle: &eventstream.Lifecycle{State: "context_compacting"},
	}}, nil)
	if len(events) != 0 {
		t.Fatalf("events = %#v, want transient envelope skipped during replay", events)
	}
}

func TestProjectReplayEventsProjectsMainDurableTrace(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusCompleted
	kind := eventstream.ToolKindExecute
	events := ProjectReplayEvents([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall,
				ToolCallID:    "call-1",
				Kind:          "RunCommand",
				Status:        eventstream.ToolStatusInProgress,
				RawInput:      map[string]any{"command": "go test ./..."},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          &kind,
				Status:        &status,
				RawOutput:     map[string]any{"stdout": "ok"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate,
			Update: eventstream.PlanUpdate{
				SessionUpdate: eventstream.UpdatePlan,
				Entries:       []eventstream.PlanEntry{{Content: "run tests", Status: "in_progress"}},
			},
		},
		{
			Kind:      eventstream.KindLifecycle,
			Lifecycle: &eventstream.Lifecycle{State: "interrupted", Reason: "user interrupt"},
		},
	}, testSurfaceProjector{})
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
		Kind:           eventstream.KindSessionUpdate,
		UsageSemantics: eventstream.UsageSemanticsProviderUsage,
		Update: eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{
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

func TestProjectReplayEventsKeepsStandardSideACPToolTrace(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusCompleted
	var projected ToolProjectionInput
	events := ProjectReplayEvents([]eventstream.Envelope{{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeParticipant,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "side-call",
			Status:        &status,
			Content: []eventstream.ToolCallContent{{
				Type: "content", Content: eventstream.TextContent{Type: "text", Text: "side result"},
			}},
		},
	}}, testSurfaceProjector{resultCapture: &projected})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one standard side ACP tool result", events)
	}
	if events[0].Kind != EventTool || events[0].ToolCallID != "side-call" {
		t.Fatalf("event = %#v, want standard tool result", events[0])
	}
	if projected.Scope != ScopeParticipant || projected.CallID != "side-call" || len(projected.Content) != 1 {
		t.Fatalf("projection input = %#v, want participant-scoped standard content", projected)
	}
}

func TestProjectReplayEventsKeepsDurableChildMirrorHistory(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusCompleted
	delivery := &eventstream.Delivery{Mode: eventstream.DeliveryMirror}
	events := ProjectReplayEvents([]eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeSubagent, ScopeID: "task-1",
			Delivery: delivery,
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateAgentMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "streamed child answer"},
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeSubagent, ScopeID: "task-1",
			Delivery: delivery,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo,
				ToolCallID:    "child-call",
				Status:        &status,
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeSubagent, ScopeID: "task-1",
			Delivery: delivery,
			Update: eventstream.PlanUpdate{
				SessionUpdate: eventstream.UpdatePlan,
				Entries:       []eventstream.PlanEntry{{Content: "inspect package", Status: "completed"}},
			},
		},
		{
			Kind: eventstream.KindLifecycle, Scope: eventstream.ScopeSubagent, ScopeID: "task-1",
			Delivery: delivery,
			Lifecycle: &eventstream.Lifecycle{
				State: eventstream.LifecycleStateCompleted,
			},
		},
	}, testSurfaceProjector{})

	if len(events) != 4 {
		t.Fatalf("events = %#v, want child narrative, tool, plan, and lifecycle", events)
	}
	if events[0].Kind != EventNarrative || events[0].Scope != ScopeSubagent || events[0].Text != "streamed child answer" || events[0].Final {
		t.Fatalf("narrative = %#v, want non-final durable child mirror", events[0])
	}
	if events[1].Kind != EventTool || events[1].ToolCallID != "child-call" {
		t.Fatalf("tool = %#v, want durable child tool", events[1])
	}
	if events[2].Kind != EventPlan || events[2].Scope != ScopeSubagent || len(events[2].PlanEntries) != 1 {
		t.Fatalf("plan = %#v, want durable child plan", events[2])
	}
	if events[3].Kind != EventLifecycle || events[3].Scope != ScopeSubagent || events[3].State != eventstream.LifecycleStateCompleted {
		t.Fatalf("lifecycle = %#v, want durable child lifecycle", events[3])
	}
}

func TestProjectReplayEventsProjectsParticipantUserPrompt(t *testing.T) {
	t.Parallel()

	events := ProjectReplayEvents([]eventstream.Envelope{{
		Kind:  eventstream.KindSessionUpdate,
		Scope: eventstream.ScopeParticipant,
		Meta:  map[string]any{"mention": "@claude"},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "summarize"},
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
