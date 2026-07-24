package tuiapp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	sdkmodel "github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/surfaces/transcript"
)

func TestNarrativeStreamUsesStableMessageIdentityWithinSegment(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.AppendStreamEvent(SEAssistant, "partial", newNarrativeSourceIdentity("message-1", "event-1", "projection-1"))
	block.AppendStreamEvent(SEAssistant, " answer", newNarrativeSourceIdentity("message-1", "event-2", "projection-2"))
	block.ReplaceFinalStreamEvent(SEAssistant, "final answer", newNarrativeSourceIdentity("message-1", "event-final", "projection-final"))

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one identity-scoped narrative", block.Events)
	}
	if got := block.Events[0].Text; got != "final answer" {
		t.Fatalf("final narrative = %q, want final answer", got)
	}
	if block.Events[0].ActiveBuffer != nil {
		t.Fatal("final narrative retained an active stream buffer")
	}
}

func TestNarrativeStreamScopesSharedMessageIdentityByKind(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.AppendStreamEvent(SEReasoning, "partial thought", newNarrativeSourceIdentity("message-1", "thought-1", "projection-1"))
	block.AppendStreamEvent(SEAssistant, "partial answer", newNarrativeSourceIdentity("message-1", "answer-1", "projection-2"))
	block.ReplaceFinalStreamEvent(SEReasoning, "final thought", newNarrativeSourceIdentity("message-1", "final-1", "projection-3"))
	block.ReplaceFinalStreamEvent(SEAssistant, "final answer", newNarrativeSourceIdentity("message-1", "final-1", "projection-4"))

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want one reasoning and one assistant narrative", block.Events)
	}
	if block.Events[0].Kind != SEReasoning || block.Events[0].Text != "final thought" {
		t.Fatalf("reasoning event = %#v, want final thought", block.Events[0])
	}
	if block.Events[1].Kind != SEAssistant || block.Events[1].Text != "final answer" {
		t.Fatalf("assistant event = %#v, want final answer", block.Events[1])
	}
}

func TestTypedMessageIdentityConvergesACPChunksAndCanonicalFinal(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	apply := func(eventID string, event *session.Event) {
		t.Helper()
		event.ID = eventID
		event.SessionID = "session-1"
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{TurnID: "turn-1"},
		)
		for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
	}

	thought := sdkmodel.NewReasoningMessage(sdkmodel.RoleAssistant, "partial thought", sdkmodel.ReasoningVisibilityVisible)
	apply("thought-chunk", session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant, MessageID: "message-1", Message: &thought,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
			MessageID:     "message-1",
			Content:       session.ProtocolTextContent("partial thought"),
		}},
	}))
	answer := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, "partial answer")
	apply("answer-chunk", session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant, MessageID: "message-1", Message: &answer,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
			MessageID:     "message-1",
			Content:       session.ProtocolTextContent("partial answer"),
		}},
	}))

	finalMessage := sdkmodel.MessageFromAssistantParts("final answer", "final thought", nil)
	apply("canonical-final", session.CanonicalizeEvent(&session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		MessageID:  "message-1",
		Message:    &finalMessage,
	}))

	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want canonical reasoning and assistant only", block.Events)
	}
	if block.Events[0].Kind != SEReasoning || block.Events[0].Text != "final thought" {
		t.Fatalf("reasoning = %#v, want converged canonical thought", block.Events[0])
	}
	if block.Events[1].Kind != SEAssistant || block.Events[1].Text != "final answer" {
		t.Fatalf("assistant = %#v, want converged canonical answer", block.Events[1])
	}
}

