package tuiapp

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (m *Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.MouseWheelMsg:
		if m.btwOverlay != nil {
			mouse := typed.Mouse()
			switch mouse.Button {
			case tea.MouseWheelUp:
				m.scrollBTW(-1)
				return m, nil
			case tea.MouseWheelDown:
				m.scrollBTW(1)
				return m, nil
			}
		}
		if m.selecting {
			return m, m.handleViewportSelectionWheel(typed.Mouse())
		}
		if handled, changed := m.tryScrollPanelAtMouse(typed.Mouse()); handled {
			if changed {
				offset := m.viewport.YOffset()
				keepFollowState := m.viewportFollowState
				m.syncViewportContent()
				maxOffset := maxInt(0, m.viewport.TotalLineCount()-m.viewport.Height())
				if offset > maxOffset {
					offset = maxOffset
				}
				m.viewport.SetYOffset(offset)
				m.setViewportFollowState(keepFollowState)
			}
			if changed {
				return m, m.ensureScrollbarTick()
			}
			return m, nil
		}
		var cmd tea.Cmd
		wasFollowTail := m.isViewportFollowTail()
		m.materializeViewportContentIfStale()
		m.viewport, cmd = m.viewport.Update(msg)
		m.refreshViewportFollowStateFromOffset()
		var resumeCmd tea.Cmd
		if m.isViewportFollowTail() && m.offscreenViewportDirty {
			m.syncViewportContent()
			resumeCmd = m.resumeRunningAnimationIfNeeded()
		} else if !wasFollowTail && m.isViewportFollowTail() {
			resumeCmd = m.resumeRunningAnimationIfNeeded()
		}
		return m, tea.Batch(cmd, m.touchViewportScrollbar(), resumeCmd)
	case tea.MouseClickMsg:
		mouse := typed.Mouse()
		if mouse.Button == tea.MouseLeft {
			if handled, cmd := m.beginScrollbarDrag(mouse); handled {
				return m, cmd
			}
		}
		if handled, cmd := m.handleInputAreaMouse(mouse, mousePhasePress); handled {
			return m, cmd
		}
		if handled, cmd := m.handleFixedAreaMouse(mouse, mousePhasePress); handled {
			return m, cmd
		}
		return m, m.handleViewportMousePress(mouse)
	case tea.MouseMotionMsg:
		mouse := typed.Mouse()
		if m.scrollbarDrag.active {
			return m, m.updateScrollbarDrag(mouse)
		}
		if cmd := m.hoverScrollbarAtMouse(mouse); cmd != nil {
			return m, cmd
		}
		if handled, cmd := m.handleInputAreaMouse(mouse, mousePhaseMotion); handled {
			return m, cmd
		}
		if handled, cmd := m.handleFixedAreaMouse(mouse, mousePhaseMotion); handled {
			return m, cmd
		}
		return m, m.handleViewportMouseMotion(mouse)
	case tea.MouseReleaseMsg:
		mouse := typed.Mouse()
		if m.scrollbarDrag.active {
			cmd := m.updateScrollbarDrag(mouse)
			m.endScrollbarDrag()
			return m, cmd
		}
		if handled, cmd := m.handleInputAreaMouse(mouse, mousePhaseRelease); handled {
			return m, cmd
		}
		if handled, cmd := m.handleFixedAreaMouse(mouse, mousePhaseRelease); handled {
			return m, cmd
		}
		return m, m.handleViewportMouseRelease(mouse)
	default:
		return m, nil
	}
}

func (m *Model) tryScrollPanelAtMouse(mouse tea.Mouse) (handled bool, changed bool) {
	contentLine, ok := m.contentLineAtViewportY(mouse.Y)
	if !ok {
		return false, false
	}
	blockID := strings.TrimSpace(m.viewportBlockIDs[contentLine])
	if blockID == "" {
		return false, false
	}
	delta := 0
	switch mouse.Button {
	case tea.MouseWheelUp:
		delta = -1
	case tea.MouseWheelDown:
		delta = 1
	default:
		return false, false
	}
	ctx := m.blockRenderContext(maxInt(1, m.viewport.Width()))
	token := ""
	if contentLine >= 0 && contentLine < len(m.viewportClickTokens) {
		token = strings.TrimSpace(m.viewportClickTokens[contentLine])
	}
	switch block := m.doc.Find(blockID).(type) {
	case *MainACPTurnBlock:
		callID, ok := strings.CutPrefix(token, "acp_tool_panel_scroll:")
		if !ok || !block.CanScrollToolPanel(callID, delta, ctx) {
			return false, false
		}
		changed = block.ScrollToolPanel(callID, delta, ctx)
		if changed {
			m.markViewportBlockDirty(block.BlockID())
		}
		return true, changed
	case *ParticipantTurnBlock:
		callID, ok := strings.CutPrefix(token, "acp_tool_panel_scroll:")
		if !ok || !block.CanScrollToolPanel(callID, delta, ctx) {
			return false, false
		}
		changed = block.ScrollToolPanel(callID, delta, ctx)
		if changed {
			m.markViewportBlockDirty(block.BlockID())
		}
		return true, changed
	default:
		return false, false
	}
}

type mousePhase int

const (
	mousePhasePress mousePhase = iota
	mousePhaseMotion
	mousePhaseRelease
)

func (m *Model) handleViewportMousePress(mouse tea.Mouse) tea.Cmd {
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	m.clearInputSelection()
	m.clearFixedSelection()
	point, ok := m.mousePointToContentPoint(mouse.X, mouse.Y, false)
	if !ok {
		return nil
	}
	m.selecting = true
	m.selectionStart = point
	m.selectionEnd = point
	m.enterViewportSelecting()
	m.bumpViewportSelectionVersion()
	return nil
}

