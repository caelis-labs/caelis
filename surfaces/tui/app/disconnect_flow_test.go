package tuiapp

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestDisconnectWizardSelectsMixedModelsFromOneList(t *testing.T) {
	var called string
	m := NewModel(Config{
		Wizards: DefaultWizards(),
		ExecuteLine: func(submission Submission) TaskResultMsg {
			called = submission.Text
			return TaskResultMsg{}
		},
		SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
			if command != "disconnect-models" {
				t.Fatalf("unexpected completion command %q", command)
			}
			return []SlashArgCandidate{
				{Value: "provider:codex/model", Display: "Codex / Model"},
				{Value: "acp:codex:model", Display: "ACP · Codex / Model"},
			}, nil
		},
	})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runConnectTestCmd(m, m.openSlashArgPicker("disconnect"))
	connectPress(m, "space")
	connectPress(m, "down")
	connectPress(m, "space")
	if called != "" {
		t.Fatalf("selection submitted before confirmation: %q", called)
	}
	waiting := m.View().Content
	for _, want := range []string{"Connected models", "[x] Codex / Model", "[x] ACP · Codex / Model", "space toggle", "Disconnect 2"} {
		if !strings.Contains(ansi.Strip(waiting), want) {
			t.Fatalf("disconnect picker missing %q:\n%s", want, ansi.Strip(waiting))
		}
	}
	connectPress(m, "enter")
	if want := `/disconnect ["provider:codex/model","acp:codex:model"]`; called != want {
		t.Fatalf("submitted = %q, want %q", called, want)
	}
	updates := renderFullscreenFramesForTest(t, 100, 30, waiting)
	assertPhysicalFullscreenFrame(t, 100, 30, waiting, updates)
}