func TestCanonicalToolCallKeepsOneAssistantMessageOnOneRenderedRow(t *testing.T) {
	t.Parallel()

	const (
		messageID = "6e4b431c-bf97-45dd-9d74-2adc43f23704"
		answer    = "原来如此！**Task read** 只适用于 RunCommand，Spawn 需要用 **Task wait**。来等待三个子代理完成："
		reasoning = "Ah, Task read only works for RunCommand handles, not Spawn handles. For Spawn, we use Task wait."
	)
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	apply := func(eventID string, event *session.Event) {
		t.Helper()
		event.ID = eventID
		event.SessionID = "session-1"
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{TurnID: "turn-1"},
		)
		for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
	}
	eventIDs := []string{"answer-chunk-1", "answer-chunk-2", "answer-chunk-3"}
	for index, chunk := range []string{"原来", "如此！", "**Task read** 只适用于 RunCommand，Spawn 需要用 **Task wait**。来等待三个子代理完成："} {
		message := sdkmodel.NewTextMessage(sdkmodel.RoleAssistant, chunk)
		apply(eventIDs[index], session.MarkUIOnly(&session.Event{
			Type: session.EventTypeAssistant, MessageID: messageID, Message: &message,
			Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     messageID,
				Content:       session.ProtocolTextContent(chunk),
			}},
		}))
	}
	final := narrativeTestToolCallEvent("task-wait", "Task", `{"action":"wait","handle":"rafe,remy,arlo"}`, reasoning, answer)
	final.MessageID = messageID
	apply("canonical-tool-call", session.CanonicalizeEvent(final))

	block := requireMainACPTurnBlockForTest(t, model)
	assistantEvents := 0
	for _, event := range block.Events {
		if event.Kind != SEAssistant {
			continue
		}
		assistantEvents++
		if event.Text != answer {
			t.Fatalf("assistant text = %q, want canonical single message", event.Text)
		}
	}
	if assistantEvents != 1 {
		t.Fatalf("events = %#v, want one assistant event", block.Events)
	}
	rows := block.Render(BlockRenderContext{
		Width: 180, TermWidth: 180,
		Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
	})
	assistantRows := make([]string, 0, 1)
	for _, row := range renderedPlainRows(rows) {
		if strings.HasPrefix(strings.TrimSpace(row), "·") {
			assistantRows = append(assistantRows, strings.TrimSpace(row))
		}
	}
	const rendered = "· 原来如此！Task read 只适用于 RunCommand，Spawn 需要用 Task wait。来等待三个子代理完成："
	if len(assistantRows) != 1 || assistantRows[0] != rendered {
		t.Fatalf("assistant rows = %#v, want one canonical rendered row", assistantRows)
	}
}

func TestNarrativeStreamFallsBackToSourceEventIdentity(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.AppendStreamEvent(SEReasoning, "first", newNarrativeSourceIdentity("", "event-1", "projection-1"))
	block.AppendStreamEvent(SEReasoning, "second", newNarrativeSourceIdentity("", "event-2", "projection-2"))

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want distinct source events to remain distinct", block.Events)
	}
	if block.Events[0].Text != "first" || block.Events[1].Text != "second" {
		t.Fatalf("events = %#v, want source order preserved", block.Events)
	}
}

func TestNarrativeStreamFinalCannotCrossSemanticBarrier(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.AppendStreamEvent(SEReasoning, "before wait", narrativeTestSource())
	block.sealNarrativeSegment()
	block.AppendStreamEvent(SEReasoning, "after wait", narrativeTestSource())
	block.ReplaceFinalStreamEvent(SEReasoning, "after wait final", narrativeTestSource())

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want one reasoning event per semantic segment", block.Events)
	}
	if block.Events[0].Text != "before wait" || block.Events[1].Text != "after wait final" {
		t.Fatalf("events = %#v, want final snapshot to replace only the open segment", block.Events)
	}
}

func TestNarrativeStreamNonNarrativeEventsSealSemanticSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		barrier func(*MainACPTurnBlock)
	}{
		{
			name: "tool",
			barrier: func(block *MainACPTurnBlock) {
				block.UpdateToolWithMeta("read-1", "READ", "file.go", "ok", true, false, ToolUpdateMeta{})
			},
		},
		{
			name: "plan",
			barrier: func(block *MainACPTurnBlock) {
				block.UpdatePlan([]planEntryState{{Content: "inspect", Status: "in_progress"}})
			},
		},
		{
			name: "notice",
			barrier: func(block *MainACPTurnBlock) {
				block.AddNotice("retrying", time.Time{}, transcript.NoticeKindModelRetry)
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			block := NewMainACPTurnBlock("turn-1")
			block.AppendStreamEvent(SEReasoning, "before", narrativeTestSource())
			test.barrier(block)
			block.AppendStreamEvent(SEReasoning, "after", narrativeTestSource())
			block.ReplaceFinalStreamEvent(SEReasoning, "after final", narrativeTestSource())

			reasoning := make([]string, 0, 2)
			for _, event := range block.Events {
				if event.Kind == SEReasoning {
					reasoning = append(reasoning, event.Text)
				}
			}
			if len(reasoning) != 2 || reasoning[0] != "before" || reasoning[1] != "after final" {
				t.Fatalf("reasoning = %#v (events %#v), want barrier-preserved segments", reasoning, block.Events)
			}
		})
	}
}

