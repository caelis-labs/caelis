package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
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
	model.handleSlashArgKey(keyPress("left"))
	if len(*submitted) != 0 {
		t.Fatalf("navigation submitted %v", *submitted)
	}
	_, cmd := model.handleSlashArgKey(keyPress("enter"))
	if cmd != nil || !model.isModelPickerSpeedStep() {
		t.Fatalf("Enter on Fast-supported model did not enter Step 2: cmd=%v step=%v", cmd != nil, model.isModelPickerSpeedStep())
	}
	speedFrame := ansi.Strip(model.renderInputOverlay())
	if !strings.Contains(speedFrame, "Standard") || !strings.Contains(speedFrame, "Fast") || strings.Contains(speedFrame, "(current)") {
		t.Fatalf("unexpected Step 2 speed frame:\n%s", speedFrame)
	}
	model.handleSlashArgKey(keyPress("down"))
	_, cmd = model.handleSlashArgKey(keyPress("enter"))
	if cmd == nil {
		t.Fatal("confirmation in Step 2 did not submit")
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
	for _, input := range []string{"left", "enter", "down", "esc"} {
		runCompletionCmd(t, model, applySlashCompletionUpdate(t, model, keyPress(input)))
	}
	if model.modelPicker == nil || model.isModelPickerSpeedStep() {
		t.Fatal("Esc from Step 2 did not return to Step 1")
	}
	candidate, _ := model.currentSlashArgCandidate()
	if draft := model.modelPickerDraft(candidate); draft.effort != "medium" {
		t.Fatalf("Esc from Step 2 lost Step 1 draft: %+v", draft)
	}
	runCompletionCmd(t, model, applySlashCompletionUpdate(t, model, keyPress("esc")))
	if model.modelPicker != nil || len(*submitted) != 0 {
		t.Fatal("cancel retained draft or submitted a command")
	}
	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	candidate, _ = model.currentSlashArgCandidate()
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
	if cmd == nil {
		t.Fatal("Enter on non-Fast model did not submit in one step")
	}
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

func TestModelPickerHoverAndEffortClick(t *testing.T) {
	model, submitted := newModelPickerTestModel(t)
	if model.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatal("model picker did not enable MouseModeAllMotion")
	}
	_ = model.renderInputOverlay()
	_, geometry, ok := model.activeCompletionGeometry()
	if !ok {
		t.Fatal("missing geometry")
	}
	model.handleMouse(tea.MouseMotionMsg(tea.Mouse{X: geometry.left + 3, Y: geometry.candidateTop}))
	if model.slashArgIndex != 0 {
		t.Fatalf("hover did not move selection to row 0: %d", model.slashArgIndex)
	}
	lines := strings.Split(ansi.Strip(model.renderInputOverlay()), "\n")
	rowLine := lines[geometry.candidateTop-geometry.top+1]
	idx := strings.Index(rowLine, "high")
	if idx < 0 {
		t.Fatalf("missing effort text on row 1:\n%s", strings.Join(lines, "\n"))
	}
	effortPoint := tea.Mouse{X: geometry.left + displayColumns(rowLine[:idx]), Y: geometry.candidateTop + 1, Button: tea.MouseLeft}
	model.handleMouse(tea.MouseClickMsg(effortPoint))
	_, cmd := model.handleMouse(tea.MouseReleaseMsg(effortPoint))
	if cmd != nil || len(*submitted) != 0 {
		t.Fatal("clicking effort submitted immediately")
	}
	if draft := model.modelPickerDraft(model.slashArgCandidates[1]); draft.effort != "medium" {
		t.Fatalf("clicking left side of effort = %q, want medium", draft.effort)
	}
}

