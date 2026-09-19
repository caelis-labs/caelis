package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/charmbracelet/x/ansi"
)

func modelPickerCandidates() []SlashArgCandidate {
	return []SlashArgCandidate{
		{Value: "custom/flash", Display: "Flash", ModelSelection: &appserver.ModelSelection{Efforts: []string{"none"}, Effort: "none"}},
		{Value: "openai/sol", Display: "Sol", ModelSelection: &appserver.ModelSelection{Efforts: []string{"low", "medium", "high"}, Effort: "high", FastSupported: true, Current: true, ContextWindowTokens: 272000}},
	}
}

func newModelPickerTestModel(t *testing.T) (*Model, *[]string) {
	t.Helper()
	var submitted []string
	model := NewModel(Config{
		Commands: DefaultCommands(), NoAnimation: true,
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = append(submitted, submission.Text)
			return TaskResultMsg{SuppressTurnDivider: true}
		},
		SlashArgComplete: func(_ context.Context, command, _ string, _ int) ([]SlashArgCandidate, error) {
			if command == "model" {
				return modelPickerCandidates(), nil
			}
			return nil, nil
		},
	})
	applySlashCompletionUpdate(t, model, tea.WindowSizeMsg{Width: 100, Height: 30})
	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	return model, &submitted
}

func TestModelPickerStagesThenSubmitsOnce(t *testing.T) {
	model, submitted := newModelPickerTestModel(t)
	if model.slashArgIndex != 1 || model.modelPickerDraft(model.slashArgCandidates[1]).effort != "high" {
		t.Fatal("picker did not restore Control's current model and effort")
	}
	for _, input := range []string{"left", "tab", "right"} {
		model.handleSlashArgKey(keyPress(input))
	}
	if len(*submitted) != 0 {
		t.Fatalf("navigation submitted %v", *submitted)
	}
	_, cmd := model.handleSlashArgKey(keyPress("enter"))
	if cmd == nil {
		t.Fatal("confirmation did not submit")
	}
	findAndRunTaskResult(cmd(), model)
	if got := strings.Join(*submitted, ","); got != "/model openai/sol medium fast" {
		t.Fatalf("submitted %q", got)
	}
	if model.modelPicker != nil || model.slashArgActive || model.textarea.Value() != "" {
		t.Fatal("confirmation left stale picker state or composer input")
	}
}

func TestModelPickerCancelDiscardsAllDrafts(t *testing.T) {
	model, submitted := newModelPickerTestModel(t)
	for _, input := range []string{"left", "tab", "right", "esc"} {
		runCompletionCmd(t, model, applySlashCompletionUpdate(t, model, keyPress(input)))
	}
	if model.modelPicker != nil || len(*submitted) != 0 {
		t.Fatal("cancel retained draft or submitted a command")
	}
	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	candidate, _ := model.currentSlashArgCandidate()
	if draft := model.modelPickerDraft(candidate); draft.effort != "high" || draft.fast {
		t.Fatalf("reopened selection retained edits: %+v", draft)
	}
	model.setInputText("hello")
	model.syncTextareaFromInput()
	model.syncSlashInputOverlayState()
	if model.modelPicker != nil {
		t.Fatal("removing /model retained draft state")
	}
}

func TestModelPickerSearchKeepsPrintableKeysAndExactModelInPanel(t *testing.T) {
	model, _ := newModelPickerTestModel(t)
	for _, r := range "flash" {
		runCompletionCmd(t, model, applySlashCompletionUpdate(t, model, keyPress(string(r))))
	}
	if got := model.textarea.Value(); got != "/model flash" {
		t.Fatalf("search input = %q", got)
	}
	if len(model.slashArgCandidates) != 1 || model.slashArgCommand != "model" {
		t.Fatalf("search left model panel: %q %+v", model.slashArgCommand, model.slashArgCandidates)
	}
	if handled, _ := model.handleSlashArgKey(keyPress("h")); handled {
		t.Fatal("plain h was consumed as a picker shortcut")
	}
}

func TestModelPickerNoneAndUnsupportedFast(t *testing.T) {
	model, submitted := newModelPickerTestModel(t)
	model.handleSlashArgKey(keyPress("up"))
	for _, input := range []string{"tab", "right"} {
		model.handleSlashArgKey(keyPress(input))
	}
	_, cmd := model.handleSlashArgKey(keyPress("enter"))
	findAndRunTaskResult(cmd(), model)
	if got := strings.Join(*submitted, ","); got != "/model custom/flash none" {
		t.Fatalf("submitted %q", got)
	}
}