func TestTranscriptUsageTelemetryDoesNotSealNarrativeSegment(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	events := []TranscriptEvent{
		{
			Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, TurnID: "turn-1",
			NarrativeKind: TranscriptNarrativeAssistant, MessageID: "message-1",
			SourceEventID: "event-1", Text: "first ",
		},
		{
			Kind: TranscriptEventUsage, Scope: ACPProjectionMain, TurnID: "turn-1",
			Usage: &eventstream.UsageSnapshot{TotalTokens: 10, ContextWindowTokens: 100},
		},
		{
			Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, TurnID: "turn-1",
			NarrativeKind: TranscriptNarrativeAssistant, MessageID: "message-1",
			SourceEventID: "event-2", Text: "second",
		},
	}

	next, _ := model.applyTranscriptEvents(events)
	model = next.(*Model)
	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 1 || block.Events[0].Kind != SEAssistant || block.Events[0].Text != "first second" {
		t.Fatalf("events = %#v, want telemetry inside one narrative segment", block.Events)
	}
}

func TestNarrativeStreamStableFinalAdoptsAnonymousProvisionalOnlyInCurrentSegment(t *testing.T) {
	t.Parallel()

	block := NewParticipantTurnBlock("participant-1", "@reviewer")
	block.AppendStreamEvent(SEAssistant, "provisional", narrativeSourceIdentity{})
	block.ReplaceFinalStreamEvent(SEAssistant, "canonical", newNarrativeSourceIdentity("message-1", "event-1", "projection-1"))

	if len(block.Events) != 1 || block.Events[0].Text != "canonical" {
		t.Fatalf("same-segment final events = %#v, want canonical adoption", block.Events)
	}

	block.sealNarrativeSegment()
	block.ReplaceFinalStreamEvent(SEAssistant, "next", newNarrativeSourceIdentity("message-1", "event-2", "projection-2"))
	if len(block.Events) != 2 || block.Events[0].Text != "canonical" || block.Events[1].Text != "next" {
		t.Fatalf("cross-segment final events = %#v, want prior narrative preserved", block.Events)
	}
}

func TestNarrativeStreamIdentityFreeFinalFailsClosedAcrossBarrier(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.AppendStreamEvent(SEAssistant, "before", narrativeSourceIdentity{})
	block.sealNarrativeSegment()
	block.AppendStreamEvent(SEAssistant, "after", narrativeSourceIdentity{})
	block.ReplaceFinalStreamEvent(SEAssistant, "before after final", narrativeSourceIdentity{})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want one assistant event per segment", block.Events)
	}
	if got := block.Events[1].Text; got != "before after final" {
		t.Fatalf("current segment final = %q, want full fail-closed snapshot without cross-segment identity", got)
	}
}

func TestNarrativeStreamCumulativeFinalRequiresExactIdentityPrefix(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	source := newNarrativeSourceIdentity("message-1", "event-1", "projection-1")
	block.AppendStreamEvent(SEAssistant, "before", source)
	block.sealNarrativeSegment()
	block.AppendStreamEvent(SEAssistant, "after", source)
	block.ReplaceFinalStreamEvent(SEAssistant, "  before\nafter final", source)

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want one assistant event per segment", block.Events)
	}
	if got := block.Events[1].Text; got != "  before\nafter final" {
		t.Fatalf("current segment final = %q, want whitespace-divergent snapshot preserved intact", got)
	}
}

func TestNarrativeStreamFinalOnlySegmentStripsExactSealedPrefix(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	source := newNarrativeSourceIdentity("message-1", "event-1", "projection-1")
	block.AppendStreamEvent(SEAssistant, "before", source)
	block.sealNarrativeSegment()
	block.ReplaceFinalStreamEvent(SEAssistant, "before\nafter final", source)

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want one assistant event per semantic segment", block.Events)
	}
	if block.Events[0].Text != "before" || block.Events[1].Text != "after final" {
		t.Fatalf("events = %#v, want only the exact current-segment suffix appended", block.Events)
	}
}

func TestNarrativeStreamFinalEqualToSealedPrefixAddsNoDuplicateSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		provisional string
	}{
		{name: "final only"},
		{name: "discard provisional", provisional: "speculative"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			block := NewParticipantTurnBlock("participant-1", "@reviewer")
			source := newNarrativeSourceIdentity("message-1", "event-1", "projection-1")
			block.AppendStreamEvent(SEReasoning, "before", source)
			block.sealNarrativeSegment()
			if test.provisional != "" {
				block.AppendStreamEvent(SEReasoning, test.provisional, source)
			}
			block.ReplaceFinalStreamEvent(SEReasoning, "before", source)

			if len(block.Events) != 1 || block.Events[0].Text != "before" {
				t.Fatalf("events = %#v, want only the sealed reasoning segment", block.Events)
			}
		})
	}
}

