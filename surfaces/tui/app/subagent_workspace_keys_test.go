package tuiapp

import (
	"image/color"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestPaneWorkspaceTerminalKeySequences(t *testing.T) {
	for _, profile := range []struct {
		name, goos string
		wsl        bool
	}{
		{"macOS", "darwin", false}, {"Linux", "linux", false}, {"Windows", "windows", false}, {"WSL", "linux", true},
	} {
		t.Run(profile.name, func(t *testing.T) {
			m, _ := newPaneTestModel(t)
			m.keys = defaultKeyMapForPlatform(profile.goos, profile.wsl)
			m.setSubagentLayout(uipreferences.Right)
			m.subagentOutputOverlay.editor.SetValue("child draft")
			m.textarea.SetValue("parent draft")
			m.closeSubagentOutputOverlay()
			cancels := 0
			m.cfg.CancelRunning = func() bool { cancels++; return true }
			m.liveTurn.Active = true
			press := func(sequence string) {
				t.Helper()
				var decoder uv.EventDecoder
				n, event := decoder.Decode([]byte(sequence))
				k, ok := event.(uv.KeyPressEvent)
				if n != len(sequence) || !ok {
					t.Fatalf("decode %q = %d, %#v", sequence, n, event)
				}
				_, cmd := m.Update(tea.KeyPressMsg{Code: k.Code, Mod: k.Mod, Text: k.Text})
				runTeaCmds(t, m, cmd)
			}
			press("\x1b[18~") // F7, legacy VT encoding used across terminal hosts.
			if m.subagentOutputOverlay == nil || !m.workspace.childFocused {
				t.Fatal("F7 failed to open")
			}
			state := m.subagentOutputOverlay
			press("\x1b[17~")
			if m.workspace.childFocused {
				t.Fatal("F6 failed to focus main")
			}
			press("\x1b[17;2~")
			if !m.workspace.childFocused {
				t.Fatal("Shift+F6 failed to focus child")
			}
			press("\x07") // Ctrl+G.
			if state.menu != "agents" {
				t.Fatal("agent menu unreachable")
			}
			press("\x1b")
			if state.menu != "" {
				t.Fatal("Esc did not dismiss menu")
			}
			press("\x0c") // Ctrl+L.
			if state.menu != "layout" {
				t.Fatal("layout menu unreachable")
			}
			press("\x1b")
			press("\x1b")
			if m.subagentOutputOverlay != state || cancels != 0 {
				t.Fatal("child Esc hid pane or cancelled parent")
			}
			press("\x0a") // Ctrl+J must work without extended keyboard reporting.
			if !strings.Contains(state.editor.Value(), "\n") {
				t.Fatal("legacy newline failed")
			}
			draft := state.editor.Value()
			m.Update(tea.KeyReleaseMsg{Code: tea.KeyF7})
			if m.subagentOutputOverlay == nil {
				t.Fatal("key release toggled pane")
			}
			press("\x1b[18~")
			if m.subagentOutputOverlay != nil || m.workspace.childFocused {
				t.Fatal("F7 did not hide and return focus")
			}
			if profile.goos == "windows" {
				press("\x1b[118;0;0;1;0;1_")
			} else {
				press("\x1b[18~")
			}
			if m.subagentOutputOverlay != state || state.editor.Value() != draft || m.textarea.Value() != "parent draft" {
				t.Fatal("reopen lost draft or selected child")
			}
			press("\x1b[17~")
			press("\x1b")
			if cancels != 1 || m.subagentOutputOverlay != state {
				t.Fatal("main Esc lost interrupt behavior or hid child")
			}
			m.closeSubagentOutputOverlay()
			m.activePrompt = newPromptState(PromptRequestMsg{Prompt: "Approval", Response: make(chan PromptResponse, 1)})
			press("\x1b[18~")
			if m.subagentOutputOverlay != nil {
				t.Fatal("workspace key escaped modal input owner")
			}
		})
	}
}

func TestPaneWorkspaceFocusPreservesTranscriptLayout(t *testing.T) {
	for _, mode := range []uipreferences.Layout{uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
		t.Run(string(mode), func(t *testing.T) {
			m, _ := newPaneTestModel(t)
			m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
			m.setSubagentLayout(mode)
			state := m.subagentOutputOverlay
			state.editor.SetValue("child input")
			m.textarea.SetValue("main input")
			m.View()
			view := m.subagentOutputViews[state.callID]
			rows := slices.Clone(view.renderCache.rows)
			renders, glamour := view.renderCache.renders, m.diag.GlamourRenderCalls
			geometry := state.geometry
			for range 4 {
				m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
				m.View()
				child := m.renderPaneEditor(state, state.geometry.contentWidth)
				main := m.wrapInputBarInContainer(m.composeInputRender().styledText())
				childBg, mainBg := m.theme.ComposerBg, m.theme.ComposerFocusBg
				if m.workspace.childFocused {
					childBg, mainBg = mainBg, childBg
				}
				assertComposerSurface(t, child, childBg)
				assertComposerSurface(t, main, mainBg)
				if !slices.Equal(rows, view.renderCache.rows) || view.renderCache.renders != renders || m.diag.GlamourRenderCalls != glamour {
					t.Fatal("focus rebuilt or changed transcript")
				}
				if state.geometry.contentX != geometry.contentX || state.geometry.contentWidth != geometry.contentWidth || state.geometry.contentY != geometry.contentY {
					t.Fatal("focus moved transcript geometry")
				}
			}
			m.applyTheme(tuikit.ResolveThemeWithState(true, true, colorprofile.Ascii))
			m.View()
			if !strings.Contains(ansi.Strip(m.renderPaneTitle(view, 80)), "▸") {
				t.Fatal("monochrome focus has no shape cue")
			}
			m.Update(tea.KeyPressMsg{Code: tea.KeyF6})
			if !strings.Contains(m.paneFooterHints(state), "Main focused") {
				t.Fatal("monochrome main focus unclear")
			}
		})
	}
}

// Check the physical cells, including text and padding, so nested ANSI styles
// cannot leave the editor with a mixture of focused and unfocused backgrounds.
func assertComposerSurface(t *testing.T, rendered string, background color.Color) {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	width := ansi.StringWidth(lines[0])
	assertOverlayCellBackgrounds(t, rendered, width, len(lines), background)
	if strings.Contains(ansi.Strip(rendered), "───") {
		t.Fatal("composer retained focus underline")
	}
}
