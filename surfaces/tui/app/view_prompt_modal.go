package tuiapp

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) renderPromptModal() string {
	if m.activePrompt == nil {
		return ""
	}
	p := m.activePrompt
	if len(p.choices) == 0 {
		return ""
	}
	bodyLines := make([]string, 0, 24)
	if title := strings.TrimSpace(p.title); title != "" {
		bodyLines = append(bodyLines, m.theme.TitleStyle().Render(title))
	}
	if len(p.details) > 0 {
		if len(bodyLines) > 0 {
			bodyLines = append(bodyLines, "")
		}
		bodyLines = append(bodyLines, m.renderPromptDetailLines(p.details)...)
	}
	visible := m.visiblePromptChoices()
	if len(visible) == 0 {
		if len(bodyLines) > 0 {
			bodyLines = append(bodyLines, "")
		}
		bodyLines = append(bodyLines, m.theme.HelpHintTextStyle().Render("no matching choices"))
		return m.renderPromptModalBox(bodyLines)
	}
	const maxVisiblePromptChoices = 8
	m.syncPromptChoiceWindow()
	start := max(m.activePrompt.scrollOffset, 0)
	start = min(start, len(visible))
	end := minInt(len(visible), start+maxVisiblePromptChoices)
	window := visible[start:end]
	choiceLabelWidth := m.promptChoiceLabelWidth(window)
	stackChoices := p.stackedChoices && m.promptChoicesNeedStacking(window, choiceLabelWidth)
	lines := make([]string, 0, len(window))
	for i := range window {
		choice := window[i]
		actualIndex := start + i
		lines = append(lines, m.renderPromptChoiceLines(choice, actualIndex == p.choiceIndex, stackChoices, choiceLabelWidth)...)
		if stackChoices && i < len(window)-1 {
			lines = append(lines, "")
		}
	}
	if len(bodyLines) > 0 {
		bodyLines = append(bodyLines, "")
	}
	bodyLines = append(bodyLines, lines...)
	return m.renderPromptModalBox(bodyLines)
}

func (m *Model) renderPromptDetailLines(details []PromptDetail) []string {
	if len(details) == 0 {
		return nil
	}
	labelColumnWidth := 0
	for _, detail := range details {
		label := strings.TrimSpace(detail.Label)
		value := sanitizePromptModalText(detail.Value)
		if label == "" || value == "" {
			continue
		}
		labelColumnWidth = maxInt(labelColumnWidth, displayColumns(strings.ToUpper(label)+":"))
	}
	if labelColumnWidth == 0 {
		return nil
	}
	valueColumnIndent := strings.Repeat(" ", labelColumnWidth+1)
	valueColumnWidth := maxInt(1, m.promptModalInnerWidth()-labelColumnWidth-1)
	lines := make([]string, 0, len(details)*2)
	detailBudget := m.promptDetailLineBudget()
	for _, detail := range details {
		label := strings.TrimSpace(detail.Label)
		value := sanitizePromptModalText(detail.Value)
		if label == "" || value == "" {
			continue
		}
		labelStyle := m.promptToneStyle(detail.Tone, m.theme.KeyLabelStyle())
		valueStyle := m.theme.TextStyle()
		if detail.Emphasis {
			valueStyle = valueStyle.Bold(true)
		}
		valueLines := make([]string, 0, strings.Count(value, "\n")+1)
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			valueLines = append(valueLines, wrapPromptModalLine(line, valueColumnWidth)...)
		}
		if len(valueLines) == 0 {
			continue
		}
		truncatedCount := 0
		if len(valueLines) > detailBudget {
			truncatedCount = len(valueLines) - detailBudget
			valueLines = append([]string(nil), valueLines[:detailBudget]...)
		}
		labelText := strings.ToUpper(label) + ":"
		labelGap := strings.Repeat(" ", labelColumnWidth-displayColumns(labelText)+1)
		lines = append(lines, labelStyle.Render(labelText)+labelGap+valueStyle.Render(valueLines[0]))
		for _, line := range valueLines[1:] {
			lines = append(lines, valueColumnIndent+valueStyle.Render(line))
		}
		if truncatedCount > 0 {
			lines = append(lines, valueColumnIndent+m.theme.HelpHintTextStyle().Render(fmt.Sprintf("… %d more lines", truncatedCount)))
		}
	}
	return lines
}