func TestModelPickerShortSpeedStepOnlyClicksPaintedChoice(t *testing.T) {
	for _, next := range []string{"Standard", "Fast"} {
		t.Run(next, func(t *testing.T) {
			model, submitted := newModelPickerTestModel(t)
			model.slashArgCandidates[1].ModelSelection.Fast = next == "Standard"
			model.Update(tea.WindowSizeMsg{Width: 100, Height: 10})
			model.Update(keyPress("enter"))
			frame := ansi.Strip(model.View().Content)
			if !model.isModelPickerSpeedStep() || strings.Contains(frame, next) {
				t.Fatalf("the other speed should be offscreen initially:\n%s", frame)
			}
			model.Update(keyPress("down"))
			point := modelPickerPoint(t, model, "› "+next, next)
			model.Update(tea.MouseClickMsg(point))
			_, cmd := model.Update(tea.MouseReleaseMsg(point))
			if cmd == nil {
				t.Fatal("clicking the painted speed did not submit")
			}
			findAndRunTaskResult(cmd(), model)
			want := "/model openai/sol high"
			if next == "Fast" {
				want += " fast"
			}
			if len(*submitted) != 1 || (*submitted)[0] != want {
				t.Fatalf("submitted %q, want %q", *submitted, want)
			}
		})
	}
}

func TestModelPickerCancelsCrossRegionClick(t *testing.T) {
	for _, fast := range []bool{false, true} {
		t.Run(fmt.Sprintf("fast-supported=%t", fast), func(t *testing.T) {
			model, submitted := newModelPickerTestModel(t)
			model.slashArgCandidates[1].ModelSelection.FastSupported = fast
			name := modelPickerPoint(t, model, "› Sol", "Sol")
			effort := modelPickerPoint(t, model, "› Sol", "high")
			for _, pair := range [][2]tea.Mouse{{effort, name}, {name, effort}} {
				model.Update(tea.MouseClickMsg(pair[0]))
				model.Update(tea.MouseMotionMsg(pair[1]))
				_, cmd := model.Update(tea.MouseReleaseMsg(pair[1]))
				if cmd != nil || model.isModelPickerSpeedStep() || len(*submitted) != 0 {
					t.Fatal("cross-region drag confirmed the model")
				}
				if draft := model.modelPickerDraft(model.slashArgCandidates[1]); draft.effort != "high" {
					t.Fatalf("cross-region drag adjusted effort to %q", draft.effort)
				}
			}
		})
	}
}

func TestModelPickerDisambiguatesSameProviderEndpoints(t *testing.T) {
	for _, sameHost := range []bool{false, true} {
		t.Run(fmt.Sprintf("same-host=%t", sameHost), func(t *testing.T) {
			model, submitted := newModelPickerTestModel(t)
			model.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
			eastHost, westHost := "east.example", "west.example"
			if sameHost {
				westHost = eastHost
			}
			east := modelPickerEndpointCandidate("east", eastHost)
			west := modelPickerEndpointCandidate("west", westHost)
			model.applySlashArgCandidates("model", "", []SlashArgCandidate{east, west}, nil)
			eastLabel, westLabel := eastHost, westHost
			if sameHost {
				eastLabel, westLabel = east.Value, west.Value
			}
			left := modelPickerPoint(t, model, "openai/shared", eastLabel)
			right := modelPickerPoint(t, model, "openai/shared", westLabel)
			if left.Y == right.Y {
				t.Fatal("distinct endpoint labels were not rendered on separate rows")
			}
			model.Update(tea.MouseClickMsg(right))
			_, cmd := model.Update(tea.MouseReleaseMsg(right))
			if cmd == nil {
				t.Fatal("endpoint click did not submit")
			}
			findAndRunTaskResult(cmd(), model)
			if want := "/model " + west.Value + " none"; len(*submitted) != 1 || (*submitted)[0] != want {
				t.Fatalf("submitted %q, want %q", *submitted, want)
			}
		})
	}
}

func modelPickerEndpointCandidate(endpoint, host string) SlashArgCandidate {
	config := modelconfig.NormalizeConfig(modelconfig.Config{
		Provider: "openai", Model: "shared", EndpointID: "custom-" + endpoint,
		BaseURL: "https://" + host + "/" + endpoint,
	})
	return SlashArgCandidate{
		Value: modelconfig.PublicSelector(config), Display: modelconfig.DisplayName(config),
		Detail: modelconfig.ChoiceDetail(config), ModelConfigID: config.ID,
		ModelSelection: &appserver.ModelSelection{Efforts: []string{"none"}, Effort: "none"},
	}
}

