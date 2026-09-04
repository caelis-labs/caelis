package transcript

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestProjectACPEventToEventsUsesTypedRelationAndDeliveryWithoutMetadata(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:         eventstream.KindSessionUpdate,
		EventID:      "event-1",
		ProjectionID: "acp-projection:ZXZlbnQtMQ:0",
		TurnID:       "turn-1",
		Scope:        eventstream.ScopeSubagent,
		ScopeID:      "task-1",
		Actor:        "worker",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "subagent output"},
			MessageID:     "message-1",
		},
		Final: true,
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	event := events[0]
	if event.AnchorToolCallID != "spawn-1" || event.AnchorToolName != "Spawn" || !event.Observation {
		t.Fatalf("typed event delivery = %#v, want typed transient child observation", event)
	}
	if event.MessageID != "message-1" {
		t.Fatalf("typed event message id = %q, want message-1", event.MessageID)
	}
	if event.SourceEventID != "event-1" {
		t.Fatalf("typed event source event id = %q, want event-1", event.SourceEventID)
	}
	if event.SourceProjectionID != "acp-projection:ZXZlbnQtMQ:0" {
		t.Fatalf("typed event source projection id = %q, want stable projection identity", event.SourceProjectionID)
	}
}

func TestProjectACPEventToEventsUsesTypedNoticeKindIndependentOfText(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		kind eventstream.NoticeKind
		want NoticeKind
	}{
		{name: "compact", kind: eventstream.NoticeKindCompact, want: NoticeKindCompact},
		{name: "compact failed", kind: eventstream.NoticeKindCompactFailed, want: NoticeKindCompactFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := ProjectACPEventToEvents(eventstream.Envelope{
				Kind:       eventstream.KindNotice,
				Notice:     "localized display text",
				NoticeKind: test.kind,
				Scope:      eventstream.ScopeMain,
			}, nil)
			if len(events) != 1 {
				t.Fatalf("events = %#v, want one notice", events)
			}
			if events[0].Text != "localized display text" || events[0].NoticeKind != test.want {
				t.Fatalf("event = %#v, want typed notice independent of display text", events[0])
			}
		})
	}
}

func TestProjectACPEventToEventsReadsLegacyAgentCommunication(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:  eventstream.KindAgentCommunication,
		Scope: eventstream.ScopeMain,
		AgentCommunication: &eventstream.AgentCommunication{
			Source: eventstream.ActorIdentity{Kind: "participant", ID: "reviewer-1", Role: "delegated", Name: "reviewer"},
			Text:   "review complete",
		},
	}, nil)
	if len(events) != 1 || events[0].Kind != EventAgentCommunication {
		t.Fatalf("events = %#v, want one Agent communication", events)
	}
	event := events[0]
	if event.NarrativeKind == NarrativeUser || event.Text != "review complete" ||
		event.AgentSourceID != "reviewer-1" || event.AgentSourceRole != "delegated" || event.AgentSourceName != "reviewer" {
		t.Fatalf("event = %#v, want typed sender without User narrative", event)
	}
}

func TestProjectACPEventToEventsPrefersTypedAgentCommunicationSource(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeMain,
		AgentCommunicationSource: &eventstream.ActorIdentity{Kind: "participant", ID: "reviewer-1", Role: "delegated", Name: "reviewer"},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "review complete"},
			MessageID:     "message-1",
			Meta: map[string]any{"caelis": map[string]any{"agent_communication": map[string]any{
				"source": map[string]any{"kind": "controller", "id": "forged", "name": "forged"},
			}}},
		},
	}, nil)
	if len(events) != 1 || events[0].Kind != EventAgentCommunication {
		t.Fatalf("events = %#v, want one standard Agent communication", events)
	}
	event := events[0]
	if event.MessageID != "message-1" || event.AgentSourceID != "reviewer-1" || event.AgentSourceName != "reviewer" {
		t.Fatalf("event = %#v, want typed source identity", event)
	}
}

func TestProjectACPEventToEventsIgnoresUntrustedAgentCommunicationMeta(t *testing.T) {
	t.Parallel()

	for _, source := range []any{
		map[string]any{"kind": "participant", "id": "reviewer-1", "name": "reviewer"},
		map[string]any{"kind": "user", "id": "user-1", "name": "user"},
		nil,
	} {
		events := ProjectACPEventToEvents(eventstream.Envelope{
			Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeMain, Actor: "user",
			Update: eventstream.ContentChunk{
				SessionUpdate: eventstream.UpdateUserMessage,
				Content:       eventstream.TextContent{Type: "text", Text: "remote input"},
				Meta:          map[string]any{"caelis": map[string]any{"agent_communication": map[string]any{"source": source}}},
			},
		}, nil)
		if len(events) != 1 || events[0].Kind != EventNarrative || events[0].NarrativeKind != NarrativeUser ||
			events[0].Text != "remote input" || events[0].Actor != "user" || events[0].AgentSourceID != "" {
			t.Fatalf("events = %#v, want ordinary user input for untrusted source %#v", events, source)
		}
	}
}

