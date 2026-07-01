package tuiapp

import "strings"

func completionCandidateDisplay(candidate CompletionCandidate) string {
	display := strings.TrimSpace(candidate.Display)
	if display != "" {
		return display
	}
	return strings.TrimSpace(candidate.Value)
}

func (m *Model) renderCompletionTextLine(display string, detail string, selected bool) string {
	display = strings.TrimSpace(display)
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	style := m.theme.CommandStyle()
	if selected {
		style = m.theme.CommandActiveStyle()
	}
	width := m.completionOverlayInnerWidth()
	if detail == "" {
		return style.Render(truncateTailDisplay(display, width))
	}
	nameColumn := completionNameColumnWidth(width)
	name := truncateTailDisplay(display, nameColumn)
	line := style.Render(name)
	if pad := nameColumn - displayColumns(name); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	separator := "  "
	detailBudget := maxInt(0, width-nameColumn-displayColumns(separator))
	if detailBudget <= 0 {
		return line
	}
	line += separator + m.theme.HelpHintTextStyle().Render(truncateTailDisplay(detail, detailBudget))
	return line
}

func (m *Model) renderCompletionValueLine(display string, detail string, selected bool) string {
	display = strings.TrimSpace(display)
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	style := m.theme.CommandStyle()
	if selected {
		style = m.theme.CommandActiveStyle()
	}
	width := m.completionOverlayInnerWidth()
	if detail == "" {
		return style.Render(truncateTailDisplay(display, width))
	}
	separator := "  "
	separatorWidth := displayColumns(separator)
	detailReserve := minInt(completionValueDetailReserve(width), displayColumns(detail))
	displayBudget := maxInt(1, width-separatorWidth-detailReserve)
	displayText := truncateTailDisplay(display, displayBudget)
	detailBudget := maxInt(0, width-displayColumns(displayText)-separatorWidth)
	line := style.Render(displayText)
	if detailBudget <= 0 {
		return line
	}
	line += separator + m.theme.HelpHintTextStyle().Render(truncateTailDisplay(detail, detailBudget))
	return line
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
