package tuiapp

import (
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestSubagentWorkspaceKeyboardJourney(t *testing.T) {
	m, client := newPaneTestModel(t)
	m.closeSubagentOutputOverlay()
	m.textarea.SetValue("parent draft")
	press := func(code rune, mod tea.KeyMod) {
		t.Helper()
		_, cmd := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
		runTeaCmds(t, m, cmd)
	}
	press(tea.KeyF6, 0)
	if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.menu != "" || !m.workspace.childFocused {
		t.Fatal("F6 did not directly open and focus child")
	}
	press(tea.KeyF6, 0)
	if !m.workspace.childFocused {
		t.Fatal("F6 focused the covered main pane")
	}
	press(tea.KeyEscape, 0)
	if m.subagentOutputOverlay == nil {
		t.Fatal("Esc hid child instead of leaving it open")
	}
	press(tea.KeyF7, 0)
	press('g', tea.ModCtrl)
	if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.menu != "agents" {
		t.Fatal("Ctrl+G cannot open from main")
	}
	press(tea.KeyEnd, 0)
	press(tea.KeyEnter, 0)
	target := m.subagentOutputOverlay.callID
	press('l', tea.ModCtrl)
	press(tea.KeyHome, 0)
	press(tea.KeyDown, 0)
	press(tea.KeyDown, 0)
	press(tea.KeyEnter, 0)
	if m.workspace.preferences.SubagentLayout != uipreferences.Right {
		t.Fatal("keyboard layout selection failed")
	}
	m.Update(tea.PasteMsg{Content: "child draft"})
	press('j', tea.ModCtrl)
	press(tea.KeyEnter, tea.ModShift)
	if got := m.subagentOutputOverlay.editor.Value(); got != "child draft\n\n" {
		t.Fatalf("newline = %q", got)
	}
	press(tea.KeyF6, 0)
	if m.workspace.childFocused {
		t.Fatal("F6 did not focus main")
	}
	press(tea.KeyF6, tea.ModShift)
	if !m.workspace.childFocused {
		t.Fatal("Shift+F6 did not focus child")
	}
	press(tea.KeyEnter, 0)
	if len(client.requests) != 1 || client.requests[0].TaskID != m.subagentRosterTasks[target].TaskID || client.requests[0].Input != "child draft" {
		t.Fatalf("wrong keyboard dispatch: %#v", client.requests)
	}
	if m.textarea.Value() != "parent draft" {
		t.Fatal("child journey changed parent draft")
	}
	press(tea.KeyF7, 0)
	if m.subagentOutputOverlay != nil {
		t.Fatal("F7 did not hide child")
	}
	press(tea.KeyF6, 0)
	if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.callID != target ||
		!m.workspace.childFocused || !m.workspaceLayout().split {
		t.Fatal("F6 did not restore the selected child and split layout")
	}
}

func TestSubagentWorkspaceKeyboardResizePreview(t *testing.T) {
	for _, mode := range []uipreferences.Layout{uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
		t.Run(string(mode), func(t *testing.T) {
			m, client := newPaneTestModel(t)
			runTeaCmds(t, m, m.setSubagentLayout(mode))
			original := m.workspace.preferences
			m.openPaneMenu("layout")
			m.handlePaneMenuKey(tea.KeyPressMsg{Code: tea.KeyEnd})
			m.activatePaneMenu()
			if !m.workspace.resizing {
				t.Fatal("resize not keyboard reachable")
			}
			code := tea.KeyRight
			if mode == uipreferences.Up || mode == uipreferences.Down {
				code = tea.KeyDown
			}
			for range 20 {
				m.handlePaneResizeKey(tea.KeyPressMsg{Code: code})
			}
			if m.workspace.resizeRatio == m.paneSplitRatio() || m.workspace.preferences != original || client.preferences != original {
				t.Fatal("preview did not move or changed committed preferences")
			}
			m.Update(tea.PasteMsg{Content: "must not enter composer"})
			if m.subagentOutputOverlay.editor.Value() != "" {
				t.Fatal("resize paste leaked")
			}
			// An older preference save must never serialize the unconfirmed preview.
			runTeaCmds(t, m, m.savePanePreferences())
			if client.preferences != original {
				t.Fatal("preview escaped through pending save")
			}
			m.handlePaneResizeKey(tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.workspace.preferences != original || m.workspace.resizing {
				t.Fatal("cancel failed")
			}
			m.beginPaneResize()
			m.handlePaneResizeKey(tea.KeyPressMsg{Code: code})
			runTeaCmds(t, m, m.handlePaneResizeKey(tea.KeyPressMsg{Code: tea.KeyEnter}))
			if client.preferences != m.workspace.preferences || client.preferences == original {
				t.Fatal("apply did not persist")
			}
			saved := client.preferences
			m.beginPaneResize()
			m.handlePaneResizeKey(tea.KeyPressMsg{Code: code})
			m.Update(tea.WindowSizeMsg{Width: 40, Height: 16})
			if m.workspace.resizing || m.workspace.preferences != saved || !m.workspace.childFocused {
				t.Fatal("terminal resize retained preview/hidden focus")
			}
		})
	}
}

func TestSubagentWorkspaceModalKeysAndPasteDoNotLeak(t *testing.T) {
	m, _ := newPaneTestModel(t)
	m.openPaneMenu("agents")
	m.Update(tea.PasteMsg{Content: "menu paste"})
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if m.subagentOutputOverlay.editor.Value() != "" {
		t.Fatal("menu input leaked")
	}
	m.subagentOutputOverlay.menu = ""
	m.activePrompt = newPromptState(PromptRequestMsg{Prompt: "Approval", Response: make(chan PromptResponse, 1)})
	for _, msg := range []tea.Msg{tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: tea.KeyF6}, tea.PasteMsg{Content: "approval input"}} {
		m.Update(msg)
	}
	if m.subagentOutputOverlay.menu != "" || m.subagentOutputOverlay.editor.Value() != "" {
		t.Fatal("approval leaked workspace input")
	}
}

func TestSubagentWorkspaceChromeAndSharedActivity(t *testing.T) {
	m, _ := newPaneTestModel(t)
	m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
	state := m.subagentOutputOverlay
	state.editor.SetValue("中文 draft")
	for _, mode := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Right, uipreferences.Down} {
		m.setSubagentLayout(mode)
		frame := m.renderSubagentOutputOverlay()
		if strings.Contains(ansi.Strip(m.renderPaneEditor(state, state.layout.innerWidth)), "Send") {
			t.Fatal("keys inside composer")
		}
		footer := ansi.Strip(strings.Split(frame, "\n")[state.layout.frameHeight-state.layout.borderInset-1])
		for _, want := range []string{"test-model", "Send", "12k / 128k"} {
			if !strings.Contains(footer, want) {
				t.Fatalf("footer omitted %q: %s", want, footer)
			}
		}
		if !strings.Contains(m.renderPaneEditor(state, state.layout.innerWidth), sgrBackgroundCode(t, m.theme.ComposerFocusBg)) {
			t.Fatal("composer has no own surface")
		}
		if themeColorCacheKey(m.theme.ComposerFocusBg) == themeColorCacheKey(m.theme.ModalBg) {
			t.Fatal("composer blends into overlay")
		}
	}
	now := time.Now()
	activity := runningActivityState{Phase: runningPhaseToolWait, Target: runningTargetShell, StartedAt: now.Add(-10 * time.Second)}
	m.runningActivity = activity
	if got, want := m.renderActivityHint(activity, now, maxInt(1, m.fixedRowContentWidth()-2), 0), m.buildRunningHintTextAt(now); got != want {
		t.Fatal("main/child activity presentation diverges")
	}
	descriptor := m.subagentRosterTasks[state.callID]
	descriptor.Running = false
	descriptor.State = "completed"
	m.subagentRosterTasks[state.callID] = descriptor
	if got := m.renderPaneHint(m.subagentOutputViews[state.callID], state, 80); got != "" {
		t.Fatalf("idle hint=%q", got)
	}
}