func modelPickerPoint(t *testing.T, model *Model, rowLabel, target string) tea.Mouse {
	t.Helper()
	frame := ansi.Strip(model.View().Content)
	for y, line := range strings.Split(frame, "\n") {
		if x := strings.Index(line, target); x >= 0 && strings.Contains(line, rowLabel) {
			return tea.Mouse{X: displayColumns(line[:x]), Y: y, Button: tea.MouseLeft}
		}
	}
	t.Fatalf("target %q missing from row %q:\n%s", target, rowLabel, frame)
	return tea.Mouse{}
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

func TestModelPickerAlignsEffortStrictlyAndPaintsContinuousSelection(t *testing.T) {
	model, _ := newModelPickerTestModel(t)
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	candidates := []SlashArgCandidate{
		{Value: "a/short", Display: "short-model", ModelSelection: &appserver.ModelSelection{Efforts: []string{"low", "medium", "high"}, Effort: "medium"}},
		{Value: "b/long", Display: "very-long-model-name-that-must-truncate-before-effort", Detail: "magpie", ModelSelection: &appserver.ModelSelection{Efforts: []string{"low", "medium", "high", "xhigh", "max"}, Effort: "xhigh", Current: true}},
		{Value: "c/none", Display: "plain-model", ModelSelection: &appserver.ModelSelection{Efforts: []string{"none"}, Effort: "none"}},
	}
	model.modelPicker = nil
	model.applySlashArgCandidates("model", "", candidates, nil)
	applySlashCompletionUpdate(t, model, tea.WindowSizeMsg{Width: 96, Height: 30})
	styled := model.renderInputOverlay()
	plain := ansi.Strip(styled)
	for _, unwanted := range []string{"●", "models", "(current)", "Fast", "context"} {
		if strings.Contains(plain, unwanted) {
			t.Fatalf("Step 1 retained unwanted chrome %q:\n%s", unwanted, plain)
		}
	}
	var gaugeCols, effortCols []int
	for _, line := range strings.Split(plain, "\n") {
		if idx := strings.Index(line, "■"); idx >= 0 {
			gaugeCols = append(gaugeCols, displayColumns(line[:idx]))
		}
		for _, tok := range []string{"medium", "xhigh", "none"} {
			if idx := strings.Index(line, tok); idx >= 0 {
				effortCols = append(effortCols, displayColumns(line[:idx]))
				break
			}
		}
	}
	if len(gaugeCols) != 2 || gaugeCols[0] != gaugeCols[1] {
		t.Fatalf("rainbow gauges misaligned: %v\n%s", gaugeCols, plain)
	}
	if len(effortCols) != 3 || effortCols[0] != effortCols[1] || effortCols[1] != effortCols[2] {
		t.Fatalf("effort labels misaligned: %v\n%s", effortCols, plain)
	}

	selectedLine := styledLineContaining(t, styled, "› very-long-model")
	width := displayColumns(ansi.Strip(selectedLine))
	screen := uv.NewScreenBuffer(width, 1)
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(selectedLine).Draw(screen, screen.Bounds())
	for x := 2; x < width-2; x++ {
		cell := screen.CellAt(x, 0)
		if cell == nil || cell.Width == 0 {
			continue
		}
		if !colorInSet(cell.Style.Bg, []color.Color{model.theme.SelectionBg}) {
			t.Fatalf("selection bar has dark gap at x=%d in %q", x, ansi.Strip(selectedLine))
		}
	}
}

// TestModelPickerAppliesProfileLessProviderRow pins the shipped Control
// projection for a selectable provider model that has no ModelProfile: it
// reports its honest no-reasoning capability instead of reaching the picker
// without typed selection metadata. Such a row stays a first-class picker row
// that paints its single effort control and applies on Enter.
func TestModelPickerAppliesProfileLessProviderRow(t *testing.T) {
	legacy := SlashArgCandidate{
		Value: "ollama/legacy-flash", Display: "Legacy Flash", ModelConfigID: "ollama@default/ollama/legacy-flash",
		ModelSelection: &appserver.ModelSelection{Efforts: []string{"none"}, Effort: "none"},
	}
	var submitted []string
	model := NewModel(Config{
		Commands: DefaultCommands(), NoAnimation: true,
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submitted = append(submitted, submission.Text)
			return TaskResultMsg{SuppressTurnDivider: true}
		},
		SlashArgComplete: func(_ context.Context, command, _ string, _ int) ([]SlashArgCandidate, error) {
			if command != "model" {
				return nil, nil
			}
			return append(modelPickerCandidates(), legacy), nil
		},
	})
	applySlashCompletionUpdate(t, model, tea.WindowSizeMsg{Width: 100, Height: 30})
	runCompletionCmd(t, model, model.openSlashArgPicker("model"))
	if model.slashArgIndex != 1 {
		t.Fatalf("picker did not start on Control's current model: index=%d", model.slashArgIndex)
	}
	painted := false
	for _, line := range strings.Split(ansi.Strip(model.renderInputOverlay()), "\n") {
		if !strings.Contains(line, "Legacy Flash") {
			continue
		}
		painted = true
		if !strings.Contains(line, "none") || strings.Contains(line, "Fast") {
			t.Fatalf("profile-less row controls = %q", line)
		}
	}
	if !painted {
		t.Fatalf("profile-less row missing from painted picker:\n%s", ansi.Strip(model.renderInputOverlay()))
	}
	model.handleSlashArgKey(keyPress("down"))
	if draft := model.modelPickerDraft(legacy); draft.effort != "none" || draft.fast {
		t.Fatalf("profile-less draft = %+v", draft)
	}
	_, cmd := model.handleSlashArgKey(keyPress("enter"))
	if cmd == nil {
		t.Fatal("Enter on the profile-less provider row was dropped")
	}
	findAndRunTaskResult(cmd(), model)
	if got := strings.Join(submitted, ","); got != "/model ollama/legacy-flash none" {
		t.Fatalf("submitted %q", got)
	}
}