func (m *Model) handleViewportMouseMotion(mouse tea.Mouse) tea.Cmd {
	if !m.selecting {
		return nil
	}
	m.selectionAutoScroll.mouse = mouse
	cmd := m.updateViewportSelectionAutoScroll(mouse)
	point, ok := m.mousePointToContentPoint(mouse.X, mouse.Y, true)
	if !ok {
		return cmd
	}
	if m.selectionEnd == point {
		return cmd
	}
	m.selectionEnd = point
	m.bumpViewportSelectionVersion()
	return cmd
}

func (m *Model) handleViewportMouseRelease(mouse tea.Mouse) tea.Cmd {
	if !m.selecting {
		return nil
	}
	m.cancelSelectionAutoScroll()
	point, ok := m.mousePointToContentPoint(mouse.X, mouse.Y, true)
	if ok {
		m.selectionEnd = point
	}
	hadSelectionRange := m.hasSelectionRange()
	m.selecting = false
	if !hadSelectionRange {
		// No text selected — treat as a click; check for panel/header toggles.
		if m.tryTogglePanelAtClick(mouse) {
			m.syncViewportContent()
		}
		m.clearSelection()
		return nil
	}
	text := m.selectionText()
	if text == "" {
		m.clearSelection()
		return nil
	}
	// Copy selected text, then immediately clear selection so the viewport
	// returns to styled content.  Keeping the viewport stuck in plain-text
	// mode after copy can trigger CJK display artifacts in bubbletea's
	// diff-based renderer when the styled↔plain content transition is large.
	cmd := m.copySelectionToClipboard(text)
	m.clearSelection()
	return cmd
}

// tryTogglePanelAtClick checks if the click hit a block-local expand toggle.
func (m *Model) tryTogglePanelAtClick(mouse tea.Mouse) bool {
	contentLine, ok := m.contentLineAtViewportY(mouse.Y)
	if !ok {
		return false
	}
	bid := m.viewportBlockIDs[contentLine]
	if bid == "" {
		return false
	}
	if contentLine >= 0 && contentLine < len(m.viewportClickTokens) {
		if token := strings.TrimSpace(m.viewportClickTokens[contentLine]); token != "" {
			if m.tryToggleFoldToken(bid, token) {
				return true
			}
		}
	}
	blk := m.doc.Find(bid)
	if blk == nil {
		return false
	}
	return false
}

func (m *Model) handleInputAreaMouse(mouse tea.Mouse, phase mousePhase) (bool, tea.Cmd) {
	if m.activePrompt != nil {
		return false, nil
	}
	if mouse.Button != tea.MouseLeft && phase == mousePhasePress {
		return false, nil
	}
	lines := m.inputPlainLines()
	if len(lines) == 0 {
		return false, nil
	}
	point, ok := m.mousePointToInputPoint(mouse.X, mouse.Y, phase != mousePhasePress, lines)
	switch phase {
	case mousePhasePress:
		if !ok || mouse.Button != tea.MouseLeft {
			return false, nil
		}
		m.clearSelection()
		m.clearFixedSelection()
		m.inputSelecting = true
		m.inputSelectionStart = point
		m.inputSelectionEnd = point
		return true, nil
	case mousePhaseMotion:
		if !m.inputSelecting || !ok {
			return false, nil
		}
		if m.inputSelectionEnd == point {
			return true, nil
		}
		m.inputSelectionEnd = point
		return true, nil
	case mousePhaseRelease:
		if !m.inputSelecting {
			return false, nil
		}
		if ok {
			m.inputSelectionEnd = point
		}
		m.inputSelecting = false
		start, end, ok := normalizedSelectionRange(m.inputSelectionStart, m.inputSelectionEnd, len(lines))
		if !ok {
			m.clearInputSelection()
			return true, nil
		}
		text := selectionTextFromLines(lines, start, end)
		if text == "" {
			m.clearInputSelection()
			return true, nil
		}
		return true, m.copySelectionToClipboard(text)
	}
	return false, nil
}

func (m *Model) handleFixedAreaMouse(mouse tea.Mouse, phase mousePhase) (bool, tea.Cmd) {
	if mouse.Button != tea.MouseLeft && phase == mousePhasePress {
		return false, nil
	}
	switch phase {
	case mousePhasePress:
		region, ok := m.fixedRegionAt(mouse.Y)
		if !ok || mouse.Button != tea.MouseLeft {
			return false, nil
		}
		point, ok := m.fixedRowPoint(region, mouse.X, false)
		if !ok {
			return false, nil
		}
		m.clearSelection()
		m.clearInputSelection()
		m.fixedSelecting = true
		m.fixedSelectionArea = region.area
		m.fixedSelectionStart = point
		m.fixedSelectionEnd = point
		return true, nil
	case mousePhaseMotion:
		if !m.fixedSelecting || m.fixedSelectionArea == fixedSelectionNone {
			return false, nil
		}
		region, ok := m.fixedRegionAt(mouse.Y)
		if !ok || region.area != m.fixedSelectionArea {
			return false, nil
		}
		point, ok := m.fixedRowPoint(region, mouse.X, true)
		if !ok {
			return false, nil
		}
		if m.fixedSelectionEnd == point {
			return true, nil
		}
		m.fixedSelectionEnd = point
		return true, nil
	case mousePhaseRelease:
		if !m.fixedSelecting {
			return false, nil
		}
		if region, ok := m.fixedRegionAt(mouse.Y); ok && region.area == m.fixedSelectionArea {
			if point, ok := m.fixedRowPoint(region, mouse.X, true); ok {
				m.fixedSelectionEnd = point
			}
		}
		m.fixedSelecting = false
		text := m.fixedSelectionText()
		if text == "" {
			m.clearFixedSelection()
			return true, nil
		}
		return true, m.copySelectionToClipboard(text)
	}
	return false, nil
}

