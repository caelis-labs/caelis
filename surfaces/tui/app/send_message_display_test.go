package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestSendMessageToolAppearsAfterSuccessAndOpensOverlay(t *testing.T) {
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
			RawOutput: map[string]any{"handle": "ziva", "state": "running"}, Meta: acpToolNameMeta("StartThread"),
		},
	})
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "ziva"
	view.title = "ziva[breeze]: delegated messaging exercise"
	message := "start " + strings.Repeat("payload ", 18) + "middle-marker " + strings.Repeat("tail ", 18)
	rawInput := map[string]any{"to": "ziva", "message": message}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1",
		Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "message-1",
			Title: `Ran SendMessage {"message":"raw-json-should-not-render","to":"parent"}`,
			Kind:  eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: rawInput, Meta: acpToolNameMeta("SendMessage"),
		},
	})

	block := requireMainACPTurnBlockForTest(t, model)
	var event SubagentEvent
	for _, candidate := range block.Events {
		if candidate.CallID == "message-1" && !candidate.Done {
			event = candidate
			break
		}
	}
	if event.CallID == "" {
		t.Fatalf("SendMessage tool event missing: %#v", block.Events)
	}
	if !strings.HasPrefix(event.Args, "@ziva[breeze]: start ") || strings.Contains(event.Args, "raw-json-should-not-render") {
		t.Fatalf("SendMessage preview = %q", event.Args)
	}
	if event.FullArgs != "@ziva[breeze]: "+strings.TrimSpace(message) {
		t.Fatalf("SendMessage full args = %q", event.FullArgs)
	}
	if event.MessageTarget != "@ziva" {
		t.Fatalf("SendMessage structured target = %q", event.MessageTarget)
	}
	completed := eventstream.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "message-1", Status: &completed,
			RawOutput: map[string]any{"id": "mail-1", "from": "parent", "to": "ziva", "message": message},
			Content:   []eventstream.ToolCallContent{{Type: "content", Content: eventstream.TextContent{Type: "text", Text: `{"id":"mail-1","from":"parent","to":"ziva","message":"receipt-must-not-render"}`}}}, Meta: acpToolNameMeta("SendMessage"),
		},
	})

	model.syncViewportContent()
	headerLine := -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• @ziva[breeze]:") {
			headerLine = index
			break
		}
	}
	if headerLine < 0 {
		t.Fatalf("semantic SendMessage header missing: %#v", model.viewportPlainLines)
	}
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, `Ran SendMessage`) || strings.Contains(plain, `"message"`) || strings.Contains(plain, "middle-marker") || strings.Contains(plain, "receipt-must-not-render") {
		t.Fatalf("collapsed SendMessage leaked raw/full input:\n%s", plain)
	}
	labelEnd := displayColumns("• @ziva[breeze]")
	if token := model.viewportClickTokens[headerLine]; token != agentMessageTargetOverlayClickToken("message-1") {
		t.Fatalf("SendMessage recipient-label token = %q", token)
	}
	if bounds := model.viewportClickBounds[headerLine]; !bounds.valid() || bounds.start != 0 || bounds.end != labelEnd {
		t.Fatalf("SendMessage recipient-label span = %#v, want [0,%d)", bounds, labelEnd)
	}
	if token := model.viewportClickAltTokens[headerLine]; token != acpToolPanelClickToken("message-1") {
		t.Fatalf("SendMessage body token = %q, want send panel toggle", token)
	}

	clickViewportLine(t, model, headerLine)
	plain = strings.Join(model.viewportPlainLines, "\n")
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-1" {
		t.Fatalf("SendMessage row did not open its target overlay: %#v", model.subagentOutputOverlay)
	}
	if strings.Contains(plain, "middle-marker") {
		t.Fatalf("SendMessage recipient-label click expanded the hidden message:\n%s", plain)
	}
	model.subagentOutputOverlay = nil

	// The body keeps the send panel's own expansion instead of navigating away.
	clickViewportColumn(t, model, headerLine, labelEnd+4)
	plain = strings.Join(model.viewportPlainLines, "\n")
	if model.subagentOutputOverlay != nil {
		t.Fatalf("SendMessage body click opened a workspace: %#v", model.subagentOutputOverlay)
	}
	if !strings.Contains(plain, "middle-marker") {
		t.Fatalf("SendMessage body click did not expand the hidden message:\n%s", plain)
	}
	clickViewportColumn(t, model, headerLine, labelEnd+4)
	if plain = strings.Join(model.viewportPlainLines, "\n"); strings.Contains(plain, "middle-marker") {
		t.Fatalf("SendMessage body click did not collapse the message:\n%s", plain)
	}

	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-2",
		Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "message-2",
			Title: "SendMessage across a later Turn", Kind: eventstream.ToolKindExecute,
			Status:   eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"to": "ziva", "message": "second Turn update"},
			Meta:     acpToolNameMeta("SendMessage"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-2", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "message-2", Status: &completed,
			RawOutput: map[string]any{"accepted": true}, Meta: acpToolNameMeta("SendMessage"),
		},
	})
	model.syncViewportContent()
	secondHeaderLine := -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• @ziva[breeze]:") && strings.Contains(line, "second Turn update") {
			secondHeaderLine = index
			break
		}
	}
	if secondHeaderLine < 0 {
		t.Fatalf("later-Turn SendMessage header missing: %#v", model.viewportPlainLines)
	}
	if token := model.viewportClickTokens[secondHeaderLine]; token != agentMessageTargetOverlayClickToken("message-2") {
		t.Fatalf("later-Turn SendMessage click token = %q", token)
	}
	clickViewportLine(t, model, secondHeaderLine)
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-1" {
		t.Fatalf("later-Turn SendMessage row lost stable Spawn owner: %#v", model.subagentOutputOverlay)
	}
}

