package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestPasteFollowsComposerCursorToLastVisibleWindow(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	// Use direct InsertString so multi-line raw text still exercises cursor
	// follow (large bracketed pastes are collapsed separately).
	lines := make([]string, 12)
	for i := range lines {
		lines[i] = fmt.Sprintf("pasted-line-%02d", i)
	}
	text := strings.Join(lines, "\n")
	m.insertComposerText(text)

	layout := m.buildComposeInputLayout()
	if layout.layout.totalRows < 12 {
		t.Fatalf("totalRows=%d, want >= 12", layout.layout.totalRows)
	}
	if layout.layout.cursorRow != layout.layout.totalRows-1 {
		t.Fatalf("cursorRow=%d, want last row %d", layout.layout.cursorRow, layout.layout.totalRows-1)
	}
	if layout.layout.cursorRow < layout.rowOffset || layout.layout.cursorRow >= layout.rowEnd {
		t.Fatalf("cursorRow=%d outside visible window [%d,%d)", layout.layout.cursorRow, layout.rowOffset, layout.rowEnd)
	}
	if layout.rowEnd-layout.rowOffset > maxInputBarRows {
		t.Fatalf("visible window height=%d, want <= %d", layout.rowEnd-layout.rowOffset, maxInputBarRows)
	}
	visible := layout.visiblePlainLines()
	if len(visible) == 0 || !strings.Contains(visible[len(visible)-1], "pasted-line-11") {
		t.Fatalf("visible window does not include last pasted line: %#v", visible)
	}
}

func TestArrowDownFollowsComposerCursorPastMaxRows(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.textarea.SetValue(strings.Join([]string{"a", "b", "c", "d", "e", "f", "g", "h"}, "\n"))
	m.moveTextareaCursorToIndex(0)
	m.syncInputFromTextarea()
	m.composerRowOffset = 0

	for i := 0; i < 7; i++ {
		updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
		m = updated.(*Model)
	}
	layout := m.buildComposeInputLayout()
	if layout.layout.cursorRow < layout.rowOffset || layout.layout.cursorRow >= layout.rowEnd {
		t.Fatalf("after KeyDown, cursorRow=%d outside visible [%d,%d)", layout.layout.cursorRow, layout.rowOffset, layout.rowEnd)
	}
}

func TestMoveTextareaCursorToIndexIsFastOnLargeBuffer(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line-%03d-xxxxxxxxxxxxxxxxxxxxxxxx\n", i)
	}
	m.textarea.SetValue(b.String())
	m.syncInputFromTextarea()
	runes := len([]rune(m.textarea.Value()))
	m.moveTextareaCursorToIndex(0)

	start := time.Now()
	m.moveTextareaCursorToIndex(runes)
	elapsed := time.Since(start)
	if m.textareaCursorIndex() != runes {
		t.Fatalf("cursor index=%d, want %d", m.textareaCursorIndex(), runes)
	}
	// Previous KeyLeft/KeyRight path was ~38s for this size; budget 200ms.
	if elapsed > 200*time.Millisecond {
		t.Fatalf("moveTextareaCursorToIndex took %v for %d runes, want < 200ms", elapsed, runes)
	}
}

func TestWheelScrollComposerStaysResponsiveOnLargeBuffer(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)

	var b strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&b, "wheel-line-%02d\n", i)
	}
	m.textarea.SetValue(b.String())
	m.moveTextareaCursorToIndex(0)
	m.syncInputFromTextareaAndFollow()

	start := time.Now()
	for i := 0; i < 20; i++ {
		if !m.scrollComposerInputBy(1) && i == 0 {
			t.Fatal("first scrollComposerInputBy returned false")
		}
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("20x scrollComposerInputBy took %v, want < 200ms", elapsed)
	}
	layout := m.buildComposeInputLayout()
	if layout.layout.cursorRow < layout.rowOffset || layout.layout.cursorRow >= layout.rowEnd {
		t.Fatalf("after wheel, cursorRow=%d outside visible [%d,%d)", layout.layout.cursorRow, layout.rowOffset, layout.rowEnd)
	}
}

