package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestThemePreviewCoalescesNavigationAfterSelectionFrame(t *testing.T) {
	m := newThemeApplyPerformanceModel(t)
	m.submitThemeCommand("/theme dracula")
	m.submitThemeCommand("/theme")
	frames := []string{m.View().Content}
	version, renders := m.viewportContentVersion, m.diag.GlamourRenderCalls
	var pending []themePreviewMsg
	for range 3 {
		_, cmd := m.Update(keyPress("up"))
		if cmd == nil {
			t.Fatal("selection did not schedule a preview")
		}
		pending = append(pending, currentThemePreview(m))
		frames = append(frames, m.View().Content)
		if m.themeName != "dracula" || m.viewportContentVersion != version || m.diag.GlamourRenderCalls != renders {
			t.Fatal("navigation synchronously rethemed the transcript")
		}
	}
	if m.activePrompt.choices[m.activePrompt.choiceIndex].value != "catppuccin" {
		t.Fatal("selection did not immediately follow navigation")
	}
	point := themePickerNamedPoint(t, m, "Catppuccin")
	assertThemePickerHoverFrame(t, m, point, frames...)
	for _, msg := range pending[:2] {
		m.Update(msg)
	}
	if m.themeName != "dracula" || m.viewportContentVersion != version {
		t.Fatal("superseded preview repainted the transcript")
	}
	m.Update(pending[2])
	if m.themeName != "catppuccin" || m.viewportContentVersion == version {
		t.Fatal("latest preview did not apply")
	}
	m.Update(pending[0])
	if m.themeName != "catppuccin" {
		t.Fatal("out-of-order preview restored a stale theme")
	}
}

func TestThemePreviewTimerDelaysColorChange(t *testing.T) {
	m := newThemePreviewTestModel(t)
	start := time.Now()
	_, cmd := m.Update(keyPress("down"))
	if cmd == nil || m.themeName != "auto" {
		t.Fatal("selection must schedule, not immediately apply, the preview")
	}
	msg := cmd()
	if _, ok := msg.(themePreviewMsg); !ok {
		t.Fatalf("preview timer returned %T", msg)
	}
	if time.Since(start) < themePreviewDelay-20*time.Millisecond {
		t.Fatal("preview timer fired before the selection could paint")
	}
	m.Update(msg)
	if m.themeName != "catppuccin" {
		t.Fatal("timer did not apply the selected theme")
	}
}

func TestThemePreviewInvalidatedByFinishReopenAndProfileChange(t *testing.T) {
	for _, action := range []string{"cancel", "confirm", "reopen", "profile", "return-to-current"} {
		t.Run(action, func(t *testing.T) {
			m := newThemePreviewTestModel(t)
			m.Update(keyPress("down"))
			old := currentThemePreview(m)
			want := "auto"
			switch action {
			case "cancel":
				m.Update(keyPress("esc"))
			case "confirm":
				m.Update(keyPress("enter"))
				want = "catppuccin"
			case "reopen":
				m.Update(keyPress("esc"))
				m.submitThemeCommand("/theme")
				m.Update(keyPress("down"))
				if currentThemePreview(m).generation != old.generation {
					t.Fatal("test must cover equal generations from distinct picker instances")
				}
			case "profile":
				m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI})
			case "return-to-current":
				m.Update(keyPress("up"))
			}
			m.Update(old)
			if m.themeName != want {
				t.Fatalf("%s let a stale preview change theme to %s", action, m.themeName)
			}
			if action == "reopen" {
				settleThemePreview(t, m)
				if m.themeName != "catppuccin" {
					t.Fatal("new picker lost its own pending preview")
				}
			}
		})
	}
}

func TestThemePickerCenteredWithoutDisplacingTranscript(t *testing.T) {
	t.Setenv("CAELIS_THEME", "dracula")
	for _, size := range [][2]int{{160, 50}, {120, 40}, {79, 24}, {48, 16}, {120, 10}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			m := NewModel(Config{NoAnimation: true, ColorProfile: colorprofile.TrueColor})
			m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			beforeHeight := m.viewport.Height()
			m.submitThemeCommand("/theme")
			view := m.View().Content
			layout := m.themePickerLayout()
			layoutHeight := 2*layout.border + 3 + layout.count + len(layout.hints)
			x, y := (size[0]-layout.width)/2, (size[1]-layoutHeight)/2
			modal := strings.Split(ansi.Strip(m.renderThemePicker()), "\n")
			frame := strings.Split(ansi.Strip(view), "\n")
			if m.viewport.Height() != beforeHeight || m.promptModalReservedHeight() != 0 {
				t.Fatal("centered picker displaced the transcript or composer")
			}
			for row, want := range modal {
				got := sliceByDisplayColumns(frame[y+row], x, x+layout.width)
				if got != want {
					t.Fatalf("picker not centered at %d,%d row %d: got %q, want %q", x, y, row, got, want)
				}
			}
			for _, forbidden := range []string{"Dark palette", "Light palette", "terminal:"} {
				if strings.Contains(strings.Join(modal, "\n"), forbidden) {
					t.Fatalf("picker retained %q", forbidden)
				}
			}
			if size[0] >= 120 && layout.width != 96 {
				t.Fatalf("wide-screen picker width = %d, want 96", layout.width)
			}
		})
	}
}

func newThemePreviewTestModel(t *testing.T) *Model {
	t.Helper()
	t.Setenv("CAELIS_THEME", "auto")
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.submitThemeCommand("/theme")
	return m
}

func currentThemePreview(m *Model) themePreviewMsg {
	return themePreviewMsg{picker: m.themePicker, generation: m.themePicker.previewGeneration}
}

// Deliver the scheduled event without making every interaction test wait for a
// wall-clock timer. TestThemePreviewTimerDelaysColorChange covers the real timer.
func settleThemePreview(t *testing.T, m *Model) {
	t.Helper()
	if m.themePicker == nil {
		t.Fatal("no picker to preview")
	}
	m.Update(currentThemePreview(m))
}
