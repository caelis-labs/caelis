package tuiapp

import "strings"

const (
	toolArgsPreviewWidth       = 96
	compactSingleLineMinBudget = 16
	compactSingleLineChrome    = 12
	// Keep compact activity readable on very wide terminals while allowing it
	// to use substantially more of ordinary viewports than the old fixed cap.
	compactSingleLineHardCap = 132
)

func compactSingleLineBudget(width int) int {
	return minInt(compactSingleLineHardCap, maxInt(compactSingleLineMinBudget, width-compactSingleLineChrome))
}

func compactSingleLineHeader(text string, width int) string {
	return truncateDisplayPreviewMiddle(text, maxInt(1, width))
}

func truncateDisplayPreviewMiddle(text string, budget int) string {
	text = strings.TrimSpace(text)
	if budget <= 0 || displayColumns(text) <= budget {
		return text
	}
	if budget <= 3 {
		return truncateDisplayCells(text, budget)
	}
	leftBudget := maxInt(1, (budget-3+1)/2)
	rightBudget := maxInt(1, budget-3-leftBudget)
	left := truncateDisplayCells(text, leftBudget)
	right := truncateDisplayCellsFromEnd(text, rightBudget)
	return strings.TrimSpace(left) + "..." + strings.TrimSpace(right)
}

func truncateDisplayCells(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		w := displayColumns(string(r))
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String()
}

func truncateDisplayCellsFromEnd(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	runes := []rune(text)
	used := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		w := displayColumns(string(runes[i]))
		if used+w > budget {
			break
		}
		used += w
		start = i
	}
	return string(runes[start:])
}