func TestHiddenTaskWaitStillSealsMainNarrativeSegment(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	events := []TranscriptEvent{
		{
			Kind:          TranscriptEventNarrative,
			Scope:         ACPProjectionMain,
			TurnID:        "turn-1",
			NarrativeKind: TranscriptNarrativeReasoning,
			Text:          "waiting for child",
			Final:         true,
		},
		{
			Kind:           TranscriptEventTool,
			Scope:          ACPProjectionMain,
			TurnID:         "turn-1",
			ToolCallID:     "task-wait-1",
			ToolName:       "TASK",
			ToolTaskAction: "wait",
			ToolStatus:     "completed",
			Final:          true,
		},
		{
			Kind:          TranscriptEventNarrative,
			Scope:         ACPProjectionMain,
			TurnID:        "turn-1",
			NarrativeKind: TranscriptNarrativeReasoning,
			Text:          "waiting again",
			Final:         true,
		},
	}

	next, _ := model.applyTranscriptEvents(events)
	model = next.(*Model)
	block := requireMainACPTurnBlockForTest(t, model)
	if len(block.Events) != 3 {
		t.Fatalf("events = %#v, want two narratives plus one structural boundary", block.Events)
	}
	if block.Events[0].Kind != SEReasoning || block.Events[0].Text != "waiting for child" {
		t.Fatalf("first reasoning = %#v, want preserved pre-wait step", block.Events[0])
	}
	if block.Events[1].Kind != SESemanticBoundary {
		t.Fatalf("middle event = %#v, want non-rendering Task boundary", block.Events[1])
	}
	if block.Events[2].Kind != SEReasoning || block.Events[2].Text != "waiting again" {
		t.Fatalf("second reasoning = %#v, want preserved post-wait step", block.Events[2])
	}
	for _, event := range block.Events {
		if event.Kind == SEToolCall {
			t.Fatalf("hidden Task wait rendered a physical panel: %#v", event)
		}
	}
	rows := block.Render(BlockRenderContext{
		Width: 120, TermWidth: 120, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
	})
	plain := renderedPlainRows(rows)
	if len(plain) != 3 || plain[0] != "› waiting for child" || plain[1] != "" || plain[2] != "› waiting again" {
		t.Fatalf("rendered rows = %#v, want one semantic gap around the hidden Task", plain)
	}
}

func TestSemanticBoundaryGapMaterializesOnlyBetweenVisibleRows(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	render := func(events []SubagentEvent) []string {
		t.Helper()
		block := NewMainACPTurnBlock("turn-1")
		block.Status = "completed"
		block.Events = events
		rows := renderedPlainRows(block.Render(BlockRenderContext{
			Width: 120, TermWidth: 120, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
		}))
		for i := range rows {
			rows[i] = strings.TrimRight(rows[i], " ")
		}
		return rows
	}

	tests := []struct {
		name   string
		events []SubagentEvent
		want   []string
	}{
		{
			name: "coalesces consecutive boundaries",
			events: []SubagentEvent{
				{Kind: SEAssistant, Text: "before"},
				{Kind: SESemanticBoundary},
				{Kind: SESemanticBoundary},
				{Kind: SEAssistant, Text: "after"},
			},
			want: []string{"· before", "", "· after"},
		},
		{
			name: "no leading gap",
			events: []SubagentEvent{
				{Kind: SESemanticBoundary},
				{Kind: SEAssistant, Text: "after"},
			},
			want: []string{"· after"},
		},
		{
			name: "no trailing gap",
			events: []SubagentEvent{
				{Kind: SEAssistant, Text: "before"},
				{Kind: SESemanticBoundary},
			},
			want: []string{"· before"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := render(test.events); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("rendered rows = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestFailedTaskReadRemainsVisibleAfterHiddenStart(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	next, _ := model.applyTranscriptEvents([]TranscriptEvent{
		{
			Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, TurnID: "turn-1",
			NarrativeKind: TranscriptNarrativeReasoning, Text: "before read", Final: true,
		},
		{
			Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "turn-1",
			ToolCallID: "read-1", ToolName: "Task", ToolTaskAction: "read", ToolStatus: "in_progress",
		},
		{
			Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "turn-1",
			ToolCallID: "read-1", ToolName: "Task", ToolTaskAction: "read",
			ToolStatus: "failed", ToolError: true, ToolOutput: "read failed", Final: true,
		},
	})
	model = next.(*Model)
	block := requireMainACPTurnBlockForTest(t, model)
	var failed *SubagentEvent
	for i := range block.Events {
		event := &block.Events[i]
		if event.Kind == SEToolCall && strings.EqualFold(event.Name, "Task") {
			failed = event
			break
		}
	}
	if failed == nil || !failed.Done || !failed.Err || !strings.Contains(failed.Output, "read failed") {
		t.Fatalf("failed Task event = %#v (events %#v), want visible terminal error", failed, block.Events)
	}
	rows := block.Render(BlockRenderContext{
		Width: 120, TermWidth: 120, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
	})
	if plain := joinRenderedPlain(rows); !strings.Contains(plain, "read failed") {
		t.Fatalf("failed Task output is not visible\nplain:\n%s", plain)
	}
}

func TestTaskObservationAndCancelVisibilityContract(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"wait", "read", "cancel"} {
		action := action
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			success := TranscriptEvent{
				Kind: TranscriptEventTool, ToolName: "Task", ToolTaskAction: action,
				ToolStatus: "completed", Final: true,
			}
			if gotAction, hidden := hiddenTaskControlAction(success); !hidden || gotAction != action {
				t.Fatalf("successful %s = %q/%v, want hidden control", action, gotAction, hidden)
			}
			failure := success
			failure.ToolStatus = "failed"
			failure.ToolError = true
			if _, hidden := hiddenTaskControlAction(failure); hidden {
				t.Fatalf("failed %s was hidden", action)
			}
		})
	}
}