func (m *Model) copySelectionToClipboard(text string) tea.Cmd {
	writer := m.cfg.WriteClipboardText
	if writer == nil {
		writer = defaultWriteClipboardText
	}
	return func() tea.Msg {
		return clipboardCopyResultMsg{err: writer(text)}
	}
}

func (m *Model) handleClipboardCopyResult(msg clipboardCopyResultMsg) tea.Cmd {
	const copyHint = "selected text copied to clipboard"
	if msg.err != nil {
		return m.reportClipboardError("copy", msg.err)
	}
	return m.showHint(copyHint, hintOptions{
		priority:       HintPriorityNormal,
		clearOnMessage: true,
		clearAfter:     copyHintDuration,
	})
}

func (m *Model) reportClipboardError(action string, err error) tea.Cmd {
	errLine := compactString(strings.TrimSpace(action+": "+err.Error()), 180)
	if errLine == "" {
		errLine = "clipboard operation failed"
	}
	m.commitLine(errLine)
	m.syncViewportContent()
	return m.showHint(errLine, hintOptions{
		priority:       HintPriorityHigh,
		clearOnMessage: true,
		clearAfter:     copyHintDuration,
	})
}

// ---------------------------------------------------------------------------
// Key handling
// ---------------------------------------------------------------------------

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(tea.KeyReleaseMsg); ok {
		return m, nil
	}
	if handled, cmd := m.handleTerminalResponseGuardKey(msg); handled {
		return m, cmd
	}
	// External prompt input takes priority.
	if m.activePrompt != nil {
		return m, m.handlePromptKey(msg)
	}
	if m.btwOverlay != nil {
		return m.handleBTWOverlayKey(msg)
	}
	if m.turnRunning() && key.Matches(msg, m.keys.Interrupt) {
		return m.requestRunningInterrupt()
	}
	// Command palette overlay.
	if m.showPalette {
		return m, m.handlePaletteKey(msg)
	}
	m.refreshCompletionOverlaysBeforeAccept(msg)
	// @mention overlay — intercept navigation keys so they don't
	// fall through to history browsing.
	if len(m.mentionCandidates) > 0 {
		if handled, cmd := m.handleMentionKey(msg); handled {
			return m, cmd
		}
	}
	// $skill overlay — same pattern.
	if len(m.skillCandidates) > 0 {
		if handled, cmd := m.handleSkillKey(msg); handled {
			return m, cmd
		}
	}
	// /resume overlay.
	if len(m.resumeCandidates) > 0 {
		if handled, cmd := m.handleResumeKey(msg); handled {
			return m, cmd
		}
	}
	// Generic slash-arg overlay (e.g. /model, /connect).
	if m.slashArgActive {
		if handled, cmd := m.handleSlashArgKey(msg); handled {
			return m, cmd
		}
	}
	// Slash command overlay (e.g. /resume, /status).
	if len(m.slashCandidates) > 0 {
		if handled, cmd := m.handleSlashCommandKey(msg); handled {
			return m, cmd
		}
	}
	m.clearInputSelection()
	if !key.Matches(msg, m.keys.Quit) {
		m.ctrlCArmed = false
		m.lastCtrlCAt = time.Time{}
	}
	if matchesModeKey(msg, m.keys.Mode) && m.cfg.ToggleMode != nil {
		hint, err := m.cfg.ToggleMode()
		if err != nil {
			return m, m.showHint(err.Error(), hintOptions{
				priority:       HintPriorityHigh,
				clearOnMessage: true,
				clearAfter:     copyHintDuration,
			})
		}
		if strings.TrimSpace(hint) == "" {
			hint = "mode updated"
		}
		if m.cfg.RefreshWorkspace != nil {
			if workspace := strings.TrimSpace(m.cfg.RefreshWorkspace()); workspace != "" {
				m.setWorkspaceDisplay(workspace)
			}
		}
		if m.cfg.RefreshStatus != nil {
			modelText, contextText := m.cfg.RefreshStatus()
			m.statusModel = normalizeStatusModel(modelText)
			m.statusContext = strings.TrimSpace(contextText)
		}
		if m.cfg.RefreshStatusView != nil {
			m.statusView = m.cfg.RefreshStatusView()
			m.normalizeStatusViewWorkspace()
		}
		m.refreshModeLabelFromConfig()
		return m, m.showHint(hint, hintOptions{
			priority:       HintPriorityNormal,
			clearOnMessage: true,
			clearAfter:     copyHintDuration,
		})
	}

	switch {
	case isViewportEndKey(msg) && (!m.isViewportFollowTail() || !m.viewport.AtBottom()):
		m.setViewportFollowState(viewportFollowTail)
		if m.offscreenViewportDirty || m.viewportSyncPending {
			m.syncViewportContent()
		}
		m.materializeViewportContentIfStale()
		m.viewport.GotoBottom()
		return m, tea.Batch(m.touchViewportScrollbar(), m.resumeRunningAnimationIfNeeded())
	case key.Matches(msg, m.keys.HalfPageUp):
		m.materializeViewportContentIfStale()
		m.viewport.HalfPageUp()
		m.refreshViewportFollowStateFromOffset()
		return m, m.touchViewportScrollbar()
	case key.Matches(msg, m.keys.HalfPageDown):
		wasFollowTail := m.isViewportFollowTail()
		m.materializeViewportContentIfStale()
		m.viewport.HalfPageDown()
		m.refreshViewportFollowStateFromOffset()
		var resumeCmd tea.Cmd
		if m.isViewportFollowTail() && m.offscreenViewportDirty {
			m.syncViewportContent()
			resumeCmd = m.resumeRunningAnimationIfNeeded()
		} else if !wasFollowTail && m.isViewportFollowTail() {
			resumeCmd = m.resumeRunningAnimationIfNeeded()
		}
		return m, tea.Batch(m.touchViewportScrollbar(), resumeCmd)
	case key.Matches(msg, m.keys.PageUp):
		m.materializeViewportContentIfStale()
		m.viewport.PageUp()
		m.refreshViewportFollowStateFromOffset()
		return m, m.touchViewportScrollbar()
	case key.Matches(msg, m.keys.PageDown):
		wasFollowTail := m.isViewportFollowTail()
		m.materializeViewportContentIfStale()
		m.viewport.PageDown()
		m.refreshViewportFollowStateFromOffset()
		var resumeCmd tea.Cmd
		if m.isViewportFollowTail() && m.offscreenViewportDirty {
			m.syncViewportContent()
			resumeCmd = m.resumeRunningAnimationIfNeeded()
		} else if !wasFollowTail && m.isViewportFollowTail() {
			resumeCmd = m.resumeRunningAnimationIfNeeded()
		}
		return m, tea.Batch(m.touchViewportScrollbar(), resumeCmd)

	case key.Matches(msg, m.keys.Quit):
		if m.turnRunning() {
			return m, m.showHint("press Esc to interrupt running task", hintOptions{
				priority:       HintPriorityHigh,
				clearOnMessage: true,
				clearAfter:     copyHintDuration,
			})
		}
		now := time.Now()
		if m.ctrlCArmed && now.Sub(m.lastCtrlCAt) <= ctrlCExitWindow {
			m.quit = true
			return m, tea.Quit
		}
		current := strings.TrimSpace(m.textarea.Value())
		if current != "" || len(m.inputAttachments) > 0 {
			m.recordHistoryEntry(current, m.inputAttachments)
		}
		m.textarea.SetValue("")
		m.textarea.CursorStart()
		m.adjustTextareaHeight()
		m.input = m.input[:0]
		m.cursor = 0
		m.clearInputAttachments()
		if m.cfg.ClearAttachments != nil {
			m.cfg.ClearAttachments()
		}
		m.historyIndex = -1
		m.historyDraft = ""
		m.historyDraftAttachments = nil
		m.ctrlCArmed = true
		m.ctrlCArmSeq++
		m.lastCtrlCAt = now
		return m, tea.Batch(
			expireCtrlCCmd(now, m.ctrlCArmSeq),
			m.showHint("press Ctrl+C again to quit", hintOptions{
				priority:       HintPriorityCritical,
				clearOnMessage: false,
				clearAfter:     ctrlCExitWindow,
			}),
		)

	case msg.String() == "ctrl+d":
		if !m.turnRunning() && len(m.input) == 0 && m.textarea.Value() == "" {
			m.quit = true
			return m, tea.Quit
		}
		return m, nil

	case msg.String() == "ctrl+p":
		if m.turnRunning() {
			return m, nil
		}
		m.togglePalette()
		return m, m.paletteAnimationCmd()

	case key.Matches(msg, m.keys.Back):
		if m.turnRunning() {
			return m.requestRunningInterrupt()
		}
		m.clearInputOverlays()
		return m, nil

	case key.Matches(msg, m.keys.HistoryPrev):
		if m.shouldUseTextareaVerticalNavigation(-1) {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			m.syncInputFromTextarea()
			return m, cmd
		}
		if !m.turnRunning() && len(m.history) > 0 {
			val := m.textarea.Value()
			if m.historyIndex == -1 {
				m.historyDraft = val
				m.historyDraftAttachments = cloneInputAttachments(m.inputAttachments)
				m.historyIndex = len(m.history) - 1
			} else if m.historyIndex > 0 {
				m.historyIndex--
			}
			if m.historyIndex >= 0 && m.historyIndex < len(m.history) {
				m.restoreHistoryEntry(m.history[m.historyIndex], m.historyAttachments[m.historyIndex])
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.HistoryNext):
		if m.shouldUseTextareaVerticalNavigation(1) {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(msg)
			m.syncInputFromTextarea()
			return m, cmd
		}
		if !m.turnRunning() && m.historyIndex != -1 {
			if m.historyIndex < len(m.history)-1 {
				m.historyIndex++
				m.restoreHistoryEntry(m.history[m.historyIndex], m.historyAttachments[m.historyIndex])
			} else {
				m.historyIndex = -1
				m.restoreHistoryEntry(m.historyDraft, m.historyDraftAttachments)
				m.historyDraft = ""
				m.historyDraftAttachments = nil
				m.adjustTextareaHeight()
			}
		}
		return m, nil

	case matchesInsertNewlineKey(msg, m.keys.InsertNewline):
		m.insertComposerText("\n")
		return m, m.requestCompletionRefresh()

	case key.Matches(msg, m.keys.Complete):
		val := m.textarea.Value()
		m.syncInputFromTextarea()
		switch {
		case len(m.mentionCandidates) > 0:
			m.applyMentionCompletion()
			m.syncTextareaFromInput()
		case len(m.skillCandidates) > 0:
			m.applySkillCompletion()
			m.syncTextareaFromInput()
		case len(m.resumeCandidates) > 0:
			m.applyResumeCompletion()
			m.syncTextareaFromInput()
		case len(m.slashArgCandidates) > 0:
			cmd := m.applySlashArgCompletion()
			m.syncTextareaFromInput()
			if cmd != nil {
				return m, cmd
			}
		case len(m.slashCandidates) > 0:
			m.applySlashCommandCompletion()
			m.syncTextareaFromInput()
		case strings.HasPrefix(strings.TrimSpace(val), "/") && !strings.Contains(strings.TrimSpace(val), " "):
			m.applySlashCommandCompletion()
			m.syncTextareaFromInput()
		}
		return m, nil

	case key.Matches(msg, m.keys.Send):
		line, attachments := submissionInput(m.textarea.Value(), m.inputAttachments)
		if line == "" && len(attachments) == 0 {
			return m, nil
		}
		mode := m.submissionModeForLine(line)
		if m.turnRunning() {
			if m.isConfiguredSlashControlLine(line) && mode != SubmissionModeOverlay {
				return m, m.showHint("slash commands are unavailable while running", hintOptions{
					priority:       HintPriorityHigh,
					clearOnMessage: true,
					clearAfter:     copyHintDuration,
				})
			}
			return m.submitLineWithDisplayAndAttachments(line, m.displayLineWithInputAttachments(line, attachments), inputAttachmentsToSubmission(attachments))
		}
		m.setInputAttachments(attachments)
		if (line == "/connect" || strings.HasPrefix(line, "/connect ")) && m.isCommandAvailable("connect") {
			if def := m.findWizard("connect"); def != nil {
				query := strings.TrimSpace(strings.TrimPrefix(line, "/connect"))
				m.startWizardWithQuery(def, query)
				return m, nil
			}
			return m.submitLine("/connect")
		}
		if m.tryOpenSlashArgPicker(line) {
			return m, nil
		}
		return m.submitLine(line)

	case key.Matches(msg, m.keys.Clear):
		m.textarea.SetValue("")
		m.textarea.CursorStart()
		m.adjustTextareaHeight()
		m.input = m.input[:0]
		m.cursor = 0
		m.clearInputAttachments()
		if m.cfg.ClearAttachments != nil {
			m.cfg.ClearAttachments()
		}
		m.clearInputOverlays()
		return m, nil

	case key.Matches(msg, m.keys.ImagePaste):
		if m.turnRunning() {
			return m, m.showHint("image paste unavailable while running; press Esc to interrupt first", hintOptions{
				priority:       HintPriorityHigh,
				clearOnMessage: true,
				clearAfter:     systemHintDuration,
			})
		}
		pastedImage, err := m.pasteClipboardImage()
		if err != nil {
			return m, m.reportClipboardError("paste image", err)
		}
		if pastedImage {
			return m, nil
		}
		pasted, err := m.pasteClipboardText()
		if err != nil {
			return m, m.reportClipboardError("paste", err)
		}
		if pasted {
			return m, nil
		}
		return m, nil

	case key.Matches(msg, m.keys.TextPaste):
		pasted, textErr := m.pasteClipboardText()
		if textErr == nil && pasted {
			return m, nil
		}
		if !m.turnRunning() && m.shouldFallbackTextPasteToImage(msg) {
			pastedImage, imageErr := m.pasteClipboardImage()
			if imageErr == nil && pastedImage {
				return m, nil
			}
		}
		if textErr != nil {
			return m, m.reportClipboardError("paste", textErr)
		}
		if pasted {
			return m, nil
		}
		return m, nil

	default:
		// Backspace should remove an attachment token when the visual cursor is
		// sitting right after that token, before it edits surrounding text.
		if !m.turnRunning() && m.attachmentCount > 0 &&
			(msg.String() == "backspace" || msg.String() == "ctrl+h") &&
			m.removeAttachmentAtCursor() {
			m.dismissVisibleHint()
			return m, nil
		}
		// Forward to textarea for general text input.
		before := m.textarea.Value()
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(msg)
		after := m.textarea.Value()
		m.inputAttachments = adjustAttachmentOffsetsForTextEdit(m.inputAttachments, before, m.textarea.Value())
		m.syncAttachmentSummary()
		m.syncInputFromTextarea()

		// Trigger @mention / $skill / slash overlays whenever the textarea value
		// actually changed. Relying on key metadata alone is not robust across
		// terminal protocols; under real PTY input some printable keys may not
		// populate msg.Key().Text even though the composer changed.
		if before != after {
			return m, tea.Batch(cmd, m.requestCompletionRefresh())
		}
		return m, cmd
	}
}

