package tuiapp

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// ---------------------------------------------------------------------------
// Command palette
// ---------------------------------------------------------------------------

func (m *Model) togglePalette() {
	m.showPalette = !m.showPalette
	m.paletteAnimating = !m.noAnimation
	if m.showPalette {
		m.palette.ResetSelected()
		if m.paletteAnimLines < 0 {
			m.paletteAnimLines = 0
		}
	}
	if m.noAnimation {
		m.paletteAnimLines = m.paletteAnimationTarget()
	}
}

func (m *Model) paletteAnimationCmd() tea.Cmd {
	if m == nil || m.noAnimation {
		return nil
	}
	return animatePaletteCmd()
}

func (m *Model) handlePaletteKey(msg tea.KeyMsg) tea.Cmd {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.showPalette = false
		m.paletteAnimating = !m.noAnimation
		if m.noAnimation {
			m.paletteAnimLines = 0
			return nil
		}
		return m.paletteAnimationCmd()
	case key.Matches(msg, m.keys.Accept):
		item, ok := m.palette.SelectedItem().(commandItem)
		if ok {
			m.textarea.SetValue("/" + item.name)
			m.textarea.CursorEnd()
			m.adjustTextareaHeight()
			m.syncInputFromTextarea()
			m.refreshSlashCommands()
		}
		m.showPalette = false
		m.paletteAnimating = !m.noAnimation
		if m.noAnimation {
			m.paletteAnimLines = 0
			return nil
		}
		return m.paletteAnimationCmd()
	}
	var cmd tea.Cmd
	m.palette, cmd = m.palette.Update(msg)
	return cmd
}

func (m *Model) requestCompletionRefresh() tea.Cmd {
	if m == nil || m.turnRunning() {
		return nil
	}
	m.completionRefreshSeq++
	seq := m.completionRefreshSeq
	return tea.Tick(completionRefreshDebounce, func(time.Time) tea.Msg {
		return completionRefreshMsg{seq: seq}
	})
}

func (m *Model) handleCompletionRefreshMsg(msg completionRefreshMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.seq != m.completionRefreshSeq {
		return m, nil
	}
	return m, m.refreshCompletionOverlaysNow()
}

func (m *Model) refreshCompletionOverlaysBeforeAccept(msg tea.KeyMsg) {
	if m == nil || m.turnRunning() || (!key.Matches(msg, m.keys.Accept) && !key.Matches(msg, m.keys.Complete)) {
		return
	}
	switch {
	case len(m.mentionCandidates) > 0:
		m.refreshMention()
	case m.resumeActive || len(m.resumeCandidates) > 0:
		// Resume completion is asynchronous. Accept/complete never waits for
		// Control or Store I/O on the Bubble Tea event loop.
	case m.slashArgActive:
		m.updateSlashArgCandidates()
	case len(m.slashCandidates) > 0:
		m.refreshSlashCommands()
	}
}

func (m *Model) refreshCompletionOverlaysNow() tea.Cmd {
	m.refreshMention()
	var resumeCmd tea.Cmd
	if m.isWizardActive() {
		if m.resumeActive {
			resumeCmd = m.updateResumeCandidates()
		}
		if m.slashArgActive {
			m.updateSlashArgCandidates()
		}
	} else {
		m.syncSlashInputOverlays()
		if m.resumeActive {
			resumeCmd = m.updateResumeCandidates()
		}
	}
	m.refreshSlashCommands()
	return resumeCmd
}

// ---------------------------------------------------------------------------
// @ File completion
// ---------------------------------------------------------------------------

const (
	completionCandidateFetchLimit = 50
	completionCandidateMaxLimit   = 1000
	completionOverlayVisibleItems = 8
)

func (m *Model) clearMention() {
	m.mentionQuery = ""
	m.mentionPrefix = ""
	m.mentionCandidates = nil
	m.mentionIndex = 0
	m.mentionStart = 0
	m.mentionEnd = 0
	m.mentionLimit = 0
}

func (m *Model) refreshMention() {
	m.refreshMentionWithLimit(0)
}

