package tuikit

import (
	"regexp"

	"charm.land/lipgloss/v2"
)

var (
	osc8ResidueOpen  = regexp.MustCompile(`(?:\x1b)?]8;;[^\x07]*\x07`)
	osc8ResidueClose = regexp.MustCompile(`(?:\x1b)?]8;;\x07`)
)

func LinkifyText(text string, style lipgloss.Style) string {
	_ = style
	return stripBrokenOSC8(text)
}

func stripBrokenOSC8(text string) string {
	if text == "" {
		return ""
	}
	text = osc8ResidueOpen.ReplaceAllString(text, "")
	text = osc8ResidueClose.ReplaceAllString(text, "")
	return text
}
