package tuiapp

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/charmbracelet/x/ansi"
)

func TestPluginWizardSourceBackAndSubmit(t *testing.T) {
	for _, action := range []string{"install", "marketplace"} {
		t.Run(action, func(t *testing.T) {
			var submitted []Submission
			m := NewModel(Config{Commands: DefaultCommands(), Wizards: DefaultWizards(), NoAnimation: true,
				ExecuteLine: func(s Submission) TaskResultMsg { submitted = append(submitted, s); return TaskResultMsg{} },
				SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
					if command == "plugin" {
						return []SlashArgCandidate{{Value: action}}, nil
					}
					return []SlashArgCandidate{{Value: "add"}}, nil
				},
			})
			m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			typeComposerThroughUpdate(t, m, "/plug")
			connectPress(m, "enter")
			connectPress(m, "enter")
			if action == "marketplace" {
				connectPress(m, "enter")
			}
			connectPaste(m, "/tmp/plugin with spaces")
			connectPress(m, "esc")
			connectPress(m, "enter")
			if m.wizardOverlay.fields[0].value != "/tmp/plugin with spaces" {
				t.Fatal("Back lost source")
			}
			frame := ansi.Strip(m.renderWizardOverlay())
			if !strings.Contains(frame, "Source") || strings.Contains(frame, "Connection") {
				t.Fatal(frame)
			}
			connectPress(m, "enter")
			want := "/plugin install /tmp/plugin with spaces"
			if action == "marketplace" {
				want = "/plugin marketplace add /tmp/plugin with spaces"
			}
			if len(submitted) != 1 || submitted[0].Text != want || m.wizardOverlay != nil || m.textarea.Value() != "" {
				t.Fatalf("submission: %+v", submitted)
			}
		})
	}
}

func TestDisconnectSearchAndBackPreservesSelection(t *testing.T) {
	var called string
	m := NewModel(Config{Wizards: DefaultWizards(), ExecuteLine: func(s Submission) TaskResultMsg { called = s.Text; return TaskResultMsg{} }, SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
		if command == "disconnect" {
			return []SlashArgCandidate{{Value: "provider"}}, nil
		}
		return []SlashArgCandidate{{Value: "first"}, {Value: "second"}}, nil
	}})
	runConnectTestCmd(m, m.openSlashArgPicker("disconnect"))
	connectPress(m, "enter")
	connectPress(m, "space")
	connectPaste(m, "second")
	connectPress(m, "space")
	connectPress(m, "esc")
	connectPress(m, "enter")
	if m.slashArgQuery != "second" || len(m.wizard.multiSelections["provider_model"]) != 2 {
		t.Fatal("Back lost selection")
	}
	connectPaste(m, " no match")
	connectPress(m, "enter")
	if called != "/disconnect provider first,second" {
		t.Fatalf("got %q", called)
	}
}

func TestDisconnectSubstringSearchConfirmsHighlightedModel(t *testing.T) {
	var submitted []string
	m := NewModel(Config{Wizards: DefaultWizards(), ExecuteLine: func(s Submission) TaskResultMsg {
		submitted = append(submitted, s.Text)
		return TaskResultMsg{}
	}, SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
		if command == "disconnect" {
			return []SlashArgCandidate{{Value: "provider"}}, nil
		}
		return []SlashArgCandidate{{Value: "openai/gpt-6-astra", Display: "GPT-6 Astra", ModelConfigID: "astra", ModelSelection: &appserver.ModelSelection{Efforts: []string{"low"}, Effort: "low"}}}, nil
	}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	runConnectTestCmd(m, m.openSlashArgPicker("disconnect"))
	connectPress(m, "enter")
	connectPaste(m, "astra")
	if len(m.slashArgCandidates) != 1 {
		t.Fatalf("matching candidates: %+v", m.slashArgCandidates)
	}
	if frame := ansi.Strip(m.renderWizardOverlay()); !strings.Contains(frame, "GPT-6 Astra") {
		t.Fatalf("matching model missing from overlay:\n%s", frame)
	}
	connectPress(m, "enter")
	if len(submitted) != 1 || submitted[0] != "/disconnect provider openai/gpt-6-astra" || m.wizardOverlay != nil {
		t.Fatalf("Enter did not confirm highlighted match: %v", submitted)
	}
}

func TestSessionPickerSearchOwnsTypingPasteAndRefresh(t *testing.T) {
	m := NewModel(Config{})
	rows := []ResumeCandidate{{SessionID: "1", Title: "Jack"}, {SessionID: "2", Title: "Kate"}}
	m.sessionPicker = &sessionPickerState{rows: rows, allRows: rows, request: 7}
	m.handleSessionPickerKey(tea.KeyPressMsg(tea.Key{Text: "K", Code: 'K'}))
	m.handlePaste(tea.PasteMsg{Content: "ate"})
	if len(m.sessionPicker.rows) != 1 || m.sessionPicker.rows[0].SessionID != "2" || m.textarea.Value() != "" {
		t.Fatal("search leaked into composer")
	}
	m.applySessionPickerResult(sessionPickerResultMsg{request: 7, rows: rows})
	if m.sessionPicker.query != "Kate" || len(m.sessionPicker.rows) != 1 {
		t.Fatal("refresh lost search")
	}
	m.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	if len(m.sessionPicker.rows) != 2 {
		t.Fatal("clear did not restore rows")
	}
}

func TestArgumentFreeSlashSelectionExecutesOnce(t *testing.T) {
	for _, command := range []string{"new", "compact"} {
		t.Run(command, func(t *testing.T) {
			var submissions []Submission
			m := NewModel(Config{Commands: []string{command}, ExecuteLine: func(s Submission) TaskResultMsg {
				submissions = append(submissions, s)
				return TaskResultMsg{}
			}})
			typeComposerThroughUpdate(t, m, "/"+command)
			connectPress(m, "enter")
			if len(submissions) != 1 || submissions[0].Text != "/"+command || m.textarea.Value() != "" {
				t.Fatalf("selection: %+v, input=%q", submissions, m.textarea.Value())
			}
		})
	}
}
