package tuiapp

import (
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func TestCopySelectionToClipboardRunsAsCommand(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := NewModel(Config{
		WriteClipboardText: func(text string) error {
			if text != "selected text" {
				t.Errorf("unexpected clipboard text %q", text)
			}
			close(started)
			<-release
			return nil
		},
	})

	cmd := model.copySelectionToClipboard("selected text")
	if cmd == nil {
		t.Fatal("expected clipboard command")
	}
	select {
	case <-started:
		t.Fatal("clipboard writer ran synchronously")
	default:
	}

	result := make(chan any, 1)
	go func() {
		result <- cmd()
	}()

	select {
	case <-started:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("clipboard command did not start")
	}
	close(release)

	select {
	case msg := <-result:
		if got, ok := msg.(clipboardCopyResultMsg); !ok {
			t.Fatalf("expected clipboardCopyResultMsg, got %T", msg)
		} else if got.err != nil {
			t.Fatalf("unexpected clipboard error: %v", got.err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("clipboard command did not finish")
	}
}

func TestCommandPanelClickTokenFillsInput(t *testing.T) {
	model := NewModel(Config{})
	if !model.tryToggleFoldToken("panel-1", commandPanelInputClickToken("/settings set sandbox.backend ")) {
		t.Fatal("tryToggleFoldToken() = false, want command panel input token handled")
	}
	if got := string(model.input); got != "/settings set sandbox.backend " {
		t.Fatalf("input = %q, want settings command", got)
	}
	if got := model.cursor; got != len(model.input) {
		t.Fatalf("cursor = %d, want end of input %d", got, len(model.input))
	}
}

func TestCommandPanelSettingsSelectClickPromptsAndSubmits(t *testing.T) {
	var called string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(sub Submission) TaskResultMsg {
			called = sub.Text
			return TaskResultMsg{}
		},
	})
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		SettingsPanel: &appviewmodel.SettingsPanelView{
			Sections: []appviewmodel.SettingsPanelSection{{
				Fields: []appviewmodel.SettingsPanelField{{
					ID:       "sandbox.backend",
					Label:    "Requested backend",
					Value:    "host",
					Editable: true,
					Options: []appviewmodel.SettingsPanelFieldOption{{
						Value: "host",
						Label: "Host",
					}, {
						Value: "macos-seatbelt",
						Label: "macOS Seatbelt",
					}},
				}},
			}},
		},
	})
	model.doc.Append(block)

	handled, cmd := model.tryCommandPanelClickToken(block.BlockID(), commandPanelInputClickToken("/settings set sandbox.backend "))
	if !handled || cmd == nil {
		t.Fatalf("tryCommandPanelClickToken() handled=%v cmd nil=%v, want prompt command", handled, cmd == nil)
	}
	if model.activePrompt == nil || len(model.activePrompt.choices) != 2 {
		t.Fatalf("active prompt = %#v, want two settings choices", model.activePrompt)
	}
	if got := model.activePrompt.choices[model.activePrompt.choiceIndex].value; got != "host" {
		t.Fatalf("default choice = %q, want host", got)
	}
	model.finishPrompt("macos-seatbelt", nil)
	msg := cmd()
	submit, ok := msg.(commandPanelSubmitMsg)
	if !ok {
		t.Fatalf("prompt command msg = %T, want commandPanelSubmitMsg", msg)
	}
	if submit.Line != "/settings set sandbox.backend macos-seatbelt" {
		t.Fatalf("submit line = %q", submit.Line)
	}
	updated, submitCmd := model.Update(submit)
	model = updated.(*Model)
	if !findAndRunTaskResult(submitCmd(), model) {
		t.Fatal("expected TaskResultMsg from submitted panel command")
	}
	if called != "/settings set sandbox.backend macos-seatbelt" {
		t.Fatalf("ExecuteLine called = %q", called)
	}
}

