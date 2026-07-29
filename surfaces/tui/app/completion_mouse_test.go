package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestSlashCompletionMouseHoverUsesSingleSelectionAndPinsWindow(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 40, numberedSlashCommands("command", 12))
	model.slashIndex = 10
	_, before, ok := model.activeCompletionGeometry()
	if !ok || before.windowStart == 0 {
		t.Fatalf("test requires a shifted completion window, got %+v", before)
	}
	point := completionMouseCandidatePoint(t, model, 0)

	_, _ = model.handleMouse(tea.MouseMotionMsg(point))
	_, after, ok := model.activeCompletionGeometry()
	if !ok {
		t.Fatal("completion geometry unavailable after hover")
	}
	if model.slashIndex != before.windowStart {
		t.Fatalf("slashIndex after hover = %d, want visible first index %d", model.slashIndex, before.windowStart)
	}
	if after.selected != model.slashIndex {
		t.Fatalf("geometry selection = %d, want single model selection %d", after.selected, model.slashIndex)
	}
	if after.windowStart != before.windowStart || after.windowEnd != before.windowEnd {
		t.Fatalf("visible window shifted on hover: before=%+v after=%+v", before, after)
	}
}

func TestSlashCompletionMouseHoverChangesOnlyRenderedSelection(t *testing.T) {
	model := NewModel(Config{
		Commands:     []string{"alpha", "bravo", "charlie"},
		ColorProfile: colorprofile.TrueColor,
		NoAnimation:  true,
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	model = updated.(*Model)
	model.setInputText("/")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	before := model.renderSlashCommandList()

	point := completionMouseCandidatePoint(t, model, 2)
	_, _ = model.handleMouse(tea.MouseMotionMsg(point))
	after := model.renderSlashCommandList()
	if model.slashIndex != 2 {
		t.Fatalf("slashIndex after hover = %d, want 2", model.slashIndex)
	}
	if before == after {
		t.Fatal("hover did not change the rendered selection style")
	}
	if ansi.Strip(before) != ansi.Strip(after) {
		t.Fatalf("hover changed completion text:\nbefore=%q\nafter=%q", ansi.Strip(before), ansi.Strip(after))
	}
}

func TestSlashCompletionMouseClickAppliesCandidate(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 40, []string{
		"alpha", "bravo", "charlie", "delta",
	})
	point := completionMouseCandidatePoint(t, model, 2)
	point.Button = tea.MouseLeft

	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	if model.selecting {
		t.Fatal("completion click leaked into viewport text selection")
	}
	point.Button = tea.MouseNone
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
	if got := model.textarea.Value(); got != "/charlie " {
		t.Fatalf("textarea after click = %q, want /charlie ", got)
	}
}

func TestSlashCompletionMouseWheelScrollsOverlayNotViewport(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 40, numberedSlashCommands("command", 12))
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("timeline row %03d", i)
	}
	model.viewport.SetContentLines(lines)

	point := completionMouseCandidatePoint(t, model, 0)
	point.Button = tea.MouseWheelDown
	startedAt := time.Unix(1, 0)
	for index := range 9 {
		handled, _ := model.handleCompletionMouseWheelAt(
			point,
			startedAt.Add(time.Duration(index)*completionWheelPulseInterval),
		)
		if !handled {
			t.Fatal("wheel over completion was not handled")
		}
	}
	if model.slashIndex != 9 {
		t.Fatalf("slashIndex after wheel = %d, want 9", model.slashIndex)
	}
	if got := model.viewport.YOffset(); got != 0 {
		t.Fatalf("viewport YOffset after wheel over completion = %d, want 0", got)
	}

	point.Button = tea.MouseWheelUp
	_, _ = model.handleCompletionMouseWheelAt(point, startedAt.Add(9*completionWheelPulseInterval))
	if model.slashIndex != 8 {
		t.Fatalf("slashIndex after wheel up = %d, want 8", model.slashIndex)
	}
}