func (m *Model) refreshMentionWithLimit(limit int) {
	previousQuery := m.mentionQuery
	previousPrefix := m.mentionPrefix
	previousLimit := m.mentionLimit
	previousSelected := CompletionCandidate{}
	if m.mentionIndex >= 0 && m.mentionIndex < len(m.mentionCandidates) {
		previousSelected = m.mentionCandidates[m.mentionIndex]
	}
	m.clearMention()
	if m.turnRunning() {
		return
	}
	start, end, query, prefix, ok := mentionQueryAtCursorWithPrefix(m.input, m.cursor)
	if !ok {
		return
	}
	limit = nextCompletionRefreshLimit(limit, previousLimit, query == previousQuery && prefix == previousPrefix)
	begin := time.Now()
	var (
		candidates []CompletionCandidate
		err        error
	)
	switch prefix {
	case "@":
		if m.cfg.FileComplete == nil {
			return
		}
		candidates, err = m.cfg.FileComplete(query, limit)
	default:
		return
	}
	latency := time.Since(begin)
	m.diag.LastMentionLatency = latency
	if err != nil || len(candidates) == 0 {
		return
	}
	m.mentionQuery = query
	m.mentionPrefix = prefix
	m.mentionCandidates = append([]CompletionCandidate(nil), candidates...)
	m.mentionStart = start
	m.mentionEnd = end
	m.mentionLimit = limit
	m.mentionIndex = preservedCompletionIndex(previousQuery, query, previousPrefix, prefix, previousSelected, candidates)
}

func (m *Model) applyMentionCompletion() {
	if len(m.mentionCandidates) == 0 {
		m.refreshMention()
		if len(m.mentionCandidates) == 0 {
			return
		}
	}
	prefix := m.mentionPrefix
	if prefix == "" {
		prefix = "@"
	}
	choice := prefix + strings.TrimSpace(m.mentionCandidates[m.mentionIndex].Value)
	replaced, nextCursor := replaceRuneSpan(m.input, m.mentionStart, m.mentionEnd, choice)
	m.input = replaced
	m.cursor = nextCursor
	if m.cursor == len(m.input) {
		m.input = append(m.input, ' ')
		m.cursor++
	}
	m.clearMention()
}

func (m *Model) handleMentionKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.clearMention()
		return true, nil
	case key.Matches(msg, m.keys.ChoosePrev):
		if len(m.mentionCandidates) > 0 {
			m.mentionIndex = wrapSelectionIndex(m.mentionIndex, len(m.mentionCandidates), -1)
		}
		return true, nil
	case key.Matches(msg, m.keys.ChooseNext):
		if len(m.mentionCandidates) > 0 {
			m.advanceMentionSelection()
		}
		return true, nil
	case key.Matches(msg, m.keys.Accept), key.Matches(msg, m.keys.Complete):
		m.applyMentionCompletion()
		m.syncTextareaFromInput()
		return true, nil
	default:
		return false, nil
	}
}

func (m *Model) advanceMentionSelection() {
	if len(m.mentionCandidates) == 0 {
		return
	}
	if m.mentionIndex < len(m.mentionCandidates)-1 {
		m.mentionIndex++
		return
	}
	oldLen := len(m.mentionCandidates)
	if m.loadMoreMentionCandidates() && len(m.mentionCandidates) > oldLen {
		m.mentionIndex = oldLen
		return
	}
	m.mentionIndex = 0
}

func (m *Model) loadMoreMentionCandidates() bool {
	oldLen := len(m.mentionCandidates)
	if !shouldLoadMoreCompletionCandidates(oldLen, m.mentionLimit) {
		return false
	}
	limit := nextCompletionPageLimit(m.mentionLimit, oldLen)
	if limit <= m.mentionLimit {
		return false
	}
	previousQuery := m.mentionQuery
	previousPrefix := m.mentionPrefix
	previousCandidates := append([]CompletionCandidate(nil), m.mentionCandidates...)
	previousIndex := m.mentionIndex
	previousStart := m.mentionStart
	previousEnd := m.mentionEnd
	m.refreshMentionWithLimit(limit)
	if len(m.mentionCandidates) == 0 {
		m.mentionQuery = previousQuery
		m.mentionPrefix = previousPrefix
		m.mentionCandidates = previousCandidates
		m.mentionIndex = previousIndex
		m.mentionStart = previousStart
		m.mentionEnd = previousEnd
		m.mentionLimit = limit
	}
	return len(m.mentionCandidates) > oldLen
}

