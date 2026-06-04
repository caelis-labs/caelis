package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/internal/evalharness"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestRegressionResize80to120to80Golden(t *testing.T) {
	m := NewModel(Config{
		AppName:         "CAELIS",
		Version:         "dev",
		Workspace:       "/tmp/workspace",
		ModelAlias:      "minimax/MiniMax-M1",
		ShowWelcomeCard: true,
		Commands:        DefaultCommands(),
		Wizards:         DefaultWizards(),
		NoColor:         true,
	})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*Model)
	frame80 := evalharness.NormalizeFrame(m.View().Content)
	lines80 := strings.Split(frame80, "\n")
	if len(lines80) != 23 {
		t.Fatalf("80x24 frame line count = %d, want 23", len(lines80))
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m = updated.(*Model)
	frame120 := evalharness.NormalizeFrame(m.View().Content)
	lines120 := strings.Split(frame120, "\n")
	if len(lines120) != 31 {
		t.Fatalf("120x32 frame line count = %d, want 31", len(lines120))
	}

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*Model)
	frame80b := evalharness.NormalizeFrame(m.View().Content)
	if frame80 != frame80b {
		t.Fatalf("resize 80→120→80 did not restore original frame\noriginal:\n%s\n\nrestored:\n%s", frame80, frame80b)
	}
}

func TestRegressionNoWelcomeCardGolden(t *testing.T) {
	m := NewModel(Config{
		AppName:    "CAELIS",
		Version:    "dev",
		Workspace:  "/tmp/workspace",
		ModelAlias: "minimax/MiniMax-M1",
		Commands:   DefaultCommands(),
		Wizards:    DefaultWizards(),
		NoColor:    true,
	})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*Model)
	frame := evalharness.NormalizeFrame(m.View().Content)

	if strings.Contains(frame, "CAELIS (vdev)") {
		t.Fatal("frame should not contain welcome card when ShowWelcomeCard=false")
	}
}

func TestRegressionToolCallWithTerminalOutputGolden(t *testing.T) {
	m := NewModel(Config{
		AppName:    "CAELIS",
		Version:    "dev",
		Workspace:  "/tmp/workspace",
		ModelAlias: "minimax/MiniMax-M1",
		Commands:   DefaultCommands(),
		Wizards:    DefaultWizards(),
		NoColor:    true,
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(*Model)

	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		SessionRef: session.SessionRef{SessionID: "sess-terminal"},
		Kind:       gateway.EventKindUserMessage,
		Narrative: &gateway.NarrativePayload{
			Role: gateway.NarrativeRoleUser,
			Text: "run ls",
		},
	}}))

	m = updated.(*Model)

	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		SessionRef: session.SessionRef{SessionID: "sess-terminal"},
		Kind:       gateway.EventKindToolCall,
		ToolCall: &gateway.ToolCallPayload{
			CallID:    "call-ls",
			ToolName:  "RUN_COMMAND",
			ToolKind:  "execute",
			ToolTitle: "ls -la",
			Status:    gateway.ToolStatusRunning,
			RawInput:  map[string]any{"command": "ls -la"},
			Content: []session.ProtocolToolCallContent{{
				Type:    "terminal",
				Content: "total 0\ndrwxr-xr-x  2 user user  40 Jan  1 00:00 .\n",
			}},
		},
	}}))

	m = updated.(*Model)

	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		SessionRef: session.SessionRef{SessionID: "sess-terminal"},
		Kind:       gateway.EventKindToolResult,
		ToolResult: &gateway.ToolResultPayload{
			CallID:    "call-ls",
			ToolName:  "RUN_COMMAND",
			ToolKind:  "execute",
			ToolTitle: "ls -la",
			Status:    gateway.ToolStatusCompleted,
			RawInput:  map[string]any{"command": "ls -la"},
			RawOutput: map[string]any{"exit_code": 0},
			Content: []session.ProtocolToolCallContent{{
				Type:    "terminal",
				Content: "total 0\ndrwxr-xr-x  2 user user  40 Jan  1 00:00 .\ntotal 4\ndrwxr-xr-x  3 user user  60 Jan  1 00:00 ..\n",
			}},
		},
	}}))

	m = updated.(*Model)

	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		SessionRef: session.SessionRef{SessionID: "sess-terminal"},
		Kind:       gateway.EventKindAssistantMessage,
		Narrative: &gateway.NarrativePayload{
			Role: gateway.NarrativeRoleAssistant,
			Text: "Listed directory contents.",
		},
	}}))

	m = updated.(*Model)

	frame := evalharness.NormalizeFrame(m.View().Content)
	lines := strings.Split(frame, "\n")
	if got := len(lines); got != 29 {
		t.Fatalf("terminal output 100x30 line count = %d, want 29", got)
	}
	wantContains := []string{
		"  ▌",
		"  ▌ run ls",
		"  • Ran ls -la",
		"   /tmp/workspace",
		"not configured",
		"   \u003e Type your message, @agent, #path/to/file, or $skill",
	}
	for _, want := range wantContains {
		if !strings.Contains(frame, want) {
			t.Fatalf("terminal output frame missing %q", want)
		}
	}
}

