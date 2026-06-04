package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/internal/evalharness"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func TestRegressionWelcomeFrame80x24Golden(t *testing.T) {
	model := NewModel(Config{
		AppName:         "CAELIS",
		Version:         "dev",
		Workspace:       "/tmp/workspace",
		ModelAlias:      "minimax/MiniMax-M1",
		ShowWelcomeCard: true,
		Commands:        DefaultCommands(),
		Wizards:         DefaultWizards(),
		NoColor:         true,
	})

	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	frame := evalharness.NormalizeFrame(updated.View().Content)
	assertRegressionFrame(t, "welcome 80x24", frame, 23, []string{
		"  ╭──────────────────────────────────────────────────────────────────╮",
		"  │ >_ CAELIS (vdev)                                                 │",
		"  │                                                                  │",
		"  │ model:     minimax/MiniMax-M1                                    │",
		"  │ workspace: /tmp/workspace                                        │",
		"  │ tip:       type / for command list                               │",
		"  ╰──────────────────────────────────────────────────────────────────╯",
		"   /tmp/workspace                                              not configured",
		strings.Repeat("─", 80),
		"   > Type your message, @agent, #path/to/file, or $skill",
		strings.Repeat("─", 80),
	})
}

func TestRegressionToolCallFrame120x32Golden(t *testing.T) {
	model := NewModel(Config{
		AppName:    "CAELIS",
		Version:    "dev",
		Workspace:  "/tmp/workspace",
		ModelAlias: "minimax/MiniMax-M1",
		Commands:   DefaultCommands(),
		Wizards:    DefaultWizards(),
		NoColor:    true,
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 32})
	m := updated.(*Model)

	updated, _ = m.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
		SessionRef: session.SessionRef{SessionID: "sess-regression"},
		Kind:       kernel.EventKindUserMessage,
		Narrative: &kernel.NarrativePayload{
			Role: kernel.NarrativeRoleUser,
			Text: "run the smoke check",
		},
	}}))

	m = updated.(*Model)
	updated, _ = m.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
		SessionRef: session.SessionRef{SessionID: "sess-regression"},
		Kind:       kernel.EventKindToolCall,
		ToolCall: &kernel.ToolCallPayload{
			CallID:    "call-1",
			ToolName:  "RUN_COMMAND",
			ToolKind:  "execute",
			ToolTitle: "go test ./surfaces/tui/app",
			Status:    kernel.ToolStatusRunning,
			RawInput: map[string]any{
				"command": "go test ./surfaces/tui/app",
			},
			Content: []session.ProtocolToolCallContent{{
				Type:    "terminal",
				Content: "ok\n",
			}},
		},
	}}))

	m = updated.(*Model)
	updated, _ = m.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
		SessionRef: session.SessionRef{SessionID: "sess-regression"},
		Kind:       kernel.EventKindToolResult,
		ToolResult: &kernel.ToolResultPayload{
			CallID:    "call-1",
			ToolName:  "RUN_COMMAND",
			ToolKind:  "execute",
			ToolTitle: "go test ./surfaces/tui/app",
			Status:    kernel.ToolStatusCompleted,
			RawInput: map[string]any{
				"command": "go test ./surfaces/tui/app",
			},
			RawOutput: map[string]any{
				"exit_code": 0,
			},
			Content: []session.ProtocolToolCallContent{{
				Type:    "terminal",
				Content: "ok\nPASS\n",
			}},
		},
	}}))

	m = updated.(*Model)
	updated, _ = m.Update(gatewayEventMsg(kernel.EventEnvelope{Event: kernel.Event{
		SessionRef: session.SessionRef{SessionID: "sess-regression"},
		Kind:       kernel.EventKindAssistantMessage,
		Narrative: &kernel.NarrativePayload{
			Role: kernel.NarrativeRoleAssistant,
			Text: "Smoke check passed.",
		},
	}}))

	m = updated.(*Model)

	frame := evalharness.NormalizeFrame(m.View().Content)
	assertRegressionFrame(t, "tool call 120x32", frame, 31, []string{
		"  ▌",
		"  ▌ run the smoke check",
		"  ▌",
		"  • Ran go test ./surfaces/tui/app",
		"   /tmp/workspace                                                                                      not configured",
		strings.Repeat("─", 120),
		"   > Type your message, @agent, #path/to/file, or $skill",
		strings.Repeat("─", 120),
	})
}

func assertRegressionFrame(t *testing.T, name string, frame string, wantLineCount int, wantNonEmpty []string) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if got := len(lines); got != wantLineCount {
		t.Fatalf("%s line count = %d, want %d\nframe:\n%s", name, got, wantLineCount, frame)
	}
	var nonEmpty []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmpty = append(nonEmpty, line)
	}
	if len(nonEmpty) != len(wantNonEmpty) {
		t.Fatalf("%s non-empty line count = %d, want %d\nnon-empty:\n%q\nframe:\n%s", name, len(nonEmpty), len(wantNonEmpty), nonEmpty, frame)
	}
	for i := range wantNonEmpty {
		if nonEmpty[i] != wantNonEmpty[i] {
			t.Fatalf("%s non-empty line %d mismatch\nwant: %q\ngot:  %q\nframe:\n%s", name, i+1, wantNonEmpty[i], nonEmpty[i], frame)
		}
	}
}