func TestCommandPanelTaskWriteClickPromptsAndSubmits(t *testing.T) {
	var called string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(sub Submission) TaskResultMsg {
			called = sub.Text
			return TaskResultMsg{}
		},
	})
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		TaskPanel: &appviewmodel.TaskPanelView{
			Supported: true,
			Tasks: []appviewmodel.TaskItem{{
				ID:            "task-1",
				Title:         "interactive command",
				SupportsInput: true,
			}},
			Actions: []appviewmodel.TaskPanelAction{{
				ID:            "task.write:task-1",
				Kind:          "write",
				Label:         "Write",
				TaskID:        "task-1",
				Enabled:       true,
				RequiresInput: true,
			}},
		},
	})
	model.doc.Append(block)

	handled, cmd := model.tryCommandPanelClickToken(block.BlockID(), commandPanelInputClickToken("/task write task-1 -- "))
	if !handled || cmd == nil {
		t.Fatalf("tryCommandPanelClickToken() handled=%v cmd nil=%v, want prompt command", handled, cmd == nil)
	}
	if model.activePrompt == nil || model.activePrompt.title != "Write to task" {
		t.Fatalf("active prompt = %#v, want task write prompt", model.activePrompt)
	}
	model.finishPrompt("continue", nil)
	submit, ok := cmd().(commandPanelSubmitMsg)
	if !ok {
		t.Fatal("prompt command did not submit task write line")
	}
	if submit.Line != "/task write task-1 -- continue" {
		t.Fatalf("submit line = %q", submit.Line)
	}
	updated, submitCmd := model.Update(submit)
	model = updated.(*Model)
	if !findAndRunTaskResult(submitCmd(), model) {
		t.Fatal("expected TaskResultMsg from submitted task write")
	}
	if called != "/task write task-1 -- continue" {
		t.Fatalf("ExecuteLine called = %q", called)
	}
}

func TestCommandPanelTaskCancelClickRequiresConfirmation(t *testing.T) {
	model := NewModel(Config{})
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		TaskPanel: &appviewmodel.TaskPanelView{
			Supported: true,
			Tasks:     []appviewmodel.TaskItem{{ID: "task-1", Title: "long command"}},
			Actions: []appviewmodel.TaskPanelAction{{
				ID:          "task.cancel:task-1",
				Kind:        "cancel",
				Label:       "Cancel",
				TaskID:      "task-1",
				Enabled:     true,
				Destructive: true,
			}},
		},
	})
	model.doc.Append(block)

	handled, cmd := model.tryCommandPanelClickToken(block.BlockID(), commandPanelInputClickToken("/task cancel task-1"))
	if !handled || cmd == nil {
		t.Fatalf("tryCommandPanelClickToken() handled=%v cmd nil=%v, want confirmation command", handled, cmd == nil)
	}
	if model.activePrompt == nil || len(model.activePrompt.choices) == 0 || model.activePrompt.choices[model.activePrompt.choiceIndex].value != "cancel" {
		t.Fatalf("active prompt = %#v, want cancel-default confirmation", model.activePrompt)
	}
	model.finishPrompt("cancel", nil)
	if msg := cmd(); msg != nil {
		t.Fatalf("cancel confirmation msg = %#v, want nil", msg)
	}

	handled, cmd = model.tryCommandPanelClickToken(block.BlockID(), commandPanelInputClickToken("/task cancel task-1"))
	if !handled || cmd == nil {
		t.Fatalf("second confirmation handled=%v cmd nil=%v", handled, cmd == nil)
	}
	model.finishPrompt("run", nil)
	submit, ok := cmd().(commandPanelSubmitMsg)
	if !ok {
		t.Fatal("run confirmation did not submit")
	}
	if submit.Line != "/task cancel task-1" {
		t.Fatalf("submit line = %q", submit.Line)
	}
}

func TestCommandPanelControllerConfigClickPromptsAndSubmits(t *testing.T) {
	var called string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(sub Submission) TaskResultMsg {
			called = sub.Text
			return TaskResultMsg{}
		},
	})
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		ControllerPanel: &appviewmodel.ControllerPanelView{
			Active:  true,
			Summary: appviewmodel.ControllerPanelSummary{Model: "gpt-remote"},
			Sections: []appviewmodel.ControllerPanelSection{{
				Fields: []appviewmodel.ControllerPanelField{{
					ID:       "controller.config.theme",
					Label:    "Theme",
					Value:    "light",
					Editable: true,
					Options: []appviewmodel.ControllerConfigChoice{{
						Value: "light",
						Name:  "Light",
					}, {
						Value: "dark",
						Name:  "Dark",
					}},
				}},
			}},
		},
	})
	model.doc.Append(block)

	handled, cmd := model.tryCommandPanelClickToken(block.BlockID(), commandPanelInputClickToken("/controller set theme "))
	if !handled || cmd == nil {
		t.Fatalf("tryCommandPanelClickToken() handled=%v cmd nil=%v, want controller config prompt", handled, cmd == nil)
	}
	if model.activePrompt == nil || model.activePrompt.title != "Set controller.config.theme" || len(model.activePrompt.choices) != 2 {
		t.Fatalf("active prompt = %#v, want controller config prompt", model.activePrompt)
	}
	model.finishPrompt("dark", nil)
	submit, ok := cmd().(commandPanelSubmitMsg)
	if !ok {
		t.Fatal("prompt command did not submit controller config line")
	}
	if submit.Line != "/controller set theme dark" {
		t.Fatalf("submit line = %q", submit.Line)
	}
	updated, submitCmd := model.Update(submit)
	model = updated.(*Model)
	if !findAndRunTaskResult(submitCmd(), model) {
		t.Fatal("expected TaskResultMsg from submitted controller config")
	}
	if called != "/controller set theme dark" {
		t.Fatalf("ExecuteLine called = %q", called)
	}
}

