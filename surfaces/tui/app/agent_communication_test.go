package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestAgentCommunicationEnvelopeUsesDedicatedTimelinePresentation(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, Scope: eventstream.ScopeMain, Actor: "breeze(kian)",
		AgentCommunicationSource: &eventstream.ActorIdentity{Kind: "participant", ID: "participant-1", Role: "delegated", Name: "breeze(kian)"},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: "**review complete**"},
			Meta: map[string]any{"caelis": map[string]any{"agent_communication": map[string]any{
				"source": map[string]any{"kind": "participant", "id": "participant-1", "role": "delegated", "name": "breeze(kian)"},
			}}},
		},
	})
	blocks := model.doc.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want one main timeline block", blocks)
	}
	block, ok := blocks[0].(*MainACPTurnBlock)
	if !ok {
		t.Fatalf("block = %T, want MainACPTurnBlock", blocks[0])
	}
	if len(block.Events) != 1 || block.Events[0].Kind != SEAgentCommunication {
		t.Fatalf("events = %#v, want one Agent communication event", block.Events)
	}
	rows := block.Render(model.blockRenderContext(80))
	plain := renderedRowsPlain(rows)
	if !strings.Contains(plain, "• kian[breeze]: **review complete**") ||
		strings.Contains(plain, "Agent message from") || strings.Contains(plain, "> ") {
		t.Fatalf("rendered Agent communication = %q", plain)
	}
}

func TestShortAgentCommunicationLinksToSourceOverlay(t *testing.T) {
	t.Parallel()

	rows := renderAgentCommunicationRows("turn-1", SubagentEvent{
		Kind: SEAgentCommunication, SourceName: "Docs Review", SourceCallID: "spawn-1", Text: "review complete",
	}, 0, 100, BlockRenderContext{Width: 100, TermWidth: 100}, acpTranscriptRenderOptions{
		AgentMessageTargetLinks: true,
	})
	if len(rows) != 1 || rows[0].ClickToken != subagentOutputOverlayClickToken("spawn-1") {
		t.Fatalf("short Agent communication rows = %#v, want source overlay link", rows)
	}
}

func TestReceivedAgentCommunicationOpensOverlayForLongMessage(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 120
	model.height = 40
	model.currentSessionID = "session-1"
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn breeze",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "breeze", "prompt": "delegated messaging exercise"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	running := eventstream.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "kian", "state": "running"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	view := requireSubagentOutputViewForTest(t, model, "spawn-1")
	view.participantID = "participant-1"
	message := "start " + strings.Repeat("payload ", 18) + "middle-marker " + strings.Repeat("tail ", 18)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		AgentCommunicationSource: &eventstream.ActorIdentity{Kind: "participant", ID: "participant-1", Role: "delegated", Name: "breeze(kian)"},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: message},
			MessageID:     "agent-message-1",
			Meta: map[string]any{"caelis": map[string]any{"agent_communication": map[string]any{
				"source": map[string]any{"kind": "participant", "id": "participant-1", "role": "delegated", "name": "breeze(kian)"},
			}}},
		},
	})

	model.syncViewportContent()
	headerLine := -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• kian[breeze]:") {
			headerLine = index
			break
		}
	}
	if headerLine < 0 {
		t.Fatalf("Received row missing: %#v", model.viewportPlainLines)
	}
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, "middle-marker") || !strings.Contains(model.viewportPlainLines[headerLine], "...") {
		t.Fatalf("Received row did not reuse compact preview:\n%s", plain)
	}
	if token := model.viewportClickTokens[headerLine]; token != subagentOutputOverlayClickToken("spawn-1") {
		t.Fatalf("Received row click token = %q", token)
	}
	if bounds := model.viewportClickBounds[headerLine]; bounds.valid() {
		t.Fatalf("Received row retained a bounded click target: %#v", bounds)
	}

	clickViewportLine(t, model, headerLine)
	plain = strings.Join(model.viewportPlainLines, "\n")
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-1" {
		t.Fatalf("Received row did not open its source overlay: %#v", model.subagentOutputOverlay)
	}
	if strings.Contains(plain, "middle-marker") {
		t.Fatalf("Received row expanded the hidden message:\n%s", plain)
	}
}

