package tuiapp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	sdkmodel "github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
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
	if buffer := block.Events[0].ActiveBuffer; buffer == nil || buffer.HasTail() {
		t.Fatalf("final narrative buffer = %#v, want sealed render cache", buffer)
	}
}

func TestNarrativePresentationSealsAtToolAndTurnBoundaries(t *testing.T) {
	t.Parallel()

	ctx := BlockRenderContext{Width: 96, Height: 30}
	toolBlock := NewMainACPTurnBlock("turn-tool")
	source := newNarrativeSourceIdentity("message-tool", "event-tool", "projection-tool")
	toolBlock.AppendStreamEvent(SEAssistant, "审查结论\n\n先确认未提交改动的范围", source)
	toolBlock.UpdateToolWithMeta("call-1", "Read", "file.go", "", false, false, ToolUpdateMeta{})
	buffer := toolBlock.Events[0].ActiveBuffer
	if buffer == nil || buffer.HasTail() {
		t.Fatalf("tool boundary buffer = %#v, want sealed stable prefix", buffer)
	}
	for _, row := range toolBlock.Render(ctx) {
		if row.activeTail {
			t.Fatalf("tool boundary retained an active raw-tail row: %#v", row)
		}
	}

	turnBlock := NewMainACPTurnBlock("turn-terminal")
	turnBlock.AppendStreamEvent(SEAssistant, "先确认未提交改动的范围", source)
	turnBlock.SetStatus("completed", "", "", time.Unix(400, 0))
	buffer = turnBlock.Events[0].ActiveBuffer
	if buffer == nil || buffer.HasTail() {
		t.Fatalf("terminal boundary buffer = %#v, want sealed stable prefix", buffer)
	}
	for _, row := range turnBlock.Render(ctx) {
		if row.activeTail {
			t.Fatalf("terminal boundary retained an active raw-tail row: %#v", row)
		}
	}

	finalOnlyBlock := NewMainACPTurnBlock("turn-final-only")
	finalOnlyBlock.ReplaceFinalStreamEvent(SEAssistant, "先确认未提交改动的范围", source)
	for _, row := range finalOnlyBlock.Render(ctx) {
		if row.activeTail {
			t.Fatalf("final-only narrative retained an active raw-tail row: %#v", row)
		}
	}
}

func TestSealedNarrativePrefixIsNotRerenderedWhenToolOutputChanges(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	block := NewMainACPTurnBlock("turn-cache")
	source := newNarrativeSourceIdentity("message-cache", "event-cache", "projection-cache")
	block.AppendStreamEvent(SEAssistant, "审查结论\n\n先确认未提交改动的范围", source)
	block.UpdateToolWithMeta("call-cache", "Read", "file.go", "first", false, false, ToolUpdateMeta{})

	ctx := model.blockRenderContext(96)
	block.Render(ctx)
	firstCalls := model.diag.GlamourRenderCalls
	if firstCalls == 0 {
		t.Fatal("sealed narrative prefix did not use Glamour")
	}

	block.UpdateToolWithMeta("call-cache", "Read", "file.go", "second", false, false, ToolUpdateMeta{})
	block.Render(ctx)
	if got := model.diag.GlamourRenderCalls; got != firstCalls {
		t.Fatalf("tool output update rerendered sealed narrative: calls %d, before %d", got, firstCalls)
	}
}