// A long SendMessage header wraps once it expands. Its continuation lines carry
// no label span, so clicking them must collapse the send panel instead of
// opening the recipient's workspace.
func TestExpandedSendMessageContinuationClickCollapsesInsteadOfNavigating(t *testing.T) {
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
			RawOutput: map[string]any{"handle": "ziva", "state": "running"}, Meta: acpToolNameMeta("StartThread"),
		},
	})
	view := model.ensureSubagentOutputView("spawn-1")
	view.taskHandle = "ziva"
	message := "start " + strings.Repeat("payload ", 18) + "middle-marker " + strings.Repeat("tail ", 18)
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "message-1", Title: "SendMessage",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"to": "ziva", "message": message}, Meta: acpToolNameMeta("SendMessage"),
		},
	})
	completed := eventstream.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "message-1", Status: &completed,
			RawOutput: map[string]any{"accepted": true}, Meta: acpToolNameMeta("SendMessage"),
		},
	})

	model.syncViewportContent()
	labelEnd := displayColumns("• @ziva[breeze]")
	headerLine := -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• @ziva[breeze]:") {
			headerLine = index
			break
		}
	}
	if headerLine < 0 {
		t.Fatalf("SendMessage header missing: %#v", model.viewportPlainLines)
	}

	clickViewportColumn(t, model, headerLine, labelEnd+4)
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, "middle-marker") {
		t.Fatalf("SendMessage body click did not expand the hidden message:\n%s", plain)
	}

	continuation := headerLine + 1
	if continuation >= len(model.viewportPlainLines) {
		t.Fatalf("expanded SendMessage header did not wrap: %#v", model.viewportPlainLines)
	}
	if line := model.viewportPlainLines[continuation]; strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "• ") {
		t.Fatalf("continuation line %d = %q, want wrapped body text", continuation, line)
	}
	bodyToken := acpToolPanelClickToken("message-1")
	if token := model.viewportClickTokens[continuation]; token != bodyToken {
		t.Fatalf("continuation token = %q, want the body action %q", token, bodyToken)
	}
	if alt := model.viewportClickAltTokens[continuation]; alt != "" {
		t.Fatalf("continuation kept a second target: %q", alt)
	}
	if bounds := model.viewportClickBounds[continuation]; bounds.valid() {
		t.Fatalf("continuation kept a label span: %#v", bounds)
	}

	clickViewportColumn(t, model, continuation, 4)
	if model.subagentOutputOverlay != nil {
		t.Fatalf("continuation click opened a workspace: %#v", model.subagentOutputOverlay)
	}
	if plain := strings.Join(model.viewportPlainLines, "\n"); strings.Contains(plain, "middle-marker") {
		t.Fatalf("continuation click did not collapse the message:\n%s", plain)
	}
}

