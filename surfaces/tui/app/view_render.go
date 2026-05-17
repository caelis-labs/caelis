package tuiapp

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// View sub-components
// ---------------------------------------------------------------------------

func (m *Model) windowTitle() string {
	title := "CAELIS"
	if alias := strings.TrimSpace(m.statusModel); alias != "" {
		title += " • " + alias
	}
	if m.running {
		title += " • running"
	}
	return title
}

func (m *Model) buildHintText() string {
	// Show hint message if set.
	if h := strings.TrimSpace(m.hint); h != "" {
		return h
	}
	if m.activePrompt != nil {
		return m.promptHintText()
	}
	if h := strings.TrimSpace(m.approvalReviewHint); h != "" {
		return m.renderApprovalReviewHintText(h)
	}
	if m.running && m.activePrompt == nil {
		return m.buildRunningHintText()
	}
	if text := m.pendingQueueHintText(); text != "" {
		return text
	}
	// Show /resume guidance.
	if len(m.resumeCandidates) > 0 {
		return m.overlayHintText("/resume")
	}
	// Show generic slash-arg guidance.
	if m.slashArgActive && m.slashArgCommand != "" {
		// Wizard-driven hint.
		if m.isWizardActive() {
			return m.wizardHintText()
		}
		// Non-wizard fallback.
		label := "/" + m.slashArgCommand
		if len(m.slashArgCandidates) == 0 {
			return ""
		}
		return m.overlayHintText(label)
	}
	// Show slash command guidance.
	if len(m.slashCandidates) > 0 {
		return m.overlayHintText("/")
	}
	return ""
}

func (m *Model) renderApprovalReviewHintText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	frame := strings.TrimSpace(ansi.Strip(m.spinner.View()))
	if frame == "" {
		frame = "⠋"
	}
	return m.theme.SpinnerStyle().Render(frame) + " " + m.theme.WarnStyle().Render(text)
}

func (m *Model) primaryDrawerHeight() int {
	drawer := m.renderPrimaryDrawer()
	if drawer == "" {
		return 0
	}
	return strings.Count(drawer, "\n") + 1
}

func (m *Model) renderPrimaryDrawer() string {
	if drawer := m.renderBTWDrawer(); drawer != "" {
		return drawer
	}
	return m.renderPlanDrawer()
}

func (m *Model) renderPlanDrawer() string {
	if len(m.planEntries) == 0 || m.width <= 0 {
		return ""
	}
	visible, _, _ := visiblePlanEntries(m.planEntries, m.planVisibleBudget())
	if len(visible) == 0 {
		return ""
	}
	contentWidth := maxInt(1, m.mainColumnWidth()-(inputHorizontalInset*2))
	lines := []string{m.theme.SeparatorStyle().Render(strings.Repeat("─", contentWidth))}
	for _, item := range visible {
		lines = append(lines, renderPlanLine(m, item))
	}
	return insetRenderedBlock(strings.Join(lines, "\n"), inputHorizontalInset)
}

func (m *Model) btwVisibleBudget() int {
	switch {
	case m.height <= 18:
		return 4
	case m.height <= 24:
		return 6
	case m.height <= 32:
		return 8
	default:
		return 10
	}
}

func (m *Model) btwContentWidth() int {
	return maxInt(1, m.mainColumnWidth()-(inputHorizontalInset*2))
}

const pendingSubmissionIcon = "↪"

func (m *Model) renderPendingSubmissionLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return m.theme.HelpHintTextStyle().Render(pendingSubmissionIcon + " " + text)
}

func (m *Model) btwContentLines() []string {
	if m == nil || m.btwOverlay == nil || m.width <= 0 {
		return nil
	}
	contentWidth := m.btwContentWidth()
	rawLines := make([]string, 0, 16)
	question := strings.TrimSpace(m.btwOverlay.Question)
	answer := strings.TrimSpace(m.btwOverlay.Answer)
	if answer == "" && m.btwOverlay.Loading {
		if pendingLine := m.renderPendingSubmissionLine(question); pendingLine != "" {
			rawLines = append(rawLines, pendingLine)
		}
		return wrapBTWContentLines(rawLines, contentWidth)
	}
	if question != "" {
		rawLines = append(rawLines, m.theme.HelpHintTextStyle().Render(question), "")
	}
	if answer != "" {
		for line := range strings.SplitSeq(answer, "\n") {
			rawLines = append(rawLines, m.theme.TextStyle().Render(strings.TrimRight(line, "\r")))
		}
	}
	if len(rawLines) == 0 {
		return nil
	}
	return wrapBTWContentLines(rawLines, contentWidth)
}