func TestCommandPanelControllerReasoningClickUsesCurrentModel(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands(), ExecuteLine: func(Submission) TaskResultMsg { return TaskResultMsg{} }})
	block := NewCommandPanelBlock(appviewmodel.CommandExecutionView{
		ControllerPanel: &appviewmodel.ControllerPanelView{
			Active:  true,
			Summary: appviewmodel.ControllerPanelSummary{Model: "gpt-remote", ReasoningEffort: "low"},
			Sections: []appviewmodel.ControllerPanelSection{{
				Fields: []appviewmodel.ControllerPanelField{{
					ID:       "controller.reasoning",
					Label:    "Reasoning",
					Value:    "low",
					Editable: true,
					Options: []appviewmodel.ControllerConfigChoice{{
						Value: "low",
						Name:  "Low",
					}, {
						Value: "high",
						Name:  "High",
					}},
				}},
			}},
		},
	})
	model.doc.Append(block)

	handled, cmd := model.tryCommandPanelClickToken(block.BlockID(), commandPanelInputClickToken("/model use gpt-remote "))
	if !handled || cmd == nil {
		t.Fatalf("tryCommandPanelClickToken() handled=%v cmd nil=%v, want controller reasoning prompt", handled, cmd == nil)
	}
	if model.activePrompt == nil || model.activePrompt.title != "Set controller.reasoning" {
		t.Fatalf("active prompt = %#v, want reasoning prompt", model.activePrompt)
	}
	model.finishPrompt("high", nil)
	submit, ok := cmd().(commandPanelSubmitMsg)
	if !ok {
		t.Fatal("prompt command did not submit controller reasoning line")
	}
	if submit.Line != "/model use gpt-remote high" {
		t.Fatalf("submit line = %q", submit.Line)
	}
}

func TestTranscriptTaskActionClickPromptsAndSubmits(t *testing.T) {
	var called string
	model := NewModel(Config{
		Commands: DefaultCommands(),
		ExecuteLine: func(sub Submission) TaskResultMsg {
			called = sub.Text
			return TaskResultMsg{}
		},
	})
	action := appviewmodel.TranscriptAction{
		ID:            "task.write:task-1",
		Kind:          "write",
		Label:         "Write",
		Command:       "/task write task-1 -- ",
		TargetID:      "task-1",
		Enabled:       true,
		RequiresInput: true,
	}

	handled, cmd := model.tryTranscriptActionClickToken(transcriptActionClickToken(action))
	if !handled || cmd == nil {
		t.Fatalf("tryTranscriptActionClickToken() handled=%v cmd nil=%v, want prompt command", handled, cmd == nil)
	}
	if model.activePrompt == nil || model.activePrompt.title != "Write" {
		t.Fatalf("active prompt = %#v, want transcript action prompt", model.activePrompt)
	}
	model.finishPrompt("continue", nil)
	submit, ok := cmd().(commandPanelSubmitMsg)
	if !ok {
		t.Fatal("prompt command did not submit transcript action")
	}
	if submit.Line != "/task write task-1 -- continue" {
		t.Fatalf("submit line = %q", submit.Line)
	}
	updated, submitCmd := model.Update(submit)
	model = updated.(*Model)
	if !findAndRunTaskResult(submitCmd(), model) {
		t.Fatal("expected TaskResultMsg from submitted transcript action")
	}
	if called != "/task write task-1 -- continue" {
		t.Fatalf("ExecuteLine called = %q", called)
	}
}