// ---------------------------------------------------------------------------
// /resume completion
// ---------------------------------------------------------------------------

func (m *Model) clearResume() {
	m.cancelResumeRequest()
	m.resumeActive = false
	m.resumeQuery = ""
	m.resumeLoaded = false
	m.resumeCandidates = nil
	m.resumeIndex = 0
}

func (m *Model) openResumePicker() {
	m.clearMention()
	m.clearSlashArg()
	m.clearSlashCompletion()
	m.resumeActive = true
	m.setInputText("/resume ")
	m.syncTextareaFromInput()
}

func (m *Model) activateResumePickerFromInput() {
	if m.resumeActive {
		return
	}
	m.clearMention()
	m.clearSlashArg()
	m.clearSlashCompletion()
	m.resumeActive = true
}

func (m *Model) cancelResumeRequest() {
	if m.resumeRequestCancel != nil {
		m.resumeRequestCancel()
	}
	m.resumeRequestCancel = nil
	m.resumeRequestPending = false
	m.resumeRequestQuery = ""
	m.resumeRequestSeq++
}

func (m *Model) updateResumeCandidates() tea.Cmd {
	if !m.resumeActive || m.cfg.ResumeComplete == nil || m.turnRunning() {
		m.cancelResumeRequest()
		m.resumeCandidates = nil
		m.resumeQuery = ""
		m.resumeLoaded = false
		m.resumeIndex = 0
		return nil
	}
	// Avoid overlapping popups.
	if len(m.mentionCandidates) > 0 || len(m.slashArgCandidates) > 0 {
		m.cancelResumeRequest()
		m.resumeCandidates = nil
		m.resumeLoaded = false
		return nil
	}
	query, ok := resumeQueryAtEnd([]rune(m.textarea.Value()))
	if !ok {
		m.cancelResumeRequest()
		m.resumeCandidates = nil
		m.resumeQuery = ""
		m.resumeLoaded = false
		m.resumeIndex = 0
		return nil
	}
	if m.resumeRequestPending && query == m.resumeRequestQuery {
		return nil
	}
	if !m.resumeRequestPending && m.resumeLoaded && query == m.resumeQuery {
		return nil
	}
	if m.resumeRequestCancel != nil {
		m.resumeRequestCancel()
	}
	requestCtx := m.cfg.Context
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	requestCtx, cancel := context.WithCancel(requestCtx)
	m.resumeRequestSeq++
	seq := m.resumeRequestSeq
	m.resumeRequestQuery = query
	m.resumeRequestPending = true
	m.resumeRequestCancel = cancel
	m.resumeCandidates = nil
	m.resumeLoaded = false
	complete := m.cfg.ResumeComplete
	return func() tea.Msg {
		started := time.Now()
		candidates, err := complete(requestCtx, query, 200)
		return resumeCompletionResultMsg{
			seq: seq, query: query, candidates: candidates, err: err, latency: time.Since(started),
		}
	}
}

func (m *Model) handleResumeCompletionResultMsg(msg resumeCompletionResultMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.seq != m.resumeRequestSeq || msg.query != m.resumeRequestQuery {
		return m, nil
	}
	m.resumeRequestPending = false
	cancel := m.resumeRequestCancel
	m.resumeRequestCancel = nil
	if cancel != nil {
		cancel()
	}
	m.diag.LastResumeLatency = msg.latency
	query, ok := resumeQueryAtEnd([]rune(m.textarea.Value()))
	if !m.resumeActive || !ok || query != msg.query || m.turnRunning() {
		return m, nil
	}
	if msg.err != nil || len(msg.candidates) == 0 {
		m.resumeCandidates = nil
		m.resumeQuery = query
		m.resumeLoaded = msg.err == nil
		m.resumeIndex = 0
		return m, nil
	}
	filtered := filterResumeCandidates(query, msg.candidates)
	if len(filtered) == 0 {
		m.resumeCandidates = nil
		m.resumeQuery = query
		m.resumeLoaded = true
		m.resumeIndex = 0
		return m, nil
	}
	m.resumeIndex = normalizeFilteredSelection(m.resumeIndex, query, m.resumeQuery, len(filtered))
	m.resumeQuery = query
	m.resumeLoaded = true
	m.resumeCandidates = filtered
	return m, nil
}

