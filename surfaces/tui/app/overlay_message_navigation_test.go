package tuiapp

import (
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestShortAndLongSpawnRowsOpenOverlayInsteadOfFolding(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		prompt string
		hidden string
		needle string
		callID string
		handle string
	}{
		{
			name:   "short",
			prompt: "inspect the workspace",
			needle: "Spawned",
			callID: "spawn-short",
			handle: "ziva",
		},
		{
			name:   "long",
			prompt: overlayNavLongText("spawn-marker"),
			hidden: "spawn-marker",
			needle: "Spawned",
			callID: "spawn-long",
			handle: "ziva",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := overlayNavModel(t)
			model = applyOverlayNavSpawn(t, model, "turn-1", tc.callID, tc.handle, tc.prompt)
			block := requireMainACPTurnBlockForTest(t, model)
			row := requireRenderedRowContaining(t, overlayNavRows(t, model, block), tc.needle)
			if row.ClickToken != subagentOutputOverlayClickToken(tc.callID) {
				t.Fatalf("Spawn click token = %q, want overlay link", row.ClickToken)
			}
			assertClickOpensOverlay(t, model, block, row, tc.callID, tc.hidden)
		})
	}
}

func TestSendMessageToChildOpensOverlayInsteadOfFolding(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		message string
		hidden  string
	}{
		{name: "short", message: "continue from the parent"},
		{name: "long", message: overlayNavLongText("send-marker"), hidden: "send-marker"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			model := overlayNavModel(t)
			model = applyOverlayNavSpawn(t, model, "turn-1", "spawn-1", "ziva", "delegated messaging exercise")
			model = applyOverlayNavSendMessage(t, model, "turn-1", "message-1", "ziva", tc.message)
			block := requireMainACPTurnBlockForTest(t, model)
			row := requireRenderedRowContaining(t, overlayNavRows(t, model, block), "• @ziva")
			if row.ClickToken != agentMessageTargetOverlayClickToken("message-1") {
				t.Fatalf("SendMessage click token = %q, want child overlay link", row.ClickToken)
			}
			assertClickOpensOverlay(t, model, block, row, "spawn-1", tc.hidden)
		})
	}
}

func TestLaterTurnAgentCommunicationOpensRetainedOwnerOverlay(t *testing.T) {
	t.Parallel()

	model := overlayNavModel(t)
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(1, 0))
	model = applyOverlayNavSpawn(t, model, "turn-1", "spawn-1", "ziva", "delegated messaging exercise")
	view := requireSubagentOutputViewForTest(t, model, "spawn-1")
	view.participantID = "participant-1"
	spawnBlock := requireMainACPTurnBlockForTest(t, model)

	model = applyACPEnvelopeForTest(t, model, overlayNavTurnCompleted("session-1", "turn-1", time.Unix(2, 0)))
	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(3, 0))
	message := overlayNavLongText("received-marker")
	model = applyOverlayNavAgentCommunication(t, model, "turn-2", "participant-1", "breeze(ziva)", "agent-message-1", message)

	blocks := mainACPTurnBlocksForTest(model)
	if len(blocks) < 2 {
		t.Fatalf("main blocks = %d, want spawn owner and a later Turn", len(blocks))
	}
	later := blocks[len(blocks)-1]
	if later.BlockID() == spawnBlock.BlockID() {
		t.Fatal("later-Turn communication stayed on the Spawn owner block")
	}
	if _, ok := model.subagentOutputOwner(later.BlockID(), "spawn-1"); ok {
		t.Fatal("later-Turn block still reports the Spawn owner")
	}

	row := requireRenderedRowContaining(t, overlayNavRows(t, model, later), "• ziva")
	if row.ClickToken != subagentOutputOverlayClickToken("spawn-1") {
		t.Fatalf("later-Turn Agent communication click token = %q, want retained Spawn overlay", row.ClickToken)
	}
	assertClickOpensOverlay(t, model, later, row, "spawn-1", "received-marker")
}