func TestSlashCompletionMouseWheelCoalescesRapidPulses(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 40, numberedSlashCommands("command", 12))
	point := completionMouseCandidatePoint(t, model, 0)
	point.Button = tea.MouseWheelDown
	startedAt := time.Unix(1, 0)

	for _, elapsed := range []time.Duration{0, 5 * time.Millisecond, 10 * time.Millisecond} {
		handled, _ := model.handleCompletionMouseWheelAt(point, startedAt.Add(elapsed))
		if !handled {
			t.Fatal("wheel over completion was not handled")
		}
	}
	if model.slashIndex != 1 {
		t.Fatalf("selection after one rapid wheel pulse cluster = %d, want 1", model.slashIndex)
	}

	_, _ = model.handleCompletionMouseWheelAt(point, startedAt.Add(completionWheelPulseInterval))
	if model.slashIndex != 2 {
		t.Fatalf("selection after sustained wheel interval = %d, want 2", model.slashIndex)
	}

	point.Button = tea.MouseWheelUp
	_, _ = model.handleCompletionMouseWheelAt(point, startedAt.Add(completionWheelPulseInterval+time.Millisecond))
	if model.slashIndex != 1 {
		t.Fatalf("selection after immediate direction reversal = %d, want 1", model.slashIndex)
	}
}

func TestSlashCompletionMouseOutsideOverlayKeepsViewportWheel(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 40, numberedSlashCommands("command", 12))
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("timeline row %03d", i)
	}
	model.viewport.SetContentLines(lines)
	_, geometry, ok := model.activeCompletionGeometry()
	if !ok {
		t.Fatal("completion geometry unavailable")
	}
	point := tea.Mouse{
		Button: tea.MouseWheelDown,
		X:      geometry.left,
		Y:      maxInt(0, geometry.top-1),
	}

	_, _ = model.handleMouse(tea.MouseWheelMsg(point))
	if model.slashIndex != 0 {
		t.Fatalf("slashIndex after outside wheel = %d, want 0", model.slashIndex)
	}
	if got := model.viewport.YOffset(); got == 0 {
		t.Fatal("outside wheel did not reach the viewport")
	}
}

func TestSlashCompletionMouseClickWorksWithoutBorder(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 79, 30, []string{"alpha", "bravo", "charlie"})
	if model.overlayUsesBorder() {
		t.Fatal("test requires borderless completion overlay")
	}
	point := completionMouseCandidatePoint(t, model, 1)
	point.Button = tea.MouseLeft

	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
	if got := model.textarea.Value(); got != "/bravo " {
		t.Fatalf("textarea after borderless click = %q, want /bravo ", got)
	}
}

func TestSlashCompletionGeometryFitsShortTerminal(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 14, numberedSlashCommands("command", 12))
	_, geometry, ok := model.activeCompletionGeometry()
	if !ok {
		t.Fatal("completion geometry unavailable")
	}
	if geometry.top < 0 {
		t.Fatalf("completion top = %d, want non-negative", geometry.top)
	}
	if bottom := geometry.top + geometry.height; bottom > model.height-model.bottomSectionHeight() {
		t.Fatalf("completion bottom = %d, overlaps fixed bottom starting at %d", bottom, model.height-model.bottomSectionHeight())
	}
	if geometry.candidateCount <= 0 || geometry.candidateCount >= completionOverlayVisibleItems {
		t.Fatalf("visible candidates = %d, want a reduced positive short-terminal window", geometry.candidateCount)
	}
	if !geometry.scroll.Show || !geometry.scroll.CanDown {
		t.Fatalf("short-terminal scroll affordance = %+v, want visible downward scroll", geometry.scroll)
	}
}

func TestCompletionGeometryMatchesRenderedOverlay(t *testing.T) {
	for _, width := range []int{79, 120} {
		t.Run(fmt.Sprintf("width-%d", width), func(t *testing.T) {
			model := newSlashCompletionMouseTestModel(t, width, 30, numberedSlashCommands("command", 12))
			_, geometry, ok := model.activeCompletionGeometry()
			if !ok {
				t.Fatal("completion geometry unavailable")
			}
			rendered := model.renderInputOverlay()
			lines := strings.Split(rendered, "\n")
			if len(lines) != geometry.height {
				t.Fatalf("rendered height = %d, geometry height = %d", len(lines), geometry.height)
			}
			for index, line := range lines {
				if got := displayColumns(line); got > geometry.width {
					t.Fatalf("rendered row %d width = %d, geometry width = %d", index, got, geometry.width)
				}
			}
			point := tea.Mouse{
				X: geometry.left + geometry.width/2,
				Y: geometry.candidateTop,
			}
			if index, hit := geometry.candidateIndexAt(point); !hit || index != geometry.windowStart {
				t.Fatalf("first painted candidate missed geometry: index=%d hit=%v geometry=%+v", index, hit, geometry)
			}
		})
	}
}

