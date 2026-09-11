package tuiapp

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (state *subagentOutputOverlayState) reconcileEditorAttachments() {
	value := state.editor.Value()
	items := rebindAttachmentsByID(value, state.attachments)
	cleaned, items, changed := stripOrphanSentinels(value, items)
	if changed {
		cursor := promptTextareaCursorIndex(state.editor)
		state.editor.SetValue(cleaned)
		movePromptTextareaCursor(&state.editor, min(cursor, len([]rune(cleaned))))
	}
	state.attachments = items
}

func (m *Model) pastePaneClipboard(state *subagentOutputOverlayState, msg tea.KeyMsg) {
	pasteImage := func() (bool, error) {
		if m.cfg.PasteClipboardImage == nil {
			return false, nil
		}
		names, _, err := m.cfg.PasteClipboardImage()
		if err != nil {
			return false, err
		}
		for _, name := range names {
			id, _ := nextPromptAttachmentIdentity(state.editor.Value(), state.attachments)
			item := inputAttachment{ID: id, Name: name, Kind: attachmentKindImage, Offset: promptTextareaCursorIndex(state.editor)}
			state.editor.InsertString(string(item.sentinelRune()))
			state.attachments = append(state.attachments, item)
		}
		state.reconcileEditorAttachments()
		return len(names) > 0, nil
	}
	imageFirst := key.Matches(msg, m.keys.ImagePaste)
	if imageFirst {
		if pasted, err := pasteImage(); pasted || err != nil {
			if err != nil {
				state.inputStatus = "Paste failed: " + err.Error()
			}
			return
		}
	}
	text, err := m.readClipboardText()
	if err == nil && text != "" {
		state.editor.InsertString(normalizeClipboardText(text))
		state.reconcileEditorAttachments()
		return
	}
	if !imageFirst && m.shouldFallbackTextPasteToImage(msg) {
		if pasted, imageErr := pasteImage(); pasted || imageErr != nil {
			if imageErr != nil {
				state.inputStatus = "Paste failed: " + imageErr.Error()
			}
			return
		}
	}
	if err != nil {
		state.inputStatus = "Paste failed: " + err.Error()
	}
}

func (m *Model) handlePaneEditorMouse(msg tea.MouseMsg) (bool, tea.Cmd) {
	state := m.subagentOutputOverlay
	mouse := msg.Mouse()
	inside := mouse.Y >= state.editorY && mouse.Y < state.editorY+state.editorHeight &&
		mouse.X >= state.geometry.contentX && mouse.X < state.geometry.contentX+state.geometry.contentWidth
	if !inside && !state.editorSelecting {
		return false, nil
	}
	layout := m.paneEditorLayout(state, state.geometry.contentWidth)
	point := layout.snapSelectionPoint(textSelectionPoint{
		line: mouse.Y - state.editorY - m.composerChrome().topRows() + layout.rowOffset,
		col:  mouse.X - state.geometry.contentX - m.composerChrome().horizontalInset(),
	})
	switch msg.(type) {
	case tea.MouseClickMsg:
		if mouse.Button != tea.MouseLeft {
			return true, nil
		}
		m.clearSubagentOutputSelection()
		state.editorSelecting = true
		state.editorSelectStart, state.editorSelectEnd = point, point
		movePromptTextareaCursor(&state.editor, layout.textareaIndexFromPoint(point))
	case tea.MouseMotionMsg:
		if state.editorSelecting {
			state.editorSelectEnd = point
			movePromptTextareaCursor(&state.editor, layout.textareaIndexFromPoint(point))
		}
	case tea.MouseReleaseMsg:
		if !state.editorSelecting {
			return true, nil
		}
		state.editorSelecting = false
		state.editorSelectEnd = point
		start, end, ok := normalizedSelectionRange(state.editorSelectStart, state.editorSelectEnd, len(layout.layout.rows))
		if !ok {
			return true, nil
		}
		text := selectionTextFromInputLines(layout.allPlainLines(), start, end, layout.promptWidth)
		if text != "" {
			if err := m.writeClipboardText(text); err != nil {
				state.inputStatus = "Copy failed: " + err.Error()
			}
		}
	}
	return true, nil
}