func TestSubagentWorkspaceSplitCompositionMatchesCells(t *testing.T) {
	for _, mode := range []uipreferences.Layout{uipreferences.Right, uipreferences.Left, uipreferences.Down, uipreferences.Up} {
		t.Run(string(mode), func(t *testing.T) {
			m, _ := newPaneTestModel(t)
			m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
			m.setSubagentLayout(mode)
			layout := m.workspaceLayout()
			var cache splitWorkspaceFrameCache
			for index := range 5 {
				style := lipgloss.NewStyle().Foreground(lipgloss.Color("#aabbcc")).Background(lipgloss.Color("#223344"))
				mainLine := normalizeFullscreenFrameLine(style.Render("中文 👩‍💻 é "+strings.Repeat("中", 70)), layout.main.width)
				baseRows := make([]string, m.height)
				for y := range baseRows {
					baseRows[y] = strings.Repeat(" ", m.width)
					if y >= layout.main.y && y < layout.main.y+layout.main.height {
						baseRows[y] = strings.Repeat(" ", layout.main.x) + mainLine + strings.Repeat(" ", m.width-layout.main.x-layout.main.width)
					}
				}
				base := strings.Join(baseRows, "\n")
				child := m.renderSubagentOutputOverlay()
				divider := m.paneDivider(layout.divider)
				got := cache.compose(base, child, divider, layout)
				want := tuikit.OverlayAt(base, child, m.width, m.height, layout.child.x, layout.child.y)
				if layout.divider.width == 1 {
					divider = strings.TrimSuffix(strings.Repeat(divider+"\n", layout.divider.height), "\n")
				}
				want = tuikit.OverlayAt(want, divider, m.width, m.height, layout.divider.x, layout.divider.y)
				a, b := uv.NewScreenBuffer(m.width, m.height), uv.NewScreenBuffer(m.width, m.height)
				a.Method, b.Method = ansi.GraphemeWidth, ansi.GraphemeWidth
				uv.NewStyledString(got).Draw(a, a.Bounds())
				uv.NewStyledString(want).Draw(b, b.Bounds())
				for y := range m.height {
					for x := range m.width {
						if !reflect.DeepEqual(a.CellAt(x, y), b.CellAt(x, y)) {
							t.Fatalf("composition differs at %d,%d: got=%#v want=%#v", x, y, a.CellAt(x, y), b.CellAt(x, y))
						}
					}
				}
				subagentPerformanceWheel(m, index)
			}
		})
	}
}

