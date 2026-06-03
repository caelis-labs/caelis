package tuiapp

import (
	"bytes"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

func TestComposerMixedWidthDeleteAvoidsRendererDCHInsideWideGlyph(t *testing.T) {
	model := NewModel(Config{})
	model.width = 24
	model.textarea.SetValue("甲a乙b丙c丁d")
	model.moveTextareaCursorToIndex(len([]rune("甲a乙b")))
	model.syncInputFromTextarea()
	before := model.renderInputBar()

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m := updated.(*Model)
	after := m.renderInputBar()

	if got := m.textarea.Value(); got != "甲a乙丙c丁d" {
		t.Fatalf("textarea value = %q, want 甲a乙丙c丁d", got)
	}
	outputs := renderComposerFramesForTest(t, model.fixedRowWidth(), before, after)
	second := outputs[1]
	if stringsContainDeleteCharacter(second) {
		t.Fatalf("renderer update = %q, must not delete inside shifted CJK glyph", second)
	}
	if !bytes.Contains([]byte(second), []byte("丙c丁d")) {
		t.Fatalf("renderer update = %q, want shifted CJK tail to be repainted", second)
	}
}

func renderComposerFramesForTest(t *testing.T, width int, frames ...string) []string {
	t.Helper()
	var buf bytes.Buffer
	renderer := uv.NewTerminalRenderer(&buf, []string{"TERM=xterm-256color", "TTY_FORCE=1"})
	renderer.SetRelativeCursor(true)

	screen := uv.NewScreenBuffer(width, 1)
	outputs := make([]string, 0, len(frames))
	for idx, frame := range frames {
		uv.NewStyledString(frame).Draw(screen, screen.Bounds())
		renderer.Render(screen.RenderBuffer)
		if err := renderer.Flush(); err != nil {
			t.Fatalf("flush frame %d: %v", idx, err)
		}
		outputs = append(outputs, buf.String())
		buf.Reset()
	}
	return outputs
}

func stringsContainDeleteCharacter(text string) bool {
	return bytes.Contains([]byte(text), []byte(ansi.DeleteCharacter(1))) ||
		bytes.Contains([]byte(text), []byte("\x1b[1P"))
}
