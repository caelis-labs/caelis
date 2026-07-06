package tuiapp

import (
	"fmt"
	"strings"

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
	lines := make([]string, 0, len(window)+2)
	if start > 0 {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("… and %d earlier", start),
		))
	}
	for i := range window {
		choice := window[i]
		actualIndex := start + i
		lines = append(lines, m.renderPromptChoiceLine(choice, actualIndex == p.choiceIndex))
	}
	if len(visible) > end {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("… and %d more", len(visible)-end),
		))
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
	lines := make([]string, 0, len(details)*2)
	detailBudget := m.promptDetailLineBudget()
	for _, detail := range details {
		label := strings.TrimSpace(detail.Label)
		value := sanitizePromptModalText(detail.Value)
		if label == "" || value == "" {
			continue
		}
		valueStyle := m.theme.TextStyle()
		if detail.Emphasis {
			valueStyle = valueStyle.Bold(true)
		}
		valueLines := strings.Split(value, "\n")
		truncatedCount := 0
		if len(valueLines) > detailBudget {
			truncatedCount = len(valueLines) - detailBudget
			valueLines = append(append([]string(nil), valueLines[:detailBudget]...), "")
		}
		first := strings.TrimRight(valueLines[0], "\r")
		if strings.TrimSpace(first) == "" {
			continue
		}
		lines = append(lines, m.theme.KeyLabelStyle().Render(strings.ToUpper(label)+":")+" "+valueStyle.Render(first))
		for _, line := range valueLines[1:] {
			line = strings.TrimRight(line, "\r")
			if strings.TrimSpace(line) == "" {
				continue
			}
			lines = append(lines, "  "+valueStyle.Render(line))
		}
		if truncatedCount > 0 {
			lines = append(lines, "  "+m.theme.HelpHintTextStyle().Render(fmt.Sprintf("… %d more lines", truncatedCount)))
		}
	}
	return lines
}

func (m *Model) renderPromptChoiceLine(choice promptChoice, selected bool) string {
	gutter := "  "
	if selected {
		gutter = "▎ "
	}
	marker := ""
	if m.activePrompt != nil && m.activePrompt.multiSelect {
		if _, ok := m.activePrompt.selected[choice.value]; ok {
			marker = "[x] "
		} else {
			marker = "[ ] "
		}
	}
	label := sanitizePromptModalText(choice.label)
	detail := sanitizePromptModalText(choice.detail)
	contentWidth := maxInt(1, m.promptModalInnerWidth()-displayColumns(gutter))
	mainText := marker + label
	if detail != "" {
		mainText += "  " + detail
	}
	mainText = truncateTailDisplay(mainText, contentWidth)
	selectedLabelStyle := m.theme.SelectionStyle().Bold(true)
	selectedDetailStyle := m.theme.SelectionStyle()
	if detail == "" {
		if selected {
			return selectedLabelStyle.Render(gutter) + selectedLabelStyle.Render(mainText)
		}
		return m.theme.HelpHintTextStyle().Render(gutter) + m.theme.TextStyle().Render(mainText)
	}
	labelWidth := maxInt(8, minInt(displayColumns(marker+label), maxInt(8, contentWidth/2)))
	if displayColumns(mainText) <= labelWidth {
		labelWidth = displayColumns(mainText)
	}
	if labelWidth >= contentWidth {
		labelWidth = maxInt(1, contentWidth-1)
	}
	labelText := truncateTailDisplay(marker+label, labelWidth)
	detailBudget := maxInt(1, contentWidth-displayColumns(labelText)-2)
	detailText := truncateTailDisplay(detail, detailBudget)
	if selected {
		return selectedLabelStyle.Render(gutter) +
			selectedLabelStyle.Render(labelText) +
			"  " +
			selectedDetailStyle.Render(detailText)
	}
	return m.theme.HelpHintTextStyle().Render(gutter) +
		m.theme.TextStyle().Render(labelText) +
		"  " +
		m.theme.HelpHintTextStyle().Render(detailText)
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
	return tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{
		Body:      filtered,
		Width:     width,
		UseBorder: hasBorder,
	})
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
	width := minInt(maxInt(44, m.fixedRowWidth()-4), 96)
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

func (m *Model) renderCompletionOverlay(_ string, lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	innerWidth := m.completionOverlayInnerWidth()

	hasBorder := m.overlayUsesBorder()
	filtered := make([]string, 0, len(lines)+2)
	if hasBorder {
		blank := strings.Repeat(" ", innerWidth)
		filtered = append(filtered, blank)
	}
	for _, line := range lines {
		if cols := displayColumns(line); cols > innerWidth {
			if innerWidth <= 3 {
				line = ansi.Truncate(line, innerWidth, "")
			} else {
				line = ansi.Truncate(line, innerWidth, "...")
			}
		}
		if pad := innerWidth - displayColumns(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		filtered = append(filtered, line)
	}
	if hasBorder {
		blank := strings.Repeat(" ", innerWidth)
		filtered = append(filtered, blank)
	}
	filtered = clampPromptModalLines(filtered, m.promptModalLineBudget(), m.theme)
	if len(filtered) == 0 {
		filtered = []string{""}
	}
	return tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{
		Body:      filtered,
		Width:     innerWidth,
		UseBorder: hasBorder,
	})
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

func (m *Model) renderInputOverlay() string {
	switch {
	case len(m.mentionCandidates) > 0:
		return m.renderMentionList()
	case len(m.skillCandidates) > 0:
		return m.renderSkillList()
	case len(m.resumeCandidates) > 0:
		return m.renderResumeList()
	case len(m.slashArgCandidates) > 0:
		return m.renderSlashArgList()
	case len(m.slashCandidates) > 0:
		return m.renderSlashCommandList()
	default:
		return ""
	}
}

func (m *Model) renderPromptInputBar() string {
	prompt := m.theme.PromptStyle().Render("> ")
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
