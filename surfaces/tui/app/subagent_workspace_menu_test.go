package tuiapp

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestSubagentWorkspaceMenuHoverAndBorder(t *testing.T) {
	for _, menu := range []string{"agents", "layout"} {
		for _, noColor := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/noColor=%t", menu, noColor), func(t *testing.T) {
				m, _ := newPaneTestModel(t)
				m.applyTheme(tuikit.ResolveThemeWithState(true, noColor, colorprofile.TrueColor))
				m.setSubagentLayout(uipreferences.Right)
				m.openPaneMenu(menu)
				baseline := m.View().Content
				state := m.subagentOutputOverlay
				frame := ansi.Strip(m.renderPaneMenu())
				if !strings.HasPrefix(frame, "┌") || !strings.HasSuffix(frame, "┘") {
					t.Fatalf("menu omitted thin border: %q", frame)
				}
				originalCall, originalPreferences := state.callID, m.uiPreferences.value
				cacheRenders := m.subagentOutputViews[state.callID].renderCache.renders
				rect := state.menuRect
				point := tea.Mouse{X: rect.x + 2, Y: rect.y + 2}
				m.Update(tea.MouseMotionMsg(point))
				hovered := m.View().Content
				if state.menuIndex != 1 || hovered == baseline || state.menuRect != rect {
					t.Fatal("hover did not move highlight without moving the menu")
				}
				if state.callID != originalCall || m.uiPreferences.value != originalPreferences || m.subagentOutputViews[state.callID].renderCache.renders != cacheRenders {
					t.Fatal("hover activated a choice or rerendered the transcript")
				}
				for _, border := range []tea.Mouse{
					{X: rect.x + 2, Y: rect.y},
					{X: rect.x + 2, Y: rect.y + rect.height - 1},
					{X: rect.x, Y: rect.y + 2},
					{X: rect.x + rect.width - 1, Y: rect.y + 2},
				} {
					m.Update(tea.MouseMotionMsg(border))
					border.Button = tea.MouseLeft
					m.Update(tea.MouseClickMsg(border))
					border.Button = tea.MouseNone
					m.Update(tea.MouseReleaseMsg(border))
					if state.menu != menu || state.menuIndex != 1 {
						t.Fatal("border selected or activated a row")
					}
				}
				// Releasing on a different row must not activate either choice.
				point.Button = tea.MouseLeft
				m.Update(tea.MouseClickMsg(point))
				point.Button = tea.MouseNone
				point.Y--
				m.Update(tea.MouseMotionMsg(point))
				m.Update(tea.MouseReleaseMsg(point))
				if state.menu != menu {
					t.Fatal("cross-row release activated a choice")
				}
				point.Y++
				m.Update(tea.MouseMotionMsg(point))
				want := state.menuItems[1].value
				m.handlePaneMenuKey(tea.KeyPressMsg{Code: tea.KeyEnter})
				if menu == "agents" && m.subagentOutputOverlay.callID != want || menu == "layout" && string(m.uiPreferences.value.SubagentLayout) != want {
					t.Fatal("Enter did not activate the hovered option")
				}
			})
		}
	}
}

func TestSubagentWorkspaceMenuHoverKeepsScrolledRowsStill(t *testing.T) {
	m, _ := newPaneTestModel(t)
	for i := range 20 {
		view := m.ensureSubagentOutputView(fmt.Sprintf("spawn-review-%02d", i))
		view.taskHandle = fmt.Sprintf("review-%02d", i)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	m.openPaneMenu("agents")
	m.handlePaneMenuKey(tea.KeyPressMsg{Code: tea.KeyEnd})
	frames := []string{m.View().Content}
	state := m.subagentOutputOverlay
	offset := state.menuOffset
	if offset == 0 {
		t.Fatal("fixture did not scroll the menu")
	}
	point := tea.Mouse{X: state.menuRect.x + 2, Y: state.menuRect.y + state.menuInset}
	m.Update(tea.MouseMotionMsg(point))
	frames = append(frames, m.View().Content)
	if state.menuOffset != offset || state.menuIndex != offset {
		t.Fatal("hover shifted the visible rows under the pointer")
	}
	point.Button = tea.MouseWheelUp
	m.Update(tea.MouseWheelMsg(point))
	frames = append(frames, m.View().Content)
	if state.menuOffset != offset-1 || state.menuIndex != offset-1 {
		t.Fatal("wheel did not reveal the preceding row")
	}
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
	for i, frame := range frames {
		assertPhysicalFullscreenFrame(t, m.width, m.height, frame, updates[:i+1])
	}
}

func TestSubagentWorkspaceMenuFrameFitsSmallTerminals(t *testing.T) {
	for _, size := range [][2]int{{3, 3}, {8, 8}, {40, 12}, {80, 24}, {160, 48}} {
		m, _ := newPaneTestModel(t)
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m.openPaneMenu("agents")
		frame := m.renderPaneMenu()
		rect := m.subagentOutputOverlay.menuRect
		if rect.x < 0 || rect.y < 0 || rect.x+rect.width > size[0] || rect.y+rect.height > size[1] {
			t.Fatalf("menu outside %v: %+v", size, rect)
		}
		lines := strings.Split(frame, "\n")
		if len(lines) != rect.height {
			t.Fatalf("height=%d, hit rectangle=%d", len(lines), rect.height)
		}
		for _, line := range lines {
			if displayColumns(line) != rect.width {
				t.Fatalf("width=%d, hit rectangle=%d: %q", displayColumns(line), rect.width, ansi.Strip(line))
			}
		}
	}
}
