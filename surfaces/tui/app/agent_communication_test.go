package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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
			RawInput: map[string]any{"agent": "breeze", "prompt": "delegated messaging exercise"}, Meta: acpToolNameMeta("StartThread"),
		},
	})
	running := eventstream.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "kian", "state": "running"}, Meta: acpToolNameMeta("StartThread"),
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
	linkToken := subagentOutputOverlayClickToken("spawn-1")
	labelEnd := displayColumns("• kian[breeze]")
	if token := model.viewportClickTokens[headerLine]; token != linkToken {
		t.Fatalf("Received source-label token = %q, want %q", token, linkToken)
	}
	if bounds := model.viewportClickBounds[headerLine]; !bounds.valid() || bounds.start != agentMessageGutterColumns || bounds.end != labelEnd {
		t.Fatalf("Received source-label span = %#v, want [%d,%d)", bounds, agentMessageGutterColumns, labelEnd)
	}
	if token := model.viewportClickAltTokens[headerLine]; !strings.HasPrefix(token, agentMessageFoldTokenPrefix) {
		t.Fatalf("Received body token = %q, want in-place fold", token)
	}

	// The source label opens the retained workspace and leaves the compact body
	// folded.
	clickViewportLine(t, model, headerLine)
	plain = strings.Join(model.viewportPlainLines, "\n")
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-1" {
		t.Fatalf("Received label click did not open its source overlay: %#v", model.subagentOutputOverlay)
	}
	if strings.Contains(plain, "middle-marker") {
		t.Fatalf("Received label click expanded the hidden message:\n%s", plain)
	}
	model.subagentOutputOverlay = nil

	// The compact body expands the message in place, without navigating away.
	clickViewportColumn(t, model, headerLine, labelEnd+4)
	plain = strings.Join(model.viewportPlainLines, "\n")
	if model.subagentOutputOverlay != nil {
		t.Fatalf("Received body click opened a workspace: %#v", model.subagentOutputOverlay)
	}
	if !strings.Contains(plain, "middle-marker") {
		t.Fatalf("Received body click did not expand the hidden message:\n%s", plain)
	}

	// A second body click collapses it again.
	clickViewportColumn(t, model, headerLine, labelEnd+4)
	if plain = strings.Join(model.viewportPlainLines, "\n"); strings.Contains(plain, "middle-marker") {
		t.Fatalf("Received body click did not collapse the message:\n%s", plain)
	}

	// Continuation lines of the expanded message carry only the in-place body
	// action, so they collapse it instead of reopening the workspace.
	clickViewportColumn(t, model, headerLine, labelEnd+4)
	continuation := headerLine + 1
	if continuation >= len(model.viewportPlainLines) || strings.TrimSpace(model.viewportPlainLines[continuation]) == "" {
		t.Fatalf("expanded Received message did not wrap: %#v", model.viewportPlainLines)
	}
	if token := model.viewportClickTokens[continuation]; !strings.HasPrefix(token, agentMessageFoldTokenPrefix) {
		t.Fatalf("Received continuation token = %q, want in-place fold", token)
	}
	if alt := model.viewportClickAltTokens[continuation]; alt != "" {
		t.Fatalf("Received continuation kept a second target: %q", alt)
	}
	clickViewportColumn(t, model, continuation, 4)
	if model.subagentOutputOverlay != nil {
		t.Fatalf("Received continuation click opened a workspace: %#v", model.subagentOutputOverlay)
	}
	if plain = strings.Join(model.viewportPlainLines, "\n"); strings.Contains(plain, "middle-marker") {
		t.Fatalf("Received continuation click did not collapse the message:\n%s", plain)
	}
}