func wrapBTWContentLines(rawLines []string, contentWidth int) []string {
	lines := make([]string, 0, len(rawLines)*2)
	for _, line := range rawLines {
		wrapped := hardWrapDisplayLine(line, contentWidth)
		if wrapped == "" {
			lines = append(lines, "")
			continue
		}
		lines = append(lines, strings.Split(wrapped, "\n")...)
	}
	return lines
}

func (m *Model) btwMaxScroll(total int) int {
	visible := m.btwVisibleBudget()
	if total <= visible {
		return 0
	}
	return total - visible
}

func (m *Model) clampBTWScroll(total int) {
	if m == nil || m.btwOverlay == nil {
		return
	}
	if m.btwOverlay.Scroll < 0 {
		m.btwOverlay.Scroll = 0
	}
	maxScroll := m.btwMaxScroll(total)
	if m.btwOverlay.Scroll > maxScroll {
		m.btwOverlay.Scroll = maxScroll
	}
}

func (m *Model) scrollBTW(delta int) {
	if m == nil || m.btwOverlay == nil || delta == 0 {
		return
	}
	total := len(m.btwContentLines())
	m.clampBTWScroll(total)
	maxScroll := m.btwMaxScroll(total)
	next := m.btwOverlay.Scroll + delta
	next = max(next, 0)
	next = min(next, maxScroll)
	m.btwOverlay.Scroll = next
}

func (m *Model) renderBTWDrawer() string {
	if m == nil || m.btwOverlay == nil || m.width <= 0 {
		return ""
	}
	lines := m.btwContentLines()
	m.clampBTWScroll(len(lines))
	start := max(m.btwOverlay.Scroll, 0)
	end := minInt(len(lines), start+m.btwVisibleBudget())
	if start > end {
		start = end
	}
	contentWidth := m.btwContentWidth()
	drawerLines := []string{m.theme.SeparatorStyle().Render(strings.Repeat("─", contentWidth))}
	drawerLines = append(drawerLines, lines[start:end]...)
	return insetRenderedBlock(strings.Join(drawerLines, "\n"), inputHorizontalInset)
}

func renderPlanLine(m *Model, item planEntryState) string {
	icon := "□"
	iconStyle := m.theme.HelpHintTextStyle()
	textStyle := m.theme.HelpHintTextStyle()
	switch strings.TrimSpace(item.Status) {
	case "completed":
		icon = "✔"
		iconStyle = m.theme.NoteStyle()
		textStyle = m.theme.NoteStyle().Strikethrough(true)
	case "in_progress":
		iconStyle = lipgloss.NewStyle().Foreground(m.theme.Focus).Bold(true)
		textStyle = lipgloss.NewStyle().Foreground(m.theme.Focus).Bold(true)
	}
	return iconStyle.Render(icon) + " " + textStyle.Render(item.Content)
}

func (m *Model) planVisibleBudget() int {
	switch {
	case m.height <= 18:
		return 1
	case m.height <= 22:
		return 2
	case m.height <= 27:
		return 3
	case m.height <= 33:
		return 4
	case m.height <= 40:
		return 5
	default:
		return 6
	}
}

func visiblePlanEntries(entries []planEntryState, limit int) ([]planEntryState, int, int) {
	if limit <= 0 || len(entries) == 0 {
		return nil, len(entries), 0
	}
	if limit >= len(entries) {
		out := append([]planEntryState(nil), entries...)
		return out, 0, 0
	}
	anchor := 0
	found := false
	for idx, item := range entries {
		if strings.TrimSpace(item.Status) == "in_progress" {
			anchor = idx
			found = true
			break
		}
	}
	if !found {
		for idx, item := range entries {
			if strings.TrimSpace(item.Status) != "completed" {
				anchor = idx
				found = true
				break
			}
		}
	}
	if !found {
		anchor = len(entries) - 1
	}
	beforeContext := 0
	if limit >= 3 {
		beforeContext = 1
	}
	start := max(anchor-beforeContext, 0)
	maxStart := len(entries) - limit
	if start > maxStart {
		start = maxStart
	}
	end := minInt(len(entries), start+limit)
	visible := append([]planEntryState(nil), entries[start:end]...)
	return visible, len(entries) - len(visible), start
}

func (m *Model) startRunningAnimation() {
	m.runningTick = 0
	m.spinnerTickScheduled = false
	if len(runningCarouselLines) > 0 {
		seed := int(time.Now().UnixNano() % int64(len(runningCarouselLines)))
		if seed < 0 {
			seed = -seed
		}
		m.runningTip = seed
	} else {
		m.runningTip = 0
	}
}

func (m *Model) stopRunningAnimation() {
	m.runningTick = 0
	m.runningTip = 0
	m.spinnerTickScheduled = false
}

