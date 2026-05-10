package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

func renderPlainUserRows(blockID, raw, rolePrefix string, width int, theme tuikit.Theme) []RenderedRow {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if width <= 0 {
		width = 1
	}
	prefixWidth := displayColumns(rolePrefix)
	bodyWidth := maxInt(1, width-prefixWidth)
	style := theme.UserStyle().Width(width)
	rows := make([]RenderedRow, 0, strings.Count(raw, "\n")+1)
	first := true
	for _, line := range strings.Split(raw, "\n") {
		segments := strings.Split(hardWrapDisplayLine(line, bodyWidth), "\n")
		if len(segments) == 0 {
			segments = []string{line}
		}
		for _, segment := range segments {
			prefix := strings.Repeat(" ", prefixWidth)
			if first {
				prefix = rolePrefix
				first = false
			}
			plain := prefix + segment
			rows = append(rows, RenderedRow{
				Styled:     style.Render(plain),
				Plain:      plain,
				BlockID:    blockID,
				PreWrapped: true,
			})
		}
	}
	return rows
}

func renderPlainReasoningRows(blockID, raw, rolePrefix string, width int, theme tuikit.Theme) []RenderedRow {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if width <= 0 {
		width = 1
	}
	prefixWidth := displayColumns(rolePrefix)
	bodyWidth := maxInt(1, width-prefixWidth)
	prefixStyled := ""
	if rolePrefix != "" {
		prefixStyled = tuikit.ColorizeLogLine(rolePrefix, tuikit.LineStyleReasoning, theme)
	}
	bodyStyle := theme.ReasoningStyle()
	rows := make([]RenderedRow, 0, strings.Count(raw, "\n")+1)
	first := true
	for _, line := range strings.Split(raw, "\n") {
		segments := strings.Split(hardWrapDisplayLine(line, bodyWidth), "\n")
		if len(segments) == 0 {
			segments = []string{line}
		}
		for _, segment := range segments {
			plain := segment
			styled := bodyStyle.Render(segment)
			if first {
				plain = rolePrefix + segment
				styled = prefixStyled + bodyStyle.Render(segment)
				first = false
			}
			rows = append(rows, RenderedRow{
				Styled:     styled,
				Plain:      plain,
				BlockID:    blockID,
				PreWrapped: true,
			})
		}
	}
	return rows
}
