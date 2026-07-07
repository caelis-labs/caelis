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

type completionLineLayout int

const (
	completionLayoutNameColumn completionLineLayout = iota
	completionLayoutValueReserve
)

func (m *Model) renderCompletionTextLine(display string, detail string, selected bool) string {
	return m.renderCompletionLine(display, detail, selected, completionLayoutNameColumn)
}

func (m *Model) renderCompletionValueLine(display string, detail string, selected bool) string {
	return m.renderCompletionLine(display, detail, selected, completionLayoutValueReserve)
}

func (m *Model) renderCompletionLine(display string, detail string, selected bool, layout completionLineLayout) string {
	display = strings.TrimSpace(display)
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	innerWidth := m.completionRowInnerWidth()

	switch layout {
	case completionLayoutNameColumn:
		nameBudget := completionNameColumnWidth(innerWidth)
		if detail == "" {
			nameBudget = innerWidth
		}
		primary := truncateTailDisplay(display, nameBudget)
		primaryPad := ""
		if pad := nameBudget - displayColumns(primary); pad > 0 {
			primaryPad = strings.Repeat(" ", pad)
		}
		if selected {
			line := primary + primaryPad
			if detail != "" {
				separator := "  "
				line += separator
				detailBudget := maxInt(0, innerWidth-nameBudget-displayColumns(separator))
				if detailBudget > 0 {
					line += truncateTailDisplay(detail, detailBudget)
				}
			}
			return m.renderCompletionSelectedLine(innerWidth, line)
		}
		primaryPart := m.theme.CommandStyle().Padding(0, 0).Render(primary)
		if detail == "" {
			return m.renderCompletionUnselectedLine(innerWidth, primaryPart, primaryPad)
		}
		separator := "  "
		detailBudget := maxInt(0, innerWidth-nameBudget-displayColumns(separator))
		detailText := truncateTailDisplay(detail, detailBudget)
		detailPart := m.theme.HelpHintTextStyle().Render(detailText)
		return m.renderCompletionUnselectedLine(innerWidth, primaryPart, primaryPad, separator, detailPart)

	case completionLayoutValueReserve:
		if selected {
			displayText := truncateTailDisplay(display, innerWidth)
			line := displayText
			if detail != "" {
				separator := "  "
				separatorWidth := displayColumns(separator)
				detailReserve := minInt(completionValueDetailReserve(innerWidth), displayColumns(detail))
				displayBudget := maxInt(1, innerWidth-separatorWidth-detailReserve)
				displayText = truncateTailDisplay(display, displayBudget)
				detailBudget := maxInt(0, innerWidth-displayColumns(displayText)-separatorWidth)

				line = displayText
				if detailBudget > 0 {
					line += separator + truncateTailDisplay(detail, detailBudget)
				}
			}
			return m.renderCompletionSelectedLine(innerWidth, line)
		}
		if detail == "" {
			displayText := truncateTailDisplay(display, innerWidth)
			primaryPart := m.theme.CommandStyle().Padding(0, 0).Render(displayText)
			return m.renderCompletionUnselectedLine(innerWidth, primaryPart)
		}
		separator := "  "
		separatorWidth := displayColumns(separator)
		detailReserve := minInt(completionValueDetailReserve(innerWidth), displayColumns(detail))
		displayBudget := maxInt(1, innerWidth-separatorWidth-detailReserve)
		displayText := truncateTailDisplay(display, displayBudget)
		detailBudget := maxInt(0, innerWidth-displayColumns(displayText)-separatorWidth)

		primaryPart := m.theme.CommandStyle().Padding(0, 0).Render(displayText)
		if detailBudget <= 0 {
			return m.renderCompletionUnselectedLine(innerWidth, primaryPart)
		}
		detailText := truncateTailDisplay(detail, detailBudget)
		detailPart := m.theme.HelpHintTextStyle().Render(detailText)
		return m.renderCompletionUnselectedLine(innerWidth, primaryPart, separator, detailPart)
	default:
		return ""
	}
}

func completionValueDetailReserve(width int) int {
	switch {
	case width >= 120:
		return 32
	case width >= 88:
		return 24
	case width >= 56:
		return 18
	default:
		return maxInt(8, width/3)
	}
}

func completionNameColumnWidth(width int) int {
	switch {
	case width >= 120:
		return 22
	case width >= 88:
		return 18
	default:
		return minInt(16, maxInt(10, width/3))
	}
}

func completionCandidateKind(candidate CompletionCandidate) string {
	return strings.TrimSpace(candidate.Kind)
}

func completionCandidateDetail(candidate CompletionCandidate) string {
	return strings.TrimSpace(candidate.Detail)
}
