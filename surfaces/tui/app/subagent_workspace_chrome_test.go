package tuiapp

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestSubagentWorkspaceAvailableLayoutsAndRestore(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		want          []string
	}{
		{"wide-tall", 160, 48, []string{"overlay", "left", "right", "up", "down"}},
		{"wide-short", 160, 24, []string{"overlay", "left", "right"}},
		{"narrow-tall", 80, 48, []string{"overlay", "up", "down"}},
		{"small", 80, 24, []string{"overlay"}},
		{"too-short", 160, 12, []string{"overlay"}},
		{"too-narrow", 40, 60, []string{"overlay"}},
		{"horizontal-edge", 121, 20, []string{"overlay", "left", "right"}},
		{"below-width", 120, 20, []string{"overlay"}},
		{"vertical-edge", 60, 41, []string{"overlay", "up", "down"}},
		{"below-height", 60, 40, []string{"overlay"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newPaneTestModel(t)
			for _, mode := range []uipreferences.Layout{uipreferences.Right, uipreferences.Down} {
				m.setSubagentLayout(mode)
				preference := m.uiPreferences.value
				m.Update(tea.WindowSizeMsg{Width: tc.width, Height: tc.height})
				var choices []string
				for _, item := range m.buildPaneMenuItems(&subagentOutputOverlayState{menu: "layout"}) {
					if item.value != "resize" {
						choices = append(choices, item.value)
					}
				}
				if !reflect.DeepEqual(choices, tc.want) {
					t.Fatalf("layouts=%v want %v", choices, tc.want)
				}
				layout := m.workspaceLayout()
				if layout.split {
					for _, pane := range []paneRect{layout.main, layout.child} {
						if pane.width < 60 || pane.height < 20 {
							t.Fatalf("unusable pane: %+v", pane)
						}
					}
				} else if !m.workspace.childFocused {
					t.Fatal("overlay left focus on covered main pane")
				}
				m.openPaneMenu("layout")
				if len(tc.want) == 1 && m.subagentOutputOverlay.menu != "" {
					t.Fatal("single available layout opened a redundant picker")
				}
				m.Update(tea.WindowSizeMsg{Width: 160, Height: 48})
				if m.uiPreferences.value != preference || !m.workspaceLayout().split {
					t.Fatal("resize lost preferred layout or ratio")
				}
			}
		})
	}
}

func TestSubagentWorkspaceLayoutMenuResizeKeepsSelectedValue(t *testing.T) {
	m, _ := newPaneTestModel(t)
	m.setSubagentLayout(uipreferences.Right)
	m.openPaneMenu("layout")
	state := m.subagentOutputOverlay
	for i, item := range m.paneMenuItems(state) {
		if item.value == "down" {
			state.menuIndex = i
		}
	}
	m.renderPaneMenu()
	state.pressedItem = "menu:down"
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 48})
	if state.pressedItem != "" || state.menuItems[state.menuIndex].value != "down" {
		t.Fatal("resize retained pressed row or retargeted keyboard selection")
	}
	m.activatePaneMenu()
	if m.uiPreferences.value.SubagentLayout != uipreferences.Down || !m.workspaceLayout().split {
		t.Fatal("resized menu activated wrong layout")
	}
}

