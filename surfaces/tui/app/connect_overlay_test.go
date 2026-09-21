package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/charmbracelet/x/ansi"
)

func wizardOverlayFixture(t *testing.T) (*Model, *[]Submission) {
	t.Helper()
	submissions := []Submission{}
	m := NewModel(Config{
		Commands: DefaultCommands(), Wizards: DefaultWizards(),
		ExecuteLine: func(s Submission) TaskResultMsg { submissions = append(submissions, s); return TaskResultMsg{} },
		SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
			var candidates []SlashArgCandidate
			switch {
			case command == "connect":
				candidates = []SlashArgCandidate{{Value: "api-key", Display: "API key"}, {Value: "acp", Display: "Local ACP Agent"}}
			case command == "connect-provider-api-key":
				candidates = []SlashArgCandidate{{Value: "openai-responses-compatible", Display: "Responses API"}, {Value: "openai", Display: "OpenAI"}, {Value: "xiaomi", Display: "Xiaomi"}}
			case strings.HasPrefix(command, "connect-baseurl:"):
				candidates = []SlashArgCandidate{{Value: "https://api.example/v1", Display: "Default endpoint"}, {Value: "https://saved.example/v1", Display: "Saved endpoint", NoAuth: true}}
			case strings.HasPrefix(command, "connect-model:"):
				candidates = []SlashArgCandidate{{Value: "alpha", Display: "Alpha", ModelMetadataComplete: true, ModelImageInputKnown: true}, {Value: "beta", Display: "Beta", ModelMetadataComplete: true, ModelImageInputKnown: true}}
			case command == "connect-acp-agent":
				candidates = []SlashArgCandidate{{Value: "codex", Display: "Codex CLI"}}
			case command == "connect-acp-launcher:codex":
				candidates = []SlashArgCandidate{{Value: "hosted", Display: "Built in"}}
			case strings.HasPrefix(command, "connect-acp-model:"):
				candidates = []SlashArgCandidate{{Value: "remote", Display: "Remote model"}}
			}
			return filterSlashArgCandidates(query, candidates), nil
		},
	})
	m.width, m.height, m.ready = 100, 30, true
	m.setInputText("keep my draft")
	m.syncTextareaFromInput()
	runConnectTestCmd(m, m.openSlashArgPicker("connect"))
	return m, &submissions
}

func connectPress(m *Model, key string) {
	_, cmd := m.Update(connectKey(key))
	runConnectTestCmd(m, cmd)
}

func connectPaste(m *Model, text string) {
	_, cmd := m.Update(tea.PasteMsg{Content: text})
	runConnectTestCmd(m, cmd)
}

func TestWizardOverlayConsumesCompletedCommandOnOpen(t *testing.T) {
	for _, input := range []string{"/conne", "/connect"} {
		for _, accept := range []string{"enter", "tab", "click"} {
			t.Run(input+"/"+accept, func(t *testing.T) {
				m, _ := wizardOverlayFixture(t)
				m.clearWizard()
				m.setInputText("")
				m.syncTextareaFromInput()
				typeComposerThroughUpdate(t, m, input)
				if len(m.slashCandidates) != 1 || m.slashCandidates[0] != "/connect" {
					t.Fatalf("completion = %#v", m.slashCandidates)
				}
				if accept == "click" {
					runConnectTestCmd(m, m.activateCompletion(completionSlashCommand))
				} else {
					connectPress(m, accept)
				}
				if m.wizardOverlay == nil || m.textarea.Value() != "" || len(m.input) != 0 {
					t.Fatalf("opening left command in composer: textarea=%q input=%q", m.textarea.Value(), string(m.input))
				}
				connectPress(m, "esc")
				if m.wizardOverlay != nil || m.textarea.Value() != "" || strings.Contains(ansi.Strip(m.View().Content), "/connect") {
					t.Fatal("closing restored consumed command")
				}
			})
		}
	}
}