func TestSubagentOverlayRendersParentMessageAsAgentCommunication(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	view := model.ensureSubagentOutputView("spawn-1")
	view.observeChildEvent(TranscriptEvent{
		Kind: TranscriptEventAgentCommunication, Scope: ACPProjectionSubagent,
		AgentSourceKind: "controller", AgentSourceID: "parent", AgentSourceName: "parent", Text: "continue",
	})
	blocks := view.document.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("overlay blocks = %#v, want one participant timeline block", blocks)
	}
	block, ok := blocks[0].(*ParticipantTurnBlock)
	if !ok || len(block.Events) != 1 || block.Events[0].Kind != SEAgentCommunication || block.Events[0].Text != "continue" || block.Events[0].SourceName != "parent" {
		t.Fatalf("overlay block = %#v, want parent user message", blocks[0])
	}
	if !subagentOutputViewHasTranscript(view) {
		t.Fatal("Agent communication did not count as overlay transcript")
	}
	plain := renderedRowsPlain(block.Render(model.blockRenderContext(80)))
	if !strings.Contains(plain, "• parent: continue") || strings.Contains(plain, "[kernel]") {
		t.Fatalf("overlay Agent communication = %q", plain)
	}
}

// A peer label is not width-limited, so a narrow pane can split it across
// physical lines. Every line the label covers keeps the workspace link; only the
// columns past the label fall back to the row's fold action.
func TestWrappedAgentCommunicationLabelKeepsItsWorkspaceLink(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	resized, _ := model.Update(tea.WindowSizeMsg{Width: 18, Height: 40})
	model = resized.(*Model)
	model.currentSessionID = "session-1"
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn breeze",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "breeze", "prompt": "delegated messaging exercise"}, Meta: acpToolNameMeta("StartThread"),
		},
	})
	running := eventstream.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
			RawOutput: map[string]any{"handle": "peer-cli-smoke", "state": "running"}, Meta: acpToolNameMeta("StartThread"),
		},
	})
	view := requireSubagentOutputViewForTest(t, model, "spawn-1")
	view.participantID = "participant-1"
	message := "start " + strings.Repeat("payload ", 18) + "middle-marker " + strings.Repeat("tail ", 18)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		AgentCommunicationSource: &eventstream.ActorIdentity{Kind: "participant", ID: "participant-1", Role: "delegated", Name: "breeze(peer-cli-smoke)"},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: message},
			MessageID:     "agent-message-1",
			Meta: map[string]any{"caelis": map[string]any{"agent_communication": map[string]any{
				"source": map[string]any{"kind": "participant", "id": "participant-1", "role": "delegated", "name": "breeze(peer-cli-smoke)"},
			}}},
		},
	})

	model.syncViewportContent()
	labelLine, continuation := -1, -1
	for index, line := range model.viewportPlainLines {
		if strings.HasPrefix(line, "• peer-cli-") {
			labelLine, continuation = index, index+1
			break
		}
	}
	if labelLine < 0 || continuation+1 >= len(model.viewportPlainLines) {
		t.Fatalf("wrapped label lines missing: %#v", model.viewportPlainLines)
	}
	labelStartLine := model.viewportPlainLines[labelLine]
	labelTailLine := model.viewportPlainLines[continuation]
	bodyLine := model.viewportPlainLines[continuation+1]
	if !strings.HasPrefix(labelTailLine, "  ") || !strings.Contains(labelTailLine, "[breeze]") {
		t.Fatalf("label continuation line = %q, want the rest of the peer label", labelTailLine)
	}
	if !strings.HasPrefix(bodyLine, "  ") || strings.Contains(bodyLine, "breeze") {
		t.Fatalf("body line = %q, want text past the peer label", bodyLine)
	}

	linkToken := subagentOutputOverlayClickToken("spawn-1")
	if strings.Contains(labelStartLine, ":") {
		t.Fatalf("label line = %q, want the label split across lines", labelStartLine)
	}
	labelTailEnd := agentMessageGutterColumns + displayColumns(strings.Split(strings.TrimPrefix(labelTailLine, "  "), ":")[0])
	spans := []struct {
		line int
		end  int
	}{
		{labelLine, displayColumns(labelStartLine)},
		{continuation, labelTailEnd},
	}
	for _, span := range spans {
		if token := model.viewportClickTokens[span.line]; token != linkToken {
			t.Fatalf("line %d token = %q, want the workspace link %q", span.line, token, linkToken)
		}
		if token := model.viewportClickAltTokens[span.line]; !strings.HasPrefix(token, agentMessageFoldTokenPrefix) {
			t.Fatalf("line %d body token = %q, want in-place fold", span.line, token)
		}
		if bounds := model.viewportClickBounds[span.line]; !bounds.valid() || bounds.start != agentMessageGutterColumns || bounds.end != span.end {
			t.Fatalf("line %d label span = %#v, want [%d,%d)", span.line, bounds, agentMessageGutterColumns, span.end)
		}
	}
	if token := model.viewportClickTokens[continuation+1]; !strings.HasPrefix(token, agentMessageFoldTokenPrefix) {
		t.Fatalf("line past the label token = %q, want the body action", token)
	}

	// The second physical label line opens the source workspace...
	clickViewportColumn(t, model, continuation, agentMessageGutterColumns)
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-1" {
		t.Fatalf("label continuation click did not open the source overlay (%q -> %q): %#v", labelStartLine, labelTailLine, model.subagentOutputOverlay)
	}
	model.subagentOutputOverlay = nil

	// ...and the body lines past the label still fold the message instead.
	clickViewportColumn(t, model, continuation+1, agentMessageGutterColumns+2)
	plain := strings.Join(model.viewportPlainLines, "\n")
	if model.subagentOutputOverlay != nil {
		t.Fatalf("body click opened a workspace: %#v", model.subagentOutputOverlay)
	}
	if !strings.Contains(plain, "middle-marker") {
		t.Fatalf("body click did not expand the message:\n%s", plain)
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
	_, _ = model.applyTranscriptEvent(base, false)
	_, _ = model.applyTranscriptEvent(TranscriptEvent{
		Kind: TranscriptEventAgentCommunication, Scope: ACPProjectionMain, TurnID: "turn-1",
		Actor: "reviewer", AgentSourceName: "reviewer", AgentSourceID: "reviewer-1", Text: "review complete",
	}, false)
	base.MessageID = "assistant-2"
	base.Text = "after"
	_, _ = model.applyTranscriptEvent(base, false)

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

func TestWrappedAgentMessageRowsKeepOneLabelTargetAndBodyAction(t *testing.T) {
	t.Parallel()

	ctx := BlockRenderContext{Width: 40, TermWidth: 40}
	bodyToken := acpToolPanelClickToken("message-1")
	linkToken := agentMessageTargetOverlayClickToken("message-1")
	row := renderSendMessageHeaderRow(
		"block",
		"@ziva[breeze]: "+strings.Repeat("payload ", 8),
		ctx,
		bodyToken,
		linkToken,
		acpHeaderMarkDefault,
		false,
	)
	rows := wrapAgentMessageRows(row, 40)
	if len(rows) < 2 {
		t.Fatalf("wrapped rows = %d, want a continuation line", len(rows))
	}
	first := rows[0]
	if first.ClickToken != linkToken || first.ClickTokenAlt != bodyToken {
		t.Fatalf("labeled row tokens = %q/%q, want %q/%q", first.ClickToken, first.ClickTokenAlt, linkToken, bodyToken)
	}
	if first.ClickStartCol != agentMessageGutterColumns || first.ClickEndCol != displayColumns("• @ziva[breeze]") {
		t.Fatalf("labeled row span = [%d,%d)", first.ClickStartCol, first.ClickEndCol)
	}
	for i, next := range rows[1:] {
		if next.ClickToken != bodyToken || next.ClickTokenAlt != "" || next.ClickStartCol != 0 || next.ClickEndCol != 0 {
			t.Fatalf("continuation row %d = %q/%q span [%d,%d)", i+1, next.ClickToken, next.ClickTokenAlt, next.ClickStartCol, next.ClickEndCol)
		}
	}
}

func TestAgentCommunicationHeaderUsesThemeHandleAndNormalBody(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: theme}
	row := renderAgentMessageRow("block", "kian[breeze]", "compact message", ctx, "")
	if got := ansiTextForForeground(t, row.Styled, ctx.Theme.AgentMessageReceivedFg); !strings.Contains(got, "kian") {
		t.Fatalf("Agent source did not receive incoming styling: %q", row.Styled)
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