type terminalResponsePendingFlushMsg struct {
	seq uint64
}

type terminalResponseFragmentMatch int

const (
	terminalResponseNoMatch terminalResponseFragmentMatch = iota
	terminalResponsePrefix
	terminalResponseComplete
)

func (m *Model) handleTerminalResponseGuardKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if m == nil {
		return false, nil
	}
	if m.terminalResponseGuardUntil.IsZero() || time.Now().After(m.terminalResponseGuardUntil) {
		m.flushTerminalResponsePending()
		return false, nil
	}
	keyEvent := msg.Key()
	if keyEvent.Mod != 0 || keyEvent.Text == "" {
		m.flushTerminalResponsePending()
		return false, nil
	}
	text := keyEvent.Text
	if strings.TrimSpace(text) == "" {
		m.flushTerminalResponsePending()
		return false, nil
	}
	if containsControlByte(text) {
		m.clearTerminalResponsePending()
		return true, nil
	}

	if m.terminalResponsePending != "" {
		candidate := m.terminalResponsePending + text
		switch terminalResponseFragmentState(candidate) {
		case terminalResponseComplete:
			m.clearTerminalResponsePending()
			return true, nil
		case terminalResponsePrefix:
			m.terminalResponsePending = candidate
			return true, m.scheduleTerminalResponsePendingFlush()
		default:
			m.flushTerminalResponsePending()
			return false, nil
		}
	}

	switch terminalResponseFragmentState(text) {
	case terminalResponseComplete:
		m.clearTerminalResponsePending()
		return true, nil
	case terminalResponsePrefix:
		m.terminalResponsePending = text
		return true, m.scheduleTerminalResponsePendingFlush()
	default:
		return false, nil
	}
}