func (m *Model) advanceRunningAnimation() {
	if len(runningCarouselLines) > 0 {
		m.runningTick++
		if m.runningTick%runningHintRotateEveryTicks == 0 {
			m.runningTip = (m.runningTip + 1) % len(runningCarouselLines)
		}
	}
}

func (m *Model) shouldThrottleRunningAnimation() bool {
	if m == nil || !m.running {
		return false
	}
	return m.shouldDeferStreamViewportSync()
}

func (m *Model) shouldRenderStaticRunningHint() bool {
	if m == nil {
		return true
	}
	return m.noAnimation ||
		m.shouldThrottleRunningAnimation() ||
		m.streamPlayback.LastFrameRenderCost >= runningTickerStaticFrameCostThreshold
}

func (m *Model) scheduleSpinnerTick() tea.Cmd {
	if m == nil || !m.running || m.spinnerTickScheduled {
		return nil
	}
	m.spinnerTickScheduled = true
	return m.spinner.Tick
}

func (m *Model) resumeRunningAnimationIfNeeded() tea.Cmd {
	if m == nil || !m.running || m.shouldThrottleRunningAnimation() {
		return nil
	}
	return m.scheduleSpinnerTick()
}

func (m *Model) buildRunningHintText() string {
	frame := strings.TrimSpace(ansi.Strip(m.spinner.View()))
	if frame == "" {
		frame = "⠋"
	}
	if len(runningCarouselLines) > 0 {
		rawText := runningCarouselLines[m.runningTip%len(runningCarouselLines)]
		text := ""
		if m.shouldRenderStaticRunningHint() {
			m.diag.RunningTickerStaticRenders++
			text = m.theme.HelpHintTextStyle().Render(strings.TrimSpace(rawText))
		} else {
			text = m.renderRunningTickerText(rawText)
		}
		prefix := m.theme.SpinnerStyle().Render(frame)
		return prefix + " " + text
	}
	return m.theme.SpinnerStyle().Render(frame)
}

func (m *Model) runningTickerStyleSet() []lipgloss.Style {
	if m == nil {
		return nil
	}
	themeKey := m.cachedThemeRenderKey()
	if len(m.runningTickerStyles) == 5 && m.runningTickerThemeKey == themeKey {
		return m.runningTickerStyles
	}
	m.diag.RunningTickerStyleCacheMisses++
	m.runningTickerThemeKey = themeKey
	m.runningTickerStyles = []lipgloss.Style{
		m.theme.HelpHintTextStyle(),
		m.theme.SecondaryTextStyle(),
		lipgloss.NewStyle().Foreground(m.theme.Info),
		lipgloss.NewStyle().Foreground(m.theme.SpinnerFg),
		lipgloss.NewStyle().Foreground(m.theme.Focus),
	}
	return m.runningTickerStyles
}

func (m *Model) renderRunningTickerText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	totalWidth := maxInt(1, displayColumns(text))
	pathWidth := float64(totalWidth) + (runningLightLead * 2)
	head := math.Mod(float64(m.runningTick)*runningLightSpeed, pathWidth) - runningLightLead
	styles := m.runningTickerStyleSet()
	if len(styles) < 5 {
		return text
	}
	m.diag.RunningTickerAnimatedRenders++

	var out strings.Builder
	column := 0
	for _, r := range runes {
		runeWidth := maxInt(1, displayColumns(string(r)))
		center := float64(column) + (float64(runeWidth) / 2)
		distance := math.Abs(center - head)
		level := 0
		intensity := 1 - (distance / runningLightBandRadius)
		switch {
		case intensity >= 0.82:
			level = 4
		case intensity >= 0.62:
			level = 3
		case intensity >= 0.42:
			level = 2
		case intensity >= 0.22:
			level = 1
		}
		out.WriteString(styles[level].Render(string(r)))
		column += runeWidth
	}
	return out.String()
}

func (m *Model) pendingQueueHintText() string {
	count := len(m.pendingQueue)
	if count == 0 {
		return ""
	}
	if count > 1 {
		return fmt.Sprintf("%d pending messages", count)
	}
	return "1 pending message"
}

func (m *Model) renderPendingQueueDrawer() string {
	if len(m.pendingQueue) == 0 || m.width <= 0 {
		return ""
	}
	contentWidth := maxInt(1, m.mainColumnWidth()-(inputHorizontalInset*2))
	lines := []string{m.theme.SeparatorStyle().Render(strings.Repeat("─", contentWidth))}
	pending := m.pendingQueue[len(m.pendingQueue)-1]
	text := strings.TrimSpace(pending.displayLine)
	if text == "" {
		text = strings.TrimSpace(pending.execLine)
	}
	if pendingLine := m.renderPendingSubmissionLine(text); pendingLine != "" {
		lines = append(lines, pendingLine)
	}
	return insetRenderedBlock(strings.Join(lines, "\n"), inputHorizontalInset)
}

