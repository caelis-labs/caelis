package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestThemePickerMouseHoverClickAndKeyboard(t *testing.T) {
	t.Setenv("CAELIS_THEME", "auto")
	for _, width := range []int{48, 79, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
			m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
			m.submitThemeCommand("/theme dracula")
			m.submitThemeCommand("/theme")
			initial := m.View()
			if initial.MouseMode != tea.MouseModeAllMotion {
				t.Fatal("open picker must request unpressed mouse motion")
			}
			point := themePickerNamedPoint(t, m, "Nord")
			if selected := themePickerNamedPoint(t, m, "Dracula"); selected.X != point.X {
				t.Fatal("selected and unselected theme names use different text columns")
			}
			version := m.viewportContentVersion
			m.Update(tea.MouseMotionMsg(point))
			hover := m.View()
			if m.themeName != "dracula" || m.viewportContentVersion != version || m.activePrompt.choices[m.activePrompt.choiceIndex].value != "dracula" {
				t.Fatal("hover changed the theme, preview selection, or transcript")
			}
			if initial.Content == hover.Content {
				t.Fatal("hover did not highlight the row")
			}
			assertThemePickerHoverFrame(t, m, point, initial.Content, hover.Content)

			point.Button = tea.MouseLeft
			m.Update(tea.MouseClickMsg(point))
			if m.themeName != "dracula" {
				t.Fatal("press previewed before a complete click")
			}
			m.Update(tea.MouseReleaseMsg(point))
			if m.themeName != "dracula" || m.activePrompt.choices[m.activePrompt.choiceIndex].value != "nord" {
				t.Fatal("click must select immediately without synchronously retheming")
			}
			settleThemePreview(t, m)
			if m.themeName != "nord" || m.themePicker == nil {
				t.Fatal("click must preview Nord and keep the picker open")
			}
			m.handleKey(keyPress("up"))
			settleThemePreview(t, m)
			if m.themeName != "dracula" || m.themePicker.hoverIndex != -1 {
				t.Fatal("keyboard preview did not take over from mouse")
			}
			m.Update(tea.MouseClickMsg(point))
			m.Update(tea.MouseReleaseMsg(point))
			m.handleKey(keyPress("esc"))
			if m.themeName != "dracula" || m.themePicker != nil || m.View().MouseMode != tea.MouseModeCellMotion {
				t.Fatal("cancel did not restore theme and mouse mode")
			}
		})
	}
}

func TestThemePickerMouseDragOutsideAndConfirmation(t *testing.T) {
	t.Setenv("CAELIS_THEME", "auto")
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.submitThemeCommand("/theme dracula")
	m.submitThemeCommand("/theme")
	nord := themePickerNamedPoint(t, m, "Nord")
	dracula := themePickerNamedPoint(t, m, "Dracula")
	for _, release := range []tea.Mouse{dracula, {X: 0, Y: 0, Button: tea.MouseLeft}} {
		m.Update(tea.MouseClickMsg(nord))
		m.Update(tea.MouseMotionMsg(release))
		m.Update(tea.MouseReleaseMsg(release))
		if m.themeName != "dracula" {
			t.Fatal("dragging away switched theme")
		}
	}
	m.Update(tea.MouseMotionMsg(nord))
	m.Update(tea.MouseMotionMsg(tea.Mouse{X: 0, Y: 0}))
	if m.themePicker.hoverIndex != -1 {
		t.Fatal("leaving the list retained hover")
	}
	right := nord
	right.Button = tea.MouseRight
	m.Update(tea.MouseClickMsg(right))
	m.Update(tea.MouseReleaseMsg(right))
	if m.themeName != "dracula" {
		t.Fatal("right click switched theme")
	}
	m.Update(tea.MouseClickMsg(nord))
	m.Update(tea.MouseReleaseMsg(nord))
	// Hover must not change what Enter confirms.
	m.Update(tea.MouseMotionMsg(dracula))
	version := m.viewportContentVersion
	m.handleKey(keyPress("enter"))
	if m.themeName != "nord" || m.activePrompt != nil {
		t.Fatal("Enter did not confirm the clicked preview")
	}
	if m.viewportContentVersion > version+1 {
		t.Fatal("confirming the current preview redundantly rebuilt the transcript")
	}
}

func TestThemePickerMouseTargetsTrackResizeAndShortWindow(t *testing.T) {
	t.Setenv("CAELIS_THEME", "auto")
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.submitThemeCommand("/theme")
	for _, size := range [][2]int{{120, 30}, {48, 16}, {79, 24}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for range len(m.activePrompt.choices) {
			m.handleKey(keyPress("down"))
			frame := m.View().Content
			layout := m.themePickerLayout()
			if got := strings.Count(m.renderThemePicker(), "\n") + 1; got != layout.height() || m.promptModalReservedHeight() != 0 {
				t.Fatalf("picker height: rendered %d, layout %d", got, layout.height())
			}
			g := m.themePicker.geometry
			for i := layout.start; i < layout.start+layout.count; i++ {
				point := tea.Mouse{X: g.choices.Min.X + 2, Y: g.choices.Min.Y + i - layout.start}
				lines := strings.Split(ansi.Strip(frame), "\n")
				if point.Y < 0 || point.Y >= len(lines) || !strings.Contains(lines[point.Y], m.activePrompt.choices[i].label) {
					t.Fatalf("size %v: hit target for %s does not match physical row:\n%s", size, m.activePrompt.choices[i].label, ansi.Strip(frame))
				}
				m.Update(tea.MouseMotionMsg(point))
				if m.themePicker.hoverIndex != i {
					t.Fatalf("hover row = %d, want %d", m.themePicker.hoverIndex, i)
				}
			}
		}
	}
}

func themePickerNamedPoint(t testing.TB, m *Model, label string) tea.Mouse {
	t.Helper()
	frame := ansi.Strip(m.View().Content)
	for y, line := range strings.Split(frame, "\n") {
		if index := strings.Index(line, label); index >= 0 {
			return tea.Mouse{X: displayColumns(line[:index]), Y: y, Button: tea.MouseLeft}
		}
	}
	t.Fatalf("theme %q not visible:\n%s", label, frame)
	return tea.Mouse{}
}

func assertThemePickerHoverFrame(t *testing.T, m *Model, point tea.Mouse, frames ...string) {
	t.Helper()
	terminal := vt.NewSafeEmulator(m.width, m.height)
	defer terminal.Close()
	for _, output := range renderFullscreenFramesForTest(t, m.width, m.height, frames...) {
		if _, err := terminal.Write([]byte(output)); err != nil {
			t.Fatal(err)
		}
	}
	want := colorprofile.ANSI256.Convert(m.theme.SelectionStyle().GetBackground())
	wr, wg, wb, _ := want.RGBA()
	g := m.themePicker.geometry
	for x := g.choices.Min.X; x < g.choices.Max.X; x++ {
		cell := terminal.CellAt(x, point.Y)
		if cell == nil || cell.Style.Bg == nil {
			t.Fatalf("hover left row cell %d unpainted", x)
		}
		r, green, b, _ := cell.Style.Bg.RGBA()
		if r != wr || green != wg || b != wb {
			t.Fatalf("hover background at %d,%d = %v, want %v", x, point.Y, cell.Style.Bg, want)
		}
	}
}