func TestSubagentWorkspacePaintCacheAcrossSurfaces(t *testing.T) {
	m := newSubagentOutputPerformanceModel(t, 160, 48, 3)
	m.subagentOutputViews[m.subagentOutputOverlay.callID].block.Status = "completed"
	for _, mode := range []uipreferences.Layout{uipreferences.Right, uipreferences.Left, uipreferences.Overlay, uipreferences.Down, uipreferences.Up} {
		m.setSubagentLayout(mode)
		for index := range 4 {
			subagentPerformanceWheel(m, index)
			cached := m.renderSubagentOutputOverlay()
			view := m.subagentOutputViews[m.subagentOutputOverlay.callID]
			renders := view.renderCache.renders
			before := view.renderCache.paintedRows
			if strings.Join(before, "") == "" {
				t.Fatal("paint cache empty")
			}
			clear(view.renderCache.paintedRows)
			fresh := m.renderSubagentOutputOverlay()
			if cached != fresh {
				aa, bb := strings.Split(cached, "\n"), strings.Split(fresh, "\n")
				for j := range aa {
					if aa[j] != bb[j] {
						t.Fatalf("%s cached row %d differs: got=%q fresh=%q", mode, j, aa[j], bb[j])
					}
				}
			}
			if view.renderCache.renders != renders {
				t.Fatal("scroll rebuilt transcript")
			}
			assertSubagentOutputCacheMatchesFresh(t, m)
		}
	}
}

func TestSubagentWorkspaceDragReflowsOnlyOnRelease(t *testing.T) {
	for _, mode := range []uipreferences.Layout{uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
		t.Run(string(mode), func(t *testing.T) {
			m, client := newPaneTestModel(t)
			runTeaCmds(t, m, m.setSubagentLayout(mode))
			m.View()
			layout := m.workspaceLayout()
			before := m.workspace.preferences
			renders := m.diag.GlamourRenderCalls
			childRenders := m.subagentOutputViews[m.subagentOutputOverlay.callID].renderCache.renders
			r := layout.divider
			m.Update(tea.MouseClickMsg{X: r.x, Y: r.y, Button: tea.MouseLeft})
			x, y := r.x, r.y
			for i := range 8 {
				if r.width == 1 {
					x = r.x + i + 1
				} else {
					y = r.y + i + 1
				}
				m.Update(tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft})
				m.View()
				if m.workspaceLayout() != layout || m.workspace.preferences != before || client.preferences != before {
					t.Fatal("drag reflowed or saved before release")
				}
			}
			if m.diag.GlamourRenderCalls != renders || m.subagentOutputViews[m.subagentOutputOverlay.callID].renderCache.renders != childRenders {
				t.Fatal("preview rendered history")
			}
			_, cmd := m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseNone})
			runTeaCmds(t, m, cmd)
			if m.workspace.dragging || m.workspace.preferences == before || client.preferences != m.workspace.preferences || m.workspaceLayout() == layout {
				t.Fatal("release did not apply and persist")
			}
			m.View()
			want := m.View().Content
			m.Update(tea.MouseClickMsg{X: m.workspaceLayout().divider.x, Y: m.workspaceLayout().divider.y, Button: tea.MouseLeft})
			m.Update(tea.MouseMotionMsg{X: 5, Y: 5, Button: tea.MouseLeft})
			m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
			if m.workspace.dragging || m.View().Content != want {
				t.Fatal("cancel retained preview")
			}
		})
	}
}