func (m *Model) dequeuePendingUserMessage(text string) {
	if len(m.pendingQueue) == 0 {
		return
	}
	needle := strings.TrimSpace(text)
	for i, pending := range m.pendingQueue {
		if pending.matchesUserMessage(needle) {
			m.pendingQueue = append(m.pendingQueue[:i], m.pendingQueue[i+1:]...)
			return
		}
	}
	m.pendingQueue = m.pendingQueue[1:]
}

func (m *Model) takeNextDeferredPendingPrompt() (pendingPrompt, bool) {
	if len(m.pendingQueue) == 0 {
		return pendingPrompt{}, false
	}
	for i, pending := range m.pendingQueue {
		if pending.dispatched {
			continue
		}
		m.pendingQueue = append(m.pendingQueue[:i], m.pendingQueue[i+1:]...)
		return pending, true
	}
	return pendingPrompt{}, false
}

func (m *Model) discardDispatchedPendingPrompts() {
	if len(m.pendingQueue) == 0 {
		return
	}
	out := m.pendingQueue[:0]
	for _, pending := range m.pendingQueue {
		if !pending.dispatched {
			out = append(out, pending)
		}
	}
	m.pendingQueue = out
}

func (m *Model) renderInputBar() string {
	if m.activePrompt != nil {
		return insetRenderedBlock(m.renderPromptInputBar(), inputHorizontalInset)
	}
	if start, end, ok := normalizedSelectionRange(m.inputSelectionStart, m.inputSelectionEnd, len(m.inputPlainLines())); ok &&
		(start.line != end.line || start.col != end.col) {
		lines := m.inputPlainLines()
		return insetRenderedBlock(strings.Join(renderSelectionOnLines(lines, start, end, m.theme.SelectionStyle()), "\n"), inputHorizontalInset)
	}

	prompt := m.theme.PromptStyle().Render("> ")
	if m.isWizardActive() && m.wizard != nil {
		query, _, ok := wizardVisibleInputAtCursor(m.wizard.def.Command, m.input, m.cursor)
		if ok {
			inputVal := query
			if m.wizard.hideInput() {
				inputVal = strings.Repeat("*", utf8.RuneCountInString(strings.TrimSpace(query)))
			}
			return insetRenderedBlock(renderMultilineInput(prompt, inputVal), inputHorizontalInset)
		}
	}
	return m.renderRegularInputBar()
}

func (m *Model) syncTextareaChrome() {
	ta := m.textarea
	m.applyTextareaChrome(&ta)
	m.textarea = ta
}

func (m *Model) applyTextareaChrome(ta *textarea.Model) {
	if ta == nil {
		return
	}
	if m == nil {
		return
	}
	first := m.inputPromptPrefix()
	width := displayColumns(first)
	if width <= 0 {
		first = "> "
		width = displayColumns(first)
	}
	continuation := strings.Repeat(" ", width)
	ta.SetPromptFunc(width, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return first
		}
		return continuation
	})
	ta.SetWidth(m.composerContentWidth())
	displayValue, _ := composeInputDisplay(ta.Value(), len([]rune(ta.Value())), m.inputAttachments)
	height := max(desiredComposerRows(displayValue, "", ta.Width(), maxInputBarRows), tuikit.ComposerMinHeight)
	ta.SetHeight(height)
}

func (m *Model) inputPromptPrefix() string {
	return "> "
}

func (m *Model) currentInputGhostHint() string {
	if m == nil || m.activePrompt != nil || m.running {
		return ""
	}
	value := m.textarea.Value()
	cursorAtEnd := m.cursor == len(m.input)
	if m.isWizardActive() && m.wizard != nil {
		if visible, visibleCursor, ok := wizardVisibleInputAtCursor(m.wizard.def.Command, []rune(value), m.cursor); ok {
			value = visible
			cursorAtEnd = visibleCursor == len([]rune(visible))
		}
	}
	if value == "" || strings.Contains(value, "\n") {
		return ""
	}
	if !cursorAtEnd {
		return ""
	}

	suggestion := ""
	switch {
	case len(m.slashCandidates) > 0 && m.slashIndex >= 0 && m.slashIndex < len(m.slashCandidates):
		suggestion = strings.TrimSpace(m.slashCandidates[m.slashIndex])
	case len(m.resumeCandidates) > 0 && m.resumeIndex >= 0 && m.resumeIndex < len(m.resumeCandidates):
		selected := strings.TrimSpace(m.resumeCandidates[m.resumeIndex].SessionID)
		if selected != "" {
			suggestion = "/resume " + selected
		}
	case len(m.slashArgCandidates) > 0:
		candidate, ok := m.currentSlashArgCandidate()
		if !ok {
			break
		}
		selected := strings.TrimSpace(candidate.Value)
		suggestion = m.suggestedSlashArgInput(selected)
	}
	if suggestion == "" || !strings.HasPrefix(suggestion, value) {
		return ""
	}
	return suggestion[len(value):]
}