func TestSendMessageReplayBatchResolvesEarlierSpawnTarget(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 120
	model.height = 40
	model.currentSessionID = "session-1"
	running := eventstream.ToolStatusInProgress
	envelopes := []eventstream.Envelope{
		{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
			Update: eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "spawn-1", Title: "Spawn breeze",
				Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
				RawInput: map[string]any{"agent": "breeze", "prompt": "delegated messaging exercise"}, Meta: acpToolNameMeta("StartThread"),
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
				RawOutput: map[string]any{"handle": "ziva", "state": "running"}, Meta: acpToolNameMeta("StartThread"),
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
			Update: eventstream.ToolCall{
				SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "message-1", Title: "SendMessage after Spawn",
				Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
				RawInput: map[string]any{"to": "ziva", "message": "continue from the replay batch"}, Meta: acpToolNameMeta("SendMessage"),
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "message-1", Status: func() *string { value := eventstream.ToolStatusCompleted; return &value }(),
				RawOutput: map[string]any{"accepted": true}, Meta: acpToolNameMeta("SendMessage"),
			},
		},
	}
	events := make([]TranscriptEvent, 0, len(envelopes))
	for _, envelope := range envelopes {
		presentation := model.projectACPEnvelopePresentation(envelope)
		events = append(events, presentation.Events...)
	}
	next, _ := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: events})
	model = next.(*Model)

	block := requireMainACPTurnBlockForTest(t, model)
	var target string
	for _, event := range block.Events {
		if event.Kind == SEToolCall && event.CallID == "message-1" {
			target = event.MessageTarget
			break
		}
	}
	if target != "@ziva" {
		t.Fatalf("replayed SendMessage target = %q, want @ziva", target)
	}
	if callID := model.subagentOutputCallIDForHandle(target); callID != "spawn-1" {
		t.Fatalf("replayed SendMessage owner = %q, want spawn-1", callID)
	}
	model.syncViewportContent()
	headerLine := -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• @ziva[breeze]:") && strings.Contains(line, "continue from the replay batch") {
			headerLine = index
			break
		}
	}
	if headerLine < 0 {
		t.Fatalf("replayed SendMessage header missing: %#v", model.viewportPlainLines)
	}
	if token := model.viewportClickTokens[headerLine]; token != agentMessageTargetOverlayClickToken("message-1") {
		t.Fatalf("replayed SendMessage click token = %q", token)
	}
}

func TestSendMessageSuccessRendersSingleLineWithoutDispatchAck(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 120
	model.height = 40
	model.currentSessionID = "session-1"
	completed := eventstream.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "message-1",
			Title: "SendMessage", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"to": "parent", "message": "status update for parent"},
			Meta:     acpToolNameMeta("SendMessage"),
		},
	})
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "message-1", Status: &completed,
			Content: []eventstream.ToolCallContent{{
				Type: "content", Content: eventstream.TextContent{Type: "text", Text: `{"id":"mail-1","status":"queued"}`},
			}},
			RawOutput: map[string]any{"id": "mail-1", "status": "queued"},
			Meta:      acpToolNameMeta("SendMessage"),
		},
	})

	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "• @parent: status update for parent") {
		t.Fatalf("successful SendMessage header missing:\n%s", plain)
	}
	t.Log(plain)
	if strings.Contains(plain, "queued") || strings.Contains(plain, "mail-1") || strings.Contains(plain, "Message sent.") || strings.Contains(plain, "└") || strings.Contains(plain, "↗") {
		t.Fatalf("successful SendMessage kept delivery chrome:\n%s", plain)
	}
}

func TestSendMessageFailureDoesNotClaimDelivery(t *testing.T) {
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 120
	model.height = 40
	model.currentSessionID = "session-1"
	failed := eventstream.ToolStatusFailed
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: "message-1",
			Title: "SendMessage", Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"to": "orbit", "message": "validation recommendation"},
			Meta:     acpToolNameMeta("SendMessage"),
		},
	})
	model.syncViewportContent()
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, " · sending") {
		t.Fatalf("pending message lost its status:\n%s", plain)
	}
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "message-1", Status: &failed,
			Content: []eventstream.ToolCallContent{{
				Type: "content", Content: eventstream.TextContent{Type: "text", Text: "ACP Agent @orbit does not support additional messages while its current turn is running. You can send a message after this turn finishes."},
			}},
			RawOutput: map[string]any{
				"error": "ACP Agent @orbit does not support additional messages while its current turn is running. You can send a message after this turn finishes.", "error_code": "unsupported",
			},
			Meta: acpToolNameMeta("SendMessage"),
		},
	})

	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "@orbit: validation recommendation · failed") || !strings.Contains(plain, "does not support additional messages") {
		t.Fatalf("failed SendMessage lost status or reason:\n%s", plain)
	}
	if strings.Contains(plain, " · sending") {
		t.Fatalf("failed message retained pending state:\n%s", plain)
	}
}

func TestSendMessageHeaderUsesOutgoingTargetStyling(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: theme}
	row := renderSendMessageHeaderRow("block", "@ziva: compact message", ctx, "", "", acpHeaderMarkDefault, false)
	if got := ansiTextForForeground(t, row.Styled, ctx.Theme.AgentMessageSentFg); !strings.Contains(got, "@ziva") {
		t.Fatalf("SendMessage target did not receive outgoing styling: %q", row.Styled)
	}
	if got := ansiTextForForeground(t, row.Styled, ctx.Theme.TextStyle().GetForeground()); !strings.Contains(got, "compact message") {
		t.Fatalf("SendMessage body did not retain normal text styling: %q", row.Styled)
	}
}