func TestModelPickerEmptyErrorAndStaleCompletion(t *testing.T) {
	model, submitted := newModelPickerTestModel(t)
	model.setInputText("/model missing")
	model.syncTextareaFromInput()
	runCompletionCmd(t, model, model.requestCurrentSlashArgCompletion())
	if !strings.Contains(ansi.Strip(model.renderInputOverlay()), "No matching models") {
		t.Fatal("empty search did not show an actionable empty state")
	}
	if _, cmd := model.handleSlashArgKey(keyPress("enter")); cmd != nil || len(*submitted) != 0 {
		t.Fatal("empty search submitted a command")
	}
	model.cfg.SlashArgComplete = func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
		return nil, errors.New("offline")
	}
	runCompletionCmd(t, model, model.requestCurrentSlashArgCompletion())
	if !strings.Contains(ansi.Strip(model.renderInputOverlay()), "Enter to retry") {
		t.Fatal("completion failure did not show retry action")
	}
	_, retry := model.handleSlashArgKey(keyPress("enter"))
	if retry == nil {
		t.Fatal("retry did not request completion")
	}
	model.handleSlashArgKey(keyPress("esc"))
	runCompletionCmd(t, model, retry)
	if model.slashArgActive || model.modelPicker != nil {
		t.Fatal("late completion reopened a canceled picker")
	}
}

func TestModelPickerRenderedGeometryAndMouse(t *testing.T) {
	for _, size := range [][2]int{{140, 40}, {80, 24}, {60, 18}, {40, 16}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			model, submitted := newModelPickerTestModel(t)
			applySlashCompletionUpdate(t, model, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			_, geometry, ok := model.activeCompletionGeometry()
			if !ok {
				t.Fatal("missing geometry")
			}
			lines := strings.Split(ansi.Strip(model.renderInputOverlay()), "\n")
			if len(lines) != geometry.height {
				t.Fatalf("paint height=%d, mouse height=%d\n%s", len(lines), geometry.height, strings.Join(lines, "\n"))
			}
			if geometry.top+geometry.height != model.height-model.bottomSectionHeight() {
				t.Fatal("picker not anchored above composer")
			}
			for _, line := range lines {
				if displayColumns(line) > geometry.width {
					t.Fatalf("overflow %q", line)
				}
			}
			header := tea.Mouse{X: geometry.left + 1, Y: geometry.top, Button: tea.MouseLeft}
			model.handleMouse(tea.MouseClickMsg(header))
			model.handleMouse(tea.MouseReleaseMsg(header))
			if len(*submitted) != 0 {
				t.Fatal("header click submitted a model")
			}
			point := tea.Mouse{X: geometry.left + 1, Y: geometry.candidateTop, Button: tea.MouseLeft}
			model.handleMouse(tea.MouseClickMsg(point))
			_, cmd := model.handleMouse(tea.MouseReleaseMsg(point))
			if cmd == nil {
				t.Fatal("first painted candidate did not activate")
			}
			findAndRunTaskResult(cmd(), model)
			if got := strings.Join(*submitted, ","); got != "/model custom/flash none" {
				t.Fatalf("clicked %q", got)
			}
		})
	}
}

func TestModelPickerUsesMainPanePaintAndPointerOrigin(t *testing.T) {
	for _, layout := range []uipreferences.Layout{uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
		t.Run(string(layout), func(t *testing.T) {
			model, _ := newPaneTestModel(t)
			model.cfg.SlashArgComplete = func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
				return modelPickerCandidates(), nil
			}
			runTeaCmds(t, model, model.setSubagentLayout(layout))
			runCompletionCmd(t, model, model.openSlashArgPicker("model"))
			_, geometry, ok := model.activeCompletionGeometry()
			if !ok {
				t.Fatal("missing picker geometry")
			}
			main := model.workspaceLayout().main
			if geometry.top+geometry.height != main.y+main.height-model.bottomSectionHeight() || geometry.left+geometry.width > main.x+main.width {
				t.Fatalf("picker outside main pane: %+v in %+v", geometry, main)
			}
			frame := strings.Split(ansi.Strip(model.View().Content), "\n")
			for i := range geometry.candidateCount {
				row := geometry.candidateTop + i
				if !strings.Contains(frame[row], model.slashArgCandidates[geometry.windowStart+i].Display) {
					t.Fatalf("pointer row %d does not match painted model: %q", row, frame[row])
				}
			}
		})
	}
}

func TestModelPickerKeepsFastColumnAligned(t *testing.T) {
	model, _ := newModelPickerTestModel(t)
	candidates := modelPickerCandidates()
	candidates[0].ModelSelection.FastSupported = true
	model.applySlashArgCandidates("model", "", candidates, nil)
	var columns []int
	for _, line := range strings.Split(ansi.Strip(model.renderInputOverlay()), "\n") {
		if index := strings.Index(line, "Fast off"); index >= 0 {
			columns = append(columns, displayColumns(line[:index]))
		}
	}
	if len(columns) != 2 || columns[0] != columns[1] {
		t.Fatalf("Fast controls are not in one column: %v\n%s", columns, ansi.Strip(model.renderInputOverlay()))
	}
}