func TestTranscriptDestructiveTaskActionClickConfirms(t *testing.T) {
	model := NewModel(Config{Commands: DefaultCommands(), ExecuteLine: func(Submission) TaskResultMsg { return TaskResultMsg{} }})
	action := appviewmodel.TranscriptAction{
		ID:          "task.cancel:task-1",
		Kind:        "cancel",
		Label:       "Cancel",
		Command:     "/task cancel task-1",
		TargetID:    "task-1",
		Enabled:     true,
		Destructive: true,
	}

	handled, cmd := model.tryTranscriptActionClickToken(transcriptActionClickToken(action))
	if !handled || cmd == nil {
		t.Fatalf("tryTranscriptActionClickToken() handled=%v cmd nil=%v, want confirm command", handled, cmd == nil)
	}
	if model.activePrompt == nil || len(model.activePrompt.choices) == 0 || model.activePrompt.choices[model.activePrompt.choiceIndex].value != "cancel" {
		t.Fatalf("active prompt = %#v, want cancel-default confirmation", model.activePrompt)
	}
	model.finishPrompt("run", nil)
	submit, ok := cmd().(commandPanelSubmitMsg)
	if !ok {
		t.Fatal("confirmation did not submit transcript action")
	}
	if submit.Line != "/task cancel task-1" {
		t.Fatalf("submit line = %q", submit.Line)
	}
}

func TestViewportSelectionMotionDedupesSameEndpoint(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)
	m.viewportStyledLines = []string{"hello world"}
	m.viewportPlainLines = []string{"hello world"}
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 0, col: 0}
	m.selectionEnd = textSelectionPoint{line: 0, col: 5}
	version := m.viewportSelectionVersion

	_ = m.handleViewportMouseMotion(tea.Mouse{X: m.mainColumnX() + tuikit.GutterNarrative + 5, Y: 0})
	if got := m.viewportSelectionVersion; got != version {
		t.Fatalf("selection version after duplicate endpoint = %d, want %d", got, version)
	}

	_ = m.handleViewportMouseMotion(tea.Mouse{X: m.mainColumnX() + tuikit.GutterNarrative + 6, Y: 0})
	if got := m.viewportSelectionVersion; got != version+1 {
		t.Fatalf("selection version after changed endpoint = %d, want %d", got, version+1)
	}
}

func TestViewportWhitespaceSelectionDoesNotToggleFoldToken(t *testing.T) {
	model := NewModel(Config{
		WriteClipboardText: func(text string) error {
			if text != "  " {
				t.Errorf("clipboard text = %q, want two spaces", text)
			}
			return nil
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)

	block := NewParticipantTurnBlock("codex-001", "codex-001")
	block.UpdateToolWithMeta("ws-1", "lookup_weather", `"weather"`, strings.Join([]string{
		"result 01",
		"result 02",
		"result 03",
		"result 04",
		"result 05",
		"result 06",
	}, "\n"), true, false, ToolUpdateMeta{ToolKind: "other"})
	m.doc.Append(block)
	m.viewportStyledLines = []string{"   "}
	m.viewportPlainLines = []string{"   "}
	m.viewportBlockIDs = []string{block.BlockID()}
	m.viewportClickTokens = []string{acpToolPanelClickToken("ws-1")}
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 0, col: 0}
	m.selectionEnd = m.selectionStart

	cmd := m.handleViewportMouseRelease(tea.Mouse{
		Button: tea.MouseLeft,
		X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      0,
	})
	if cmd == nil {
		t.Fatal("whitespace selection should still produce a copy command")
	}
	if got, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("copy command returned %T, want clipboardCopyResultMsg", got)
	} else if got.err != nil {
		t.Fatalf("copy command returned error: %v", got.err)
	}
	if block.toolPanelFullOutput("ws-1") {
		t.Fatal("drag selection over a clickable row must not toggle the fold state")
	}
}

func TestImagePasteWhileRunningShowsFeedback(t *testing.T) {
	model := NewModel(Config{
		PasteClipboardImage: func() ([]string, string, error) {
			t.Fatal("PasteClipboardImage must not run while model is running")
			return nil, "", nil
		},
	})
	model.running = true

	updated, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Text: imagePasteKeysForPlatform(runtime.GOOS, isWSL())[0]}))
	m := updated.(*Model)
	if cmd == nil {
		t.Fatal("running image paste should schedule hint cleanup")
	}
	if !strings.Contains(m.hint, "image") && !strings.Contains(m.hint, "running") {
		t.Fatalf("model hint = %q, want image/running feedback", m.hint)
	}
}

func TestModeToggleRunsWhileRunning(t *testing.T) {
	called := false
	model := NewModel(Config{
		ToggleMode: func() (string, error) {
			called = true
			return "mode updated", nil
		},
	})
	model.running = true

	updated, cmd := model.handleKey(keyPress("shift+tab"))
	m := updated.(*Model)
	if !called {
		t.Fatal("ToggleMode was not called while running")
	}
	if cmd == nil {
		t.Fatal("mode toggle should schedule hint cleanup")
	}
	if !strings.Contains(m.hint, "mode updated") {
		t.Fatalf("model hint = %q, want mode update feedback", m.hint)
	}
}