func TestProductionMainACPTurnUsesActiveTailViewportPathUntilToolBoundary(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 96, Height: 24})
	model = updated.(*Model)
	event := TranscriptEvent{
		Kind:          TranscriptEventNarrative,
		NarrativeKind: TranscriptNarrativeAssistant,
		Scope:         ACPProjectionMain,
		ScopeID:       "session-active-tail",
		TurnID:        "turn-active-tail",
		MessageID:     "message-active-tail",
		Actor:         "assistant",
		Text:          "先确认",
	}
	model = applyTranscriptEventForTest(t, model, event)
	model.syncViewportContent()

	event.Text = "未提交改动的范围"
	model = applyTranscriptEventForTest(t, model, event)
	block := requireMainACPTurnBlockForTest(t, model)
	if _, ok := model.doc.Find(block.BlockID()).(*MainACPTurnBlock); !ok {
		t.Fatalf("production transcript block = %T, want *MainACPTurnBlock", model.doc.Find(block.BlockID()))
	}
	if !model.dirtyViewportBlocksOnlyActiveNarrative() {
		t.Fatal("active MainACPTurnBlock tail did not use the direct viewport path")
	}

	block.UpdateToolWithMeta("call-active-tail", "Read", "file.go", "", false, false, ToolUpdateMeta{})
	model.markViewportBlockDirty(block.BlockID())
	if model.dirtyViewportBlocksOnlyActiveNarrative() {
		t.Fatal("tool boundary incorrectly retained the active-tail viewport path")
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

func TestNarrativeStreamKeepsDistinctMessageIdentitiesSeparate(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.AppendStreamEvent(
		SEReasoning,
		"first",
		newNarrativeSourceIdentity("message-1", "event-1", "projection-1"),
	)
	block.AppendStreamEvent(
		SEReasoning,
		"second",
		newNarrativeSourceIdentity("message-2", "event-2", "projection-2"),
	)

	if len(block.Events) != 2 || block.Events[0].Text != "first" || block.Events[1].Text != "second" {
		t.Fatalf("events = %#v, want distinct MessageIDs to retain separate owners", block.Events)
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

func TestACPEnvelopeReasoningIdentitySurvivesInterleavedToolEvent(t *testing.T) {
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

	const messageID = "message-1"
	first := sdkmodel.NewReasoningMessage(
		sdkmodel.RoleAssistant,
		"**Identifying spec duplication and reuse opportunities**",
		sdkmodel.ReasoningVisibilityVisible,
	)
	apply("thought-1", session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant, MessageID: messageID, Message: &first,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
			MessageID:     messageID,
			Content:       session.ProtocolTextContent(first.ReasoningText()),
		}},
	}))
	apply("tool-1", session.CanonicalizeEvent(narrativeTestToolCallEvent(
		"read-1",
		"Read",
		`{"path":"generated.md"}`,
		"",
		"",
	)))
	secondText := "\n**Identifying major test coverage gaps in runners**\n**Inspecting generated skill documentation sources**"
	second := sdkmodel.NewReasoningMessage(sdkmodel.RoleAssistant, secondText, sdkmodel.ReasoningVisibilityVisible)
	apply("thought-2", session.MarkUIOnly(&session.Event{
		Type: session.EventTypeAssistant, MessageID: messageID, Message: &second,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentThought),
			MessageID:     messageID,
			Content:       session.ProtocolTextContent(secondText),
		}},
	}))
	finalText := first.ReasoningText() + secondText
	final := sdkmodel.NewReasoningMessage(sdkmodel.RoleAssistant, finalText, sdkmodel.ReasoningVisibilityVisible)
	apply("thought-final", session.CanonicalizeEvent(&session.Event{
		Type: session.EventTypeAssistant, MessageID: messageID, Message: &final,
	}))

	block := requireMainACPTurnBlockForTest(t, model)
	var reasoning []SubagentEvent
	for _, event := range block.Events {
		if event.Kind == SEReasoning {
			reasoning = append(reasoning, event)
		}
	}
	if len(reasoning) != 1 || reasoning[0].Text != finalText {
		t.Fatalf("reasoning = %#v (events %#v), want one canonical message owner", reasoning, block.Events)
	}
	var reasoningHeads int
	for _, row := range renderedPlainRows(block.Render(model.blockRenderContext(180))) {
		if strings.HasPrefix(strings.TrimSpace(row), "›") {
			reasoningHeads++
		}
	}
	if reasoningHeads != 1 {
		t.Fatalf("reasoning rendered as %d blocks, want 1", reasoningHeads)
	}
}

