package tuiapp

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m *Model) ensureSubagentEditor(state *subagentOutputOverlayState) {
	if state.editorReady {
		return
	}
	state.editor = newPromptTextarea(m.theme)
	state.editor.CharLimit = 65536
	state.editorReady = true
	state.historyIndex = -1
}
func (m *Model) paneEditorLayout(state *subagentOutputOverlayState, width int) composerInputLayout {
	m.ensureSubagentEditor(state)
	contentWidth := maxInt(1, width-2-2*m.composerChrome().horizontalInset())
	state.editor.SetWidth(contentWidth + 2)
	value, index := state.editor.Value(), promptTextareaCursorIndex(state.editor)
	display, cursor, mapping := composeInputDisplayWithMap(value, index, state.attachments)
	layout := layoutComposerDisplay(display, cursor, contentWidth)
	offset := clampComposerRowOffset(state.editorOffset, len(layout.rows), maxInputBarRows)
	if layout.cursorRow < offset {
		offset = layout.cursorRow
	}
	if layout.cursorRow >= offset+maxInputBarRows {
		offset = layout.cursorRow - maxInputBarRows + 1
	}
	state.editorOffset = offset
	end := minInt(len(layout.rows), offset+maxInputBarRows)
	return composerInputLayout{prompt: "> ", promptWidth: 2, continuation: "  ", value: value, cursorIndex: index, contentWidth: contentWidth, layout: layout, displayToValue: mapping, rowOffset: offset, rowEnd: end}
}
func (m *Model) renderPaneEditor(state *subagentOutputOverlayState, width int) string {
	layout := m.paneEditorLayout(state, width)
	theme := m.theme
	chrome := m.promptComposerChrome(m.workspace.childFocused)
	render := renderPromptEditor(layout, theme, state.editor, chrome, "", "")
	if state.editorSelecting {
		if start, end, ok := normalizedSelectionRange(state.editorSelectStart, state.editorSelectEnd, len(layout.layout.rows)); ok {
			styles := promptSelectionStyles(theme, chrome)
			render.styledLines = strings.Split(renderPromptAwareInputSelection(render.plainLines, start, end, layout.rowOffset, layout.promptWidth, layout.prompt, styles), "\n")
		}
	}
	state.editorCursor = render.cursor
	if state.editorCursor != nil {
		state.editorCursor.X += chrome.horizontalInset()
		state.editorCursor.Y += chrome.topRows()
	}
	return wrapPromptEditor(render.styledText(), chrome, width, lipgloss.NewStyle().Background(chrome.background))
}
func (m *Model) handlePaneEditorKey(msg tea.KeyMsg) tea.Cmd {
	state := m.subagentOutputOverlay
	if state == nil {
		return nil
	}
	k := msg.Key()
	if state.menu != "" {
		return m.handlePaneMenuKey(msg)
	}
	state.editorSelecting = false
	defer state.reconcileEditorAttachments()
	switch {
	case k.Code == tea.KeyUp && state.editor.Value() == "" && len(state.history) > 0:
		state.historyIndex = len(state.history) - 1
		state.editor.SetValue(state.history[state.historyIndex])
		state.attachments = cloneInputAttachments(state.historyAttachments[state.historyIndex])
		movePromptTextareaCursor(&state.editor, len([]rune(state.editor.Value())))
		return nil
	case k.Code == tea.KeyDown && state.historyIndex >= 0:
		state.editor.Reset()
		state.attachments = nil
		state.historyIndex = -1
		return nil
	case k.Code == tea.KeyEscape:
		m.clearSubagentOutputSelection()
		return nil
	case key.Matches(msg, m.keys.InsertNewline):
		state.editor.InsertString("\n")
		return nil
	case k.Code == tea.KeyEnter && k.Mod&tea.ModShift == 0 && k.Mod&tea.ModAlt == 0:
		return m.submitPanePrompt()
	case k.Code == tea.KeyEnd && state.editor.Value() == "":
		state.followTail = true
		return nil
	case k.Code == tea.KeyHome && state.editor.Value() == "":
		state.offset = 0
		state.followTail = false
		m.demandEarlierHistory(state.callID)
		return nil
	case k.Code == tea.KeyPgUp:
		m.scrollSubagentOutputOverlay(-m.subagentOutputOverlayPageSize())
		return nil
	case k.Code == tea.KeyPgDown:
		m.scrollSubagentOutputOverlay(m.subagentOutputOverlayPageSize())
		return nil
	case key.Matches(msg, m.keys.ImagePaste), key.Matches(msg, m.keys.TextPaste):
		m.pastePaneClipboard(state, msg)
		return nil
	case msg.String() == "ctrl+c":
		state.inputStatus = m.paneKeyHint(m.keys.PaneToggle) + " · agent keeps running"
		return nil
	}
	m.ensureSubagentEditor(state)
	if k.Code == tea.KeyEnter {
		state.editor.InsertString("\n")
		return nil
	}
	state.historyIndex = -1
	var cmd tea.Cmd
	state.editor, cmd = state.editor.Update(msg)
	return cmd
}

// Keep the editor's cursor drawing separate from its input state. Bubble Tea
// has one physical cursor, owned by the focused pane in the current frame.
func (m *Model) paneCursor() *tea.Cursor {
	state := m.subagentOutputOverlay
	if state == nil || m.subagentOverlay != nil || !m.workspace.childFocused || state.menu != "" || m.workspace.resizing || m.workspace.dragging || state.editorCursor == nil {
		return nil
	}
	cursor := *state.editorCursor
	cursor.X += state.geometry.contentX
	cursor.Y += state.editorY
	if cursor.X < 0 || cursor.X >= m.width || cursor.Y < 0 || cursor.Y >= m.height {
		return nil
	}
	return &cursor
}