func (m *Model) renderPromptChoiceLines(choice promptChoice, selected, stacked bool, labelColumnWidth int) []string {
	gutter := "  "
	if selected {
		gutter = "▎ "
	}
	marker := m.promptChoiceMarker(choice)
	label := sanitizePromptModalText(choice.label)
	detail := sanitizePromptModalText(choice.detail)
	contentWidth := maxInt(1, m.promptModalInnerWidth()-displayColumns(gutter))
	if stacked && detail != "" {
		labelText := truncateTailDisplay(marker+label, contentWidth)
		labelStyle := m.promptToneStyle(choice.tone, m.theme.TextStyle())
		if selected {
			labelStyle = m.theme.SelectionStyle().Bold(true)
		} else if choice.tone == PromptToneDefault {
			labelStyle = labelStyle.Bold(true)
		}
		labelLine := m.theme.HelpHintTextStyle().Render(gutter) + labelStyle.Render(labelText)
		if selected {
			labelLine = labelStyle.Render(gutter + labelText)
		}
		detailIndent := strings.Repeat(" ", displayColumns(gutter)+2)
		detailWidth := maxInt(1, contentWidth-2)
		lines := []string{labelLine}
		for _, line := range wrapPromptModalLine(detail, detailWidth) {
			lines = append(lines, detailIndent+m.theme.HelpHintTextStyle().Render(line))
		}
		return lines
	}
	mainText := marker + label
	if detail != "" {
		mainText += "  " + detail
	}
	mainText = truncateTailDisplay(mainText, contentWidth)
	selectedLabelStyle := m.theme.SelectionStyle().Bold(true)
	selectedDetailStyle := m.theme.SelectionStyle()
	if detail == "" {
		if selected {
			return []string{selectedLabelStyle.Render(gutter) + selectedLabelStyle.Render(mainText)}
		}
		return []string{m.theme.HelpHintTextStyle().Render(gutter) + m.promptToneStyle(choice.tone, m.theme.TextStyle()).Render(mainText)}
	}
	separator := "  "
	labelWidth := minInt(maxInt(1, labelColumnWidth), maxInt(1, contentWidth-displayColumns(separator)-1))
	labelText := padLineToDisplayWidth(truncateTailDisplay(marker+label, labelWidth), labelWidth)
	detailBudget := maxInt(1, contentWidth-labelWidth-displayColumns(separator))
	detailText := truncateTailDisplay(detail, detailBudget)
	if selected {
		return []string{selectedLabelStyle.Render(gutter+labelText+separator) +
			selectedDetailStyle.Render(detailText)}
	}
	return []string{m.theme.HelpHintTextStyle().Render(gutter) +
		m.promptToneStyle(choice.tone, m.theme.TextStyle()).Render(labelText) +
		separator +
		m.theme.HelpHintTextStyle().Render(detailText)}
}

func (m *Model) promptChoiceMarker(choice promptChoice) string {
	if m.activePrompt == nil || !m.activePrompt.multiSelect {
		return ""
	}
	if _, ok := m.activePrompt.selected[choice.value]; ok {
		return "[x] "
	}
	return "[ ] "
}

func (m *Model) promptChoiceLabelWidth(choices []promptChoice) int {
	width := 0
	for _, choice := range choices {
		width = maxInt(width, displayColumns(m.promptChoiceMarker(choice)+sanitizePromptModalText(choice.label)))
	}
	return width
}

func (m *Model) promptChoicesNeedStacking(choices []promptChoice, labelColumnWidth int) bool {
	contentWidth := maxInt(1, m.promptModalInnerWidth()-displayColumns("  "))
	for _, choice := range choices {
		detail := sanitizePromptModalText(choice.detail)
		if detail == "" {
			continue
		}
		if strings.Contains(detail, "\n") || labelColumnWidth+displayColumns("  ")+displayColumns(detail) > contentWidth {
			return true
		}
	}
	return false
}

func (m *Model) promptToneStyle(tone PromptTone, fallback lipgloss.Style) lipgloss.Style {
	switch tone {
	case PromptToneAccent:
		return m.theme.Tokens().Accent.Bold(true)
	case PromptToneWarning:
		return m.theme.Tokens().Warning.Bold(true)
	case PromptToneDanger:
		return m.theme.Tokens().Danger.Bold(true)
	default:
		return fallback
	}
}

func (m *Model) renderPromptModalBox(lines []string) string {
	return m.renderPromptModalBoxWithWidth(lines, m.promptModalOuterWidth())
}

func (m *Model) renderPromptModalBoxWithWidth(lines []string, width int) string {
	if width <= 0 {
		width = 72
	}
	hasBorder := m.overlayUsesBorder()
	chrome := m.overlayBorderChromeWidth()
	innerWidth := maxInt(8, width-chrome)
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		filtered = append(filtered, wrapPromptModalLine(line, innerWidth)...)
	}
	filtered = clampPromptModalLines(filtered, m.promptModalLineBudget(), m.theme)
	if len(filtered) == 0 {
		filtered = []string{""}
	}
	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{
		Body:      filtered,
		Width:     width,
		UseBorder: hasBorder,
	})
	if m.activePrompt != nil && len(m.activePrompt.choices) > 0 {
		return m.attachPromptChoiceFooter(frame)
	}
	return frame
}

func (m *Model) promptDetailLineBudget() int {
	switch {
	case m.height <= 18:
		return 8
	case m.height <= 26:
		return 12
	default:
		return 18
	}
}

func (m *Model) promptModalInnerWidth() int {
	return maxInt(8, m.promptModalOuterWidth()-m.overlayBorderChromeWidth())
}