func TestCanonicalToolCallKeepsOneAssistantMessageOnOneRenderedRow(t *testing.T) {
	t.Parallel()

	const (
		messageID = "6e4b431c-bf97-45dd-9d74-2adc43f23704"
		answer    = "原来如此！**Task read** 可查看单个任务；这里用 **Task wait** 批量等待三个子代理完成："
		reasoning = "Task read accepts one handle. Use Task wait to observe three subagents together."
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
	for index, chunk := range []string{"原来", "如此！", "**Task read** 可查看单个任务；这里用 **Task wait** 批量等待三个子代理完成："} {
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
	const rendered = "· 原来如此！Task read 可查看单个任务；这里用 Task wait 批量等待三个子代理完成："
	if len(assistantRows) != 1 || assistantRows[0] != rendered {
		t.Fatalf("assistant rows = %#v, want one canonical rendered row", assistantRows)
	}
}

func TestNarrativeStreamBucketsAnonymousDeltasByOutputType(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.AppendStreamEvent(SEReasoning, "first", newNarrativeSourceIdentity("", "event-1", "projection-1"))
	block.AppendStreamEvent(SEReasoning, " second", newNarrativeSourceIdentity("", "event-2", "projection-2"))
	block.AppendStreamEvent(SEAssistant, "answer", newNarrativeSourceIdentity("", "event-3", "projection-3"))
	block.AppendStreamEvent(SEAssistant, " complete", newNarrativeSourceIdentity("", "event-4", "projection-4"))
	block.AppendStreamEvent(SEReasoning, "verify", newNarrativeSourceIdentity("typed-reasoning", "event-5", "projection-5"))
	block.AppendStreamEvent(SEAssistant, "final", newNarrativeSourceIdentity("", "event-6", "projection-6"))

	if len(block.Events) != 4 {
		t.Fatalf("events = %#v, want four contiguous output-type buckets", block.Events)
	}
	if block.Events[0].Kind != SEReasoning || block.Events[0].Text != "first second" ||
		block.Events[1].Kind != SEAssistant || block.Events[1].Text != "answer complete" ||
		block.Events[2].Kind != SEReasoning || block.Events[2].Text != "verify" ||
		block.Events[3].Kind != SEAssistant || block.Events[3].Text != "final" {
		t.Fatalf("events = %#v, want anonymous deltas joined only within contiguous output-type runs", block.Events)
	}
}

func TestNarrativeStreamStableIdentitySurvivesForeignSemanticBoundary(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	source := narrativeTestSource()
	block.AppendStreamEvent(SEReasoning, "before wait", source)
	block.advanceNarrativeBoundary()
	block.AppendStreamEvent(SEReasoning, " after wait", source)
	block.ReplaceFinalStreamEvent(SEReasoning, "before wait after wait final", source)

	if len(block.Events) != 1 {
		t.Fatalf("events = %#v, want one identity-owned reasoning event", block.Events)
	}
	if block.Events[0].Text != "before wait after wait final" {
		t.Fatalf("events = %#v, want final snapshot to replace its original owner", block.Events)
	}
}

func TestNarrativeStreamNonNarrativeEventsCannotCloseIdentifiedMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		barrier func(*MainACPTurnBlock)
	}{
		{
			name: "tool",
			barrier: func(block *MainACPTurnBlock) {
				block.UpdateToolWithMeta("read-1", "Read", "file.go", "ok", true, false, ToolUpdateMeta{})
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
			source := narrativeTestSource()
			block.AppendStreamEvent(SEReasoning, "before", source)
			test.barrier(block)
			block.AppendStreamEvent(SEReasoning, " after", source)
			block.ReplaceFinalStreamEvent(SEReasoning, "before after final", source)

			var reasoning []string
			for _, event := range block.Events {
				if event.Kind == SEReasoning {
					reasoning = append(reasoning, event.Text)
				}
			}
			if len(reasoning) != 1 || reasoning[0] != "before after final" {
				t.Fatalf("reasoning = %#v (events %#v), want one identity-owned message", reasoning, block.Events)
			}
		})
	}
}

func TestNarrativeStreamNonNarrativeEventsStillSeparateAnonymousMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		barrier func(*MainACPTurnBlock)
	}{
		{
			name: "tool",
			barrier: func(block *MainACPTurnBlock) {
				block.UpdateToolWithMeta("read-1", "Read", "file.go", "ok", true, false, ToolUpdateMeta{})
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
			block.AppendStreamEvent(SEReasoning, "before", narrativeSourceIdentity{})
			test.barrier(block)
			block.AppendStreamEvent(SEReasoning, "after", narrativeSourceIdentity{})
			block.ReplaceFinalStreamEvent(SEReasoning, "after final", narrativeSourceIdentity{})

			var reasoning []string
			for _, event := range block.Events {
				if event.Kind == SEReasoning {
					reasoning = append(reasoning, event.Text)
				}
			}
			if len(reasoning) != 2 || reasoning[0] != "before" || reasoning[1] != "after final" {
				t.Fatalf("reasoning = %#v (events %#v), want anonymous boundary-preserved messages", reasoning, block.Events)
			}
		})
	}
}

