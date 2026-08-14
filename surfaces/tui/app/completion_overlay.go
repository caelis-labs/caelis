package tuiapp

import (
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
	return m.renderCompletionTableLine(display, detail, identityWidth, selected)
}

func (m *Model) renderCompletionTableLines(geometry completionOverlayGeometry, rows []completionTableRow) []string {
	identityWidth := completionTableIdentityWidth(rows, m.completionRowInnerWidth())
	lines := make([]string, 0, len(rows))
	for i, row := range rows {
		lines = append(lines, m.renderCompletionTableLine(
			row.identity,
			row.hint,
			identityWidth,
			geometry.windowStart+i == geometry.selected,
		))
	}
	return lines
}

func (m *Model) renderCompletionTableLine(identity string, hint string, identityWidth int, selected bool) string {
	identity = strings.TrimSpace(identity)
	hint = strings.Join(strings.Fields(strings.TrimSpace(hint)), " ")
	innerWidth := m.completionRowInnerWidth()
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
		return m.renderCompletionSelectedTableLine(innerWidth, identityPad, hintText)
	}
	identityCore := strings.TrimRight(identityPad, " ")
	identityGap := strings.Repeat(" ", maxInt(0, displayColumns(identityPad)-displayColumns(identityCore)))
	return m.renderCompletionUnselectedLine(
		innerWidth,
		m.theme.CommandStyle().Padding(0, 0).Render(identityCore),
		identityGap,
		separator,
		m.theme.HelpHintTextStyle().Render(hintText),
	)
}

func (m *Model) renderCompletionSelectedTableLine(innerWidth int, identityPad string, hintText string) string {
	separator := "  "
	used := displayColumns(identityPad) + displayColumns(separator) + displayColumns(hintText)
	trail := ""
	if pad := innerWidth - used; pad > 0 {
		trail = strings.Repeat(" ", pad)
	}
	base := m.theme.CommandActiveStyle().Padding(0, 0).UnsetBold()
	return base.Bold(true).Render(" "+identityPad) +
		base.Render(separator+hintText+trail+" ")
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