func TestUnresolvedAndParentTargetsDoNotCreateOverlayLinks(t *testing.T) {
	t.Parallel()

	t.Run("parent send message", func(t *testing.T) {
		t.Parallel()
		model := overlayNavModel(t)
		model = applyOverlayNavSendMessage(t, model, "turn-1", "message-parent", "parent", overlayNavLongText("parent-marker"))
		block := requireMainACPTurnBlockForTest(t, model)
		row := requireRenderedRowContaining(t, overlayNavRows(t, model, block), "• @parent:")
		if strings.HasPrefix(row.ClickToken, subagentOutputOverlayTokenPrefix) ||
			strings.HasPrefix(row.ClickToken, agentMessageTargetOverlayTokenPrefix) {
			t.Fatalf("parent SendMessage manufactured a dead overlay link: %q", row.ClickToken)
		}
		if model.tryToggleFoldToken(block.BlockID(), agentMessageTargetOverlayClickToken("message-parent")) {
			t.Fatal("parent SendMessage overlay token opened a workspace")
		}
		if model.subagentOutputOverlay != nil {
			t.Fatalf("parent SendMessage opened overlay %#v", model.subagentOutputOverlay)
		}
	})

	t.Run("unresolved send message", func(t *testing.T) {
		t.Parallel()
		model := overlayNavModel(t)
		model = applyOverlayNavSendMessage(t, model, "turn-1", "message-missing", "orbit", "status update")
		block := requireMainACPTurnBlockForTest(t, model)
		row := requireRenderedRowContaining(t, overlayNavRows(t, model, block), "• @orbit:")
		if strings.HasPrefix(row.ClickToken, subagentOutputOverlayTokenPrefix) ||
			strings.HasPrefix(row.ClickToken, agentMessageTargetOverlayTokenPrefix) {
			t.Fatalf("unresolved SendMessage manufactured a dead overlay link: %q", row.ClickToken)
		}
		if model.tryToggleFoldToken(block.BlockID(), agentMessageTargetOverlayClickToken("message-missing")) {
			t.Fatal("unresolved SendMessage overlay token opened a workspace")
		}
	})

	t.Run("unmatched agent communication", func(t *testing.T) {
		t.Parallel()
		model := overlayNavModel(t)
		model = applyOverlayNavAgentCommunication(t, model, "turn-1", "participant-unknown", "breeze(orbit)", "agent-message-unknown", overlayNavLongText("unmatched-marker"))
		block := requireMainACPTurnBlockForTest(t, model)
		row := requireRenderedRowContaining(t, overlayNavRows(t, model, block), "• orbit[breeze]:")
		if strings.HasPrefix(row.ClickToken, subagentOutputOverlayTokenPrefix) ||
			strings.HasPrefix(row.ClickToken, agentMessageTargetOverlayTokenPrefix) {
			t.Fatalf("unmatched Agent communication manufactured a dead overlay link: %q", row.ClickToken)
		}
		if model.tryToggleFoldToken(block.BlockID(), subagentOutputOverlayClickToken("spawn-missing")) {
			t.Fatal("unmatched Agent communication overlay token opened a workspace")
		}
	})
}

func overlayNavModel(t *testing.T) *Model {
	t.Helper()
	model := NewModel(Config{NoColor: true, NoAnimation: true})
	model.width = 120
	model.height = 40
	model.currentSessionID = "session-1"
	return model
}

func overlayNavLongText(marker string) string {
	return "start " + strings.Repeat("payload ", 18) + marker + " " + strings.Repeat("tail ", 18)
}

func applyOverlayNavSpawn(t *testing.T, model *Model, turnID, callID, handle, prompt string) *Model {
	t.Helper()
	running := eventstream.ToolStatusInProgress
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID, Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: callID, Title: "Spawn breeze",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"agent": "breeze", "prompt": prompt}, Meta: acpToolNameMeta("Spawn"),
		},
	})
	return applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID, Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: callID, Status: &running,
			RawOutput: map[string]any{"handle": handle, "state": "running"}, Meta: acpToolNameMeta("Spawn"),
		},
	})
}

