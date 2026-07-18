package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
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
	raw = normalizeReasoningDisplayText(raw)
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
		styledSegments, plainSegments := renderInlineMarkdownWrappedSegments(line, bodyStyle, theme, bodyWidth)
		for i := range plainSegments {
			plain := strings.Repeat(" ", prefixWidth) + plainSegments[i]
			styled := strings.Repeat(" ", prefixWidth) + styledSegments[i]
			if first {
				plain = rolePrefix + plainSegments[i]
				styled = prefixStyled + styledSegments[i]
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

// normalizeReasoningDisplayText keeps the canonical reasoning payload intact
// while presenting adjacent GPT summary spans as separate steps. A sequence
// such as **first****second** is two complete strong spans with no provider
// whitespace between them; incomplete streaming markers remain untouched.
func normalizeReasoningDisplayText(raw string) string {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lines[i] = splitAdjacentReasoningStrongSpans(line)
	}
	return strings.Join(lines, "\n")
}

func splitAdjacentReasoningStrongSpans(line string) string {
	var out strings.Builder
	for i := 0; i < len(line); {
		if line[i] == '\\' && i+1 < len(line) {
			out.WriteString(line[i : i+2])
			i += 2
			continue
		}
		if line[i] == '`' {
			if _, next, ok := parseInlineCodeSpan(line, i); ok {
				out.WriteString(line[i:next])
				i = next
				continue
			}
		}
		if strings.HasPrefix(line[i:], "**") {
			if _, next, ok := parseInlineDelimited(line, i, "**"); ok && strings.HasPrefix(line[next:], "**") {
				if _, _, adjacentOK := parseInlineDelimited(line, next, "**"); adjacentOK {
					out.WriteString(line[i:next])
					out.WriteByte('\n')
					i = next
					continue
				}
			}
		}
		out.WriteByte(line[i])
		i++
	}
	return out.String()
}

func reasoningDisplayPlainText(raw string) string {
	lines := strings.Split(normalizeReasoningDisplayText(raw), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(stripInlineMarkdown(line))
		if line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
