package tuiapp

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestPaneComposerMouseEditsAndCopiesWrappedUnicode(t *testing.T) {
	for _, mode := range []uipreferences.Layout{uipreferences.Overlay, uipreferences.Left, uipreferences.Right, uipreferences.Up, uipreferences.Down} {
		t.Run(string(mode), func(t *testing.T) {
			m, _ := newPaneTestModel(t)
			m.applyTheme(tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor))
			runTeaCmds(t, m, m.setSubagentLayout(mode))
			m.textarea.SetValue("parent unchanged")
			state := m.subagentOutputOverlay
			state.editor.SetValue("a你bc\nsecond line")
			var copied string
			m.cfg.WriteClipboardText = func(text string) error { copied = text; return nil }
			frame := m.View()
			x := state.geometry.contentX + 3
			y := state.editorY + m.composerChrome().topRows()
			line := strings.Split(ansi.Strip(frame.Content), "\n")[y]
			if got := sliceByDisplayColumns(line, state.geometry.contentX, x+1); got != " > a" {
				t.Fatalf("padded prompt=%q, want %q", got, " > a")
			}
			m.Update(tea.MouseClickMsg{X: x + 3, Y: y, Button: tea.MouseLeft})
			m.Update(tea.MouseReleaseMsg{X: x + 3, Y: y, Button: tea.MouseLeft})
			if got := promptTextareaCursorIndex(state.editor); got != 2 {
				t.Fatalf("click index=%d, want after CJK", got)
			}
			frame = m.View()
			if frame.Cursor == nil || frame.Cursor.X != x+3 || frame.Cursor.Y != y {
				t.Fatalf("cursor=%+v, want (%d, %d)", frame.Cursor, x+3, y)
			}
			m.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})
			if got := state.editor.Value(); got != "a你Xbc\nsecond line" {
				t.Fatalf("edited=%q", got)
			}
			m.View()
			m.Update(tea.MouseClickMsg{X: x + 1, Y: y, Button: tea.MouseLeft})
			m.Update(tea.MouseMotionMsg{X: x + 6, Y: y + 1, Button: tea.MouseLeft})
			m.View()
			m.Update(tea.MouseReleaseMsg{X: x + 6, Y: y + 1, Button: tea.MouseLeft})
			if copied != "你Xbc\nsecond" {
				t.Fatalf("copied=%q", copied)
			}
			if m.textarea.Value() != "parent unchanged" {
				t.Fatal("edited parent")
			}
		})
	}
}

func TestPaneComposerImagesKeepOrderAndRecall(t *testing.T) {
	m, client := newPaneTestModel(t)
	m.keys = defaultKeyMapForPlatform("darwin", false)
	data, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a5l8AAAAASUVORK5CYII=")
	path := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	m.cfg.PasteClipboardImage = func() ([]string, string, error) { return []string{path}, "", nil }
	state := m.subagentOutputOverlay
	state.editor.SetValue("beforeafter")
	movePromptTextareaCursor(&state.editor, 6)
	m.Update(tea.KeyPressMsg{Code: 'v', Mod: tea.ModCtrl})
	if !strings.Contains(ansi.Strip(m.View().Content), "[image #1]") {
		t.Fatal("image token missing")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	runTeaCmds(t, m, cmd)
	if len(client.requests) != 1 {
		t.Fatal("no image submission")
	}
	parts := client.requests[0].ContentParts
	if len(parts) != 3 || parts[0].Text != "before" || parts[1].Type != model.ContentPartImage || parts[2].Text != "after" {
		t.Fatalf("image order=%#v", parts)
	}
	decoded, err := base64.StdEncoding.DecodeString(parts[1].Data)
	if err != nil || string(decoded) != string(data) {
		t.Fatal("image bytes changed")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if len(state.attachments) != 1 {
		t.Fatal("recall lost image")
	}
	state.editor.MoveToEnd()
	movePromptTextareaCursor(&state.editor, 7)
	m.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	if len(state.attachments) != 0 || state.editor.Value() != "beforeafter" {
		t.Fatal("image deletion rebound another token")
	}
}