func (m *Model) handleTerminalResponsePendingFlush(msg terminalResponsePendingFlushMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.seq != m.terminalResponsePendingSeq || m.terminalResponsePending == "" {
		return m, nil
	}
	m.flushTerminalResponsePending()
	return m, m.requestCompletionRefresh()
}

func (m *Model) scheduleTerminalResponsePendingFlush() tea.Cmd {
	if m == nil {
		return nil
	}
	m.terminalResponsePendingSeq++
	seq := m.terminalResponsePendingSeq
	return tea.Tick(terminalResponsePendingFlushDelay, func(time.Time) tea.Msg {
		return terminalResponsePendingFlushMsg{seq: seq}
	})
}

func (m *Model) flushTerminalResponsePending() {
	if m == nil || m.terminalResponsePending == "" {
		return
	}
	pending := m.terminalResponsePending
	m.clearTerminalResponsePending()
	m.insertComposerText(pending)
}

func (m *Model) clearTerminalResponsePending() {
	if m == nil {
		return
	}
	if m.terminalResponsePending != "" {
		m.terminalResponsePending = ""
	}
	m.terminalResponsePendingSeq++
}

func looksLikeTerminalResponseFragment(text string) bool {
	return terminalResponseFragmentState(text) == terminalResponseComplete
}

