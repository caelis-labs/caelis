package tuiapp

import (
	"context"
	"path/filepath"
	"sort"
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
	case len(m.skillCandidates) > 0:
		m.refreshSkill()
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
	m.refreshSkill()
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
// # File completion
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
	case "#":
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
		prefix = "#"
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
// $skill completion
// ---------------------------------------------------------------------------

func (m *Model) clearSkill() {
	m.skillQuery = ""
	m.skillCandidates = nil
	m.skillIndex = 0
	m.skillStart = 0
	m.skillEnd = 0
	m.skillLimit = 0
}

func (m *Model) refreshSkill() {
	m.refreshSkillWithLimit(0)
}

func (m *Model) refreshSkillWithLimit(limit int) {
	previousQuery := m.skillQuery
	previousLimit := m.skillLimit
	previousSelected := CompletionCandidate{}
	if m.skillIndex >= 0 && m.skillIndex < len(m.skillCandidates) {
		previousSelected = m.skillCandidates[m.skillIndex]
	}
	m.clearSkill()
	if m.cfg.SkillComplete == nil || m.turnRunning() {
		return
	}
	// Don't show skill popup if mention popup is active.
	if len(m.mentionCandidates) > 0 {
		return
	}
	start, end, query, ok := skillQueryAtCursor(m.input, m.cursor)
	if !ok {
		return
	}
	limit = nextCompletionRefreshLimit(limit, previousLimit, query == previousQuery)
	candidates, err := m.cfg.SkillComplete(query, limit)
	if err != nil || len(candidates) == 0 {
		return
	}
	m.skillQuery = query
	m.skillCandidates = append([]CompletionCandidate(nil), candidates...)
	m.skillStart = start
	m.skillEnd = end
	m.skillLimit = limit
	m.skillIndex = preservedCompletionIndex(previousQuery, query, "", "", previousSelected, candidates)
}

func (m *Model) applySkillCompletion() {
	if len(m.skillCandidates) == 0 {
		m.refreshSkill()
		if len(m.skillCandidates) == 0 {
			return
		}
	}
	choice := "$" + strings.TrimSpace(m.skillCandidates[m.skillIndex].Value)
	replaced, nextCursor := replaceRuneSpan(m.input, m.skillStart, m.skillEnd, choice)
	m.input = replaced
	m.cursor = nextCursor
	if m.cursor == len(m.input) {
		m.input = append(m.input, ' ')
		m.cursor++
	}
	m.clearSkill()
}

func (m *Model) handleSkillKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		m.clearSkill()
		return true, nil
	case key.Matches(msg, m.keys.ChoosePrev):
		if len(m.skillCandidates) > 0 {
			m.skillIndex = wrapSelectionIndex(m.skillIndex, len(m.skillCandidates), -1)
		}
		return true, nil
	case key.Matches(msg, m.keys.ChooseNext):
		if len(m.skillCandidates) > 0 {
			m.advanceSkillSelection()
		}
		return true, nil
	case key.Matches(msg, m.keys.Accept), key.Matches(msg, m.keys.Complete):
		m.applySkillCompletion()
		m.syncTextareaFromInput()
		return true, nil
	default:
		return false, nil
	}
}

func (m *Model) advanceSkillSelection() {
	if len(m.skillCandidates) == 0 {
		return
	}
	if m.skillIndex < len(m.skillCandidates)-1 {
		m.skillIndex++
		return
	}
	oldLen := len(m.skillCandidates)
	if m.loadMoreSkillCandidates() && len(m.skillCandidates) > oldLen {
		m.skillIndex = oldLen
		return
	}
	m.skillIndex = 0
}

func (m *Model) loadMoreSkillCandidates() bool {
	oldLen := len(m.skillCandidates)
	if !shouldLoadMoreCompletionCandidates(oldLen, m.skillLimit) {
		return false
	}
	limit := nextCompletionPageLimit(m.skillLimit, oldLen)
	if limit <= m.skillLimit {
		return false
	}
	previousQuery := m.skillQuery
	previousCandidates := append([]CompletionCandidate(nil), m.skillCandidates...)
	previousIndex := m.skillIndex
	previousStart := m.skillStart
	previousEnd := m.skillEnd
	m.refreshSkillWithLimit(limit)
	if len(m.skillCandidates) == 0 {
		m.skillQuery = previousQuery
		m.skillCandidates = previousCandidates
		m.skillIndex = previousIndex
		m.skillStart = previousStart
		m.skillEnd = previousEnd
		m.skillLimit = limit
	}
	return len(m.skillCandidates) > oldLen
}

// renderSkillList renders the $skill candidates as an overlay list.
func (m *Model) renderSkillList() string {
	if len(m.skillCandidates) == 0 {
		return ""
	}
	contentWidth := maxInt(24, m.completionOverlayInnerWidth())
	maxItems := minInt(completionOverlayVisibleItems, len(m.skillCandidates))
	start, end := completionWindowRange(m.skillIndex, len(m.skillCandidates), maxItems)
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		selected := i == m.skillIndex
		lines = append(lines, m.renderSkillCandidateLine(m.skillCandidates[i], selected, contentWidth))
	}
	return m.renderCompletionOverlay("", lines)
}

func (m *Model) renderSkillCandidateLine(candidate CompletionCandidate, selected bool, width int) string {
	display := completionCandidateDisplay(candidate)
	kind := completionCandidateKind(candidate)
	detail := completionCandidateDetail(candidate)
	return m.renderCompletionCandidateRow(display, kind, detail, selected, width)
}