func TestWizardOverlayConnectionFormBackSearchAndMultiSelect(t *testing.T) {
	m, submissions := wizardOverlayFixture(t)
	connectPress(m, "enter") // source
	connectPress(m, "enter") // provider
	if len(m.wizardOverlay.fields) != 2 {
		t.Fatalf("connection form = %#v step=%s query=%q candidates=%#v", m.wizardOverlay.fields, m.wizardStepKey(), m.slashArgQuery, m.slashArgCandidates)
	}
	connectPress(m, "tab")
	connectPaste(m, "sk-private")
	frame := ansi.Strip(m.renderWizardOverlay())
	if strings.Contains(frame, "sk-private") || !strings.Contains(frame, "API key") || !strings.Contains(frame, "••") {
		t.Fatalf("secret field frame:\n%s", frame)
	}
	if m.textarea.Value() != "keep my draft" {
		t.Fatal("form replaced composer draft")
	}
	connectPress(m, "enter")
	connectPress(m, "space")
	connectPaste(m, "beta")
	connectPress(m, "space")
	if m.slashArgQuery != "beta" || len(m.wizard.multiSelections["model"]) != 2 {
		t.Fatalf("search/selection lost: %q %#v", m.slashArgQuery, m.wizard.multiSelections)
	}
	connectPress(m, "esc")
	if len(m.wizardOverlay.fields) != 2 || m.wizardOverlay.fields[1].value != "sk-private" {
		t.Fatal("back lost form draft")
	}
	connectPress(m, "enter")
	if m.slashArgQuery != "beta" || len(m.wizard.multiSelections["model"]) != 2 {
		t.Fatalf("forward lost model draft: %q %#v", m.slashArgQuery, m.wizard.multiSelections)
	}
	connectPaste(m, "no-match")
	connectPress(m, "enter")
	if len(*submissions) != 1 || !strings.Contains((*submissions)[0].Text, "alpha,beta") {
		t.Fatalf("submission = %#v", *submissions)
	}
	if m.wizardOverlay != nil {
		t.Fatal("success did not return to composer")
	}
	if m.textarea.Value() != "keep my draft" || len(m.history) != 0 {
		t.Fatal("connect polluted composer/history")
	}

}

func TestWizardOverlayChangingEndpointDropsKeyAndModelDraft(t *testing.T) {
	m, _ := wizardOverlayFixture(t)
	connectPress(m, "enter")
	connectPress(m, "enter")
	connectPress(m, "tab")
	connectPaste(m, "secret")
	connectPress(m, "enter")
	connectPress(m, "space")
	connectPress(m, "esc")
	connectPress(m, "shift+tab")
	connectPress(m, "ctrl+u")
	if m.wizardOverlay.fields[1].value != "" {
		t.Fatal("clearing endpoint retained credential")
	}
	connectPaste(m, "https://another.example/v1")
	if m.wizardOverlay.fields[1].value != "" {
		t.Fatal("endpoint change retained credential")
	}
	connectPress(m, "enter")
	if m.wizardOverlay.err != "Enter an API key." || m.wizardOverlay.field != 1 {
		t.Fatalf("missing key not focused: %#v", m.wizardOverlay)
	}
	connectPaste(m, "new-secret")
	connectPress(m, "enter")
	if len(m.wizard.multiSelections["model"]) != 0 || m.slashArgQuery != "" {
		t.Fatal("new endpoint reused model draft")
	}
}

func TestWizardOverlayCustomModelValidatesAndSubmitsOneForm(t *testing.T) {
	m, submissions := wizardOverlayFixture(t)
	connectPress(m, "enter")
	connectPress(m, "enter")
	connectPress(m, "tab")
	connectPaste(m, "secret")
	connectPress(m, "enter")
	connectPress(m, "ctrl+n")
	connectPaste(m, "custom")
	connectPress(m, "tab")
	connectPress(m, "right")
	connectPress(m, "tab")
	connectPaste(m, "100")
	connectPress(m, "tab")
	connectPaste(m, "200")
	connectPress(m, "enter")
	if m.wizardOverlay.err != "Max output exceeds context." || len(*submissions) != 0 {
		t.Fatal("invalid limits were submitted")
	}
	connectPress(m, "ctrl+u")
	connectPaste(m, "40")
	connectPress(m, "enter")
	if len(*submissions) != 1 || !strings.HasSuffix((*submissions)[0].Text, "100 40 - - true") {
		t.Fatalf("custom payload = %#v", *submissions)
	}
}

func TestWizardOverlayCustomBackRestoresModelSearch(t *testing.T) {
	m, _ := wizardOverlayFixture(t)
	connectPress(m, "enter")
	connectPress(m, "enter")
	connectPress(m, "tab")
	connectPaste(m, "secret")
	connectPress(m, "enter")
	connectPaste(m, "alpha")
	connectPress(m, "ctrl+n")
	connectPress(m, "tab")
	connectPress(m, "right")
	connectPress(m, "esc")
	if len(m.wizardOverlay.fields) != 0 || m.slashArgQuery != "alpha" {
		t.Fatal("custom back did not restore model list")
	}
	connectPress(m, "ctrl+n")
	if m.wizardOverlay.fields[1].value != "true" || m.wizardOverlay.field != 1 {
		t.Fatal("returning to custom model lost its draft")
	}
}