// containsControlByte reports whether s contains ESC (0x1B), CSI (0x9B), or OSC (0x9D).
// These are terminal control-introducing bytes; checking bytes avoids invalid-UTF-8 lint warnings.
func containsControlByte(s string) bool {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 0x1b, 0x9b, 0x9d:
			return true
		}
	}
	return false
}

func terminalResponseFragmentState(text string) terminalResponseFragmentMatch {
	text = strings.TrimSpace(text)
	if text == "" {
		return terminalResponseNoMatch
	}
	if containsControlByte(text) {
		return terminalResponseComplete
	}
	if isRepeatedDAZeroTail(text) {
		return terminalResponseComplete
	}
	if isRepeatedDAZeroTailPrefix(text) {
		return terminalResponsePrefix
	}
	if isPrefixedTerminalNumericReport(text, "c") {
		return terminalResponseComplete
	}
	if isPrefixedTerminalNumericReport(text, "u") {
		return terminalResponseComplete
	}
	if isPrefixedTerminalNumericReport(text, "$y") {
		return terminalResponseComplete
	}
	if isPrefixedTerminalNumericReportPrefix(text) {
		return terminalResponsePrefix
	}
	if state := terminalColorReportState(text); state != terminalResponseNoMatch {
		return state
	}
	return terminalResponseNoMatch
}

func isRepeatedDAZeroTail(text string) bool {
	if len(text) < 2 || len(text)%2 != 0 {
		return false
	}
	for i := 0; i < len(text); i += 2 {
		if text[i:i+2] != "0c" {
			return false
		}
	}
	return true
}

func isRepeatedDAZeroTailPrefix(text string) bool {
	if text == "" {
		return false
	}
	for i := 0; i < len(text); i++ {
		switch i % 2 {
		case 0:
			if text[i] != '0' {
				return false
			}
		default:
			if text[i] != 'c' {
				return false
			}
		}
	}
	return len(text)%2 == 1
}

func isPrefixedTerminalNumericReport(text string, suffix string) bool {
	if text == "" || suffix == "" || !strings.HasSuffix(text, suffix) {
		return false
	}
	body := strings.TrimSuffix(text, suffix)
	if body == "" {
		return false
	}
	switch body[0] {
	case '?', '>', '=':
	default:
		return false
	}
	body = body[1:]
	if body == "" {
		return false
	}
	hasDigit := false
	for _, r := range body {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == ';':
		default:
			return false
		}
	}
	return hasDigit
}

func isPrefixedTerminalNumericReportPrefix(text string) bool {
	if text == "" {
		return false
	}
	switch text[0] {
	case '?', '>', '=':
	default:
		return false
	}
	body := text[1:]
	if body == "" {
		return true
	}
	hasDigit := false
	for idx, r := range body {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case r == ';':
		case r == '$' && hasDigit && idx == len(body)-1:
			return true
		default:
			return false
		}
	}
	return true
}

func terminalColorReportState(text string) terminalResponseFragmentMatch {
	lower := strings.ToLower(text)
	prefixes := []string{"10;rgb:", "11;rgb:", "12;rgb:"}
	for _, prefix := range prefixes {
		if strings.HasPrefix(prefix, lower) {
			return terminalResponsePrefix
		}
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		body := strings.TrimPrefix(lower, prefix)
		if body == "" {
			return terminalResponsePrefix
		}
		parts := strings.Split(body, "/")
		if len(parts) > 3 {
			return terminalResponseNoMatch
		}
		for _, part := range parts {
			if part == "" {
				return terminalResponsePrefix
			}
			for _, r := range part {
				if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
					return terminalResponseNoMatch
				}
			}
		}
		if len(parts) < 3 {
			return terminalResponsePrefix
		}
		if len(parts[0]) >= 2 && len(parts[0]) == len(parts[1]) && len(parts[1]) == len(parts[2]) {
			return terminalResponseComplete
		}
		return terminalResponsePrefix
	}
	return terminalResponseNoMatch
}

