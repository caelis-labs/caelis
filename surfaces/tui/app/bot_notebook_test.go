package tuiapp

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/bot"
)

// notebookSettingsFixture stages one legacy Bot: not notebook-enabled until the
// user opts in from /settings.
func notebookSettingsFixture(t *testing.T) (*Model, *fakeBotClient) {
	t.Helper()
	value := bot.Bot{
		ID: "bot-1", SessionID: "chat-1", Revision: 7, ModelSelector: "openai/sol",
		Config: bot.Config{Name: "Ada", Description: "old", Model: "durable-sol", Effort: "high", Fast: true},
	}
	client := &fakeBotClient{bots: []bot.Bot{value}}
	m := newBotTestModel(t, 80, 24, client, func(s Submission) TaskResultMsg {
		t.Fatalf("Bot notebook settings reached work command: %s", s.Text)
		return TaskResultMsg{}
	})
	stageBotForGolden(m, value)
	return m, client
}

func focusNotebookField(t *testing.T, m *Model) {
	t.Helper()
	for i, field := range m.wizardOverlay.fields {
		if field.key == botNotebookFieldKey {
			m.wizardOverlay.field = i
			return
		}
	}
	t.Fatalf("settings form has no Notebook field: %+v", m.wizardOverlay.fields)
}

func TestBotSettingsNotebookEnableOptIn(t *testing.T) {
	m, client := notebookSettingsFixture(t)
	// An unsent draft and the selected conversation must survive the opt-in.
	m.setInputText("unfinished note")
	m.syncTextareaFromInput()
	runConnectTestCmd(m, m.startBotSettingsFlow(false))
	focusNotebookField(t, m)
	if got := m.wizardOverlay.fields[m.wizardOverlay.field].value; got != botNotebookOff {
		t.Fatalf("initial Notebook = %q, want Off", got)
	}
	if frame := ansi.Strip(m.renderWizardOverlay()); !strings.Contains(frame, "Keeps your history; starts a new model context") {
		t.Fatalf("opt-in explanation missing from form:\n%s", frame)
	}
	if frame := ansi.Strip(m.renderWizardOverlay()); strings.Contains(frame, "prompt cache") {
		t.Fatalf("form leaked implementation jargon:\n%s", frame)
	}
	// Enter toggles Off -> Enable, and Enter again clears the staged opt-in.
	connectPress(m, "enter")
	if got := m.wizardOverlay.fields[m.wizardOverlay.field].value; got != botNotebookEnable {
		t.Fatalf("after enter = %q, want Enable", got)
	}
	connectPress(m, "enter")
	if got := m.wizardOverlay.fields[m.wizardOverlay.field].value; got != botNotebookOff {
		t.Fatalf("toggle back = %q, want Off", got)
	}
	connectPress(m, "enter")
	m.wizardOverlay.field = len(m.wizardOverlay.fields)
	connectPress(m, "enter")
	if m.wizardOverlay != nil {
		t.Fatal("save did not close settings")
	}
	if len(client.updated) != 1 || !client.updated[0].EnableNotebook {
		t.Fatalf("explicit enable not requested: %+v", client.updated)
	}
	value, ok := m.activeBot()
	if !ok || !value.NotebookEnabled {
		t.Fatalf("active Bot not refreshed as enabled: %+v", value)
	}
	if got := m.textarea.Value(); got != "unfinished note" {
		t.Fatalf("unsent draft = %q, want preserved", got)
	}
	if got := m.botActiveSessionID(); got != "chat-1" {
		t.Fatalf("active conversation = %q, want chat-1", got)
	}
	if m.sessionPicker != nil {
		t.Fatal("opt-in opened a picker")
	}
}

func TestBotSettingsNotebookCancelKeepsLegacy(t *testing.T) {
	m, client := notebookSettingsFixture(t)
	runConnectTestCmd(m, m.startBotSettingsFlow(false))
	focusNotebookField(t, m)
	connectPress(m, "enter")
	if got := m.wizardOverlay.fields[m.wizardOverlay.field].value; got != botNotebookEnable {
		t.Fatalf("staged = %q, want Enable", got)
	}
	connectPress(m, "esc")
	if m.wizardOverlay != nil {
		t.Fatal("esc did not close settings")
	}
	if len(client.updated) != 0 {
		t.Fatalf("cancel saved: %+v", client.updated)
	}
	if value, _ := m.activeBot(); value.NotebookEnabled {
		t.Fatal("cancel enabled the notebook")
	}
}

func TestBotSettingsNotebookNoOpSavesNothing(t *testing.T) {
	m, client := notebookSettingsFixture(t)
	runConnectTestCmd(m, m.startBotSettingsFlow(false))
	m.wizardOverlay.field = len(m.wizardOverlay.fields)
	connectPress(m, "enter")
	if len(client.updated) != 0 {
		t.Fatalf("an unchanged form saved: %+v", client.updated)
	}
	if m.wizardOverlay != nil {
		t.Fatal("no-op did not close settings")
	}
	if value, _ := m.activeBot(); value.NotebookEnabled {
		t.Fatal("no-op enabled the notebook")
	}
}

