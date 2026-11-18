package tuiapp

import (
	"image/color"
	"strings"
)

func completionCandidateDisplay(candidate CompletionCandidate) string {
	display := strings.TrimSpace(candidate.Display)
	if display != "" {
		return display
	}
	return strings.TrimSpace(candidate.Value)
}

func padLineToDisplayWidth(text string, width int) string {
	if pad := width - displayColumns(text); pad > 0 {
		return text + strings.Repeat(" ", pad)
	}
	return text
}

func (m *Model) renderCompletionSelectedLine(innerWidth int, line string) string {
	return m.theme.CommandActiveStyle().Render(padLineToDisplayWidth(line, innerWidth))
}

func (m *Model) renderCompletionUnselectedLine(innerWidth int, parts ...string) string {
	return " " + padLineToDisplayWidth(strings.Join(parts, ""), innerWidth) + " "
}

func (m *Model) completionRowInnerWidth() int {
	chrome := 2
	if m.overlayUsesBorder() {
		chrome = 6
	}
	return maxInt(1, m.completionOverlayInnerWidth()-chrome)
}

type completionTableRow struct {
	identity string
	hint     string
}

func (m *Model) renderCompletionTextLine(display string, detail string, selected bool) string {
	display = strings.TrimSpace(display)
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	identityWidth := displayColumns(display)
	if detail == "" {
		identityWidth = m.completionRowInnerWidth()
	}
	return m.renderCompletionTableLine(display, detail, identityWidth, m.completionRowInnerWidth(), selected, nil)
}

func (m *Model) renderCompletionTableLines(geometry completionOverlayGeometry, rows []completionTableRow) []string {
	innerWidth := m.completionRowInnerWidth()
	indent := ""
	if len(geometry.groupedRows) > 0 {
		indent = "  "
		innerWidth -= len(indent)
	}
	activeStyle := m.theme.CommandActiveStyle().Padding(0, 0)
	var hintColor, activeHintColor color.Color
	if geometry.kind == completionSlashCommand && !m.slashSkillOnly {
		hintColor = m.theme.ReadableTextColor(m.theme.CommandSubText, m.theme.ModalBg)
		if !activeStyle.GetReverse() {
			activeHintColor = m.theme.ReadableTextColor(m.theme.CommandSubText, activeStyle.GetBackground())
		}
	}
	identityWidth := completionTableIdentityWidth(rows, innerWidth)
	lines := make([]string, 0, len(rows))
	for i, row := range rows {
		selected := geometry.windowStart+i == geometry.selected
		prefix := indent
		detailColor := hintColor
		if selected {
			if indent != "" {
				prefix = activeStyle.Render(indent)
			}
			detailColor = activeHintColor
		}
		lines = append(lines, prefix+m.renderCompletionTableLine(row.identity, row.hint, identityWidth, innerWidth, selected, detailColor))
	}
	return lines
}

func (m *Model) renderCompletionTableLine(identity, hint string, identityWidth, innerWidth int, selected bool, detailColor color.Color) string {
	identity = strings.TrimSpace(identity)
	hint = strings.Join(strings.Fields(strings.TrimSpace(hint)), " ")
	return m.renderPickerColumnsLine(identity, hint, identityWidth, innerWidth, selected, detailColor)
}

// renderCompletionColumnsLine preserves spacing in preformatted control
// columns while sharing the normal completion row styles and width budget.
func (m *Model) renderCompletionColumnsLine(identity string, hint string, identityWidth int, selected bool) string {
	return m.renderPickerColumnsLine(identity, hint, identityWidth, m.completionRowInnerWidth(), selected, nil)
}

func (m *Model) renderPickerColumnsLine(identity, hint string, identityWidth, innerWidth int, selected bool, detailColor color.Color) string {
	if hint == "" || identityWidth <= 0 {
		if selected {
			return m.renderCompletionSelectedLine(innerWidth, truncateTailDisplay(identity, innerWidth))
		}
		displayText := truncateTailDisplay(identity, innerWidth)
		return m.renderCompletionUnselectedLine(innerWidth, m.theme.CommandStyle().Padding(0, 0).Render(displayText))
	}

	identityWidth = minInt(identityWidth, innerWidth)
	identityText := truncateTailDisplay(identity, identityWidth)
	identityPad := padRightDisplay(identityText, identityWidth)
	separator := "  "
	hintBudget := maxInt(0, innerWidth-displayColumns(identityPad)-displayColumns(separator))
	hintText := ""
	if hintBudget > 0 {
		hintText = truncateTailDisplay(hint, hintBudget)
	}
	if hintText == "" {
		if selected {
			return m.renderCompletionSelectedLine(innerWidth, truncateTailDisplay(identity, innerWidth))
		}
		return m.renderCompletionUnselectedLine(innerWidth, m.theme.CommandStyle().Padding(0, 0).Render(truncateTailDisplay(identity, innerWidth)))
	}

	if selected {
		return m.renderCompletionSelectedTableLine(innerWidth, identityPad, hintText, detailColor)
	}
	detailStyle := m.theme.HelpHintTextStyle()
	if detailColor != nil {
		detailStyle = detailStyle.Foreground(detailColor)
	}
	identityCore := strings.TrimRight(identityPad, " ")
	identityGap := strings.Repeat(" ", maxInt(0, displayColumns(identityPad)-displayColumns(identityCore)))
	return m.renderCompletionUnselectedLine(
		innerWidth,
		m.theme.CommandStyle().Padding(0, 0).Render(identityCore),
		identityGap,
		separator,
		detailStyle.Render(hintText),
	)
}

func (m *Model) renderCompletionSelectedTableLine(innerWidth int, identityPad string, hintText string, detailColor color.Color) string {
	separator := "  "
	used := displayColumns(identityPad) + displayColumns(separator) + displayColumns(hintText)
	trail := ""
	if pad := innerWidth - used; pad > 0 {
		trail = strings.Repeat(" ", pad)
	}
	base := m.theme.CommandActiveStyle().Padding(0, 0).UnsetBold()
	detailStyle := base
	if detailColor != nil {
		detailStyle = detailStyle.Foreground(detailColor)
	}
	return base.Bold(true).Render(" "+identityPad) +
		detailStyle.Render(separator+hintText+trail+" ")
}

func completionTableIdentityWidth(rows []completionTableRow, innerWidth int) int {
	if innerWidth <= 0 {
		return 1
	}
	maxIdentity := 0
	hasHint := false
	for _, row := range rows {
		maxIdentity = maxInt(maxIdentity, displayColumns(strings.TrimSpace(row.identity)))
		if strings.TrimSpace(row.hint) != "" {
			hasHint = true
		}
	}
	if !hasHint || maxIdentity <= 0 {
		return innerWidth
	}
	separator := 2
	minHint := minInt(16, maxInt(8, innerWidth/5))
	maxAllowed := maxInt(8, innerWidth-separator-minHint)
	return minInt(maxIdentity, maxAllowed)
}