func TestWizardOverlayCustomEndpointAndSavedCredential(t *testing.T) {
	t.Run("custom preset provider endpoint", func(t *testing.T) {
		m, submissions := wizardOverlayFixture(t)
		connectPress(m, "enter")
		connectPaste(m, "xiaomi")
		connectPress(m, "enter")
		connectPaste(m, "https://custom.example/v1")
		connectPress(m, "enter")
		if len(m.wizardOverlay.fields) != 2 || m.wizardOverlay.fields[0].value != "https://custom.example/v1" {
			t.Fatal("custom endpoint did not open combined form")
		}
		connectPress(m, "tab")
		connectPaste(m, "demo-key")
		connectPress(m, "enter")
		connectPress(m, "enter")
		if len(*submissions) != 1 || !strings.Contains((*submissions)[0].Text, "xiaomi alpha https://custom.example/v1") {
			t.Fatalf("custom endpoint submission: %#v", *submissions)
		}
	})
	t.Run("reuse saved credential", func(t *testing.T) {
		m, submissions := wizardOverlayFixture(t)
		connectPress(m, "enter")
		connectPress(m, "enter")
		connectPress(m, "ctrl+u")
		connectPaste(m, "https://saved.example/v1")
		connectPress(m, "enter")
		if m.wizardStepKey() != "model" || m.wizard.state["_reuseauth"] != "true" {
			t.Fatal("saved endpoint required another key")
		}
		connectPress(m, "enter")
		if len(*submissions) != 1 || strings.Fields((*submissions)[0].Text)[5] != "-" {
			t.Fatalf("saved endpoint submission: %#v", *submissions)
		}
	})
	t.Run("explicit key replaces saved credential", func(t *testing.T) {
		m, submissions := wizardOverlayFixture(t)
		connectPress(m, "enter")
		connectPress(m, "enter")
		connectPress(m, "ctrl+u")
		connectPaste(m, "https://saved.example/v1")
		connectPress(m, "tab")
		connectPaste(m, "replacement-key")
		connectPress(m, "enter")
		connectPress(m, "enter")
		if len(*submissions) != 1 || strings.Fields((*submissions)[0].Text)[5] != "replacement-key" {
			t.Fatalf("explicit credential was ignored: %#v", *submissions)
		}
	})
}

func TestWizardOverlayACPSkipsUniqueLauncherAndBackSkipsIt(t *testing.T) {
	m, submissions := wizardOverlayFixture(t)
	connectPress(m, "down")
	connectPress(m, "enter")
	connectPress(m, "enter")
	if m.wizardStepKey() != "acp_model" || m.wizard.state["acp_launcher"] != "hosted" {
		t.Fatalf("ACP step = %s", m.wizardStepKey())
	}
	connectPress(m, "esc")
	if m.wizardStepKey() != "acp_agent" {
		t.Fatalf("Back visited automatic launcher: %s", m.wizardStepKey())
	}
	connectPress(m, "enter")
	connectPress(m, "enter")
	if len(*submissions) != 1 {
		t.Fatal("ACP did not submit")
	}
	payload, err := parseACPConnectWizardPayload(strings.TrimPrefix((*submissions)[0].Text, "/connect acp "))
	if err != nil || payload.Model != "remote" || payload.Launcher != "hosted" {
		t.Fatalf("ACP payload = %#v, %v", payload, err)
	}
}

func TestWizardOverlayLateCatalogCannotRestoreCancelledStep(t *testing.T) {
	m, _ := wizardOverlayFixture(t)
	connectPress(m, "down")
	connectPress(m, "enter")
	_, cmd := m.Update(connectKey("enter"))
	// Apply the launcher response, but hold the newly scheduled discovery result.
	msg := cmd()
	_, next := m.Update(msg)
	batch := next().(tea.BatchMsg)
	loaded := batch[0]()
	connectPress(m, "esc")
	m.Update(loaded)
	if m.wizardStepKey() != "acp_agent" || m.slashArgLoadPending {
		t.Fatal("stale discovery restored cancelled step")
	}
}

func TestWizardOverlayUnknownSubmissionCannotBeRetried(t *testing.T) {
	m, submissions := wizardOverlayFixture(t)
	m.cfg.ExecuteLine = func(s Submission) TaskResultMsg {
		*submissions = append(*submissions, s)
		return TaskResultMsg{Err: appserver.NewOutcomeError(appserver.OutcomeUnknown, errors.New("result unknown"))}
	}
	connectPress(m, "down")
	connectPress(m, "enter")
	connectPress(m, "enter")
	connectPress(m, "enter")
	if m.wizardOverlay == nil || !m.wizardOverlay.blocked || m.wizardOverlay.pending {
		t.Fatal("unknown mutation was not fenced")
	}
	connectPress(m, "enter")
	if len(*submissions) != 1 {
		t.Fatal("unknown mutation was replayed")
	}
}