func (m *Model) renderCompletionCandidateRow(display string, kind string, detail string, selected bool, width int) string {
	display = strings.Join(strings.Fields(strings.TrimSpace(display)), " ")
	kind = strings.Join(strings.Fields(strings.TrimSpace(kind)), " ")
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	nameStyle := m.theme.CommandStyle()
	if selected {
		nameStyle = m.theme.CommandActiveStyle()
	}

	if kind == "" && detail == "" {
		return nameStyle.Render(truncateTailDisplay(display, maxInt(1, width-2)))
	}

	nameColumn := minInt(20, maxInt(10, width/5))
	if width < 72 {
		nameColumn = minInt(16, maxInt(8, width/4))
	}

	name := truncateTailDisplay(display, nameColumn)
	renderedName := nameStyle.Render(name)
	targetWidth := nameColumn + 2

	line := renderedName
	used := displayColumns(line)

	if used < targetWidth {
		line += strings.Repeat(" ", targetWidth-used)
		used = targetWidth
	}
	line += " "
	used += 1

	if kind != "" {
		badge := "[" + kind + "]"
		line += m.theme.HelpHintTextStyle().Render(badge)
		used += displayColumns(badge)
		if detail != "" {
			line += "  "
			used += 2
		}
	}
	if detail != "" {
		detailBudget := maxInt(0, width-used)
		if detailBudget > 0 {
			line += m.theme.HelpHintTextStyle().Render(truncateTailDisplay(detail, detailBudget))
		}
	}
	return line
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
	m.clearSkill()
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
	m.clearSkill()
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
	if len(m.mentionCandidates) > 0 || len(m.skillCandidates) > 0 || len(m.slashArgCandidates) > 0 {
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

// ---------------------------------------------------------------------------
// Slash command completion
// ---------------------------------------------------------------------------

func (m *Model) refreshSlashCommands() {
	selected := ""
	if m.slashIndex >= 0 && m.slashIndex < len(m.slashCandidates) {
		selected = strings.TrimSpace(m.slashCandidates[m.slashIndex])
	}
	m.clearSlashCompletion()
	if m.turnRunning() || m.slashArgActive || m.isWizardActive() {
		return
	}
	// Avoid overlapping popups.
	if len(m.mentionCandidates) > 0 || len(m.skillCandidates) > 0 || len(m.resumeCandidates) > 0 || len(m.slashArgCandidates) > 0 {
		return
	}
	query, ok := slashCommandQueryAtCursor(m.input, m.cursor)
	if !ok {
		return
	}
	candidates := make([]string, 0, len(m.cfg.Commands))
	for _, cmd := range m.cfg.Commands {
		full := "/" + strings.TrimSpace(cmd)
		if full == "/" {
			continue
		}
		if query == "" || strings.HasPrefix(strings.ToLower(full), "/"+strings.ToLower(query)) {
			candidates = append(candidates, full)
		}
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return
	}
	m.slashCandidates = candidates
	m.slashIndex = 0
	if selected != "" {
		for i, candidate := range candidates {
			if candidate == selected {
				m.slashIndex = i
				break
			}
		}
	}
	m.slashPrefix = "/" + query
}

func (m *Model) applySlashCommandCompletion() {
	if len(m.slashCandidates) == 0 {
		m.refreshSlashCommands()
		if len(m.slashCandidates) == 0 {
			return
		}
	}
	selected := strings.TrimSpace(m.slashCandidates[m.slashIndex])
	if selected == "" {
		return
	}
	line := selected + " "
	m.setInputText(line)
	m.clearSlashCompletion()
	m.tryOpenSlashArgPicker(line)
}

func (m *Model) handleSlashCommandKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		if _, ok := slashCommandQueryAtCursor(m.input, m.cursor); ok {
			m.setInputText("")
			m.syncTextareaFromInput()
		}
		m.clearSlashCompletion()
		return true, nil
	case key.Matches(msg, m.keys.ChoosePrev):
		if len(m.slashCandidates) > 0 {
			m.slashIndex = wrapSelectionIndex(m.slashIndex, len(m.slashCandidates), -1)
		}
		return true, nil
	case key.Matches(msg, m.keys.ChooseNext):
		if len(m.slashCandidates) > 0 {
			m.slashIndex = wrapSelectionIndex(m.slashIndex, len(m.slashCandidates), 1)
		}
		return true, nil
	case key.Matches(msg, m.keys.Complete):
		m.applySlashCommandCompletion()
		m.syncTextareaFromInput()
		if m.resumeActive {
			return true, m.requestCompletionRefresh()
		}
		return true, nil
	case key.Matches(msg, m.keys.Accept):
		if m.turnRunning() || len(m.slashCandidates) == 0 {
			return true, nil
		}
		m.applySlashCommandCompletion()
		m.syncTextareaFromInput()
		if m.resumeActive {
			return true, m.requestCompletionRefresh()
		}
		return true, nil
	default:
		return false, nil
	}
}

func (m *Model) renderSlashCommandList() string {
	if len(m.slashCandidates) == 0 {
		return ""
	}
	maxItems := minInt(8, len(m.slashCandidates))
	start := 0
	if m.slashIndex >= maxItems {
		start = m.slashIndex - maxItems + 1
	}
	maxStart := maxInt(0, len(m.slashCandidates)-maxItems)
	if start > maxStart {
		start = maxStart
	}
	end := minInt(len(m.slashCandidates), start+maxItems)
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		display := m.slashCandidates[i]
		lines = append(lines, m.renderCompletionTextLine(display, m.commandCompletionDetail(display), i == m.slashIndex))
	}
	return m.renderCompletionOverlay("Commands", lines)
}

func (m *Model) clearSlashCompletion() {
	m.slashCandidates = nil
	m.slashIndex = 0
	m.slashPrefix = ""
}

func (m *Model) clearInputOverlays() {
	m.clearMention()
	m.clearSkill()
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
		return []string{value, display, detail}
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