func (m *Model) applyResumeCompletion() {
	if len(m.resumeCandidates) == 0 {
		return
	}
	choice := strings.TrimSpace(m.resumeCandidates[m.resumeIndex].SessionID)
	if choice == "" {
		return
	}
	m.setInputText("/resume " + choice + " ")
	m.clearResume()
}

func (m *Model) handleResumeKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		if _, ok := resumeQueryAtCursor(m.input, m.cursor); ok {
			m.setInputText("")
			m.syncTextareaFromInput()
		}
		m.clearResume()
		return true, nil
	case key.Matches(msg, m.keys.ChoosePrev):
		if len(m.resumeCandidates) > 0 {
			m.resumeIndex = wrapSelectionIndex(m.resumeIndex, len(m.resumeCandidates), -1)
		}
		return true, nil
	case key.Matches(msg, m.keys.ChooseNext):
		if len(m.resumeCandidates) > 0 {
			m.resumeIndex = wrapSelectionIndex(m.resumeIndex, len(m.resumeCandidates), 1)
		}
		return true, nil
	case key.Matches(msg, m.keys.Complete):
		if len(m.resumeCandidates) == 0 {
			m.resumeLoaded = false
			return true, m.updateResumeCandidates()
		}
		m.applyResumeCompletion()
		m.syncTextareaFromInput()
		return true, nil
	case key.Matches(msg, m.keys.Accept):
		if m.turnRunning() {
			return true, nil
		}
		if len(m.resumeCandidates) == 0 {
			m.resumeLoaded = false
			return true, m.updateResumeCandidates()
		}
		selected := strings.TrimSpace(m.resumeCandidates[m.resumeIndex].SessionID)
		if selected == "" {
			return true, nil
		}
		_, cmd := m.submitLine("/resume " + selected)
		return true, cmd
	default:
		return false, nil
	}
}

func (m *Model) clearInputOverlays() {
	m.clearMention()
	m.clearResume()
	m.clearSlashArg()
	m.clearSlashCompletion()
	if m.showPalette {
		m.showPalette = false
	}
}

func filterSlashArgCandidates(query string, candidates []SlashArgCandidate) []SlashArgCandidate {
	return filterByPrefix(query, candidates, func(one SlashArgCandidate) []string {
		value := strings.TrimSpace(one.Value)
		display := strings.TrimSpace(one.Display)
		if display == "" {
			display = value
		}
		detail := strings.TrimSpace(one.Detail)
		values := []string{value, display, detail}
		if _, local, ok := strings.Cut(value, ":"); ok {
			values = append(values, local)
		}
		return values
	})
}

func filterResumeCandidates(query string, candidates []ResumeCandidate) []ResumeCandidate {
	return filterByPrefix(query, candidates, func(one ResumeCandidate) []string {
		return []string{
			strings.TrimSpace(one.SessionID),
			strings.TrimSpace(one.Title),
			strings.TrimSpace(one.Prompt),
			strings.TrimSpace(one.Model),
			strings.TrimSpace(one.Workspace),
			strings.TrimSpace(one.Age),
		}
	})
}

func preservedCompletionIndex(previousQuery string, query string, previousPrefix string, prefix string, previousSelected CompletionCandidate, candidates []CompletionCandidate) int {
	if len(candidates) == 0 {
		return 0
	}
	if previousQuery != query || previousPrefix != prefix {
		return 0
	}
	selectedKey := completionCandidateStableKey(previousSelected)
	if selectedKey == "" {
		return 0
	}
	for i, candidate := range candidates {
		if completionCandidateStableKey(candidate) == selectedKey {
			return i
		}
	}
	return 0
}

