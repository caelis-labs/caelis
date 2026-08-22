package tuiapp

import (
	"strings"
)

func truncateTailDisplay(text string, width int) string {
	text = strings.TrimSpace(text)
	if text == "" || width <= 0 || displayColumns(text) <= width {
		return text
	}
	if width <= 3 {
		return sliceByDisplayColumns(text, 0, width)
	}
	return sliceByDisplayColumns(text, 0, width-3) + "..."
}