func TestIdentifiedReasoningRendersOnceAcrossHiddenCompletedTool(t *testing.T) {
	t.Parallel()

	type narrativeBlock interface {
		AppendStreamEvent(SubagentEventKind, string, narrativeSourceIdentity, ...time.Time)
		ReplaceFinalStreamEvent(SubagentEventKind, string, narrativeSourceIdentity, ...time.Time)
		UpdateToolWithMeta(string, string, string, string, bool, bool, ToolUpdateMeta)
		Render(BlockRenderContext) []RenderedRow
	}
	tests := []struct {
		name  string
		block func() narrativeBlock
	}{
		{name: "main", block: func() narrativeBlock { return NewMainACPTurnBlock("turn-1") }},
		{name: "participant", block: func() narrativeBlock {
			return NewParticipantTurnBlock("participant-1", "@reviewer")
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			block := test.block()
			source := newNarrativeSourceIdentity("message-1", "event-1", "projection-1")
			block.AppendStreamEvent(SEReasoning, "**Identifying spec duplication and reuse opportunities**", source)
			block.UpdateToolWithMeta("late-hidden-tool", "Read", "", "", true, false, ToolUpdateMeta{ToolKind: "read"})
			block.AppendStreamEvent(
				SEReasoning,
				"\n**Identifying major test coverage gaps in runners**\n**Inspecting generated skill documentation sources**",
				source,
			)
			block.ReplaceFinalStreamEvent(
				SEReasoning,
				"**Identifying spec duplication and reuse opportunities**\n**Identifying major test coverage gaps in runners**\n**Inspecting generated skill documentation sources**",
				source,
			)

			model := NewModel(Config{NoColor: true, NoAnimation: true})
			var reasoningHeads int
			for _, row := range renderedPlainRows(block.Render(model.blockRenderContext(180))) {
				trimmed := strings.TrimSpace(row)
				if strings.HasPrefix(trimmed, "›") {
					reasoningHeads++
				}
			}
			if reasoningHeads != 1 {
				t.Fatalf("same reasoning message rendered as %d blocks, want 1", reasoningHeads)
			}
		})
	}
}

func TestHistoricalPresentationRepairsDoNotAdvanceAnonymousNarrative(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(*MainACPTurnBlock)
		repair func(*MainACPTurnBlock)
	}{
		{
			name: "tool final",
			setup: func(block *MainACPTurnBlock) {
				block.UpdateToolWithMeta("read-1", "Read", "file.go", "", false, false, ToolUpdateMeta{ToolKind: "read"})
			},
			repair: func(block *MainACPTurnBlock) {
				block.UpdateToolWithMeta("read-1", "Read", "", "", true, false, ToolUpdateMeta{ToolKind: "read"})
			},
		},
		{
			name: "approval settlement",
			setup: func(block *MainACPTurnBlock) {
				block.UpdateToolWithMeta("write-1", "Write", "file.go", "", false, false, ToolUpdateMeta{ToolKind: "edit"})
				block.AddApprovalReviewEvent("write-1", "Write", "file.go", "pending", "", "", "")
			},
			repair: func(block *MainACPTurnBlock) {
				block.AddApprovalReviewEvent("write-1", "Write", "file.go", "approved", "", "session", "approved")
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			block := NewMainACPTurnBlock("turn-1")
			test.setup(block)
			block.AppendStreamEvent(SEReasoning, "first ", narrativeSourceIdentity{})
			test.repair(block)
			block.AppendStreamEvent(SEReasoning, "second", narrativeSourceIdentity{})

			var reasoning []SubagentEvent
			for _, event := range block.Events {
				if event.Kind == SEReasoning {
					reasoning = append(reasoning, event)
				}
			}
			if len(reasoning) != 1 || reasoning[0].Text != "first second" {
				t.Fatalf("reasoning = %#v (events %#v), want repair to preserve anonymous owner", reasoning, block.Events)
			}
		})
	}
}

func TestNarrativeFinalClosesOnlyItsOwnMessage(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	source := newNarrativeSourceIdentity("message-1", "event-1", "projection-1")
	block.AppendStreamEvent(SEAssistant, "partial", source)
	block.ReplaceFinalStreamEvent(SEAssistant, "final", source)
	block.AppendStreamEvent(SEAssistant, " late delta", source)
	block.ReplaceFinalStreamEvent(SEAssistant, "richer final", source)

	if len(block.Events) != 1 || block.Events[0].Text != "richer final" {
		t.Fatalf("events = %#v, want late delta ignored and duplicate final converged", block.Events)
	}
}