func nextCompletionRefreshLimit(requested int, previous int, sameQuery bool) int {
	if requested <= 0 {
		requested = completionCandidateFetchLimit
		if sameQuery && previous > requested {
			requested = previous
		}
	}
	if requested > completionCandidateMaxLimit {
		return completionCandidateMaxLimit
	}
	return requested
}

func shouldLoadMoreCompletionCandidates(loaded int, limit int) bool {
	return loaded > 0 && limit > 0 && loaded >= limit && limit < completionCandidateMaxLimit
}

func nextCompletionPageLimit(limit int, loaded int) int {
	next := maxInt(limit, loaded) + completionCandidateFetchLimit
	if next > completionCandidateMaxLimit {
		return completionCandidateMaxLimit
	}
	return next
}

func completionWindowRange(index int, total int, visible int) (int, int) {
	if total <= 0 || visible <= 0 {
		return 0, 0
	}
	if visible > total {
		visible = total
	}
	if index < 0 {
		index = 0
	}
	if index >= total {
		index = total - 1
	}
	start := 0
	if index >= visible {
		start = index - visible + 1
	}
	maxStart := maxInt(0, total-visible)
	if start > maxStart {
		start = maxStart
	}
	return start, minInt(total, start+visible)
}

func completionCandidateStableKey(candidate CompletionCandidate) string {
	parts := []string{
		strings.TrimSpace(candidate.Value),
		strings.TrimSpace(candidate.Display),
		strings.TrimSpace(candidate.Path),
	}
	if parts[0] == "" && parts[1] == "" && parts[2] == "" {
		return ""
	}
	for i, part := range parts {
		parts[i] = strings.ToLower(part)
	}
	return strings.Join(parts, "\x00")
}

func shortWorkspaceLabel(workspace string) string {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return ""
	}
	return filepath.Base(workspace)
}

func shortSessionLabel(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return "id:" + sessionID
}

func (m *Model) setInputText(text string) {
	m.input = []rune(text)
	m.cursor = len(m.input)
	m.clearInputAttachments()
	if m.cfg.ClearAttachments != nil {
		m.cfg.ClearAttachments()
	}
}

func (m *Model) recordHistoryEntry(value string, attachments []inputAttachment) {
	entry := strings.TrimSpace(value)
	clonedAttachments := cloneInputAttachments(attachments)
	if entry == "" && len(clonedAttachments) == 0 {
		return
	}
	// Slash commands are control inputs and should not pollute user message history.
	// Expand collapsed pastes so "/help"-only pastes still filter correctly.
	if m.isConfiguredSlashControlLine(strings.TrimSpace(expandComposerText(entry, clonedAttachments))) {
		return
	}
	if len(m.history) == 0 || m.history[len(m.history)-1] != entry || !inputAttachmentsEqual(m.historyAttachments[len(m.historyAttachments)-1], clonedAttachments) {
		m.history = append(m.history, entry)
		m.historyAttachments = append(m.historyAttachments, clonedAttachments)
	}
}

func (m *Model) isConfiguredSlashControlLine(line string) bool {
	name := slashCommandName(line)
	if name == "" {
		return false
	}
	if !m.isCommandAvailable(name) {
		return false
	}
	_, ok := controlprompt.Lookup(name)
	return ok
}

func (m *Model) isCommandAvailable(name string) bool {
	name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
	if name == "" {
		return false
	}
	if len(m.cfg.Commands) == 0 {
		return true
	}
	for _, command := range m.cfg.Commands {
		if strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(command, "/")), name) {
			return true
		}
	}
	return false
}

func slashCommandName(line string) string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "/") {
		return ""
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return ""
	}
	name := strings.TrimPrefix(fields[0], "/")
	return strings.ToLower(strings.TrimSpace(name))
}

func inputAttachmentsEqual(left []inputAttachment, right []inputAttachment) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