func TestProjectACPEventToEventsProjectsParentCommunicationAsSubagentUserMessage(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:                     eventstream.KindSessionUpdate,
		Scope:                    eventstream.ScopeSubagent,
		AgentCommunicationSource: &eventstream.ActorIdentity{Kind: "controller", ID: "controller-1", Name: "parent"},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "continue"},
			Meta: map[string]any{"caelis": map[string]any{"agent_communication": map[string]any{
				"source": map[string]any{"kind": string(session.ActorKindController), "id": "controller-1", "name": "parent"},
			}}},
		},
	}, nil)
	if len(events) != 1 || events[0].Kind != EventNarrative || events[0].NarrativeKind != NarrativeUser ||
		events[0].Actor != "parent" || events[0].Text != "continue" {
		t.Fatalf("events = %#v, want parent user message", events)
	}
}

func TestProjectACPEventToEventsPrefersTypedRelationAndDeliveryOverConflictingLegacyMetadata(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:    eventstream.KindSessionUpdate,
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "typed-spawn-1",
			ToolName:   "Spawn",
		},
		Delivery: &eventstream.Delivery{},
		Meta: map[string]any{
			"caelis": map[string]any{
				"transient": true,
				"runtime": map[string]any{
					"stream": map[string]any{
						"parent_call_id": "legacy-task-1",
						"parent_tool":    "Task",
					},
				},
			},
		},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "typed wins"},
		},
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	event := events[0]
	if event.AnchorToolCallID != "typed-spawn-1" || event.AnchorToolName != "Spawn" || event.Observation {
		t.Fatalf("event = %#v, want typed relation and zero delivery without observation authority", event)
	}
}

func TestProjectACPEventToEventsRetainsEventOnlyPlanParentRelation(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:    eventstream.KindSessionUpdate,
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		ParentTool: &eventstream.ParentToolRelation{
			ToolCallID: "spawn-1",
			ToolName:   "Spawn",
		},
		Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient},
		Update: eventstream.PlanUpdate{
			SessionUpdate: eventstream.UpdatePlan,
			Entries: []eventstream.PlanEntry{{
				Content: "inspect semantic delivery",
				Status:  "in_progress",
			}},
		},
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one plan event", events)
	}
	event := events[0]
	if event.Kind != EventPlan || event.AnchorToolCallID != "spawn-1" || event.AnchorToolName != "Spawn" {
		t.Fatalf("event-only plan = %#v, want Spawn relation", event)
	}
}

func TestProjectACPEventToEventsFallsBackToLegacyRelationAndDeliveryMetadata(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:    eventstream.KindSessionUpdate,
		TurnID:  "turn-1",
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		Actor:   "worker",
		Meta: map[string]any{
			"caelis": map[string]any{
				"transient": true,
				"runtime": map[string]any{
					"stream": map[string]any{
						"parent_call_id": "spawn-1",
						"parent_tool":    "Spawn",
					},
				},
			},
		},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "subagent output"},
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
	if event.AnchorToolCallID != "spawn-1" || event.AnchorToolName != "Spawn" || event.Observation {
		t.Fatalf("event anchor = %#v, want compatibility anchor without typed observation authority", event)
	}
}

func TestProjectACPEventToEventsFallsBackToLegacyRelationMetadataInUpdate(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:    eventstream.KindSessionUpdate,
		Scope:   eventstream.ScopeSubagent,
		ScopeID: "task-1",
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "legacy update metadata"},
			Meta: map[string]any{"caelis": map[string]any{"runtime": map[string]any{"stream": map[string]any{
				"parent_call_id": "spawn-1",
				"parent_tool":    "Spawn",
			}}}},
		},
	}, nil)
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if event := events[0]; event.AnchorToolCallID != "spawn-1" || event.AnchorToolName != "Spawn" || event.Observation {
		t.Fatalf("event = %#v, want legacy update relation without typed observation authority", event)
	}
}

