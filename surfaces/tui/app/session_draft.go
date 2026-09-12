package tuiapp

// Drafts belong to this terminal's presentation, never to the Host's Session
// transcript. Reattaching rebuilds output from Control while preserving input.
type sessionDraft struct {
	text        string
	cursor      int
	attachments []inputAttachment
	pending     pendingPromptQueue
	children    map[string]sessionComposerDraft
}

type sessionComposerDraft struct {
	text        string
	attachments []inputAttachment
}

type sessionHistoryReadyMsg struct{}

func (m *Model) saveSessionDraft() {
	if m.sessionDrafts == nil {
		m.sessionDrafts = make(map[string]sessionDraft)
	}
	draft := sessionDraft{
		text: m.textarea.Value(), cursor: m.textareaCursorIndex(),
		attachments: cloneInputAttachments(m.inputAttachments),
		children:    make(map[string]sessionComposerDraft),
	}
	for id, child := range m.sessionDrafts[m.currentSessionID].children {
		draft.children[id] = child
	}
	for _, pending := range m.pendingQueue {
		if pending.state != pendingPromptQueued && pending.state != pendingPromptDispatchScheduled {
			continue
		}
		pending.state = pendingPromptQueued
		pending.attachments = cloneAttachments(pending.attachments)
		draft.pending = append(draft.pending, pending)
	}
	for callID, view := range m.subagentOutputViews {
		if view.pane != nil && view.pane.editorReady {
			draft.children[callID] = sessionComposerDraft{
				text: view.pane.editor.Value(), attachments: cloneInputAttachments(view.pane.attachments),
			}
		}
	}
	m.sessionDrafts[m.currentSessionID] = draft
}

func (m *Model) restoreSessionDraft() {
	draft := m.sessionDrafts[m.currentSessionID]
	m.textarea.SetValue(draft.text)
	m.moveTextareaCursorToIndex(draft.cursor)
	m.inputAttachments = cloneInputAttachments(draft.attachments)
	m.syncAttachmentSummary()
	m.syncInputFromTextarea()
	m.pendingQueue = draft.pending
	m.historyIndex = -1
	m.historyDraft = ""
	m.historyDraftAttachments = nil
}

func (m *Model) restoreChildSessionDraft(pane *subagentOutputOverlayState) {
	draft, ok := m.sessionDrafts[m.currentSessionID].children[pane.callID]
	if !ok {
		return
	}
	pane.editor.SetValue(draft.text)
	pane.attachments = cloneInputAttachments(draft.attachments)
	delete(m.sessionDrafts[m.currentSessionID].children, pane.callID)
}