func TestBotSettingsRenameDoesNotEnableNotebook(t *testing.T) {
	m, client := notebookSettingsFixture(t)
	runConnectTestCmd(m, m.startBotSettingsFlow(false))
	connectPaste(m, " Lovelace")
	m.wizardOverlay.field = len(m.wizardOverlay.fields)
	connectPress(m, "enter")
	if len(client.updated) != 1 {
		t.Fatalf("updates: %+v", client.updated)
	}
	if client.updated[0].EnableNotebook {
		t.Fatal("rename implicitly enabled the notebook")
	}
	if got := client.updated[0].Config.Name; got != "Ada Lovelace" {
		t.Fatalf("rename lost: %q", got)
	}
}

func TestBotSettingsNotebookReadOnlyOnceEnabled(t *testing.T) {
	m, client := notebookSettingsFixture(t)
	client.bots[0].NotebookEnabled = true
	value := client.bots[0]
	stageBotForGolden(m, value)
	runConnectTestCmd(m, m.startBotSettingsFlow(false))
	focusNotebookField(t, m)
	if got := m.wizardOverlay.fields[m.wizardOverlay.field].value; got != botNotebookOn {
		t.Fatalf("enabled Notebook = %q, want On", got)
	}
	connectPress(m, "enter")
	if got := m.wizardOverlay.fields[m.wizardOverlay.field].value; got != botNotebookOn {
		t.Fatalf("enter changed an enabled notebook: %q", got)
	}
	m.wizardOverlay.fields[0].value = "Ada Lovelace"
	m.wizardOverlay.field = len(m.wizardOverlay.fields)
	connectPress(m, "enter")
	if len(client.updated) != 1 {
		t.Fatalf("updates: %+v", client.updated)
	}
	if client.updated[0].EnableNotebook {
		t.Fatal("an enabled notebook was re-requested")
	}
}

func TestBotModelOnlyDoesNotEnableNotebook(t *testing.T) {
	m, client := notebookSettingsFixture(t)
	m.cfg.SlashArgComplete = func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
		candidates := modelPickerCandidates()
		candidates[0].ModelConfigID, candidates[1].ModelConfigID = "durable-flash", "durable-sol"
		return candidates, nil
	}
	runConnectTestCmd(m, m.startBotSettingsFlow(true))
	if m.wizardOverlay.fields != nil {
		t.Fatal("model-only flow opened the settings form")
	}
	connectPress(m, "up")
	connectPress(m, "enter")
	if len(client.updated) != 1 {
		t.Fatalf("updates: %+v", client.updated)
	}
	if client.updated[0].EnableNotebook {
		t.Fatal("model-only flow enabled the notebook")
	}
}

// TestBotNotebookRenderEvidence pins the opt-in row and help geometry at the
// minimal, common, and wide terminal sizes: the new-context boundary and the
// history-kept help must stay visible even at the smallest supported terminal.
func TestBotNotebookRenderEvidence(t *testing.T) {
	value := bot.Bot{ID: "bot-1", SessionID: "chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Model: "gpt-5"}}
	for _, size := range botRenderSizes {
		t.Run(size.name, func(t *testing.T) {
			model := newBotTestModel(t, size.width, size.height, &fakeBotClient{bots: []bot.Bot{value}}, nil)
			stageBotForGolden(model, value)
			model.Update(model.startBotSettingsFlow(false)())
			focusNotebookField(t, model)
			model.syncViewportContent()
			frame := ansi.Strip(model.View().Content)
			for _, want := range []string{"Notebook", "Off · new context", "keeps history"} {
				if !strings.Contains(frame, want) {
					t.Fatalf("%s: missing %q:\n%s", size.name, want, frame)
				}
			}
			if strings.Contains(frame, "prompt cache") || strings.Contains(frame, "not reused") {
				t.Fatalf("%s: leaked implementation jargon:\n%s", size.name, frame)
			}
			if size.width >= 80 && !strings.Contains(frame, "Keeps your history; starts a new model context") {
				t.Fatalf("%s: wrapped explanation missing:\n%s", size.name, frame)
			}
			assertBotFrameBounds(t, model, frame)
			updates := renderFullscreenFramesForTest(t, size.width, size.height, frame)
			assertPhysicalFullscreenFrame(t, size.width, size.height, frame, updates)
		})
	}
}

// TestBotNotebookMinimalOptInStaysVisible covers the smallest supported terminal:
// the boundary is on the opt-in row, and moving focus to the Save action must
// not hide it before the user commits.
func TestBotNotebookMinimalOptInStaysVisible(t *testing.T) {
	value := bot.Bot{ID: "bot-1", SessionID: "chat-1", Revision: 4, Config: bot.Config{Name: "Ada", Model: "gpt-5"}}
	model := newBotTestModel(t, 40, 12, &fakeBotClient{bots: []bot.Bot{value}}, nil)
	stageBotForGolden(model, value)
	model.Update(model.startBotSettingsFlow(false)())
	focusNotebookField(t, model)
	model.Update(connectKey("enter")) // stage Enable
	frame := ansi.Strip(model.View().Content)
	for _, want := range []string{"Enable · new context", "keeps history"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("40x12 opt-in missing %q:\n%s", want, frame)
		}
	}
	// Focusing the Save action must keep the boundary on the row and the staged
	// help in the footer.
	model.wizardOverlay.field = len(model.wizardOverlay.fields)
	model.syncViewportContent()
	frame = ansi.Strip(model.View().Content)
	for _, want := range []string{"Enable · new context", "keeps history"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("40x12 Save focus lost %q:\n%s", want, frame)
		}
	}
	assertBotFrameBounds(t, model, frame)
	updates := renderFullscreenFramesForTest(t, 40, 12, frame)
	assertPhysicalFullscreenFrame(t, 40, 12, frame, updates)
}
