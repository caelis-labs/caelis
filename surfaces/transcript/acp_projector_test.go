package transcript

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestProjectACPEventToEventsProjectsNarrativeScopeAndAnchors(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:    eventstream.KindSessionUpdate,
		TurnID:  "turn-1",
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		Actor:   "worker",
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"stream": map[string]any{
						"parent_call_id":          "spawn-1",
						"parent_tool":             "SPAWN",
						"mirrored_to_parent_tool": true,
					},
				},
			},
		},
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "subagent output"},
		},
		Final: true,
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	event := events[0]
	if event.Kind != EventNarrative || event.NarrativeKind != NarrativeAssistant || event.Text != "subagent output" {
		t.Fatalf("event narrative = %#v, want assistant output", event)
	}
	if event.Scope != ScopeSubagent || event.ScopeID != "task-1" || event.Actor != "worker" || event.TurnID != "turn-1" {
		t.Fatalf("event scope = %#v, want subagent/task-1/worker/turn-1", event)
	}
	if event.AnchorToolCallID != "spawn-1" || event.AnchorToolName != "SPAWN" || !event.MirroredToParentTool {
		t.Fatalf("event anchor = %#v, want parent SPAWN anchor", event)
	}
}

func TestProjectACPEventToEventsDelegatesToolUpdate(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	kind := schema.ToolKindExecute
	var captured ToolProjectionInput
	events := ProjectACPEventToEvents(eventstream.Envelope{
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
			RawOutput:     map[string]any{"stdout": "done\n"},
		},
	}, testSurfaceProjector{
		toolName:       "RUN_COMMAND",
		resultCapture:  &captured,
		requireDefault: "in_progress",
		t:              t,
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolName != "RUN_COMMAND" || events[0].ToolCallID != "call-1" {
		t.Fatalf("event = %#v, want delegated tool event", events[0])
	}
	rawOutput := RawMap(captured.RawOutput)
	if !captured.GatewayProjection || rawOutput["stdout"] != "done\n" || captured.RawOutput == nil {
		t.Fatalf("captured = %#v, want gateway projection and raw output", captured)
	}
}

func TestProjectACPEventToEventsSkipsPlanTools(t *testing.T) {
	t.Parallel()

	called := false
	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ToolCall{
			ToolCallID: "plan-1",
			Title:      "PLAN",
		},
	}, testSurfaceProjector{callCalled: &called})
	if len(events) != 0 || called {
		t.Fatalf("events = %#v, called = %v, want plan tool skipped", events, called)
	}
}

func TestProjectACPEventToEventsProjectsApprovalReview(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindApprovalReview,
		ApprovalReview: &eventstream.ApprovalReview{
			ToolCallID: "call-1",
			ToolName:   "RUN_COMMAND",
			RawInput:   map[string]any{"command": "git status"},
			Status:     "approved",
			Text:       "Automatic approval review approved (risk: low, authorization: allow)",
		},
	}, testSurfaceProjector{approvalPreview: "git status"})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one approval event", events)
	}
	event := events[0]
	if event.Kind != EventApproval || event.ApprovalCommand != "git status" || event.ApprovalRisk != "low" || event.ApprovalAuth != "allow" {
		t.Fatalf("event = %#v, want approval review projection", event)
	}
}

type testSurfaceProjector struct {
	t               *testing.T
	toolName        string
	resultCapture   *ToolProjectionInput
	requireDefault  string
	callCalled      *bool
	approvalPreview string
}

func (p testSurfaceProjector) ResolveToolName(map[string]any, string, string) string {
	return p.toolName
}

func (p testSurfaceProjector) ProjectToolCall(ToolProjectionInput) Event {
	if p.callCalled != nil {
		*p.callCalled = true
	}
	return Event{Kind: EventTool}
}

func (p testSurfaceProjector) ProjectToolResult(input ToolProjectionInput, defaultSuccessStatus string) (Event, bool) {
	if p.t != nil && p.requireDefault != "" && defaultSuccessStatus != p.requireDefault {
		p.t.Fatalf("defaultSuccessStatus = %q, want %s", defaultSuccessStatus, p.requireDefault)
	}
	if p.resultCapture != nil {
		*p.resultCapture = input
	}
	return Event{Kind: EventTool, ToolName: input.ToolName, ToolCallID: input.CallID}, true
}

func (p testSurfaceProjector) ApprovalCommandPreview(map[string]any) string {
	return p.approvalPreview
}
