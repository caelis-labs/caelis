package tuiapp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
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
	if m == nil || m.running {
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
	m.refreshCompletionOverlaysNow()
	return m, nil
}

func (m *Model) refreshCompletionOverlaysBeforeAccept(msg tea.KeyMsg) {
	if m == nil || m.running || (!key.Matches(msg, m.keys.Accept) && !key.Matches(msg, m.keys.Complete)) {
		return
	}
	switch {
	case len(m.mentionCandidates) > 0:
		m.refreshMention()
	case len(m.skillCandidates) > 0:
		m.refreshSkill()
	case m.resumeActive || len(m.resumeCandidates) > 0:
		m.updateResumeCandidates()
	case m.slashArgActive:
		m.updateSlashArgCandidates()
	case len(m.slashCandidates) > 0:
		m.refreshSlashCommands()
	}
}

func (m *Model) refreshCompletionOverlaysNow() {
	m.refreshMention()
	m.refreshSkill()
	if m.isWizardActive() {
		if m.resumeActive {
			m.updateResumeCandidates()
		}
		if m.slashArgActive {
			m.updateSlashArgCandidates()
		}
	} else {
		m.syncSlashInputOverlays()
	}
	m.refreshSlashCommands()
}

// ---------------------------------------------------------------------------
// @Mention completion
// ---------------------------------------------------------------------------

func (m *Model) clearMention() {
	m.mentionQuery = ""
	m.mentionPrefix = ""
	m.mentionCandidates = nil
	m.mentionIndex = 0
	m.mentionStart = 0
	m.mentionEnd = 0
}