func TestSubagentOverlayRendersParentMessageAsUserInput(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	view := model.ensureSubagentOutputView("spawn-1")
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionSubagent,
		NarrativeKind: TranscriptNarrativeUser, Actor: "parent", Text: "continue",
	})
	blocks := view.document.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("overlay blocks = %#v, want one participant timeline block", blocks)
	}
	block, ok := blocks[0].(*ParticipantTurnBlock)
	if !ok || len(block.Events) != 1 || block.Events[0].Kind != SEUserInput || block.Events[0].Text != "parent: continue" {
		t.Fatalf("overlay block = %#v, want parent user message", blocks[0])
	}
	if !subagentOutputViewHasTranscript(view) {
		t.Fatal("Agent communication did not count as overlay transcript")
	}
	plain := renderedRowsPlain(block.Render(model.blockRenderContext(80)))
	if !strings.Contains(plain, "> parent: continue") || strings.Contains(plain, "[kernel]") {
		t.Fatalf("overlay Agent communication = %q", plain)
	}
}

func TestAgentCommunicationPreservesMainTimelineOrder(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	base := TranscriptEvent{
		Kind: TranscriptEventNarrative, Scope: ACPProjectionMain, TurnID: "turn-1",
		NarrativeKind: TranscriptNarrativeAssistant, MessageID: "assistant-1",
	}
	base.Text = "before"
	_, _ = model.applyTranscriptEvent(base)
	_, _ = model.applyTranscriptEvent(TranscriptEvent{
		Kind: TranscriptEventAgentCommunication, Scope: ACPProjectionMain, TurnID: "turn-1",
		Actor: "reviewer", AgentSourceName: "reviewer", AgentSourceID: "reviewer-1", Text: "review complete",
	})
	base.MessageID = "assistant-2"
	base.Text = "after"
	_, _ = model.applyTranscriptEvent(base)

	blocks := model.doc.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want one ordered main timeline", blocks)
	}
	block, ok := blocks[0].(*MainACPTurnBlock)
	if !ok || len(block.Events) != 3 {
		t.Fatalf("main timeline = %#v", blocks[0])
	}
	if block.Events[0].Kind != SEAssistant || block.Events[0].Text != "before" ||
		block.Events[1].Kind != SEAgentCommunication || block.Events[1].Text != "review complete" ||
		block.Events[2].Kind != SEAssistant || block.Events[2].Text != "after" {
		t.Fatalf("ordered events = %#v", block.Events)
	}
	plain := renderedRowsPlain(block.Render(model.blockRenderContext(80)))
	before := strings.Index(plain, "before")
	communication := strings.Index(plain, "• reviewer: review complete")
	after := strings.Index(plain, "after")
	if before < 0 || communication <= before || after <= communication {
		t.Fatalf("render order = %q", plain)
	}
}

func TestAgentCommunicationHeaderUsesThemeHandleAndNormalBody(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: theme}
	row := renderAgentMessageRow("block", "kian[breeze]", "compact message", ctx, "")
	if got := ansiTextForForeground(t, row.Styled, ctx.Theme.Focus); !strings.Contains(got, "kian") {
		t.Fatalf("Agent source did not receive focus styling: %q", row.Styled)
	}
	if got := ansiTextForForeground(t, row.Styled, ctx.Theme.TextStyle().GetForeground()); !strings.Contains(got, "compact message") {
		t.Fatalf("Agent message did not retain normal text styling: %q", row.Styled)
	}
}

func TestAgentCommunicationSourceMatchingFailsClosedOnKnownIdentityMismatch(t *testing.T) {
	view := &subagentOutputView{
		taskHandle: "kian", participantID: "participant-other",
		actor: "kian[breeze]", title: "kian[breeze]: delegated messaging exercise",
	}
	if agentCommunicationSourceMatchesView("participant-1", "breeze(kian)", taskstream.TaskDescriptor{}, view) {
		t.Fatal("display name overrode a mismatched trusted participant identity")
	}
}

func renderedRowsPlain(rows []RenderedRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, row.Plain)
	}
	return strings.Join(parts, "\n")
}