func (m *Model) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if m.activePrompt != nil {
		return m, m.handlePromptPaste(msg)
	}
	if m.btwOverlay != nil {
		return m, nil
	}
	before := m.textarea.Value()
	var cmd tea.Cmd
	m.textarea, cmd = m.textarea.Update(msg)
	m.inputAttachments = adjustAttachmentOffsetsForTextEdit(m.inputAttachments, before, m.textarea.Value())
	m.syncAttachmentSummary()
	m.syncInputFromTextarea()
	return m, tea.Batch(cmd, m.requestCompletionRefresh())
}

func (m *Model) insertComposerText(text string) {
	if m == nil || text == "" {
		return
	}
	before := m.textarea.Value()
	m.textarea.InsertString(text)
	m.inputAttachments = adjustAttachmentOffsetsForTextEdit(m.inputAttachments, before, m.textarea.Value())
	m.syncAttachmentSummary()
	m.syncInputFromTextarea()
}

func (m *Model) pasteClipboardImage() (bool, error) {
	if m == nil || m.cfg.PasteClipboardImage == nil {
		return false, nil
	}
	oldAttachmentCount := len(m.inputAttachments)
	names, _, err := m.cfg.PasteClipboardImage()
	if err != nil {
		return false, err
	}
	if len(names) == 0 {
		return false, nil
	}
	added := names
	if oldAttachmentCount < len(names) {
		added = names[oldAttachmentCount:]
	}
	m.insertAttachmentsAtCursor(added)
	m.dismissVisibleHint()
	m.syncTextareaChrome()
	return len(added) > 0, nil
}

func (m *Model) shouldFallbackTextPasteToImage(msg tea.KeyMsg) bool {
	if msg.String() != "ctrl+v" {
		return false
	}
	if strings.EqualFold(clipboardGOOS, "windows") {
		return true
	}
	return isWSL()
}

func (m *Model) submitLine(line string) (tea.Model, tea.Cmd) {
	return m.submitLineWithDisplayAndAttachments(line, m.displayLineWithAttachments(line), inputAttachmentsToSubmission(m.inputAttachments))
}

func (m *Model) requestRunningInterrupt() (tea.Model, tea.Cmd) {
	m.clearInputOverlays()
	if m.cfg.CancelRunning == nil {
		return m, nil
	}
	if m.runningInterruptRequested {
		return m, m.showHint("interrupt already requested", hintOptions{
			priority:       HintPriorityHigh,
			clearOnMessage: true,
			clearAfter:     copyHintDuration,
		})
	}
	cancel := m.cfg.CancelRunning
	m.runningInterruptRequested = true
	m.setRunningInterruptActivity()
	return m, tea.Batch(
		m.showHint("interrupt requested", hintOptions{
			priority:       HintPriorityCritical,
			clearOnMessage: true,
			clearAfter:     systemHintDuration,
		}),
		func() tea.Msg {
			return RunningInterruptResultMsg{Accepted: cancel()}
		},
	)
}

func (m *Model) submitLineWithDisplay(execLine string, displayLine string) (tea.Model, tea.Cmd) {
	return m.submitLineWithDisplayAndAttachments(execLine, displayLine, inputAttachmentsToSubmission(m.inputAttachments))
}

func (m *Model) submitLineWithDisplayAndAttachments(execLine string, displayLine string, attachments []Attachment) (tea.Model, tea.Cmd) {
	return m.submitLineWithDisplayAndAttachmentsOptions(execLine, displayLine, attachments, submitLineOptions{recordHistory: true})
}

type submitLineOptions struct {
	recordHistory bool
}

func (m *Model) submitLineWithDisplayAndAttachmentsOptions(execLine string, displayLine string, attachments []Attachment, opts submitLineOptions) (tea.Model, tea.Cmd) {
	alreadyRunning := m.turnRunning()
	mode := m.submissionModeForLine(execLine)
	if alreadyRunning && mode != SubmissionModeOverlay && m.isConfiguredSlashControlLine(execLine) {
		return m, m.showHint("A turn is still running. Wait for it to finish or interrupt it before sending another prompt.", hintOptions{
			priority:       HintPriorityHigh,
			clearOnMessage: true,
			clearAfter:     copyHintDuration,
		})
	}
	deferUntilIdle := alreadyRunning && mode != SubmissionModeOverlay && !m.canSubmitRunningPromptNow()
	layoutMayChange := mode == SubmissionModeOverlay
	attachments = cloneAttachments(attachments)
	displayLine = strings.TrimSpace(displayLine)
	switch mode {
	case SubmissionModeOverlay:
		m.openBTWOverlay(execLine)
	default:
		deferDisplayLine := m.deferLocalUserDisplayLine(execLine)
		if alreadyRunning {
			m.pendingQueue = append(m.pendingQueue, pendingPrompt{
				execLine:    strings.TrimSpace(execLine),
				displayLine: displayLine,
				attachments: cloneAttachments(attachments),
				dispatched:  !deferUntilIdle,
			})
		} else if !deferDisplayLine {
			m.commitUserDisplayLine(displayLine)
		}
	}
	m.setViewportFollowState(viewportFollowTail)

	// Push to history.
	if opts.recordHistory && mode != SubmissionModeOverlay {
		m.recordHistoryEntry(strings.TrimSpace(execLine), attachmentsToInputAttachments(attachments))
		m.historyIndex = -1
		m.historyDraft = ""
		m.historyDraftAttachments = nil
	}
	submission := Submission{
		Text:        strings.TrimSpace(execLine),
		DisplayText: displayLine,
		Attachments: attachments,
		Mode:        mode,
	}

	// Clear input.
	m.textarea.SetValue("")
	m.textarea.CursorStart()
	m.adjustTextareaHeight()
	m.input = m.input[:0]
	m.cursor = 0
	m.clearInputAttachments()
	m.clearInputOverlays()
	if layoutMayChange {
		m.ensureViewportLayout()
	}

	if !alreadyRunning && mode != SubmissionModeOverlay {
		m.beginLiveTurn(mode, mode == SubmissionModeDefault && !m.isConfiguredSlashControlLine(execLine), time.Now())
	} else {
		m.ensureViewportLayout()
	}
	m.syncViewportContent()

	if deferUntilIdle {
		return m, nil
	}
	if m.cfg.ExecuteLine == nil {
		if !alreadyRunning {
			m.stopLiveTurn()
		}
		return m, nil
	}
	cmds := []tea.Cmd{
		m.executeLineCmd(submission),
		m.scheduleSpinnerTick(),
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) deferLocalUserDisplayLine(line string) bool {
	name := slashCommandName(line)
	if name == "" {
		return false
	}
	if strings.EqualFold(name, "review") && isCoreLocalSlashCommand(name) {
		return true
	}
	return m.isKnownDynamicAgentSlashLine(line)
}

func (m *Model) executeLineCmd(submission Submission) tea.Cmd {
	return func() tea.Msg {
		if m.cfg.executeLineCmd != nil {
			return m.cfg.executeLineCmd(submission)
		}
		return m.cfg.ExecuteLine(submission)
	}
}

func (m *Model) canSubmitRunningPromptNow() bool {
	if m == nil || m.cfg.CanSubmitRunningPrompt == nil {
		return true
	}
	return m.cfg.CanSubmitRunningPrompt()
}

func (m *Model) submitPendingPrompt(prompt pendingPrompt) (tea.Model, tea.Cmd) {
	return m.submitLineWithDisplayAndAttachmentsOptions(prompt.execLine, prompt.displayLine, prompt.attachments, submitLineOptions{})
}

func (m *Model) isKnownDynamicAgentSlashLine(line string) bool {
	name := slashCommandName(line)
	if name == "" || m == nil {
		return false
	}
	if _, ok := lookupSlashCommandSpec(name); ok {
		return false
	}
	for _, command := range m.cfg.Commands {
		if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(command, "/")), name) {
			return true
		}
	}
	return false
}