func TestRegressionFollowTailAfterScrollGolden(t *testing.T) {
	m := NewModel(Config{
		AppName:    "CAELIS",
		Version:    "dev",
		Workspace:  "/tmp/workspace",
		ModelAlias: "minimax/MiniMax-M1",
		Commands:   DefaultCommands(),
		Wizards:    DefaultWizards(),
		NoColor:    true,
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*Model)

	seedLongTranscript(m, 50)
	m.syncViewportContent()

	if m.viewportFollowState != viewportFollowTail {
		t.Fatalf("initial follow state = %v, want follow tail", m.viewportFollowState)
	}

	m.viewport.SetYOffset(10)
	m.setViewportFollowState(viewportPinnedHistory)

	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	m = updated.(*Model)

	if m.viewportFollowState != viewportFollowTail {
		t.Fatalf("after End key: follow state = %v, want follow tail", m.viewportFollowState)
	}
	if !m.viewport.AtBottom() {
		t.Fatalf("after End key: viewport not at bottom, y offset = %d", m.viewport.YOffset())
	}
}

func TestRegressionSlashCommandCompletionGolden(t *testing.T) {
	m := NewModel(Config{
		AppName:    "CAELIS",
		Version:    "dev",
		Workspace:  "/tmp/workspace",
		ModelAlias: "minimax/MiniMax-M1",
		Commands:   []string{"status", "model", "connect", "resume", "help"},
		Wizards:    DefaultWizards(),
		NoColor:    true,
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(*Model)

	for _, ch := range "/st" {
		updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: string(ch)}))
		m = updated.(*Model)
	}

	frame := evalharness.NormalizeFrame(m.View().Content)
	assertRegressionFrame(t, "slash command /st", frame, 23, []string{
		"   /tmp/workspace                                              not configured",
		strings.Repeat("─", 80),
		"   \u003e /st",
		strings.Repeat("─", 80),
	})
}

func TestRegressionApprovalModalGolden(t *testing.T) {
	m := NewModel(Config{
		AppName:    "CAELIS",
		Version:    "dev",
		Workspace:  "/tmp/workspace",
		ModelAlias: "minimax/MiniMax-M1",
		Commands:   DefaultCommands(),
		Wizards:    DefaultWizards(),
		NoColor:    true,
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(*Model)

	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		SessionRef: session.SessionRef{SessionID: "sess-approval"},
		Kind:       gateway.EventKindUserMessage,
		Narrative: &gateway.NarrativePayload{
			Role: gateway.NarrativeRoleUser,
			Text: "delete temp files",
		},
	}}))

	m = updated.(*Model)

	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		SessionRef: session.SessionRef{SessionID: "sess-approval"},
		Kind:       gateway.EventKindToolCall,
		ToolCall: &gateway.ToolCallPayload{
			CallID:    "call-rm",
			ToolName:  "RUN_COMMAND",
			ToolKind:  "execute",
			ToolTitle: "rm -rf /tmp/demo",
			Status:    gateway.ToolStatusWaitingApproval,
			RawInput:  map[string]any{"command": "rm -rf /tmp/demo"},
		},
	}}))

	m = updated.(*Model)

	updated, _ = m.Update(gatewayEventMsg(gateway.EventEnvelope{Event: gateway.Event{
		SessionRef: session.SessionRef{SessionID: "sess-approval"},
		Kind:       gateway.EventKindApprovalRequested,
		ApprovalPayload: &gateway.ApprovalPayload{
			ToolName:   "RUN_COMMAND",
			ToolCallID: "call-rm",
			Status:     gateway.ApprovalStatusPending,
			RawInput:   map[string]any{"command": "rm -rf /tmp/demo"},
		},
	}}))

	m = updated.(*Model)

	frame := evalharness.NormalizeFrame(m.View().Content)
	lines := strings.Split(frame, "\n")
	if got := len(lines); got != 29 {
		t.Fatalf("approval modal line count = %d, want 29", got)
	}
	wantContains := []string{
		"  ▌",
		"  ▌ delete temp files",
		"  • Ran rm -rf /tmp/demo",
		"   /tmp/workspace",
		"not configured",
		"   \u003e Type your message, @agent, #path/to/file, or $skill",
	}
	for _, want := range wantContains {
		if !strings.Contains(frame, want) {
			t.Fatalf("approval modal frame missing %q", want)
		}
	}
}