func TestDurableTaskWaitNarrativeSiblingsRemainVisibleAcrossHiddenControls(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(300, 0))
	var seq uint64
	apply := func(eventID string, event *session.Event) {
		t.Helper()
		seq++
		event.ID = eventID
		event.Seq = seq
		event.SessionID = "session-1"
		event.Visibility = session.VisibilityCanonical
		event = session.CanonicalizeEvent(event)
		base := acpprojector.EnvelopeBaseFromSessionEvent(
			session.SessionRef{SessionID: "session-1"},
			event,
			acpprojector.SessionEventTransport{TurnID: "turn-1"},
		)
		for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
			model = applyACPEnvelopeForTest(t, model, envelope)
		}
	}
	apply("spawn-call", narrativeTestToolCallEvent("spawn-1", "Spawn", `{"agent":"breeze","prompt":"inspect"}`, "", ""))
	apply("spawn-running", narrativeTestToolResultEvent(
		"spawn-1", "Spawn", "running",
		map[string]any{"agent": "breeze", "prompt": "inspect"},
		map[string]any{"handle": "child-1", "state": "running", "target_kind": "subagent"},
	))
	apply("wait-one-call", narrativeTestToolCallEvent(
		"wait-1", "Task", `{"action":"wait","handle":"child-1"}`,
		"The sub-agent has been spawned. I will wait for it to complete.",
		"Waiting for the sub-agent.",
	))
	apply("wait-one-result", narrativeTestToolResultEvent(
		"wait-1", "Task", "completed",
		map[string]any{"action": "wait", "handle": "child-1"},
		map[string]any{"action": "wait", "handle": "child-1", "state": "running", "target_kind": "subagent"},
	))
	apply("wait-two-call", narrativeTestToolCallEvent(
		"wait-2", "Task", `{"action":"wait","handle":"child-1"}`,
		"The task is still running after the first wait. I will wait again.",
		"The sub-agent is still working.",
	))
	apply("wait-two-result", narrativeTestToolResultEvent(
		"wait-2", "Task", "completed",
		map[string]any{"action": "wait", "handle": "child-1"},
		map[string]any{
			"action": "wait", "handle": "child-1", "state": "completed", "target_kind": "subagent",
			"parent_call": "spawn-1", "parent_tool": "Spawn", "final_message": "done",
		},
	))
	apply("read-call", narrativeTestToolCallEvent(
		"read-1", "Read", `{"path":"report.md"}`,
		"The sub-agent completed. I will read the report to verify it.",
		"Reading the generated report.",
	))

	block := requireMainACPTurnBlockForTest(t, model)
	boundaries := 0
	for _, event := range block.Events {
		if event.Kind == SESemanticBoundary {
			boundaries++
		}
		if event.Kind == SEToolCall && strings.EqualFold(event.Name, "Task") {
			t.Fatalf("hidden Task control rendered a physical event: %#v", event)
		}
	}
	if boundaries != 2 {
		t.Fatalf("semantic boundaries = %d (events %#v), want one per completed wait cycle", boundaries, block.Events)
	}

	rows := block.Render(BlockRenderContext{
		Width: 120, TermWidth: 120, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
	})
	plain := joinRenderedPlain(rows)
	for _, text := range []string{
		"The sub-agent has been spawned. I will wait for it to complete.",
		"Waiting for the sub-agent.",
		"The task is still running after the first wait. I will wait again.",
		"The sub-agent is still working.",
		"The sub-agent completed. I will read the report to verify it.",
		"Reading the generated report.",
	} {
		if !strings.Contains(plain, text) {
			t.Fatalf("rendered transcript missing %q\nplain:\n%s\nevents: %#v", text, plain, block.Events)
		}
	}
	if strings.Contains(plain, "Explored") {
		t.Fatalf("single subsequent Read hid wait narratives in an exploration container\nplain:\n%s", plain)
	}
}

