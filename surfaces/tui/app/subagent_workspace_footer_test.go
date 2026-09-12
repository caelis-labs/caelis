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

func TestSubagentWorkspaceFooterRightAnchors(t *testing.T) {
	m, _ := newPaneTestModel(t)
	m.setSubagentLayout(uipreferences.Right)
	state := m.subagentOutputOverlay
	for _, contextSize := range []uint64{0, 128000} {
		descriptor := m.subagentRosterTasks[state.callID]
		descriptor.ContextSize = contextSize
		m.subagentRosterTasks[state.callID] = descriptor
		for _, model := range []string{"test-model", strings.Repeat("模型👩‍💻", 30)} {
			descriptor.Model = model
			m.subagentRosterTasks[state.callID] = descriptor
			for width := 0; width <= 160; width++ {
				hideX := -1
				for _, mode := range []string{"main", "child", "draft", "menu", "resize", "drag"} {
					m.workspace.childFocused = mode != "main"
					m.workspace.resizing, m.workspace.dragging = mode == "resize", mode == "drag"
					state.menu = ""
					state.editor.SetValue("")
					if mode == "menu" {
						state.menu = "agents"
					}
					if mode == "draft" {
						state.editor.SetValue("draft\nsecond line")
					}
					footer := ansi.Strip(m.renderPaneFooter(state, width))
					if got := displayColumns(footer); got != width {
						t.Fatalf("%s width=%d got %d: %q", mode, width, got, footer)
					}
					if state.footerHideX < 0 || state.footerHideX+state.footerHideWidth > width {
						t.Fatalf("out-of-bounds Hide at width %d: %d+%d", width, state.footerHideX, state.footerHideWidth)
					}
					if hideX >= 0 && hideX != state.footerHideX {
						t.Fatalf("%s moved Hide at width %d: %d -> %d", mode, width, hideX, state.footerHideX)
					}
					hideX = state.footerHideX
					if width >= 60 {
						_, usage := m.paneStatusParts(state)
						wantRight := "F7 Hide"
						if usage != "" {
							wantRight += "  " + usage
						}
						if !strings.HasSuffix(footer, wantRight) || strings.Count(footer, "F7 Hide") != 1 {
							t.Fatalf("%s right edge at width %d: %q, want %q", mode, width, footer, wantRight)
						}
					}
				}
			}
		}
	}
}

func TestSubagentWorkspaceFooterHidePhysicalFrames(t *testing.T) {
	for _, mode := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
		for _, noColor := range []bool{false, true} {
			name := string(mode)
			if noColor {
				name += "/no-color"
			}
			t.Run(name, func(t *testing.T) {
				m, client := newPaneTestModel(t)
				m.applyTheme(tuikit.ResolveThemeWithState(true, noColor, colorprofile.TrueColor))
				m.setSubagentLayout(mode)
				if mode != uipreferences.Overlay && !m.workspaceLayout().split {
					t.Fatal("physical split fixture fell back to overlay")
				}
				state := m.subagentOutputOverlay
				view := m.subagentOutputViews[state.callID]
				descriptor := m.subagentRosterTasks[state.callID]
				state.editor.SetValue("retained draft\nsecond line")
				m.textarea.SetValue("parent draft")
				m.workspace.childFocused = mode == uipreferences.Overlay
				frames := []string{m.View().Content}
				g := state.geometry
				footer := sliceByDisplayColumns(ansi.Strip(strings.Split(frames[0], "\n")[g.footerY]), g.contentX, g.contentX+g.contentWidth)
				if !strings.HasSuffix(footer, "F7 Hide  12k / 128k · 9%") {
					t.Fatalf("footer geometry does not match rendered row: %q", footer)
				}
				point := tea.Mouse{X: g.contentX + state.footerHideX, Y: g.footerY}
				focused, renders := m.workspace.childFocused, view.renderCache.renders
				m.Update(tea.MouseMotionMsg(point))
				frames = append(frames, m.View().Content)
				if !state.footerHideHovered || frames[0] == frames[1] || m.workspace.childFocused != focused || view.renderCache.renders != renders {
					t.Fatal("footer hover omitted feedback or changed focus/history")
				}
				m.Update(tea.MouseMotionMsg{X: point.X - 1, Y: point.Y})
				if state.footerHideHovered {
					t.Fatal("leaving Hide retained hover")
				}
				point.Button = tea.MouseLeft
				m.Update(tea.MouseClickMsg(point))
				if m.subagentOutputOverlay == nil {
					t.Fatal("press hid pane before release")
				}
				point.X += state.footerHideWidth - 1
				point.Button = tea.MouseNone
				m.Update(tea.MouseReleaseMsg(point))
				frames = append(frames, m.View().Content)
				if m.subagentOutputOverlay != nil || m.workspace.childFocused || m.View().MouseMode != tea.MouseModeCellMotion {
					t.Fatal("footer Hide did not restore main pane")
				}
				if !reflect.DeepEqual(m.subagentRosterTasks[state.callID], descriptor) || m.subagentOutputCurrentStatus(view) != subagentOutputRunning || len(client.requests) != 0 {
					t.Fatal("hiding changed child execution or submitted input")
				}
				m.openSubagentWorkspace()
				frames = append(frames, m.View().Content)
				if m.subagentOutputOverlay != state || state.editor.Value() != "retained draft\nsecond line" || m.textarea.Value() != "parent draft" || state.footerHideHovered {
					t.Fatal("reopening lost composer state or retained hover")
				}
				updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
				for i, frame := range frames {
					assertPhysicalFullscreenFrame(t, m.width, m.height, frame, updates[:i+1])
				}
				t.Logf("footer: %s", footer)
			})
		}
	}
}

func TestSubagentWorkspaceFooterHideRequiresCompleteClick(t *testing.T) {
	for _, scenario := range []string{"release-only", "right-click", "right-release", "release-outside", "press-outside", "resize", "selection", "valid-left-release"} {
		t.Run(scenario, func(t *testing.T) {
			m, _ := newPaneTestModel(t)
			m.setSubagentLayout(uipreferences.Right)
			m.View()
			state := m.subagentOutputOverlay
			point := tea.Mouse{X: state.geometry.contentX + state.footerHideX, Y: state.geometry.footerY, Button: tea.MouseLeft}
			press, release := point, point
			switch scenario {
			case "right-click":
				press.Button, release.Button = tea.MouseRight, tea.MouseRight
			case "right-release":
				release.Button = tea.MouseRight
			case "release-outside":
				release.X += state.footerHideWidth
			case "press-outside":
				press.X--
			case "selection":
				press.Y = state.geometry.contentY
			}
			if scenario != "release-only" {
				m.Update(tea.MouseClickMsg(press))
			}
			if scenario == "resize" {
				m.Update(tea.WindowSizeMsg{Width: 180, Height: 50})
				m.View()
				release.X, release.Y = state.geometry.contentX+state.footerHideX, state.geometry.footerY
			}
			m.Update(tea.MouseReleaseMsg(release))
			if scenario == "valid-left-release" {
				if m.subagentOutputOverlay != nil {
					t.Fatal("matching left release did not hide pane")
				}
				return
			}
			if m.subagentOutputOverlay == nil || state.pressedItem != "" {
				t.Fatal("incomplete click hid pane or retained stale press")
			}
			m.Update(tea.MouseReleaseMsg(point))
			if m.subagentOutputOverlay == nil {
				t.Fatal("unmatched later release hid pane")
			}
		})
	}
}