func TestCompletionMouseHoverSupportsSharedOverlayKinds(t *testing.T) {
	tests := []struct {
		name     string
		open     func(*Model)
		selected func(*Model) int
	}{
		{
			name: "mention",
			open: func(model *Model) {
				model.mentionCandidates = []CompletionCandidate{
					{Value: "alpha.go"}, {Value: "bravo.go"},
				}
			},
			selected: func(model *Model) int { return model.mentionIndex },
		},
		{
			name: "resume",
			open: func(model *Model) {
				model.resumeActive = true
				model.resumeCandidates = []ResumeCandidate{
					{SessionID: "alpha"}, {SessionID: "bravo"},
				}
			},
			selected: func(model *Model) int { return model.resumeIndex },
		},
		{
			name: "slash argument",
			open: func(model *Model) {
				model.setInputText("/model ")
				model.syncTextareaFromInput()
				model.slashArgActive = true
				model.slashArgCommand = "model"
				model.slashArgCandidates = []SlashArgCandidate{
					{Value: "alpha"}, {Value: "bravo"},
				}
			},
			selected: func(model *Model) int { return model.slashArgIndex },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := newCompletionMouseTestModel(t, 120, 40, Config{})
			tt.open(model)
			point := completionMouseCandidatePoint(t, model, 1)

			_, _ = model.handleMouse(tea.MouseMotionMsg(point))
			if got := tt.selected(model); got != 1 {
				t.Fatalf("selection after hover = %d, want 1", got)
			}
		})
	}
}

func TestCompletionMouseClickAppliesSharedOverlayKinds(t *testing.T) {
	t.Run("mention", func(t *testing.T) {
		model := newCompletionMouseTestModel(t, 120, 40, Config{})
		model.setInputText("@")
		model.syncTextareaFromInput()
		model.mentionPrefix = "@"
		model.mentionStart = 0
		model.mentionEnd = 1
		model.mentionCandidates = []CompletionCandidate{
			{Value: "alpha.go"}, {Value: "bravo.go"},
		}

		clickCompletionCandidate(t, model, 1)
		if got := model.textarea.Value(); got != "@bravo.go " {
			t.Fatalf("mention click = %q, want @bravo.go ", got)
		}
	})

	t.Run("resume", func(t *testing.T) {
		var submitted []Submission
		model := newCompletionMouseTestModel(t, 120, 40, Config{
			ExecuteLine: func(submission Submission) TaskResultMsg {
				submitted = append(submitted, submission)
				return TaskResultMsg{}
			},
		})
		model.setInputText("/resume ")
		model.syncTextareaFromInput()
		model.resumeActive = true
		model.resumeCandidates = []ResumeCandidate{
			{SessionID: "alpha"}, {SessionID: "bravo"},
		}

		cmd := clickCompletionCandidate(t, model, 1)
		if cmd == nil || !findAndRunTaskResult(cmd(), model) {
			t.Fatal("resume click did not execute the existing Enter path")
		}
		if len(submitted) != 1 || submitted[0].Text != "/resume bravo" {
			t.Fatalf("resume submissions = %#v, want /resume bravo", submitted)
		}
	})

	t.Run("slash argument", func(t *testing.T) {
		candidates := []SlashArgCandidate{{Value: "alpha"}, {Value: "bravo"}}
		model := newCompletionMouseTestModel(t, 120, 40, Config{
			SlashArgComplete: func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
				return candidates, nil
			},
		})
		model.setInputText("/model ")
		model.syncTextareaFromInput()
		model.slashArgActive = true
		model.slashArgCommand = "model"
		model.slashArgCandidates = candidates

		clickCompletionCandidate(t, model, 1)
		if got := model.textarea.Value(); got != "/model bravo " {
			t.Fatalf("slash-arg click = %q, want /model bravo ", got)
		}
	})
}