func TestHiddenTaskBoundaryIsSymmetricForParticipantAndSubagentLanes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		scope   ACPProjectionScope
		scopeID string
		turnID  string
	}{
		{name: "participant", scope: ACPProjectionParticipant, scopeID: "participant-1", turnID: "participant-turn-1"},
		{name: "subagent_without_parent_panel", scope: ACPProjectionSubagent, scopeID: "task-1", turnID: "turn-1"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			model := NewModel(Config{NoColor: true, NoAnimation: true})
			base := TranscriptEvent{Scope: test.scope, ScopeID: test.scopeID, TurnID: test.turnID, Actor: "worker"}
			events := []TranscriptEvent{
				{
					Kind: TranscriptEventNarrative, Scope: base.Scope, ScopeID: base.ScopeID, TurnID: base.TurnID, Actor: base.Actor,
					NarrativeKind: TranscriptNarrativeReasoning, Text: "before wait", Final: true,
				},
				{
					Kind: TranscriptEventTool, Scope: base.Scope, ScopeID: base.ScopeID, TurnID: base.TurnID, Actor: base.Actor,
					ToolCallID: "wait-1", ToolName: "Task", ToolTaskAction: "wait", ToolStatus: "in_progress",
				},
				{
					Kind: TranscriptEventTool, Scope: base.Scope, ScopeID: base.ScopeID, TurnID: base.TurnID, Actor: base.Actor,
					ToolCallID: "wait-1", ToolName: "Task", ToolTaskAction: "wait",
					ToolStatus: "completed", Final: true,
				},
				{
					Kind: TranscriptEventNarrative, Scope: base.Scope, ScopeID: base.ScopeID, TurnID: base.TurnID, Actor: base.Actor,
					NarrativeKind: TranscriptNarrativeReasoning, Text: "after wait", Final: true,
				},
			}

			next, _ := model.applyTranscriptEvents(events)
			model = next.(*Model)
			var block *ParticipantTurnBlock
			for _, docBlock := range model.doc.Blocks() {
				if candidate, ok := docBlock.(*ParticipantTurnBlock); ok {
					block = candidate
					break
				}
			}
			if block == nil {
				t.Fatal("participant lane block missing")
			}
			var boundaries int
			var reasoning []string
			for _, event := range block.Events {
				switch event.Kind {
				case SESemanticBoundary:
					boundaries++
				case SEReasoning:
					reasoning = append(reasoning, event.Text)
				case SEToolCall:
					if strings.EqualFold(event.Name, "Task") {
						t.Fatalf("hidden Task rendered a physical participant event: %#v", event)
					}
				}
			}
			if boundaries != 1 {
				t.Fatalf("boundaries = %d (events %#v), want call+result deduplicated", boundaries, block.Events)
			}
			if len(reasoning) != 2 || reasoning[0] != "before wait" || reasoning[1] != "after wait" {
				t.Fatalf("reasoning = %#v, want both sides of hidden Task", reasoning)
			}
		})
	}
}

func TestHiddenParticipantTaskDoesNotCreateEmptyTurnBlock(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	next, _ := model.applyTranscriptEvents([]TranscriptEvent{{
		Kind: TranscriptEventTool, Scope: ACPProjectionParticipant, ScopeID: "participant-1", TurnID: "turn-1",
		ToolCallID: "wait-1", ToolName: "Task", ToolTaskAction: "wait", ToolStatus: "completed", Final: true,
	}})
	model = next.(*Model)
	for _, block := range model.doc.Blocks() {
		if _, ok := block.(*ParticipantTurnBlock); ok {
			t.Fatalf("hidden Task created an empty participant block: %#v", model.doc.Blocks())
		}
	}
}