func (m *Model) promptModalOuterWidth() int {
	available := m.fixedRowWidth()
	width := minInt(maxInt(20, available-4), 120)
	width = minInt(width, available)
	if width <= 0 {
		width = 72
	}
	return width
}

func (m *Model) promptModalLineBudget() int {
	if m.height <= 0 {
		return 24
	}
	return maxInt(8, m.height-8)
}

func wrapPromptModalLine(line string, width int) []string {
	if width <= 0 {
		return []string{line}
	}
	if strings.TrimSpace(line) == "" {
		return []string{""}
	}
	wrapped := hardWrapDisplayLine(line, width)
	if wrapped == "" {
		return []string{""}
	}
	return strings.Split(wrapped, "\n")
}

func clampPromptModalLines(lines []string, budget int, theme tuikit.Theme) []string {
	if budget <= 0 || len(lines) <= budget {
		return lines
	}
	if budget == 1 {
		return []string{theme.HelpHintTextStyle().Render("…")}
	}
	truncated := append([]string(nil), lines[:budget-1]...)
	truncated = append(truncated, theme.HelpHintTextStyle().Render(fmt.Sprintf("… %d more lines", len(lines)-budget+1)))
	return truncated
}

func sanitizePromptModalText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == "<nil>" {
		return ""
	}
	return value
}

func (m *Model) renderCompletionOverlay(geometry completionOverlayGeometry, lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	innerWidth := geometry.innerWidth
	hasBorder := geometry.chrome.useBorder
	contentLimit := innerWidth
	if hasBorder {
		contentLimit = innerWidth - 4
		if contentLimit < 1 {
			contentLimit = 1
		}
	}

	filtered := make([]string, 0, len(lines)+geometry.chrome.topInsetRows+geometry.chrome.bottomInsetRows)
	for range geometry.chrome.topInsetRows {
		blank := strings.Repeat(" ", contentLimit)
		filtered = append(filtered, blank)
	}
	for _, line := range lines {
		if cols := displayColumns(line); cols > contentLimit {
			if contentLimit <= 3 {
				line = ansi.Truncate(line, contentLimit, "")
			} else {
				line = ansi.Truncate(line, contentLimit, "...")
			}
		}
		if pad := contentLimit - displayColumns(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		filtered = append(filtered, line)
	}
	for range geometry.chrome.bottomInsetRows {
		blank := strings.Repeat(" ", contentLimit)
		filtered = append(filtered, blank)
	}
	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{
		Body:      filtered,
		Width:     innerWidth,
		UseBorder: hasBorder,
	})
	return m.attachCompletionOverlayFooter(frame, geometry)
}

func (m *Model) completionOverlayInnerWidth() int {
	return maxInt(1, m.completionOverlayWidth())
}

func (m *Model) completionOverlayWidth() int {
	chrome := m.overlayBorderChromeWidth()
	width := maxInt(44, m.fixedRowWidth()-chrome)
	if m.width > 0 {
		width = minInt(width, maxInt(44, m.width-chrome))
	}
	if width <= 0 {
		width = 72
	}
	return width
}

func (m *Model) renderPromptInputBar() string {
	bg := m.theme.ComposerBg
	promptStyle := m.theme.PromptStyle()
	if bg != nil && !m.theme.NoColor {
		promptStyle = promptStyle.Background(bg)
	}
	prompt := promptStyle.Render("> ")
	value, cursor := m.promptInputValue()
	return renderMultilineInput(prompt, insertPromptCursor(value, cursor, m.promptCursorGlyph()))
}

func (m *Model) promptInputValue() (string, int) {
	if m.activePrompt == nil {
		return "", 0
	}
	if m.activePrompt.filterable {
		return string(m.activePrompt.filter), m.activePrompt.cursor
	}
	value := string(m.activePrompt.input)
	if m.activePrompt.secret {
		value = strings.Repeat("*", len(m.activePrompt.input))
	}
	return value, m.activePrompt.cursor
}

func insertPromptCursor(value string, cursor int, cursorGlyph string) string {
	runes := []rune(value)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	head := string(runes[:cursor])
	tail := string(runes[cursor:])
	return head + cursorGlyph + tail
}

func (m *Model) promptCursorGlyph() string {
	return m.theme.PromptStyle().Render("█")
}

func (m *Model) promptHintText() string {
	if m.activePrompt == nil {
		return ""
	}
	text := strings.TrimSpace(m.activePrompt.prompt)
	if text == "" {
		text = strings.TrimSpace(m.activePrompt.title)
	}
	text = strings.TrimSuffix(text, ":")
	text = strings.TrimSpace(text)
	if len(m.activePrompt.choices) > 0 {
		footer := "↑/↓ move  enter confirm  esc cancel"
		if m.activePrompt.filterable {
			if m.activePrompt.multiSelect {
				return "type filter  space toggle  " + footer
			}
			return "type filter  " + footer
		}
		if m.activePrompt.multiSelect {
			return "space toggle  " + footer
		}
		return footer
	}
	if text == "" {
		return "Enter a value"
	}
	return "Enter " + text
}
