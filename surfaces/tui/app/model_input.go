package tuiapp

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
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
	case *SubagentPanelBlock:
		if !block.CanScroll(delta, ctx) {
			return false, false
		}
		changed = block.Scroll(delta, ctx)
		if changed {
			touchScrollbarDeadline(block.scrollbarVisibleUntilPtr(), time.Now())
			m.markViewportBlockDirty(block.BlockID())
		}
		return true, changed
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
	point, ok := m.mousePointToContentPoint(mouse.X, mouse.Y, true)
	if !ok {
		return nil
	}
	if m.selectionEnd == point {
		return nil
	}
	m.selectionEnd = point
	m.bumpViewportSelectionVersion()
	return nil
}

func (m *Model) handleViewportMouseRelease(mouse tea.Mouse) tea.Cmd {
	if !m.selecting {
		return nil
	}
	point, ok := m.mousePointToContentPoint(mouse.X, mouse.Y, true)
	if ok {
		m.selectionEnd = point
	}
	m.selecting = false
	text := m.selectionText()
	if text == "" {
		// No text selected — treat as a click; check for panel header toggle.
		if m.tryTogglePanelAtClick(mouse) {
			m.syncViewportContent()
		}
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
			if m.tryToggleACPToolPanelToken(bid, token) {
				return true
			}
		}
	}
	blk := m.doc.Find(bid)
	if blk == nil {
		return false
	}
	if _, ok := blk.(*TranscriptBlock); ok {
		if panel := m.findInlineSubagentPanelByAnchorBlockID(bid); panel != nil {
			m.toggleInlineSubagentPanel(panel)
			return true
		}
	}
	if turn, ok := blk.(*ParticipantTurnBlock); ok {
		if contentLine > 0 && m.viewportBlockIDs[contentLine-1] == bid {
			return false
		}
		turn.Expanded = !turn.Expanded
		return true
	}
	if sp, ok := blk.(*SubagentPanelBlock); ok {
		// Inline subagent panels toggle from the tool call line only.
		_ = sp
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
	errLine := strings.TrimSpace(action + ": " + err.Error())
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
	// External prompt input takes priority.
	if m.activePrompt != nil {
		return m, m.handlePromptKey(msg)
	}
	if m.btwOverlay != nil {
		return m.handleBTWOverlayKey(msg)
	}
	if m.running && key.Matches(msg, m.keys.Interrupt) {
		m.clearInputOverlays()
		if m.cfg.CancelRunning != nil && m.cfg.CancelRunning() {
			return m, m.showHint("interrupt requested", hintOptions{
				priority:       HintPriorityCritical,
				clearOnMessage: true,
				clearAfter:     systemHintDuration,
			})
		}
		return m, nil
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
	// Generic slash-arg overlay (e.g. /model, /sandbox, /connect).
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
	if matchesModeKey(msg, m.keys.Mode) && !m.running && m.cfg.ToggleMode != nil {
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
				m.cfg.Workspace = workspace
			}
		}
		if m.cfg.RefreshStatus != nil {
			modelText, contextText := m.cfg.RefreshStatus()
			m.statusModel = normalizeStatusModel(modelText)
			m.statusContext = strings.TrimSpace(contextText)
		}
		if m.cfg.RefreshStatusView != nil {
			m.statusView = m.cfg.RefreshStatusView()
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
		if m.running {
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
		if !m.running && len(m.input) == 0 && m.textarea.Value() == "" {
			m.quit = true
			return m, tea.Quit
		}
		return m, nil

	case msg.String() == "ctrl+p":
		if m.running {
			return m, nil
		}
		m.togglePalette()
		return m, m.paletteAnimationCmd()

	case key.Matches(msg, m.keys.Back):
		if m.running {
			m.clearInputOverlays()
			if m.cfg.CancelRunning != nil && m.cfg.CancelRunning() {
				return m, m.showHint("interrupt requested", hintOptions{
					priority:       HintPriorityCritical,
					clearOnMessage: true,
					clearAfter:     systemHintDuration,
				})
			}
			return m, nil
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
		if !m.running && len(m.history) > 0 {
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
		if !m.running && m.historyIndex != -1 {
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
			m.applySlashArgCompletion()
			m.syncTextareaFromInput()
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
		if m.running {
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
		if m.running {
			return m, m.showHint("image paste unavailable while running; press Esc to interrupt first", hintOptions{
				priority:       HintPriorityHigh,
				clearOnMessage: true,
				clearAfter:     systemHintDuration,
			})
		}
		oldAttachmentCount := len(m.inputAttachments)
		if m.cfg.PasteClipboardImage != nil {
			names, _, err := m.cfg.PasteClipboardImage()
			if err != nil {
				errLine := "paste: " + err.Error()
				m.commitLine(errLine)
				m.syncViewportContent()
				return m, nil
			}
			if len(names) > 0 {
				added := names
				if oldAttachmentCount < len(names) {
					added = names[oldAttachmentCount:]
				}
				m.insertAttachmentsAtCursor(added)
				m.dismissVisibleHint()
				m.syncTextareaChrome()
				return m, nil
			}
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
		pasted, err := m.pasteClipboardText()
		if err != nil {
			return m, m.reportClipboardError("paste", err)
		}
		if pasted {
			return m, nil
		}
		return m, nil

	default:
		// Backspace should remove an attachment token when the visual cursor is
		// sitting right after that token, before it edits surrounding text.
		if !m.running && m.attachmentCount > 0 &&
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

func (m *Model) submitLine(line string) (tea.Model, tea.Cmd) {
	return m.submitLineWithDisplayAndAttachments(line, m.displayLineWithAttachments(line), inputAttachmentsToSubmission(m.inputAttachments))
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
	alreadyRunning := m.running
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
		if alreadyRunning {
			m.pendingQueue = append(m.pendingQueue, pendingPrompt{
				execLine:    strings.TrimSpace(execLine),
				displayLine: displayLine,
				attachments: cloneAttachments(attachments),
				dispatched:  !deferUntilIdle,
			})
		} else {
			m.commitUserDisplayLine(displayLine)
		}
	}
	if !alreadyRunning && m.shouldAutoFollowSubmittedSideACP(execLine, mode) {
		m.setViewportFollowState(viewportFollowTail)
	}

	// Push to history.
	if opts.recordHistory && mode != SubmissionModeOverlay {
		m.recordHistoryEntry(strings.TrimSpace(execLine), attachmentsToInputAttachments(attachments))
		m.historyIndex = -1
		m.historyDraft = ""
		m.historyDraftAttachments = nil
	}
	submission := Submission{
		Text:        strings.TrimSpace(execLine),
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

	m.running = true
	if !alreadyRunning {
		m.runStartedAt = time.Now()
		m.hasLastRunDuration = false
		m.showTurnDivider = mode == SubmissionModeDefault && !m.isConfiguredSlashControlLine(execLine)
		m.startRunningAnimation()
	} else {
		m.ensureViewportLayout()
	}
	m.syncViewportContent()

	if deferUntilIdle {
		return m, nil
	}
	if m.cfg.ExecuteLine == nil {
		if !alreadyRunning {
			m.running = false
		}
		return m, nil
	}
	cmds := []tea.Cmd{
		func() tea.Msg {
			return m.cfg.ExecuteLine(submission)
		},
		m.scheduleSpinnerTick(),
	}
	return m, tea.Batch(cmds...)
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

func (m *Model) shouldAutoFollowSubmittedSideACP(line string, mode SubmissionMode) bool {
	if mode != SubmissionModeDefault || m == nil || m.viewportFollowState == viewportSelecting || m.hasSelectionRange() {
		return false
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "@") {
		return true
	}
	return m.isKnownDynamicAgentSlashLine(trimmed)
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

func (m *Model) tryToggleACPToolPanelToken(blockID string, token string) bool {
	if key, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_reasoning:"); ok {
		return m.tryToggleACPReasoningToken(blockID, key)
	}
	if key, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_exploration_stage:"); ok {
		return m.tryToggleACPExplorationStageToken(blockID, key)
	}
	if key, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_task_stage:"); ok {
		return m.tryToggleACPExplorationStageToken(blockID, key)
	}
	if rawIDs, ok := strings.CutPrefix(strings.TrimSpace(token), "acp_exploration_group:"); ok {
		return m.tryToggleACPExplorationGroupToken(blockID, rawIDs)
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

func (m *Model) tryToggleACPExplorationGroupToken(blockID string, rawIDs string) bool {
	callIDs := splitNonEmptyCommaList(rawIDs)
	if len(callIDs) == 0 {
		return false
	}
	switch blk := m.doc.Find(strings.TrimSpace(blockID)).(type) {
	case *ParticipantTurnBlock:
		for _, callID := range callIDs {
			blk.setToolPanelExpanded(callID, true)
		}
		return true
	case *MainACPTurnBlock:
		for _, callID := range callIDs {
			blk.setToolPanelExpanded(callID, true)
		}
		return true
	default:
		return false
	}
}

func splitNonEmptyCommaList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			out = append(out, value)
		}
	}
	return out
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

func (m *Model) commitUserDisplayLine(displayLine string) {
	displayLine = strings.TrimSpace(displayLine)
	if displayLine == "" {
		return
	}
	normalized := normalizeUserDisplayLine(displayLine)
	if m.userDisplayDedupOK && normalized != "" && normalizeUserDisplayLine(m.lastUserDisplayLine) == normalized {
		return
	}
	userLine := "▌ " + displayLine
	if m.hasCommittedLine {
		m.insertSpacing(tuikit.LineStyleUser, userLine)
	}
	block := NewUserNarrativeBlock(displayLine)
	m.doc.Append(block)
	m.lastCommittedStyle = tuikit.LineStyleUser
	m.lastCommittedRaw = userLine
	m.lastUserDisplayLine = displayLine
	m.userDisplayDedupOK = true
	m.hasCommittedLine = true
}

func normalizeUserDisplayLine(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func (m *Model) displayLineWithAttachments(line string) string {
	return m.displayLineWithInputAttachments(line, m.inputAttachments)
}

func (m *Model) displayLineWithInputAttachments(line string, attachments []inputAttachment) string {
	return composeDisplayWithToken(line, attachments, func(name string) string {
		name = strings.TrimSpace(name)
		if name == "" {
			return ""
		}
		return "[image: " + name + "] "
	})
}

func (m *Model) shouldUseTextareaVerticalNavigation(direction int) bool {
	if m.running {
		return false
	}
	if strings.TrimSpace(m.textarea.Value()) == "" {
		return false
	}
	lineInfo := m.textarea.LineInfo()
	if m.textarea.LineCount() <= 1 && lineInfo.Height <= 1 {
		return false
	}
	switch {
	case direction < 0:
		return m.textarea.Line() > 0 || lineInfo.RowOffset > 0
	case direction > 0:
		return m.textarea.Line() < m.textarea.LineCount()-1 || lineInfo.RowOffset+1 < lineInfo.Height
	default:
		return false
	}
}

func (m *Model) userTurnDividerLabel() string {
	if m.hasLastRunDuration {
		return formatTurnDuration(m.lastRunDuration)
	}
	return ""
}

func formatTurnDuration(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	minutes := int(d / time.Minute)
	seconds := int((d % time.Minute) / time.Second)
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}

func centeredDivider(width int, label string) string {
	if width <= 0 {
		return ""
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return strings.Repeat("─", width)
	}
	label = " " + label + " "
	labelWidth := displayColumns(label)
	if labelWidth >= width {
		return label
	}
	remaining := width - labelWidth
	left := remaining / 2
	right := remaining - left
	if left < 2 {
		left = 2
	}
	if right < 2 {
		right = 2
	}
	return strings.Repeat("─", left) + label + strings.Repeat("─", right)
}

func (m *Model) tryOpenSlashArgPicker(line string) bool {
	text := strings.TrimSpace(line)
	if text == "/resume" {
		if !m.isCommandAvailable("resume") {
			return false
		}
		m.openResumePicker()
		return len(m.resumeCandidates) > 0
	}
	if strings.HasPrefix(text, "/") && !strings.Contains(text, " ") {
		cmd := strings.TrimPrefix(text, "/")
		if !m.isCommandAvailable(cmd) {
			return false
		}
		// Check registered wizards first, then well-known simple commands.
		if m.findWizard(cmd) != nil {
			m.openSlashArgPicker(cmd)
			return m.slashArgActive
		}
		switch text {
		case "/agent", "/model", "/sandbox":
			m.openSlashArgPicker(cmd)
			return len(m.slashArgCandidates) > 0
		}
	}
	return false
}

func isViewportEndKey(msg tea.KeyMsg) bool {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return false
	}
	key := tea.Key(press)
	return key.Code == tea.KeyEnd && key.Mod == 0
}