func TestHiddenParticipantTaskReadRepairsCommandOwnerAcrossTurns(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	const (
		turnID      = "participant-turn-1"
		participant = "participant-1"
		first       = "first\r\n"
		second      = "second\r\n"
	)
	base := TranscriptEvent{
		Scope: ACPProjectionParticipant, ScopeID: turnID, TurnID: turnID, ParticipantID: participant, Actor: "worker",
	}
	events := []TranscriptEvent{
		{
			Kind: TranscriptEventTool, Scope: base.Scope, ScopeID: base.ScopeID, TurnID: base.TurnID,
			ParticipantID: base.ParticipantID, Actor: base.Actor,
			ToolCallID: "command-1", ToolName: "RUN_COMMAND", ToolKind: "execute", ToolStatus: "in_progress",
			ToolTaskHandle: "command-3", ToolTerminal: true, ToolOutput: first, ToolOutputTerminal: true,
			ToolOutputCursor: int64(len([]byte(first))), ToolOutputCursorKnown: true,
		},
		{
			Kind: TranscriptEventTool, Scope: base.Scope, ScopeID: "participant-turn-2", TurnID: "participant-turn-2",
			ParticipantID: base.ParticipantID, Actor: base.Actor,
			ToolCallID: "read-1", ToolName: "TASK", ToolKind: "execute", ToolStatus: "completed", Final: true,
			ToolTaskAction: "read", ToolTaskHandle: "command-3", ToolTaskTargetKind: "command",
			ToolOutput: second, ToolOutputTerminal: true,
			ToolOutputStartCursor: int64(len([]byte(first))), ToolOutputStartCursorKnown: true,
			ToolOutputCursor: int64(len([]byte(first + second))), ToolOutputCursorKnown: true,
		},
	}
	next, _ := model.applyTranscriptEvents(events)
	model = next.(*Model)
	block := model.findParticipantTurnBlock(turnID)
	if block == nil {
		t.Fatal("participant block missing")
	}
	physical := physicalTranscriptEventsForTest(block.Events)
	if len(physical) != 1 || physical[0].CallID != "command-1" {
		t.Fatalf("participant events = %#v, want only command owner", block.Events)
	}
	if physical[0].Output != first+second {
		t.Fatalf("participant command output = %q, want %q", physical[0].Output, first+second)
	}
}

func TestHiddenMainTaskReadUsesNormalizedOwnerIndex(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	const (
		first  = "first\n"
		second = "second\n"
	)
	events := []TranscriptEvent{
		{
			Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "turn-1",
			ToolCallID: "command-1", ToolName: "RUN_COMMAND", ToolKind: "execute", ToolStatus: "in_progress",
			ToolTaskHandle: "@COMMAND-3", ToolTerminal: true, ToolOutput: first, ToolOutputTerminal: true,
			ToolOutputCursor: int64(len([]byte(first))), ToolOutputCursorKnown: true,
		},
		{
			Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "turn-2",
			ToolCallID: "read-1", ToolName: "TASK", ToolKind: "execute", ToolStatus: "completed", Final: true,
			ToolTaskAction: "read", ToolTaskHandle: "command-3", ToolTaskTargetKind: "command",
			ToolOutput: second, ToolOutputTerminal: true,
			ToolOutputStartCursor: int64(len([]byte(first))), ToolOutputStartCursorKnown: true,
			ToolOutputCursor: int64(len([]byte(first + second))), ToolOutputCursorKnown: true,
		},
	}
	next, _ := model.applyTranscriptEvents(events)
	model = next.(*Model)
	block := requireMainACPTurnBlockForTest(t, model)
	physical := physicalTranscriptEventsForTest(block.Events)
	if len(physical) != 1 || physical[0].CallID != "command-1" {
		t.Fatalf("main events = %#v, want only the indexed command owner", block.Events)
	}
	if physical[0].Output != first+second {
		t.Fatalf("command output = %q, want normalized owner output %q", physical[0].Output, first+second)
	}
}