func (m *Model) suggestedSlashArgInput(choice string) string {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return ""
	}
	if m.isWizardActive() && m.wizard != nil {
		return choice
	}
	command := strings.TrimSpace(m.slashArgCommand)
	switch {
	case command == "model":
		return "/model " + choice
	case command == "model use":
		return "/model use " + choice
	case command == "model del":
		return "/model del " + choice
	case strings.HasPrefix(command, "model use "):
		return "/" + command + " " + choice
	case strings.HasPrefix(command, "model del "):
		return "/" + command + " " + choice
	default:
		if command == "" {
			return ""
		}
		return "/" + command + " " + choice
	}
}

func (m *Model) inputPlainLines() []string {
	return m.regularInputPlainLines()
}

func insetRenderedBlock(text string, inset int) string {
	if inset <= 0 || text == "" {
		return text
	}
	pad := strings.Repeat(" ", inset)
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func renderMultilineInput(prompt string, input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) == 0 {
		return prompt
	}
	indent := strings.Repeat(" ", maxInt(0, lipgloss.Width(prompt)))
	lines[0] = prompt + lines[0]
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderStatusHeader() string {
	style := m.theme.StatusStyle().Width(m.fixedRowWidth())
	return m.renderFixedRow(fixedSelectionHeader, m.headerRowText(), m.renderHeaderRowStyledText(), style)
}

func (m *Model) renderHintRow() string {
	style := m.theme.HintRowStyle().Width(m.fixedRowWidth())
	return m.renderFixedRow(fixedSelectionHint, m.hintRowText(), m.renderHintRowStyledText(), style)
}

func (m *Model) hintRowText() string {
	return composeStyledFooter(m.fixedRowContentWidth(), ansi.Strip(m.buildHintText()), "")
}

func (m *Model) renderHintRowStyledText() string {
	w := m.fixedRowContentWidth()
	if w <= 0 {
		return ""
	}
	text := m.buildHintText()
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return composeStyledFooter(w, text, "")
}

func (m *Model) headerRowText() string {
	left, right := fitHeaderRowParts(m.fixedRowContentWidth(), m.headerWorkspaceText(), m.headerModelText())
	return composeStyledFooter(m.fixedRowContentWidth(), left, right)
}

func (m *Model) renderHeaderRowStyledText() string {
	tok := m.theme.Tokens()
	leftPlain, rightPlain := fitHeaderRowParts(m.fixedRowContentWidth(), m.headerWorkspaceText(), m.headerModelText())
	left := tok.TextPrimary.Render(leftPlain)
	right := tok.TextPrimary.Render(rightPlain)
	return composeStyledFooter(m.fixedRowContentWidth(), left, right)
}

func (m *Model) headerWorkspaceText() string {
	if workspace := strings.TrimSpace(m.statusView.Workspace); workspace != "" {
		return workspace
	}
	return strings.TrimSpace(m.cfg.Workspace)
}

func (m *Model) headerModelText() string {
	return m.statusView.HeaderModelText(m.statusModel)
}

func (m *Model) renderStatusFooter() string {
	components := m.theme.ComponentStyles()
	style := components.Status.Bar.Width(m.fixedRowWidth())
	if m.fixedSelectionArea == fixedSelectionFooter {
		return m.renderFixedRow(fixedSelectionFooter, m.footerRowText(), m.renderFooterRowStyledText(), style)
	}
	contentWidth := m.fixedRowContentWidth()
	leftPlain, rightPlain := fitGenericFooterParts(contentWidth, m.footerLeftText(), m.footerContextText())
	left := styleFooterLeft(m, leftPlain)
	right := components.Status.Text.Render(rightPlain)
	return style.Render(composeStyledFooter(contentWidth, left, right))
}

func (m *Model) shouldRenderPalette() bool {
	return m.showPalette || m.paletteAnimLines > 0
}

func (m *Model) fullPaletteLineCount() int {
	if m.width <= 0 || m.height <= 0 {
		return 0
	}
	text := ansi.Strip(m.theme.ModalStyle().Render(m.palette.View()))
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func (m *Model) renderPaletteOverlay() string {
	full := m.theme.ModalStyle().Render(m.palette.View())
	if full == "" {
		return ""
	}
	lines := strings.Split(full, "\n")
	visible := m.paletteAnimLines
	if visible <= 0 {
		return ""
	}
	if visible >= len(lines) {
		return full
	}
	return strings.Join(lines[len(lines)-visible:], "\n")
}

func (m *Model) renderViewportScrollbar(vpView string) string {
	if m.viewportScrollbarWidth() == 0 || vpView == "" {
		return vpView
	}
	if !m.shouldShowViewportScrollbar(time.Now()) {
		return vpView
	}
	total := len(m.viewportStyledLines)
	visible := maxInt(1, m.viewport.Height())
	if total <= visible {
		return vpView
	}
	lines := strings.Split(vpView, "\n")
	if len(lines) == 0 {
		return vpView
	}
	return strings.Join(addScrollbar(lines, m.viewport.Width(), visible, m.viewportVisibleOffset(), total, m.theme, true), "\n")
}

func (m *Model) viewportViewCacheKey(showScrollbar bool) string {
	if m == nil {
		return ""
	}
	return strings.Join([]string{
		strconv.FormatUint(m.viewportContentVersion, 10),
		strconv.FormatUint(m.viewportSelectionVersion, 10),
		strconv.Itoa(m.viewport.Width()),
		strconv.Itoa(m.viewport.Height()),
		strconv.Itoa(m.viewport.YOffset()),
		strconv.Itoa(len(m.viewportStyledLines)),
		strconv.FormatBool(m.viewportContentStale),
		strconv.FormatBool(showScrollbar),
	}, "|")
}

func (m *Model) renderViewportView() string {
	if m == nil {
		return ""
	}
	showScrollbar := m.viewportScrollbarWidth() > 0 && m.shouldShowViewportScrollbar(time.Now())
	key := m.viewportViewCacheKey(showScrollbar)
	if key != "" && key == m.lastViewportViewKey {
		return m.lastViewportViewRendered
	}
	var vpView string
	if m.hasSelectionRange() {
		vpView = strings.TrimRight(m.renderViewportLinesView(true), "\n")
		m.diag.SelectionVisibleRenders++
	} else if m.viewportContentStale {
		vpView = strings.TrimRight(m.renderViewportLinesView(false), "\n")
	} else {
		vpView = strings.TrimRight(m.viewport.View(), "\n")
	}
	if showScrollbar {
		vpView = m.renderViewportScrollbar(vpView)
	}
	m.lastViewportViewKey = key
	m.lastViewportViewRendered = vpView
	return vpView
}

func (m *Model) renderViewportSelectionView() string {
	return m.renderViewportLinesView(true)
}

func (m *Model) renderViewportLinesView(applySelection bool) string {
	if m == nil || len(m.viewportStyledLines) == 0 || m.viewport.Height() <= 0 {
		return m.viewport.View()
	}
	offset := m.viewportVisibleOffset()
	if offset >= len(m.viewportStyledLines) {
		offset = maxInt(0, len(m.viewportStyledLines)-1)
	}
	end := minInt(len(m.viewportStyledLines), offset+maxInt(1, m.viewport.Height()))
	if end < offset {
		end = offset
	}
	styled := append([]string(nil), m.viewportStyledLines[offset:end]...)
	plain := m.viewportPlainLines[offset:end]
	if applySelection {
		start, finish, ok := normalizedSelectionRange(m.selectionStart, m.selectionEnd, len(m.viewportPlainLines))
		if ok && len(styled) > 0 && finish.line >= offset && start.line < end {
			localStart := textSelectionPoint{line: maxInt(start.line, offset) - offset, col: start.col}
			localFinish := textSelectionPoint{line: minInt(finish.line, end-1) - offset, col: finish.col}
			if start.line < offset {
				localStart.col = 0
			}
			if finish.line >= end {
				localFinish.col = displayColumns(plain[len(plain)-1])
			}
			styled = renderSelectionOnStyledLines(styled, plain, localStart, localFinish, m.theme.SelectionStyle())
		}
	}
	vp := m.viewport
	vp.SetContentLines(styled)
	vp.SetYOffset(0)
	return vp.View()
}

func (m *Model) viewportVisibleOffset() int {
	if m == nil {
		return 0
	}
	if m.viewportContentStale && m.isViewportFollowTail() {
		return m.viewportMaxOffset()
	}
	return maxInt(0, m.viewport.YOffset())
}

func (m *Model) viewportLineCount() int {
	if m == nil {
		return 0
	}
	if len(m.viewportStyledLines) > 0 || m.viewportContentStale {
		return len(m.viewportStyledLines)
	}
	return m.viewport.TotalLineCount()
}

func (m *Model) viewportMaxOffset() int {
	if m == nil {
		return 0
	}
	return maxInt(0, m.viewportLineCount()-maxInt(1, m.viewport.Height()))
}

func (m *Model) footerRowText() string {
	left, right := fitGenericFooterParts(m.fixedRowContentWidth(), m.footerLeftText(), m.footerContextText())
	return composeStyledFooter(m.fixedRowContentWidth(), left, right)
}

func (m *Model) footerLeftText() string {
	mode := strings.TrimSpace(m.statusView.FooterModeText(m.modeLabel()))
	helpText := m.footerHelpText()
	switch {
	case mode == "":
		return helpText
	case helpText == "":
		return mode
	default:
		return mode + "  " + helpText
	}
}

func (m *Model) renderFooterRowStyledText() string {
	leftPlain, rightPlain := fitGenericFooterParts(m.fixedRowContentWidth(), m.footerLeftText(), m.footerContextText())
	left := styleFooterLeft(m, leftPlain)
	right := m.theme.TextStyle().Render(rightPlain)
	return composeStyledFooter(m.fixedRowContentWidth(), left, right)
}

func (m *Model) footerHelpText() string {
	return formatFooterBindingKeys(m.currentFooterHelp().ShortHelp())
}

func (m *Model) footerContextText() string {
	text := formatStatusContextDisplay(strings.TrimSpace(m.statusView.FooterContextText(m.statusContext)))
	if text == "0" {
		return ""
	}
	return text
}

func (m *Model) modeLabel() string {
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m.statusModeLabel)
}

func composeStyledFooter(width int, left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if width <= 0 {
		return ""
	}
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	if left == "" && right == "" {
		return strings.Repeat(" ", width)
	}
	if left == "" {
		if rightWidth >= width {
			return right
		}
		return strings.Repeat(" ", width-rightWidth) + right
	}
	if right == "" {
		if leftWidth >= width {
			return left
		}
		return left + strings.Repeat(" ", width-leftWidth)
	}
	gap := max(width-leftWidth-rightWidth, 1)
	return left + strings.Repeat(" ", gap) + right
}

func fitHeaderRowParts(width int, workspace string, model string) (string, string) {
	return fitFooterParts(width, workspace, model, truncateWorkspaceStatusDisplay, truncateMiddleDisplayWidthPlain, 20, 24)
}

func fitGenericFooterParts(width int, left string, right string) (string, string) {
	return fitFooterParts(width, left, right, truncateTailDisplay, truncateTailDisplay, 16, 10)
}

func fitFooterParts(width int, left string, right string, leftTrunc func(string, int) string, rightTrunc func(string, int) string, minLeft int, minRight int) (string, string) {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if width <= 0 {
		return "", ""
	}
	if left == "" {
		return "", rightTrunc(right, width)
	}
	if right == "" {
		return leftTrunc(left, width), ""
	}

	leftMin := minInt(maxInt(4, minLeft), maxInt(4, width-1))
	rightMin := minInt(maxInt(4, minRight), maxInt(4, width-1))
	maxRight := maxInt(rightMin, width-leftMin-1)
	if displayColumns(right) > maxRight {
		right = rightTrunc(right, maxRight)
	}

	leftBudget := maxInt(0, width-displayColumns(right)-1)
	left = leftTrunc(left, leftBudget)

	if total := displayColumns(left) + displayColumns(right) + 1; total > width {
		overflow := total - width
		if displayColumns(left) > leftMin {
			left = leftTrunc(left, maxInt(leftMin, displayColumns(left)-overflow))
		}
	}
	if total := displayColumns(left) + displayColumns(right) + 1; total > width {
		overflow := total - width
		if displayColumns(right) > rightMin {
			right = rightTrunc(right, maxInt(rightMin, displayColumns(right)-overflow))
		}
	}
	if total := displayColumns(left) + displayColumns(right) + 1; total > width {
		left = leftTrunc(left, maxInt(0, width-displayColumns(right)-1))
	}
	if total := displayColumns(left) + displayColumns(right) + 1; total > width {
		right = rightTrunc(right, maxInt(0, width-displayColumns(left)-1))
	}
	return left, right
}

func truncateWorkspaceStatusDisplay(input string, width int) string {
	input = strings.TrimSpace(input)
	if input == "" || width <= 0 || displayColumns(input) <= width {
		return input
	}
	path, branch, dirty, ok := parseWorkspaceStatusDisplay(input)
	if !ok {
		return truncateMiddleDisplayWidthPlain(input, width)
	}
	if branch == "" {
		return truncateMiddleDisplayWidthPlain(path, width)
	}
	suffix := " [⎇ " + branch
	if dirty {
		suffix += "*"
	}
	suffix += "]"
	if displayColumns(suffix) >= width {
		return truncateTailDisplay(suffix, width)
	}
	contentBudget := maxInt(1, width-displayColumns(" [⎇ ")-displayColumns("]"))
	if dirty {
		contentBudget--
	}
	pathBudget := maxInt(8, minInt(contentBudget*2/3, contentBudget-8))
	branchBudget := maxInt(8, contentBudget-pathBudget)
	if pathWidth := displayColumns(path); pathWidth < pathBudget {
		branchBudget = minInt(contentBudget-pathWidth, contentBudget-1)
		pathBudget = contentBudget - branchBudget
	}
	if branchWidth := displayColumns(branch); branchWidth < branchBudget {
		pathBudget = minInt(contentBudget-branchWidth, contentBudget-1)
		branchBudget = contentBudget - pathBudget
	}
	if branchBudget < 8 {
		branchBudget = minInt(contentBudget, 8)
		pathBudget = maxInt(1, contentBudget-branchBudget)
	}
	if pathBudget < 8 {
		pathBudget = minInt(contentBudget, 8)
		branchBudget = maxInt(1, contentBudget-pathBudget)
	}
	path = truncateMiddleDisplayWidthPlain(path, pathBudget)
	branch = truncateTailDisplay(branch, branchBudget)
	out := path + " [⎇ " + branch
	if dirty {
		out += "*"
	}
	out += "]"
	if displayColumns(out) > width {
		return truncateTailDisplay(out, width)
	}
	return out
}

func parseWorkspaceStatusDisplay(input string) (path string, branch string, dirty bool, ok bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", false, false
	}
	open := strings.LastIndex(input, " [⎇ ")
	if open <= 0 || !strings.HasSuffix(input, "]") {
		return "", "", false, false
	}
	path = strings.TrimSpace(input[:open])
	branch = strings.TrimSpace(strings.TrimSuffix(input[open+len(" [⎇ "):], "]"))
	if strings.HasSuffix(branch, "*") {
		dirty = true
		branch = strings.TrimSpace(strings.TrimSuffix(branch, "*"))
	}
	if path == "" || branch == "" {
		return "", "", false, false
	}
	return path, branch, dirty, true
}