func (m *Model) allowsBTWSubmission() bool {
	if m == nil {
		return false
	}
	for _, one := range m.cfg.Commands {
		if strings.EqualFold(strings.TrimSpace(one), "btw") {
			return true
		}
	}
	return false
}

func (m *Model) tryToggleFoldToken(blockID string, token string) bool {
	if key, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_reasoning:"); ok {
		return m.tryToggleACPReasoningToken(blockID, key)
	}
	if key, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_exploration_stage:"); ok {
		return m.tryToggleACPExplorationStageToken(blockID, key)
	}
	if key, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_exploration_stable:"); ok {
		return m.tryToggleACPExplorationStageToken(blockID, key)
	}
	if key, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_task_stage:"); ok {
		return m.tryToggleACPExplorationStageToken(blockID, key)
	}
	callID, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_tool_panel:")
	if !ok || strings.TrimSpace(callID) == "" {
		return false
	}
	switch blk := m.doc.Find(strings.TrimSpace(blockID)).(type) {
	case *ParticipantTurnBlock:
		return blk.toggleToolPanelClick(callID)
	case *MainACPTurnBlock:
		return blk.toggleToolPanelClick(callID)
	default:
		return false
	}
}

func (m *Model) tryToggleACPToolPanelToken(blockID string, token string) bool {
	return m.tryToggleFoldToken(blockID, token)
}

func (m *Model) tryToggleACPReasoningToken(blockID string, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	switch blk := m.doc.Find(strings.TrimSpace(blockID)).(type) {
	case *ParticipantTurnBlock:
		return blk.toggleReasoningExpanded(key)
	case *MainACPTurnBlock:
		return blk.toggleReasoningExpanded(key)
	default:
		return false
	}
}

func (m *Model) tryToggleACPExplorationStageToken(blockID string, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	switch blk := m.doc.Find(strings.TrimSpace(blockID)).(type) {
	case *ParticipantTurnBlock:
		return blk.toggleExplorationExpanded(key)
	case *MainACPTurnBlock:
		return blk.toggleExplorationExpanded(key)
	default:
		return false
	}
}

func (m *Model) submissionModeForLine(line string) SubmissionMode {
	trimmed := strings.TrimSpace(line)
	if m.allowsBTWSubmission() && (trimmed == "/btw" || strings.HasPrefix(trimmed, "/btw ")) {
		return SubmissionModeOverlay
	}
	return SubmissionModeDefault
}

func (m *Model) openBTWOverlay(line string) {
	if m == nil {
		return
	}
	question := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "/btw"))
	m.btwDismissed = false
	m.btwOverlay = &btwOverlayState{
		Question: question,
		Loading:  true,
		Scroll:   0,
	}
}

func (m *Model) handleBTWOverlayKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m == nil || m.btwOverlay == nil {
		return m, nil
	}
	switch {
	case key.Matches(msg, m.keys.Back):
		m.dropPendingStreamSmoothing(streamSmoothingKey("btw", "", "answer", ""))
		m.btwOverlay = nil
		m.btwDismissed = true
		m.ensureViewportLayout()
		return m, nil
	case key.Matches(msg, m.keys.HistoryPrev):
		m.scrollBTW(-1)
		return m, nil
	case key.Matches(msg, m.keys.HistoryNext):
		m.scrollBTW(1)
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		m.scrollBTW(-m.btwVisibleBudget())
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		m.scrollBTW(m.btwVisibleBudget())
		return m, nil
	default:
		return m, nil
	}
}