func TestTurnBoundaryCanCloseAllNarrativeOwners(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	source := newNarrativeSourceIdentity("message-1", "event-1", "projection-1")
	block.ReplaceFinalStreamEvent(SEAssistant, "first turn", source)
	block.closeNarrativeStream()
	block.ReplaceFinalStreamEvent(SEAssistant, "next turn", source)

	if len(block.Events) != 2 || block.Events[0].Text != "first turn" || block.Events[1].Text != "next turn" {
		t.Fatalf("events = %#v, want Turn epoch to prevent owner reuse", block.Events)
	}
}

func TestTranscriptUsageTelemetryDoesNotAdvanceNarrativeBoundary(t *testing.T) {
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
			Usage: &session.UsageSnapshot{TotalTokens: 10, ContextWindowTokens: 100},
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

	block.advanceNarrativeBoundary()
	block.ReplaceFinalStreamEvent(SEAssistant, "next", newNarrativeSourceIdentity("message-2", "event-2", "projection-2"))
	if len(block.Events) != 2 || block.Events[0].Text != "canonical" || block.Events[1].Text != "next" {
		t.Fatalf("distinct final events = %#v, want prior identified message preserved", block.Events)
	}
}

func TestNarrativeStreamIdentityFreeFinalFailsClosedAcrossBarrier(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	block.AppendStreamEvent(SEAssistant, "before", narrativeSourceIdentity{})
	block.advanceNarrativeBoundary()
	block.AppendStreamEvent(SEAssistant, "after", narrativeSourceIdentity{})
	block.ReplaceFinalStreamEvent(SEAssistant, "before after final", narrativeSourceIdentity{})

	if len(block.Events) != 2 {
		t.Fatalf("events = %#v, want one assistant event per segment", block.Events)
	}
	if got := block.Events[1].Text; got != "before after final" {
		t.Fatalf("current segment final = %q, want full fail-closed snapshot without cross-segment identity", got)
	}
}

func TestNarrativeStreamStableFinalReplacesOriginalOwnerAcrossBoundary(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	source := newNarrativeSourceIdentity("message-1", "event-1", "projection-1")
	block.AppendStreamEvent(SEAssistant, "before", source)
	block.advanceNarrativeBoundary()
	block.AppendStreamEvent(SEAssistant, "after", source)
	block.ReplaceFinalStreamEvent(SEAssistant, "  before\nafter final", source)

	if len(block.Events) != 1 || block.Events[0].Text != "  before\nafter final" {
		t.Fatalf("events = %#v, want stable final on the original message owner", block.Events)
	}
}

func TestNarrativeStreamStableFinalOnlyUpdateDoesNotCreateBoundarySegment(t *testing.T) {
	t.Parallel()

	block := NewMainACPTurnBlock("turn-1")
	source := newNarrativeSourceIdentity("message-1", "event-1", "projection-1")
	block.AppendStreamEvent(SEAssistant, "before", source)
	block.advanceNarrativeBoundary()
	block.ReplaceFinalStreamEvent(SEAssistant, "before\nafter final", source)

	if len(block.Events) != 1 || block.Events[0].Text != "before\nafter final" {
		t.Fatalf("events = %#v, want final to update the identified owner", block.Events)
	}
}

func TestNarrativeStreamFinalEqualToIdentifiedMessageAddsNoDuplicate(t *testing.T) {
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
			block.advanceNarrativeBoundary()
			if test.provisional != "" {
				block.AppendStreamEvent(SEReasoning, test.provisional, source)
			}
			block.ReplaceFinalStreamEvent(SEReasoning, "before", source)

			if len(block.Events) != 1 || block.Events[0].Text != "before" {
				t.Fatalf("events = %#v, want only the identified reasoning message", block.Events)
			}
		})
	}
}