func TestSemanticBoundaryStillAllowsNewDenseExplorationRun(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.Status = "completed"
	block.AppendStreamEvent(SEReasoning, "pre-wait reasoning", narrativeTestSource())
	block.sealNarrativeSegmentWithGap()
	block.AppendStreamEvent(SEReasoning, "new dense exploration", narrativeTestSource())
	block.UpdateToolWithMeta("read-1", "Read", "first.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})
	block.UpdateToolWithMeta("read-2", "Read", "second.go", "", true, false, ToolUpdateMeta{ToolKind: "read"})

	runs := collectStableExplorationRuns(block.Events, block.Status)
	if len(runs) != 1 || len(runs[0]) != 2 || runs[0][0] != "read-1" || runs[0][1] != "read-2" {
		t.Fatalf("stable exploration runs = %#v, want the new two-tool stage compacted", runs)
	}
	rows := block.Render(BlockRenderContext{
		Width: 120, TermWidth: 120,
		Theme: NewModel(Config{NoColor: true, NoAnimation: true}).theme,
	})
	plain := joinRenderedPlain(rows)
	if !strings.Contains(plain, "pre-wait reasoning") || !strings.Contains(plain, "Explored") {
		t.Fatalf("rendered transcript lost the boundary or dense new stage\nplain:\n%s", plain)
	}
}

func narrativeTestToolCallEvent(toolCallID, toolName, args, reasoning, assistant string) *session.Event {
	input := map[string]any{}
	switch toolName {
	case "Spawn":
		input = map[string]any{"agent": "breeze", "prompt": "inspect"}
	case "Task":
		input = map[string]any{"action": "wait", "handle": "child-1"}
	case "Read":
		input = map[string]any{"path": "report.md"}
	}
	event := &session.Event{
		Type: session.EventTypeToolCall,
		Tool: &session.EventTool{ID: toolCallID, Name: toolName, Status: "pending", Input: input},
		Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
			metautil.RuntimeToolName: toolName,
		}),
	}
	if reasoning != "" || assistant != "" {
		message := sdkmodel.MessageFromAssistantParts(assistant, reasoning, []sdkmodel.ToolCall{{
			ID: toolCallID, Name: toolName, Args: args,
		}})
		event.Message = &message
	}
	return event
}

func narrativeTestToolResultEvent(
	toolCallID string,
	toolName string,
	status string,
	input map[string]any,
	output map[string]any,
) *session.Event {
	return &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{
			ID: toolCallID, Name: toolName, Status: status, Input: input, Output: output,
		},
		Meta: metautil.WithRuntimeSection(nil, metautil.RuntimeTool, map[string]any{
			metautil.RuntimeToolName: toolName,
		}),
	}
}

func TestTranscriptNarrativeSourceIdentityPrefersMessageThenEvent(t *testing.T) {
	t.Parallel()

	source := narrativeSourceIdentityFromTranscriptEvent(transcript.Event{
		MessageID:          "message-1",
		SourceEventID:      "event-1",
		SourceProjectionID: "projection-1",
	})
	if got := source.stableKey(); got != "message:message-1" {
		t.Fatalf("stable key = %q, want message identity", got)
	}
	source.MessageID = ""
	if got := source.stableKey(); got != "event:event-1" {
		t.Fatalf("stable key = %q, want source event identity", got)
	}
}

func TestTranscriptNarrativeIdentityFlowsAcrossMainParticipantAndSubagentScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		scope   ACPProjectionScope
		scopeID string
		actor   string
	}{
		{name: "main", scope: ACPProjectionMain},
		{name: "participant", scope: ACPProjectionParticipant, scopeID: "participant-1", actor: "@reviewer"},
		{name: "subagent_without_parent_panel", scope: ACPProjectionSubagent, scopeID: "task-1", actor: "worker"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			model := NewModel(Config{NoColor: true, NoAnimation: true})
			events := []TranscriptEvent{
				{
					Kind: TranscriptEventNarrative, Scope: test.scope, ScopeID: test.scopeID, Actor: test.actor, TurnID: "turn-1",
					NarrativeKind: TranscriptNarrativeAssistant, MessageID: "message-1",
					SourceEventID: "event-1", SourceProjectionID: "projection-1", Text: "partial ", Final: false,
				},
				{
					Kind: TranscriptEventNarrative, Scope: test.scope, ScopeID: test.scopeID, Actor: test.actor, TurnID: "turn-1",
					NarrativeKind: TranscriptNarrativeAssistant, MessageID: "message-1",
					SourceEventID: "event-2", SourceProjectionID: "projection-2", Text: "answer", Final: false,
				},
				{
					Kind: TranscriptEventNarrative, Scope: test.scope, ScopeID: test.scopeID, Actor: test.actor, TurnID: "turn-1",
					NarrativeKind: TranscriptNarrativeAssistant, MessageID: "message-1",
					SourceEventID: "event-3", SourceProjectionID: "projection-3", Text: "canonical answer", Final: true,
				},
			}

			next, _ := model.applyTranscriptEvents(events)
			model = next.(*Model)
			var narratives []SubagentEvent
			for _, docBlock := range model.doc.Blocks() {
				switch block := docBlock.(type) {
				case *MainACPTurnBlock:
					if test.scope == ACPProjectionMain {
						narratives = block.Events
					}
				case *ParticipantTurnBlock:
					if test.scope != ACPProjectionMain {
						narratives = block.Events
					}
				}
			}
			if len(narratives) != 1 || narratives[0].Kind != SEAssistant || narratives[0].Text != "canonical answer" {
				t.Fatalf("narratives = %#v, want one identity-scoped canonical answer", narratives)
			}
		})
	}
}
