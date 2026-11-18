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

func TestUnifiedMagpieWizardConnectsFromModelPicker(t *testing.T) {
	const localURL = "http://127.0.0.1:3425/v1"
	const savedURL = "https://saved.example/v1"
	const chat = "codex/gpt-6-luna"
	const judgment = "typesafe/jev-latest"
	for _, test := range []struct {
		name   string
		models []string
		action string
	}{
		{name: "chat", models: []string{chat}, action: "Connect 1 model"},
		{name: "judgment", models: []string{judgment}, action: "Connect 1 model"},
		{name: "mixed", models: []string{chat, judgment}, action: "Connect 2 models"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var submitted []Submission
			m := NewModel(Config{Commands: DefaultCommands(), Wizards: DefaultWizards(), ExecuteLine: func(s Submission) TaskResultMsg { submitted = append(submitted, s); return TaskResultMsg{} }, SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
				var candidates []SlashArgCandidate
				switch {
				case command == "connect-options":
					candidates = []SlashArgCandidate{{Value: "magpie", Display: "magpie", Detail: "Local gateway"}}
				case command == "connect-baseurl:magpie":
					candidates = []SlashArgCandidate{
						{Value: localURL, Display: "local", Detail: "Running Magpie gateway", NoAuth: true},
						{Value: savedURL, Detail: "configured auth", NoAuth: true},
					}
				case strings.HasPrefix(command, "connect-models:"):
					for _, name := range test.models {
						candidates = append(candidates, SlashArgCandidate{Value: name, Display: name, ModelMetadataComplete: true, ModelImageInputKnown: true})
					}
				}
				return filterSlashArgCandidates(command, query, candidates), nil
			}})
			m.width, m.height, m.ready = 120, 32, true
			runConnectTestCmd(m, m.openSlashArgPicker("connect"))
			connectPress(m, "enter")
			if m.wizardStepKey() != "endpoint" {
				t.Fatalf("provider step was repeated: %s", m.wizardStepKey())
			}
			connectPress(m, "down")
			endpointFrame := wizardVisibleText(m.renderWizardOverlay())
			localRow := strings.Join(strings.Fields(styledLineContaining(t, endpointFrame, "· local")), " ")
			if !strings.Contains(endpointFrame, "/connect › magpie › Endpoint") || strings.Count(endpointFrame, localURL) != 1 ||
				!strings.Contains(localRow, "· local "+localURL+" · Running Magpie gateway") {
				t.Fatalf("local endpoint address should appear only on its row:\n%s", endpointFrame)
			}
			savedRow := strings.Join(strings.Fields(styledLineContaining(t, endpointFrame, "› "+savedURL)), " ")
			if strings.Count(endpointFrame, savedURL) != 1 || !strings.Contains(savedRow, savedURL+" configured auth") {
				t.Fatalf("saved endpoint should show its address once and retain its auth hint:\n%s", endpointFrame)
			}
			m.width = 64
			narrowFrame := wizardVisibleText(m.renderWizardOverlay())
			if row := styledLineContaining(t, narrowFrame, "· local"); !strings.Contains(row, localURL) {
				t.Fatalf("local endpoint address clipped at 64 columns:\n%s", narrowFrame)
			}
			m.width = 120
			connectPress(m, "up")
			connectPress(m, "enter")
			if m.wizardStepKey() != "model" {
				t.Fatalf("unexpected credential step: %s", m.wizardStepKey())
			}
			modelFrame := ansi.Strip(m.renderWizardOverlay())
			if !strings.Contains(modelFrame, "/connect › magpie › local › Models") || strings.Contains(modelFrame, localURL) {
				t.Fatalf("model picker should identify the connection in the breadcrumb:\n%s", modelFrame)
			}
			for i := range test.models {
				if i > 0 {
					connectPress(m, "down")
				}
				connectPress(m, "space")
			}
			frame := ansi.Strip(m.renderWizardOverlay())
			if !strings.Contains(frame, test.action) {
				t.Fatalf("model picker should offer %q:\n%s", test.action, frame)
			}
			connectPress(m, "enter")
			if len(submitted) != 1 || m.wizardOverlay != nil {
				t.Fatalf("model picker did not connect directly: submissions = %#v, step = %q", submitted, m.wizardStepKey())
			}
			if want := "/connect magpie " + strings.Join(test.models, ",") + " " + localURL + " "; !strings.HasPrefix(submitted[0].Text, want) {
				t.Fatalf("submission = %q, want prefix %q", submitted[0].Text, want)
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
		case "disconnect-models":
			rows = []SlashArgCandidate{
				{Value: "deepseek/deepseek-flash", Display: "deepseek/deepseek-flash", Detail: "direct"},
				{Value: "magpie@local/deepseek/deepseek-flash", Display: "deepseek/deepseek-flash", Detail: "magpie"},
			}
		}
		return filterSlashArgCandidates(command, query, rows), nil
	}
	runConnectTestCmd(m, m.openSlashArgPicker("disconnect"))
	frame := ansi.Strip(m.renderWizardOverlay())
	if !strings.Contains(frame, "direct") || !strings.Contains(frame, "magpie") || strings.Contains(frame, "magpie@local/") {
		t.Fatalf("ambiguous disconnect rows:\n%s", frame)
	}
	connectPress(m, "down")
	connectPress(m, "enter")
	if len(*submissions) != 1 || (*submissions)[0].Text != `/disconnect ["magpie@local/deepseek/deepseek-flash"]` {
		t.Fatalf("wrong route removed: %#v", *submissions)
	}
}
