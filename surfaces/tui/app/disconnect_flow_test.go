package tuiapp

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestDisconnectWizardSelectsMultipleTargetsBeforeSubmission(t *testing.T) {
	for _, kind := range []string{"provider", "acp"} {
		t.Run(kind, func(t *testing.T) {
			called := ""
			m := NewModel(Config{
				Wizards: DefaultWizards(),
				ExecuteLine: func(submission Submission) TaskResultMsg {
					called = submission.Text
					return TaskResultMsg{}
				},
				SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
					if command == "disconnect" {
						return []SlashArgCandidate{{Value: kind, Display: kind}}, nil
					}
					candidates := []SlashArgCandidate{
						{Value: "first", Display: "first"},
						{Value: "second", Display: "second"},
						{Value: "third", Display: "third"},
					}
					return candidates, nil
				},
			})
			runConnectTestCmd(m, m.openSlashArgPicker("disconnect"))
			_, cmd := m.handleWizardEnter()
			runConnectTestCmd(m, cmd)
			if got := m.slashArgCommand; got != "disconnect-"+kind {
				t.Fatalf("target picker = %q", got)
			}
			press := func(code rune) {
				t.Helper()
				handled, cmd := m.handleSlashArgKey(tea.KeyPressMsg(tea.Key{Code: code}))
				if !handled {
					t.Fatalf("key %v not handled", code)
				}
				runConnectTestCmd(m, cmd)
			}
			press(tea.KeySpace)
			press(tea.KeyDown)
			press(tea.KeyTab)
			press(tea.KeyTab) // Clear second; only first and third should be removed.
			press(tea.KeyDown)
			press(tea.KeySpace)
			if called != "" {
				t.Fatalf("selection submitted before confirmation: %q", called)
			}
			frame := ansi.Strip(m.renderSlashArgList())
			for _, want := range []string{"[x] first", "[ ] second", "[x] third", "click/space/tab toggle", "enter confirm"} {
				if !strings.Contains(frame, want) {
					t.Fatalf("disconnect picker missing %q:\n%s", want, frame)
				}
			}
			press(tea.KeyEnter)
			if want := "/disconnect " + kind + " first,third"; called != want {
				t.Fatalf("submitted = %q, want %q", called, want)
			}
		})
	}
}