func applyOverlayNavSendMessage(t *testing.T, model *Model, turnID, callID, to, message string) *Model {
	t.Helper()
	completed := eventstream.ToolStatusCompleted
	model = applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID, Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCall{
			SessionUpdate: eventstream.UpdateToolCall, ToolCallID: callID, Title: "SendMessage",
			Kind: eventstream.ToolKindExecute, Status: eventstream.ToolStatusInProgress,
			RawInput: map[string]any{"to": to, "message": message}, Meta: acpToolNameMeta("SendMessage"),
		},
	})
	return applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID, Scope: eventstream.ScopeMain,
		Update: eventstream.ToolCallUpdate{
			SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: callID, Status: &completed,
			RawOutput: map[string]any{"accepted": true}, Meta: acpToolNameMeta("SendMessage"),
		},
	})
}

func applyOverlayNavAgentCommunication(t *testing.T, model *Model, turnID, sourceID, sourceName, messageID, text string) *Model {
	t.Helper()
	return applyACPEnvelopeForTest(t, model, eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate, SessionID: "session-1", TurnID: turnID, Scope: eventstream.ScopeMain,
		AgentCommunicationSource: &eventstream.ActorIdentity{Kind: "participant", ID: sourceID, Role: "delegated", Name: sourceName},
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateUserMessage,
			Content:       eventstream.TextContent{Type: "text", Text: text},
			MessageID:     messageID,
			Meta: map[string]any{"caelis": map[string]any{"agent_communication": map[string]any{
				"source": map[string]any{"kind": "participant", "id": sourceID, "role": "delegated", "name": sourceName},
			}}},
		},
	})
}

func overlayNavTurnCompleted(sessionID, turnID string, at time.Time) eventstream.Envelope {
	env := eventstream.TurnCompleted("", "", turnID, at)
	env.SessionID = sessionID
	env.ScopeID = sessionID
	return env
}

func overlayNavRows(t *testing.T, model *Model, block *MainACPTurnBlock) []RenderedRow {
	t.Helper()
	if model == nil || block == nil {
		t.Fatal("overlay navigation render is missing a model or block")
	}
	return block.Render(model.blockRenderContext(120))
}

func requireRenderedRowContaining(t *testing.T, rows []RenderedRow, needle string) RenderedRow {
	t.Helper()
	for _, row := range rows {
		if strings.Contains(row.Plain, needle) {
			return row
		}
	}
	t.Fatalf("row containing %q missing:\n%s", needle, renderedRowsPlain(rows))
	return RenderedRow{}
}

func assertClickOpensOverlay(t *testing.T, model *Model, block *MainACPTurnBlock, row RenderedRow, wantCallID, hidden string) {
	t.Helper()
	token := strings.TrimSpace(row.ClickToken)
	if token == "" {
		t.Fatal("click token is empty")
	}
	if strings.HasPrefix(token, agentMessageFoldTokenPrefix) || strings.HasPrefix(token, "acp_tool_panel:") {
		t.Fatalf("fold token stole overlay navigation: %q", token)
	}
	if hidden != "" && strings.Contains(row.Plain, hidden) {
		t.Fatalf("compact row leaked hidden text %q: %q", hidden, row.Plain)
	}
	if !model.tryToggleFoldToken(block.BlockID(), token) {
		t.Fatalf("tryToggleFoldToken(%q) = false", token)
	}
	if model.subagentOutputOverlay == nil || model.subagentOutputOverlay.callID != wantCallID {
		t.Fatalf("overlay = %#v, want call %q", model.subagentOutputOverlay, wantCallID)
	}
	if hidden != "" && strings.Contains(renderedRowsPlain(overlayNavRows(t, model, block)), hidden) {
		t.Fatalf("click expanded the transcript instead of opening the overlay:\n%s", renderedRowsPlain(overlayNavRows(t, model, block)))
	}
}
