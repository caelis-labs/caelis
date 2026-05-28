package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
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
	rows := make([]RenderedRow, 0, strings.Count(raw, "\n")+3)
	rows = append(rows, userSurfaceRow(blockID, userRailOnlyPrefix(rolePrefix), "", width, theme))
	for _, line := range strings.Split(raw, "\n") {
		segments := strings.Split(hardWrapDisplayLine(line, bodyWidth), "\n")
		if len(segments) == 0 {
			segments = []string{line}
		}
		for _, segment := range segments {
			rows = append(rows, userSurfaceRow(blockID, rolePrefix, segment, width, theme))
		}
	}
	rows = append(rows, userSurfaceRow(blockID, userRailOnlyPrefix(rolePrefix), "", width, theme))
	return rows
}

func userRailOnlyPrefix(rolePrefix string) string {
	if trimmed := strings.TrimRight(rolePrefix, " "); trimmed != "" {
		return trimmed
	}
	return rolePrefix
}

func userSurfaceRow(blockID, prefix, segment string, width int, theme tuikit.Theme) RenderedRow {
	if width <= 0 {
		width = 1
	}
	prefixWidth := displayColumns(prefix)
	bodyWidth := maxInt(1, width-prefixWidth)
	plain := prefix + segment
	styled := theme.UserPrefixStyle().Background(theme.UserBg).Render(prefix) +
		theme.UserStyle().Width(bodyWidth).Render(segment)
	return RenderedRow{
		Styled:     styled,
		Plain:      plain,
		BlockID:    blockID,
		PreWrapped: true,
	}
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