func TestWizardOverlayRejectedSubmissionRetainsFilteredDraft(t *testing.T) {
	m, submissions := wizardOverlayFixture(t)
	m.cfg.ExecuteLine = func(s Submission) TaskResultMsg {
		*submissions = append(*submissions, s)
		if len(*submissions) == 1 {
			return TaskResultMsg{Err: appserver.NewOutcomeError(appserver.OutcomeRejected, errors.New("Try again"))}
		}
		return TaskResultMsg{}
	}
	connectPress(m, "enter")
	connectPress(m, "enter")
	connectPress(m, "tab")
	connectPaste(m, "demo-key")
	connectPress(m, "enter")
	connectPaste(m, "beta")
	connectPress(m, "enter")
	if m.wizardOverlay == nil || m.wizardOverlay.blocked || m.slashArgQuery != "beta" || len(m.slashArgCandidates) != 1 || m.slashArgCandidates[0].Value != "beta" {
		t.Fatal("rejection lost filtered model draft")
	}
	connectPress(m, "enter")
	if len(*submissions) != 2 || (*submissions)[0].Text != (*submissions)[1].Text || m.wizardOverlay != nil {
		t.Fatalf("retry submission: %#v", *submissions)
	}
}

func TestWizardOverlayMouseActionDoesNotIncludeHelp(t *testing.T) {
	m, _ := wizardOverlayFixture(t)
	m.renderWizardOverlay()
	g := m.wizardOverlay.geometry
	click := func(x, y int) {
		m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
		_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
		runConnectTestCmd(m, cmd)
	}
	click(g.backX+g.backWidth+1, g.actionY)
	if m.wizardStepKey() != "source" {
		t.Fatal("help text click submitted the step")
	}
	click(g.actionX+1, g.actionY)
	if m.wizardStepKey() != "provider" {
		t.Fatal("action click did not advance")
	}
	m.renderWizardOverlay()
	g = m.wizardOverlay.geometry
	click(g.closeX, g.closeY)
	if m.wizardOverlay != nil || m.textarea.Value() != "keep my draft" {
		t.Fatal("close did not restore composer")
	}
}

func TestWizardOverlayLongFieldKeepsCaretVisible(t *testing.T) {
	m, _ := wizardOverlayFixture(t)
	connectPress(m, "enter")
	connectPress(m, "enter")
	connectPress(m, "ctrl+u")
	connectPaste(m, "https://example.com/"+strings.Repeat("long-path/", 20))
	for _, cursor := range []int{0, 30, 200} {
		m.wizardOverlay.cursor = cursor
		frame := ansi.Strip(m.renderWizardField(m.wizardOverlay.fields[0], true, 36))
		if !strings.Contains(frame, "▏") || displayColumns(frame) > 36 {
			t.Fatalf("cursor %d not visible: %q", cursor, frame)
		}
	}
}

func TestWizardOverlayFramesBoundedAndComposerVisible(t *testing.T) {
	for _, size := range [][2]int{{120, 36}, {80, 24}, {44, 18}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m, _ := wizardOverlayFixture(t)
			m.width, m.height = size[0], size[1]
			connectPress(m, "enter")
			connectPress(m, "enter")
			connectPress(m, "tab")
			connectPaste(m, "super-secret")
			frame := ansi.Strip(m.View().Content)
			if !strings.Contains(frame, "keep my draft") || strings.Contains(frame, "super-secret") {
				t.Fatalf("composer/secret frame:\n%s", frame)
			}
			for _, line := range strings.Split(frame, "\n") {
				if displayColumns(line) > size[0] {
					t.Fatalf("overflow: %q", line)
				}
			}
			if strings.Count(frame, "\n")+1 > size[1] {
				t.Fatalf("height overflow:\n%s", frame)
			}
			if strings.Count(frame, "/connect") != 1 {
				t.Fatalf("duplicate connect prompts:\n%s", frame)
			}
		})
	}
}

func connectKey(name string) tea.KeyMsg {
	k := tea.Key{}
	switch name {
	case "enter":
		k.Code = tea.KeyEnter
	case "tab":
		k.Code = tea.KeyTab
	case "shift+tab":
		k.Code = tea.KeyTab
		k.Mod = tea.ModShift
	case "esc":
		k.Code = tea.KeyEscape
	case "up":
		k.Code = tea.KeyUp
	case "down":
		k.Code = tea.KeyDown
	case "left":
		k.Code = tea.KeyLeft
	case "right":
		k.Code = tea.KeyRight
	case "space":
		k.Code = tea.KeySpace
		k.Text = " "
	case "ctrl+n":
		k.Code = 'n'
		k.Mod = tea.ModCtrl
	case "ctrl+u":
		k.Code = 'u'
		k.Mod = tea.ModCtrl
	default:
		k.Text = name
	}
	return tea.KeyPressMsg(k)
}