func TestModelPickerMixedEffortStepGaugeHasNoUnreachableSlots(t *testing.T) {
	candidates := []SlashArgCandidate{
		{
			Value: "codex/gpt-6-mini", Display: "codex/gpt-6-mini", ModelConfigID: "openai-codex/codex/gpt-6-mini", Detail: "openai-codex",
			ModelSelection: &appserver.ModelSelection{Efforts: []string{"low", "medium", "high"}, Effort: "high", Current: true},
		},
		{
			Value: "codex/gpt-6.2-sol", Display: "codex/gpt-6.2-sol", ModelConfigID: "openai-codex/codex/gpt-6.2-sol", Detail: "openai-codex",
			ModelSelection: &appserver.ModelSelection{Efforts: []string{"none", "low", "medium", "high", "xhigh"}, Effort: "low"},
		},
	}
	model, _ := newModelPickerTestModel(t)
	model.modelPicker = nil
	model.applySlashArgCandidates("model", "", candidates, nil)
	applySlashCompletionUpdate(t, model, tea.WindowSizeMsg{Width: 96, Height: 30})
	plain := ansi.Strip(model.renderInputOverlay())
	var miniLine, solLine string
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "codex/gpt-6-mini") {
			miniLine = line
		}
		if strings.Contains(line, "codex/gpt-6.2-sol") {
			solLine = line
		}
	}
	if !strings.Contains(miniLine, "■■■") || strings.Contains(miniLine, "□") {
		t.Fatalf("3-step model row rendered unreachable empty gauge slots: %q", miniLine)
	}
	if !strings.Contains(solLine, "■■□□□") {
		t.Fatalf("5-step model row missing expected gauge: %q", solLine)
	}
	miniEffortCol := displayColumns(miniLine[:strings.Index(miniLine, "high")])
	solEffortCol := displayColumns(solLine[:strings.Index(solLine, "low")])
	if miniEffortCol != solEffortCol {
		t.Fatalf("effort columns misaligned (%d vs %d):\n%s\n%s", miniEffortCol, solEffortCol, miniLine, solLine)
	}
}