func TestHiddenTaskWaitStillCreatesAnonymousNarrativeBoundary(t *testing.T) {
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
			ToolName:       "Task",
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

func TestFailedTaskControlRemainsVisibleWithoutExecutePresentation(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"read", "wait"} {
		action := action
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			model := NewModel(Config{NoColor: true, NoAnimation: true})
			next, _ := model.applyTranscriptEvents([]TranscriptEvent{
				{
					Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, TurnID: "turn-1",
					NarrativeKind: TranscriptNarrativeReasoning, Text: "before " + action, Final: true,
				},
				{
					Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "turn-1",
					ToolCallID: action + "-1", ToolName: "Task", ToolTaskAction: action, ToolStatus: "in_progress",
				},
				{
					Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "turn-1",
					ToolCallID: action + "-1", ToolName: "Task", ToolTaskAction: action,
					ToolStatus: "failed", ToolError: true, ToolOutput: action + " failed", Final: true,
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
			if failed == nil || !failed.Done || !failed.Err || !strings.Contains(failed.Output, action+" failed") {
				t.Fatalf("failed Task event = %#v (events %#v), want visible terminal error", failed, block.Events)
			}
			rows := block.Render(BlockRenderContext{
				Width: 120, TermWidth: 120, Theme: model.theme, ThemeKey: themeRenderCacheKey(model.theme),
			})
			if plain := joinRenderedPlain(rows); !strings.Contains(plain, action+" failed") ||
				strings.Contains(plain, "Ran "+action) || strings.Contains(plain, "Ran Task") {
				t.Fatalf("failed Task output did not retain Task-specific presentation\nplain:\n%s", plain)
			}
		})
	}
}

func TestTaskControlVisibilityContract(t *testing.T) {
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
			observedTargetFailure := success
			observedTargetFailure.ToolStatus = "failed"
			observedTargetFailure.ToolError = true
			observedTargetFailure.ToolTaskState = "failed"
			if gotAction, hidden := hiddenTaskControlAction(observedTargetFailure); !hidden || gotAction != action {
				t.Fatalf("observed target failure for %s = %q/%v, want hidden observer", action, gotAction, hidden)
			}
			runningControlFailure := success
			runningControlFailure.ToolStatus = "failed"
			runningControlFailure.ToolError = true
			runningControlFailure.ToolTaskState = "running"
			if _, hidden := hiddenTaskControlAction(runningControlFailure); hidden {
				t.Fatalf("running-state control failure for %s was hidden", action)
			}
			controlFailure := success
			controlFailure.ToolStatus = "failed"
			controlFailure.ToolError = true
			if _, hidden := hiddenTaskControlAction(controlFailure); hidden {
				t.Fatalf("control-invocation failure for %s was hidden", action)
			}
		})
	}

	write := TranscriptEvent{
		Kind: TranscriptEventTool, ToolName: "Task", ToolTaskAction: "write",
		ToolStatus: "completed", Final: true,
	}
	if _, hidden := hiddenTaskControlAction(write); hidden {
		t.Fatal("successful Task write was hidden instead of retaining its interaction row")
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
			ToolCallID: "command-1", ToolName: "RunCommand", ToolKind: "execute", ToolStatus: "in_progress",
			ToolTaskHandle: "command-3", ToolTerminal: true, ToolOutput: first, ToolOutputTerminal: true,
			ToolOutputCursor: int64(len([]byte(first))), ToolOutputCursorKnown: true,
		},
		{
			Kind: TranscriptEventTool, Scope: base.Scope, ScopeID: "participant-turn-2", TurnID: "participant-turn-2",
			ParticipantID: base.ParticipantID, Actor: base.Actor,
			ToolCallID: "read-1", ToolName: "Task", ToolKind: "other", ToolStatus: "completed", Final: true,
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
			ToolCallID: "command-1", ToolName: "RunCommand", ToolKind: "execute", ToolStatus: "in_progress",
			ToolTaskHandle: "@COMMAND-3", ToolTerminal: true, ToolOutput: first, ToolOutputTerminal: true,
			ToolOutputCursor: int64(len([]byte(first))), ToolOutputCursorKnown: true,
		},
		{
			Kind: TranscriptEventTool, Scope: ACPProjectionMain, TurnID: "turn-2",
			ToolCallID: "read-1", ToolName: "Task", ToolKind: "other", ToolStatus: "completed", Final: true,
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
	block.advanceNarrativeBoundaryWithGap()
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

func TestTranscriptNarrativeSourceIdentityUsesOnlyCanonicalMessageID(t *testing.T) {
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
	if got := source.stableKey(); got != "" {
		t.Fatalf("stable key = %q, want anonymous type bucket despite transport event identity", got)
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