func (m *Model) refreshMention() {
	m.clearMention()
	if m.cfg.MentionComplete == nil || m.running {
		return
	}
	start, end, query, prefix, ok := mentionQueryAtCursorWithPrefix(m.input, m.cursor)
	if !ok {
		return
	}
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
		candidates, err = m.cfg.FileComplete(query, 8)
	default:
		candidates, err = m.cfg.MentionComplete(query, 8)
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
	m.mentionIndex = 0
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
			m.mentionIndex = wrapSelectionIndex(m.mentionIndex, len(m.mentionCandidates), 1)
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

// ---------------------------------------------------------------------------
// $skill completion
// ---------------------------------------------------------------------------

func (m *Model) clearSkill() {
	m.skillQuery = ""
	m.skillCandidates = nil
	m.skillIndex = 0
	m.skillStart = 0
	m.skillEnd = 0
}

func (m *Model) refreshSkill() {
	m.clearSkill()
	if m.cfg.SkillComplete == nil || m.running {
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
	candidates, err := m.cfg.SkillComplete(query, 8)
	if err != nil || len(candidates) == 0 {
		return
	}
	m.skillQuery = query
	m.skillCandidates = append([]CompletionCandidate(nil), candidates...)
	m.skillStart = start
	m.skillEnd = end
	m.skillIndex = 0
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
			m.skillIndex = wrapSelectionIndex(m.skillIndex, len(m.skillCandidates), 1)
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

// renderSkillList renders the $skill candidates as an overlay list.
func (m *Model) renderSkillList() string {
	if len(m.skillCandidates) == 0 {
		return ""
	}
	maxItems := minInt(8, len(m.skillCandidates))
	var lines []string
	for i := 0; i < maxItems; i++ {
		prefix := "  "
		display := "$" + completionCandidateDisplay(m.skillCandidates[i])
		detail := completionCandidateDetail(m.skillCandidates[i])
		if i == m.skillIndex {
			prefix = m.theme.PromptStyle().Render("▸ ")
			line := prefix + m.theme.CommandActiveStyle().Render(display)
			if detail != "" {
				line += "  " + m.theme.HelpHintTextStyle().Render(detail)
			}
			lines = append(lines, line)
		} else {
			line := prefix + m.theme.HelpHintTextStyle().Render(display)
			if detail != "" {
				line += "  " + m.theme.HelpHintTextStyle().Render(detail)
			}
			lines = append(lines, line)
		}
	}
	if len(m.skillCandidates) > maxItems {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d more", len(m.skillCandidates)-maxItems),
		))
	}
	return m.renderCompletionOverlay("Skills", lines)
}

// ---------------------------------------------------------------------------
// /resume completion
// ---------------------------------------------------------------------------

func (m *Model) clearResume() {
	m.resumeActive = false
	m.resumeQuery = ""
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
	m.updateResumeCandidates()
}

func (m *Model) activateResumePickerFromInput() {
	if m.resumeActive {
		m.updateResumeCandidates()
		return
	}
	m.clearMention()
	m.clearSkill()
	m.clearSlashArg()
	m.clearSlashCompletion()
	m.resumeActive = true
	m.updateResumeCandidates()
}

func (m *Model) updateResumeCandidates() {
	if !m.resumeActive || m.cfg.ResumeComplete == nil || m.running {
		m.resumeCandidates = nil
		m.resumeQuery = ""
		m.resumeIndex = 0
		return
	}
	// Avoid overlapping popups.
	if len(m.mentionCandidates) > 0 || len(m.skillCandidates) > 0 || len(m.slashArgCandidates) > 0 {
		m.resumeCandidates = nil
		return
	}
	query, ok := resumeQueryAtEnd([]rune(m.textarea.Value()))
	if !ok {
		m.resumeCandidates = nil
		m.resumeQuery = ""
		m.resumeIndex = 0
		return
	}
	candidates, err := m.cfg.ResumeComplete(query, 200)
	if err != nil || len(candidates) == 0 {
		m.resumeCandidates = nil
		m.resumeQuery = query
		m.resumeIndex = 0
		return
	}
	filtered := filterResumeCandidates(query, candidates)
	if len(filtered) == 0 {
		m.resumeCandidates = nil
		m.resumeQuery = query
		m.resumeIndex = 0
		return
	}
	m.resumeIndex = normalizeFilteredSelection(m.resumeIndex, query, m.resumeQuery, len(filtered))
	m.resumeQuery = query
	m.resumeCandidates = filtered
}

func (m *Model) applyResumeCompletion() {
	if len(m.resumeCandidates) == 0 {
		m.updateResumeCandidates()
		if len(m.resumeCandidates) == 0 {
			return
		}
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
		m.applyResumeCompletion()
		m.syncTextareaFromInput()
		return true, nil
	case key.Matches(msg, m.keys.Accept):
		if m.running || len(m.resumeCandidates) == 0 {
			return true, nil
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

func (m *Model) renderResumeList() string {
	if len(m.resumeCandidates) == 0 {
		return ""
	}
	maxItems := minInt(8, len(m.resumeCandidates))
	start := 0
	if m.resumeIndex >= maxItems {
		start = m.resumeIndex - maxItems + 1
	}
	maxStart := maxInt(0, len(m.resumeCandidates)-maxItems)
	if start > maxStart {
		start = maxStart
	}
	end := minInt(len(m.resumeCandidates), start+maxItems)
	var lines []string
	if start > 0 {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d earlier", start),
		))
	}
	for i := start; i < end; i++ {
		item := m.resumeCandidates[i]
		title := firstNonEmpty(strings.TrimSpace(item.Title), strings.TrimSpace(item.Prompt), strings.TrimSpace(item.SessionID))
		age := strings.TrimSpace(item.Age)
		if age == "" {
			age = "-"
		}
		meta := compactNonEmpty([]string{
			age,
			strings.TrimSpace(item.Model),
			shortWorkspaceLabel(item.Workspace),
			shortSessionLabel(item.SessionID),
		})
		display := title
		if len(meta) > 0 {
			display += "  " + strings.Join(meta, " · ")
		}
		prefix := "  "
		if i == m.resumeIndex {
			prefix = m.theme.PromptStyle().Render("▸ ")
			lines = append(lines, prefix+m.theme.CommandActiveStyle().Render(display))
		} else {
			lines = append(lines, prefix+m.theme.HelpHintTextStyle().Render(display))
		}
	}
	if end < len(m.resumeCandidates) {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d more", len(m.resumeCandidates)-end),
		))
	}
	return m.renderCompletionOverlay("Recent", lines)
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
	if m.running {
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
		return true, nil
	case key.Matches(msg, m.keys.Accept):
		if m.running || len(m.slashCandidates) == 0 {
			return true, nil
		}
		m.applySlashCommandCompletion()
		m.syncTextareaFromInput()
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
	var lines []string
	if start > 0 {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d earlier", start),
		))
	}
	for i := start; i < end; i++ {
		prefix := "  "
		if i == m.slashIndex {
			prefix = m.theme.PromptStyle().Render("▸ ")
			lines = append(lines, prefix+m.theme.CommandActiveStyle().Render(m.slashCandidates[i]))
		} else {
			lines = append(lines, prefix+m.theme.HelpHintTextStyle().Render(m.slashCandidates[i]))
		}
	}
	if end < len(m.slashCandidates) {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d more", len(m.slashCandidates)-end),
		))
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

func completionCandidateDisplay(candidate CompletionCandidate) string {
	display := strings.TrimSpace(candidate.Display)
	if display != "" {
		return display
	}
	return strings.TrimSpace(candidate.Value)
}

func completionCandidateDetail(candidate CompletionCandidate) string {
	return strings.TrimSpace(candidate.Detail)
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
	if entry == "" {
		return
	}
	// Slash commands are control inputs and should not pollute user message history.
	if m.isConfiguredSlashControlLine(entry) {
		return
	}
	clonedAttachments := cloneInputAttachments(attachments)
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
	_, ok := lookupSlashCommandSpec(name)
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