func TestSubagentWorkspaceChromeHoverAndClose(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		m, _ := newPaneTestModel(t)
		m.applyTheme(tuikit.ResolveThemeWithState(true, noColor, colorprofile.TrueColor))
		m.setSubagentLayout(uipreferences.Right)
		state := m.subagentOutputOverlay
		view := m.subagentOutputViews[state.callID]
		view.actor = "breeze[orbit]"
		state.editor.SetValue("retained draft")
		m.workspace.childFocused = false
		baseline := m.View()
		if baseline.MouseMode != tea.MouseModeAllMotion {
			t.Fatal("visible chrome does not request hover events")
		}
		title := m.renderPaneTitle(view, state.geometry.contentWidth)
		if plain := ansi.Strip(title); !strings.Contains(plain, "breeze[orbit] ≡") || !strings.Contains(plain, "◨") || !strings.Contains(plain, "[x]") || strings.Contains(plain, "Ctrl+") || strings.Contains(plain, "▾") {
			t.Fatalf("unexpected chrome: %q", plain)
		}
		if !noColor {
			muted := sgrForegroundCode(t, m.theme.MutedText)
			if got := normalizeInlineStyleText(textWithSGRForeground(title, muted)); got != "[orbit]" {
				t.Fatalf("binding contrast affected other title text: %q", got)
			}
		}
		layout, renders := m.workspaceLayout(), view.renderCache.renders
		for _, action := range state.headerActions {
			point := tea.Mouse{X: state.geometry.contentX + action.x, Y: state.geometry.headerY}
			m.Update(tea.MouseMotionMsg(point))
			hovered := m.View()
			if state.hoveredHeader != action.value || hovered.Content == baseline.Content || m.workspace.childFocused || m.workspaceLayout() != layout || view.renderCache.renders != renders {
				t.Fatalf("hover changed focus/history/layout or omitted feedback: %s", action.value)
			}
			m.Update(tea.MouseMotionMsg{X: layout.main.x + 2, Y: layout.main.y + 3})
			if state.hoveredHeader != "" {
				t.Fatal("leaving child retained hover")
			}
		}
		var closeAction paneHeaderAction
		for _, action := range state.headerActions {
			if action.value == "close" {
				closeAction = action
			}
		}
		// The opening bracket is clickable, not only the final character.
		point := tea.Mouse{X: state.geometry.contentX + closeAction.x + 1, Y: state.geometry.headerY, Button: tea.MouseLeft}
		m.Update(tea.MouseClickMsg(point))
		point.Button = tea.MouseNone
		m.Update(tea.MouseReleaseMsg(point))
		if m.subagentOutputOverlay != nil || m.View().MouseMode != tea.MouseModeCellMotion {
			t.Fatal("close did not hide pane and restore mouse mode")
		}
		m.openSubagentWorkspace()
		if m.subagentOutputOverlay.editor.Value() != "retained draft" || m.subagentOutputOverlay.hoveredHeader != "" {
			t.Fatal("reopen lost draft or retained hover")
		}
	}
}

func TestSubagentWorkspaceEmptyComposersAndBoundedHeader(t *testing.T) {
	m, _ := newPaneTestModel(t)
	state := m.subagentOutputOverlay
	if m.textarea.Placeholder != "" || state.editor.Placeholder != "" || strings.TrimSpace(ansi.Strip(m.renderPaneEditor(state, 60))) != ">" {
		t.Fatal("empty composer contains hint text")
	}
	view := m.subagentOutputViews[state.callID]
	view.taskHandle = strings.Repeat("审查", 20)
	view.actor = view.taskHandle + "[orbit]"
	for width := 1; width <= 90; width++ {
		title := m.renderPaneTitle(view, width)
		if displayColumns(title) != width {
			t.Fatalf("header width=%d, want %d: %q", displayColumns(title), width, ansi.Strip(title))
		}
		previousEnd := 0
		for _, action := range state.headerActions {
			if action.x < previousEnd || action.width < 1 || action.x+action.width > width {
				t.Fatalf("overlapping/out-of-bounds hit area: %+v at width %d", action, width)
			}
			previousEnd = action.x + action.width
		}
	}
}

func TestSubagentWorkspaceChromePhysicalFrames(t *testing.T) {
	for _, mode := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
		t.Run(string(mode), func(t *testing.T) {
			m, _ := newPaneTestModel(t)
			m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
			m.setSubagentLayout(mode)
			view := m.subagentOutputViews[m.subagentOutputOverlay.callID]
			view.actor = "breeze[orbit]"
			frames := []string{m.View().Content}
			for _, action := range m.subagentOutputOverlay.headerActions {
				g := m.subagentOutputOverlay.geometry
				m.Update(tea.MouseMotionMsg{X: g.contentX + action.x, Y: g.headerY})
				frames = append(frames, m.View().Content)
			}
			m.openPaneMenu("agents")
			frames = append(frames, m.View().Content)
			state := m.subagentOutputOverlay
			m.Update(tea.MouseMotionMsg{X: state.menuRect.x + state.menuInset + 1, Y: state.menuRect.y + state.menuInset + 1})
			frames = append(frames, m.View().Content)
			m.handlePaneMenuKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			m.openPaneMenu("layout")
			frames = append(frames, m.View().Content)
			m.Update(tea.MouseMotionMsg{X: state.menuRect.x + state.menuInset + 1, Y: state.menuRect.y + state.menuInset + 1})
			frames = append(frames, m.View().Content)
			m.handlePaneMenuKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			frames = append(frames, m.View().Content)
			updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
			for i, frame := range frames {
				assertPhysicalFullscreenFrame(t, m.width, m.height, frame, updates[:i+1])
			}
			t.Logf("title: %s", ansi.Strip(m.renderPaneTitle(view, m.subagentOutputOverlay.geometry.contentWidth)))
		})
	}
}