func TestCompletionMousePressRejectsCandidateRefreshAtSameRow(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 40, []string{"alpha", "bravo"})
	point := completionMouseCandidatePoint(t, model, 0)
	point.Button = tea.MouseLeft
	_, _ = model.handleMouse(tea.MouseClickMsg(point))

	model.slashCandidates = []string{"/bravo", "/alpha"}
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
	if got := model.textarea.Value(); got != "/" {
		t.Fatalf("refreshed candidate was wrongly applied: %q", got)
	}
}

func TestCompletionMouseHoverRecoversAfterCandidateShrink(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 40, []string{"alpha", "bravo", "charlie"})
	point := completionMouseCandidatePoint(t, model, 2)
	_, _ = model.handleMouse(tea.MouseMotionMsg(point))
	model.slashCandidates = model.slashCandidates[:1]

	point = completionMouseCandidatePoint(t, model, 0)
	_, _ = model.handleMouse(tea.MouseMotionMsg(point))
	if model.slashIndex != 0 {
		t.Fatalf("selection after candidate shrink = %d, want 0", model.slashIndex)
	}
}

func TestCompletionMouseBlankChromeConsumesClick(t *testing.T) {
	model := newSlashCompletionMouseTestModel(t, 120, 40, []string{"alpha", "bravo"})
	_, geometry, ok := model.activeCompletionGeometry()
	if !ok {
		t.Fatal("completion geometry unavailable")
	}
	point := tea.Mouse{
		Button: tea.MouseLeft,
		X:      geometry.left + 1,
		Y:      geometry.top,
	}

	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	_, _ = model.handleMouse(tea.MouseReleaseMsg(point))
	if model.selecting {
		t.Fatal("overlay chrome click leaked into viewport selection")
	}
	if got := model.textarea.Value(); got != "/" {
		t.Fatalf("overlay chrome click changed textarea to %q", got)
	}
}

func newSlashCompletionMouseTestModel(t *testing.T, width int, height int, commands []string) *Model {
	t.Helper()
	model := newCompletionMouseTestModel(t, width, height, Config{})
	model.setCommands(commands)
	model.setInputText("/")
	model.syncTextareaFromInput()
	model.refreshSlashCommands()
	if len(model.slashCandidates) == 0 {
		t.Fatal("slash completion did not open")
	}
	return model
}

func newCompletionMouseTestModel(t *testing.T, width int, height int, cfg Config) *Model {
	t.Helper()
	cfg.NoColor = true
	cfg.NoAnimation = true
	model := NewModel(cfg)
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(*Model)
}

func completionMouseCandidatePoint(t *testing.T, model *Model, visibleRow int) tea.Mouse {
	t.Helper()
	_, geometry, ok := model.activeCompletionGeometry()
	if !ok {
		t.Fatal("completion geometry unavailable")
	}
	if visibleRow < 0 || visibleRow >= geometry.candidateCount {
		t.Fatalf("visible row %d outside completion geometry %+v", visibleRow, geometry)
	}
	point := tea.Mouse{
		X: geometry.left + maxInt(1, geometry.width/2),
		Y: geometry.candidateTop + visibleRow,
	}
	if index, hit := geometry.candidateIndexAt(point); !hit {
		t.Fatalf("candidate point %+v missed geometry %+v", point, geometry)
	} else if index != geometry.windowStart+visibleRow {
		t.Fatalf("candidate index = %d, want %d", index, geometry.windowStart+visibleRow)
	}
	return point
}

func clickCompletionCandidate(t *testing.T, model *Model, visibleRow int) tea.Cmd {
	t.Helper()
	point := completionMouseCandidatePoint(t, model, visibleRow)
	point.Button = tea.MouseLeft
	_, _ = model.handleMouse(tea.MouseClickMsg(point))
	_, cmd := model.handleMouse(tea.MouseReleaseMsg(point))
	return cmd
}
