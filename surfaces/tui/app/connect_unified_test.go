package tuiapp

import (
	"context"
	"image/color"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestUnifiedMagpieWizardConfirmsTypeAfterModel(t *testing.T) {
	for _, kind := range []string{"chat", "judgment", "auto"} {
		t.Run(kind, func(t *testing.T) {
			var submitted []Submission
			name := "codex/gpt-6-luna"
			if kind == "judgment" {
				name = "typesafe/jev-latest"
			}
			if kind == "auto" {
				name = "codex/gpt-6-luna,typesafe/jev-latest"
			}
			m := NewModel(Config{Commands: DefaultCommands(), Wizards: DefaultWizards(), ExecuteLine: func(s Submission) TaskResultMsg { submitted = append(submitted, s); return TaskResultMsg{} }, SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
				var candidates []SlashArgCandidate
				switch {
				case command == "connect-presets":
					candidates = []SlashArgCandidate{{Value: "magpie", Display: "magpie", Detail: "Local gateway"}}
				case command == "connect-baseurl:magpie":
					candidates = []SlashArgCandidate{{Value: "http://127.0.0.1:3425/v1", NoAuth: true}}
				case strings.HasPrefix(command, "connect-models:"):
					candidates = []SlashArgCandidate{{Value: name, Display: name, ModelMetadataComplete: true, ModelImageInputKnown: true}}
				case strings.HasPrefix(command, "connect-model-type:"):
					candidates = []SlashArgCandidate{{Value: kind, Display: kind, Detail: "Declared model type"}}
				}
				return filterSlashArgCandidates(query, candidates), nil
			}})
			m.width, m.height, m.ready = 120, 32, true
			runConnectTestCmd(m, m.openSlashArgPicker("connect"))
			connectPress(m, "enter")
			if m.wizardStepKey() != "endpoint" {
				t.Fatalf("provider step was repeated: %s", m.wizardStepKey())
			}
			connectPress(m, "enter")
			if m.wizardStepKey() != "model" {
				t.Fatalf("unexpected credential step: %s", m.wizardStepKey())
			}
			connectPress(m, "space")
			connectPress(m, "enter")
			if m.wizardStepKey() != "model_type" || len(submitted) != 0 {
				t.Fatal("type was not confirmed after model selection")
			}
			frame := ansi.Strip(m.renderWizardOverlay())
			if !strings.Contains(frame, "Model type") || !strings.Contains(frame, "Declared model type") {
				t.Fatalf("type frame:\n%s", frame)
			}
			connectPress(m, "enter")
			if len(submitted) != 1 || m.wizardOverlay != nil {
				t.Fatalf("submission: %#v", submitted)
			}
			provider := "magpie"
			if kind == "judgment" {
				provider = "magpie-judgment"
			}
			if !strings.HasPrefix(submitted[0].Text, "/connect "+provider+" "+name+" ") {
				t.Fatalf("protocol or upstream ID changed: %s", submitted[0].Text)
			}
		})
	}
}

func TestModelPickerShowsRoutesWithoutChangingSelection(t *testing.T) {
	m, submitted := newModelPickerTestModel(t)
	m.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	m.slashArgCandidates = []SlashArgCandidate{
		{Value: "deepseek/deepseek-flash", Display: "deepseek/deepseek-flash", Detail: "direct", ModelConfigID: "direct-id", ModelSelection: &appserver.ModelSelection{Efforts: []string{"none"}, Effort: "none"}},
		{Value: "magpie@local/deepseek/deepseek-flash", Display: "deepseek/deepseek-flash", Detail: "magpie", ModelConfigID: "gateway-id", ModelSelection: &appserver.ModelSelection{Efforts: []string{"none"}, Effort: "none"}},
	}
	m.slashArgIndex = 1
	styled := m.renderInputOverlay()
	frame := ansi.Strip(styled)
	if strings.Contains(frame, "magpie@local/") || !strings.Contains(frame, "magpie") || !strings.Contains(frame, "direct") {
		t.Fatalf("route display:\n%s", frame)
	}
	line := styledLineContaining(t, styled, "› deepseek/deepseek-flash")
	plain := ansi.Strip(line)
	noteX := ansi.StringWidth(plain[:strings.Index(plain, "magpie")])
	screen := uv.NewScreenBuffer(ansi.StringWidth(line), 1)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(line).Draw(screen, screen.Bounds())
	note := screen.CellAt(noteX, 0)
	if note == nil || !colorInSet(note.Style.Fg, []color.Color{m.theme.HelpHintFg}) {
		t.Fatalf("route annotation is not subdued: %#v", note)
	}
	if path := os.Getenv("CAELIS_MODEL_UI_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(frame), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, cmd := m.handleSlashArgKey(keyPress("enter"))
	findAndRunTaskResult(cmd(), m)
	if len(*submitted) != 1 || (*submitted)[0] != "/model magpie@local/deepseek/deepseek-flash none" {
		t.Fatalf("display label used as selector: %#v", *submitted)
	}
}

func TestDisconnectDistinguishesMatchingDisplayNames(t *testing.T) {
	m, submissions := wizardOverlayFixture(t)
	m.clearWizard()
	m.cfg.SlashArgComplete = func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
		var rows []SlashArgCandidate
		switch command {
		case "disconnect":
			rows = []SlashArgCandidate{{Value: "provider"}}
		case "disconnect-provider":
			rows = []SlashArgCandidate{
				{Value: "deepseek/deepseek-flash", Display: "deepseek/deepseek-flash", Detail: "direct"},
				{Value: "magpie@local/deepseek/deepseek-flash", Display: "deepseek/deepseek-flash", Detail: "magpie"},
			}
		}
		return filterSlashArgCandidates(query, rows), nil
	}
	runConnectTestCmd(m, m.openSlashArgPicker("disconnect"))
	connectPress(m, "enter")
	frame := ansi.Strip(m.renderWizardOverlay())
	if !strings.Contains(frame, "direct") || !strings.Contains(frame, "magpie") || strings.Contains(frame, "magpie@local/") {
		t.Fatalf("ambiguous disconnect rows:\n%s", frame)
	}
	connectPress(m, "down")
	connectPress(m, "enter")
	if len(*submissions) != 1 || (*submissions)[0].Text != "/disconnect provider magpie@local/deepseek/deepseek-flash" {
		t.Fatalf("wrong route removed: %#v", *submissions)
	}
}