func truncateMiddleDisplayWidthPlain(input string, limit int) string {
	text := strings.Join(strings.Fields(strings.TrimSpace(input)), " ")
	if text == "" || limit <= 0 || displayColumns(text) <= limit {
		return text
	}
	if limit <= 3 {
		return sliceByDisplayColumns(text, 0, limit)
	}
	head := maxInt(1, (limit-3)*2/3)
	tail := maxInt(1, (limit-3)-head)
	total := displayColumns(text)
	prefix := sliceByDisplayColumns(text, 0, head)
	suffix := sliceByDisplayColumns(text, total-tail, total)
	return prefix + "..." + suffix
}

func styleFooterLeft(m *Model, plain string) string {
	plain = strings.TrimSpace(plain)
	if plain == "" || m == nil {
		return ""
	}
	mode := strings.TrimSpace(m.modeLabel())
	help := strings.TrimSpace(m.footerHelpText())
	switch {
	case mode == "":
		return m.theme.HelpHintTextStyle().Render(plain)
	case help == "":
		modeStyle := m.theme.TextStyle().Bold(true)
		if mode == "full_access" {
			modeStyle = m.theme.WarnStyle().Bold(true)
		}
		return modeStyle.Render(plain)
	default:
		parts := strings.SplitN(plain, "  ", 2)
		modeText := parts[0]
		helpText := ""
		if len(parts) > 1 {
			helpText = parts[1]
		}
		modeStyle := m.theme.TextStyle().Bold(true)
		if modeText == "full_access" {
			modeStyle = m.theme.WarnStyle().Bold(true)
		}
		if helpText == "" {
			return modeStyle.Render(modeText)
		}
		return modeStyle.Render(modeText) + "  " + m.theme.HelpHintTextStyle().Render(helpText)
	}
}

func formatFooterBindingKeys(bindings []key.Binding) string {
	parts := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if !binding.Enabled() {
			continue
		}
		keyLabel := strings.TrimSpace(binding.Help().Key)
		if keyLabel == "" {
			continue
		}
		parts = append(parts, keyLabel)
	}
	return strings.Join(parts, "  ")
}

func formatStatusContextDisplay(text string) string {
	return strings.TrimSpace(text)
}

func (m *Model) adjustTextareaHeight() {
	displayValue, _ := composeInputDisplay(m.textarea.Value(), len([]rune(m.textarea.Value())), m.inputAttachments)
	height := max(desiredComposerRows(displayValue, "", m.textarea.Width(), maxInputBarRows), tuikit.ComposerMinHeight)
	if m.textarea.Height() != height {
		m.textarea.SetHeight(height)
		// Textarea height change affects bottomSectionHeight; reconcile
		// the viewport so View() doesn't need to mutate state.
		m.ensureViewportLayout()
	}
}

func hardWrapDisplayLine(line string, width int) string {
	if width <= 0 || line == "" {
		return line
	}
	return ansi.Hardwrap(line, width, true)
}
