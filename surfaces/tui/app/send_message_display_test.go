package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestSendMessageToolRendersActionTargetAndOpensTargetFromWholeRow(t *testing.T) {
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
			RawOutput: map[string]any{"handle": "ziva", "state": "running"}, Meta: acpToolNameMeta("Spawn"),
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
	if !strings.HasPrefix(event.Args, "ziva[breeze]: start ") || strings.Contains(event.Args, "raw-json-should-not-render") {
		t.Fatalf("SendMessage preview = %q", event.Args)
	}
	if event.FullArgs != "ziva[breeze]: "+strings.TrimSpace(message) {
		t.Fatalf("SendMessage full args = %q", event.FullArgs)
	}
	if event.MessageTarget != "@ziva" {
		t.Fatalf("SendMessage structured target = %q", event.MessageTarget)
	}

	model.syncViewportContent()
	headerLine := -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• Sent ziva[breeze]:") {
			headerLine = index
			break
		}
	}
	if headerLine < 0 {
		t.Fatalf("semantic SendMessage header missing: %#v", model.viewportPlainLines)
	}
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, `Ran SendMessage`) || strings.Contains(plain, `"message"`) || strings.Contains(plain, "middle-marker") {
		t.Fatalf("collapsed SendMessage leaked raw/full input:\n%s", plain)
	}
	if token := model.viewportClickTokens[headerLine]; token != agentMessageTargetOverlayClickToken("message-1") {
		t.Fatalf("SendMessage header click token = %q", token)
	}
	if bounds := model.viewportClickBounds[headerLine]; bounds.valid() {
		t.Fatalf("SendMessage retained a bounded sub-target instead of whole-row navigation: %#v", bounds)
	}

	clickViewportLine(t, model, headerLine)
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != "spawn-1" {
		t.Fatalf("Sent row did not open Spawn overlay: %#v", model.subagentOutputOverlay)
	}
	plain = strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, "middle-marker") {
		t.Fatalf("Sent navigation also expanded the hidden message:\n%s", plain)
	}

	model.closeSubagentOutputOverlay()
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
	model.syncViewportContent()
	secondHeaderLine := -1
	for index, line := range model.viewportPlainLines {
		if strings.Contains(line, "• Sent ") && strings.Contains(line, "second Turn update") {
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
		t.Fatalf("later-Turn Sent row lost stable Spawn owner: %#v", model.subagentOutputOverlay)
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
				RawInput: map[string]any{"agent": "breeze", "prompt": "delegated messaging exercise"}, Meta: acpToolNameMeta("Spawn"),
			},
		},
		{
			Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
			Update: eventstream.ToolCallUpdate{
				SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "spawn-1", Status: &running,
				RawOutput: map[string]any{"handle": "ziva", "state": "running"}, Meta: acpToolNameMeta("Spawn"),
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
		if strings.Contains(line, "• Sent ") && strings.Contains(line, "continue from the replay batch") {
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
				Type: "content", Content: eventstream.TextContent{Type: "text", Text: "Message sent."},
			}},
			RawOutput: map[string]any{"accepted": true, "state": "delivered", "to": "parent"},
			Meta:      acpToolNameMeta("SendMessage"),
		},
	})

	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "• Sent ") || !strings.Contains(plain, "status update for parent") {
		t.Fatalf("successful SendMessage header missing:\n%s", plain)
	}
	if strings.Contains(plain, "Message sent.") || strings.Contains(plain, "└") || strings.Contains(plain, "↗") {
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
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: "turn-1", Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "message-1", Status: &failed,
			Content: []eventstream.ToolCallContent{{
				Type: "content", Content: eventstream.TextContent{Type: "text", Text: "ACP Agent @orbit lacks _session/steering; cancel turn, retry SendMessage."},
			}},
			RawOutput: map[string]any{
				"error": "ACP Agent @orbit lacks _session/steering; cancel turn, retry SendMessage.", "error_code": "unsupported",
			},
			Meta: acpToolNameMeta("SendMessage"),
		},
	})

	model.syncViewportContent()
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "• Failed to send orbit: validation recommendation") ||
		!strings.Contains(plain, "ACP Agent @orbit lacks _session/steering; cancel turn, retry SendMessage.") {
		t.Fatalf("failed SendMessage presentation mismatch:\n%s", plain)
	}
	if strings.Contains(plain, "• Sent ") || strings.Contains(plain, "internal/acpagentbridge") {
		t.Fatalf("failed SendMessage claimed delivery or leaked an implementation path:\n%s", plain)
	}
}

func TestSentHeaderUsesSpawnTargetStyling(t *testing.T) {
	model := NewModel(Config{})
	ctx := model.blockRenderContext(100)
	detail := "ziva[breeze]: compact message"
	got := styleACPTranscriptHeaderDetail(ctx, "Sent", detail)
	want := styleSpawnedHeaderDetail(ctx, detail)
	if got != want {
		t.Fatalf("Sent target styling = %q, want Spawn-style %q", got, want)
	}
	if plain := ctx.Theme.ToolArgsStyle().Render(detail); got == plain {
		t.Fatalf("Sent target did not receive focus styling: %q", got)
	}
}