func TestProjectACPEventToEventsDelegatesToolUpdate(t *testing.T) {
	t.Parallel()

	status := eventstream.ToolStatusCompleted
	kind := eventstream.ToolKindExecute
	var captured ToolProjectionInput
	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Meta: map[string]any{
			"caelis": map[string]any{
				"bridge": map[string]any{"source": "gateway_projection"},
			},
		},
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Kind:          &kind,
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "done\n"},
			Content:       []eventstream.ToolCallContent{},
		},
	}, testSurfaceProjector{
		resultCapture:  &captured,
		requireDefault: "in_progress",
		t:              t,
	})
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one transcript event", events)
	}
	if events[0].ToolName != "" || events[0].ToolCallID != "call-1" {
		t.Fatalf("event = %#v, want delegated tool event", events[0])
	}
	rawOutput := RawMap(captured.RawOutput)
	if !captured.GatewayProjection || !captured.ContentPresent || rawOutput["stdout"] != "done\n" || captured.RawOutput == nil {
		t.Fatalf("captured = %#v, want gateway projection, explicit empty content, and raw output", captured)
	}
}

func TestProjectACPEventToEventsTreatsTerminalToolCallAsFinalSnapshot(t *testing.T) {
	t.Parallel()

	callCalled := false
	var captured ToolProjectionInput
	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall,
			ToolCallID:    "read-1",
			Title:         "Read AGENTS.md",
			Kind:          eventstream.ToolKindRead,
			Status:        eventstream.ToolStatusCompleted,
			RawInput:      map[string]any{"path": "AGENTS.md"},
			RawOutput:     map[string]any{"result": "done"},
		},
	}, testSurfaceProjector{
		t:              t,
		resultCapture:  &captured,
		requireDefault: eventstream.ToolStatusCompleted,
		callCalled:     &callCalled,
	})
	if callCalled || len(events) != 1 {
		t.Fatalf("call projection = %v, events = %#v, want one final result projection", callCalled, events)
	}
	if captured.ToolName != "" || captured.ToolKind != eventstream.ToolKindRead || captured.ToolTitle != "Read AGENTS.md" {
		t.Fatalf("captured identity = %#v, want exact name empty with standard kind/title preserved", captured)
	}
	if RawMap(captured.RawOutput)["result"] != "done" {
		t.Fatalf("captured raw output = %#v, want one-shot output preserved", captured.RawOutput)
	}
}

func TestProjectACPEventToEventsSkipsPlanTools(t *testing.T) {
	t.Parallel()

	called := false
	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.ToolCall{
			ToolCallID: "plan-1",
			Title:      "Plan",
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
			ToolName:   "RunCommand",
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
		Kind:           eventstream.KindSessionUpdate,
		UsageSemantics: eventstream.UsageSemanticsProviderUsage,
		Update: eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{
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
	if events[0].UsageReplace {
		t.Fatal("Caelis provider usage should merge independently observed fields")
	}
}

func TestProjectACPEventToEventsMarksStandardUsageAsReplaceableGauge(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind:           eventstream.KindSessionUpdate,
		UsageSemantics: eventstream.UsageSemanticsContextGauge,
		Update: eventstream.UsageUpdate{
			SessionUpdate: eventstream.UpdateUsage, Size: 200000, Used: 0,
			Meta: map[string]any{"caelis": map[string]any{"usage": map[string]any{"total_tokens": 99999}}},
		},
	}, nil)
	if len(events) != 1 || events[0].Usage == nil || !events[0].UsageReplace || events[0].Usage.TotalTokens != 0 || events[0].Usage.ContextWindowTokens != 200000 {
		t.Fatalf("standard usage event = %#v, want replaceable zero gauge", events)
	}
}

func TestProjectACPEventToEventsKeepsLegacyUnannotatedProviderUsageMerge(t *testing.T) {
	t.Parallel()

	events := ProjectACPEventToEvents(eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: eventstream.UsageUpdateFromSnapshot(session.UsageSnapshot{
			PromptTokens: 1200,
			TotalTokens:  1600,
		}, nil),
	}, nil)
	if len(events) != 1 || events[0].Usage == nil || events[0].UsageReplace || events[0].Usage.PromptTokens != 1200 || events[0].Usage.ContextWindowTokens != 0 {
		t.Fatalf("legacy provider usage event = %#v, want merge-compatible breakdown", events)
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
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateCompact,
			Content:       eventstream.TextContent{Type: "text", Text: "CONTEXT CHECKPOINT\nObjective: continue"},
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
	resultCapture   *ToolProjectionInput
	requireDefault  string
	callCalled      *bool
	approvalPreview string
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