func TestInputSelectionDragStillCopiesAndAutoScrolls(t *testing.T) {
	// Regression: selection + edge auto-scroll must keep working after cursor-move rewrite.
	var copied string
	model := NewModel(Config{
		NoAnimation: true,
		WriteClipboardText: func(text string) error {
			copied = text
			return nil
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.textarea.SetValue(strings.Join([]string{
		"line0",
		"line1",
		"line2",
		"line3",
		"line4",
		"line5",
	}, "\n"))
	m.moveTextareaCursorToIndex(0)
	m.syncInputFromTextareaAndFollow()
	if got := m.composeInputLayout().layout.totalRows; got <= maxInputBarRows {
		t.Fatalf("composer rows = %d, want more than visible max %d", got, maxInputBarRows)
	}

	startY, height, ok := m.inputAreaBounds()
	if !ok {
		t.Fatal("input area bounds unavailable")
	}
	startMouse := tea.Mouse{Button: tea.MouseLeft, X: inputSelectionMouseX(m, 0), Y: startY}
	outsideMouse := tea.Mouse{Button: tea.MouseLeft, X: inputSelectionMouseX(m, len([]rune("line4"))), Y: startY + height}

	handled, _ := m.handleInputAreaMouse(startMouse, mousePhasePress)
	if !handled {
		t.Fatal("input selection press was not handled")
	}
	handled, cmd := m.handleInputAreaMouse(outsideMouse, mousePhaseMotion)
	if !handled || cmd == nil {
		t.Fatalf("outside motion handled=%v cmd=%v, want scheduled input auto-scroll", handled, cmd != nil)
	}
	token := m.selectionAutoScroll.scheduledToken
	if token == 0 {
		t.Fatal("expected auto-scroll token")
	}
	updated, _ = m.Update(frameTickMsg{kind: frameTickSelectionScroll, token: token, at: time.Now()})
	m = updated.(*Model)
	if got := m.inputSelectionEnd.line; got < 4 {
		t.Fatalf("input selection end line = %d, want advanced to >= 4 after auto-scroll", got)
	}
	handled, cmd = m.handleInputAreaMouse(outsideMouse, mousePhaseRelease)
	if !handled || cmd == nil {
		t.Fatalf("release handled=%v cmd=%v, want copy command", handled, cmd != nil)
	}
	if got, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("copy command returned %T, want clipboardCopyResultMsg", got)
	} else if got.err != nil {
		t.Fatalf("copy command returned error: %v", got.err)
	}
	if !strings.Contains(copied, "line0") || !strings.Contains(copied, "line4") {
		t.Fatalf("copied text = %q, want multi-line selection covering line0..line4", copied)
	}
}

func TestClickStillPreservesComposerRowOffset(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{NoAnimation: true})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.textarea.SetValue(strings.Join([]string{
		"line0", "line1", "line2", "line3", "line4", "line5",
	}, "\n"))
	m.moveTextareaCursorToIndex(len([]rune("line0\nline1\nline2\nline3\nline4\n")))
	m.syncInputFromTextareaAndFollow()
	m.composerRowOffset = 2

	startY, _, ok := m.inputAreaBounds()
	if !ok {
		t.Fatal("input area bounds unavailable")
	}
	beforeOffset := m.composerRowOffset
	clickX := inputSelectionMouseX(m, len([]rune("line2")))
	handled, cmd := m.handleInputAreaMouse(tea.Mouse{Button: tea.MouseLeft, X: clickX, Y: startY}, mousePhasePress)
	if !handled || cmd != nil {
		t.Fatalf("press handled=%v cmd=%v, want handled without command", handled, cmd != nil)
	}
	if got := m.composerRowOffset; got != beforeOffset {
		t.Fatalf("composer row offset after click = %d, want unchanged %d", got, beforeOffset)
	}
}
