package transcript

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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

func TestProjectACPEventToEventsProjectsUsageUpdate(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{
			PromptTokens:      12,
			CachedInputTokens: 3,
			CompletionTokens:  5,
			ReasoningTokens:   2,
			TotalTokens:       17,
		}, nil),
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one usage event", events)
	}
	if events[0].Kind != EventUsage || events[0].Usage == nil || events[0].Usage.PromptTokens != 12 || events[0].Usage.TotalTokens != 17 {
		t.Fatalf("event = %#v, want usage projection", events[0])
	}
}

func TestProjectACPEventToEventsProjectsAttemptResetNotice(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:   eventstream.KindLifecycle,
		TurnID: "turn-1",
		Scope:  eventstream.ScopeMain,
		Lifecycle: &eventstream.Lifecycle{
			State: "attempt_reset",
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"attempt_reset": map[string]any{
						"attempt":        1,
						"cause":          "model: http status 400 body=bad request",
						"max_retries":    5,
						"retry_delay_ms": 1000,
						"retrying":       true,
					},
				},
			},
		},
	}, nil)
	if len(events) != 2 {
		t.Fatalf("events = %#v, want lifecycle plus retry notice", events)
	}
	if events[0].Kind != EventLifecycle || events[0].State != "attempt_reset" {
		t.Fatalf("first event = %#v, want attempt_reset lifecycle", events[0])
	}
	if events[1].Kind != EventNotice || events[1].Text != "Retrying model request (1/5, retry in 1s)" {
		t.Fatalf("second event = %#v, want product retry notice", events[1])
	}
	if events[1].NoticeKind != NoticeKindModelRetry {
		t.Fatalf("second event notice kind = %q, want model retry", events[1].NoticeKind)
	}
	if strings.Contains(events[1].Text, "http status 400") || strings.Contains(events[1].Text, "bad request") {
		t.Fatalf("retry notice leaked provider error: %q", events[1].Text)
	}
	if cause := MetaString(events[0].Meta, "caelis", "runtime", "attempt_reset", "cause"); cause != "" {
		t.Fatalf("lifecycle meta leaked retry cause: %q", cause)
	}
	if cause := MetaString(events[1].Meta, "caelis", "runtime", "attempt_reset", "cause"); cause != "" {
		t.Fatalf("notice meta leaked retry cause: %q", cause)
	}
	if events[0].TurnID != "turn-1" || events[1].TurnID != "turn-1" {
		t.Fatalf("turn ids = %q, %q; want turn-1", events[0].TurnID, events[1].TurnID)
	}
}

func TestProjectACPEventToEventsProjectsCompactNoticeOnly(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateCompact,
			Content:       schema.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
		},
		Final: true,
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one compact notice", events)
	}
	if events[0].Kind != EventNotice || events[0].Text != CompactNoticeLabel {
		t.Fatalf("event = %#v, want lightweight compact notice", events[0])
	}
	if events[0].NoticeKind != NoticeKindCompact {
		t.Fatalf("event notice kind = %q, want compact", events[0].NoticeKind)
	}
	if strings.Contains(events[0].Text, "CONTEXT CHECKPOINT") {
		t.Fatalf("compact notice leaked checkpoint body: %#v", events[0])
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
