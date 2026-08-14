package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// userNarrativePrefix matches the composer prompt so submitted messages read
// as the same compose surface instead of a separate chat-bubble rail.
const userNarrativePrefix = "> "

func renderPlainUserRows(blockID, raw, rolePrefix string, width int, theme tuikit.Theme) []RenderedRow {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if width <= 0 {
		width = 1
	}
	chrome := userNarrativeChrome(theme)
	prompt := normalizeUserNarrativePrefix(rolePrefix)
	pad := userNarrativeOuterPad()
	markerWidth := displayColumns(pad) + displayColumns(prompt)
	bodyWidth := maxInt(1, width-markerWidth)
	rows := make([]RenderedRow, 0, strings.Count(raw, "\n")+3)
	if chrome {
		rows = append(rows, userSurfacePadRow(blockID, width, theme))
	}
	first := true
	for _, line := range strings.Split(raw, "\n") {
		segments := strings.Split(hardWrapDisplayLine(line, bodyWidth), "\n")
		if len(segments) == 0 {
			segments = []string{line}
		}
		for _, segment := range segments {
			marker := prompt
			if !first {
				marker = strings.Repeat(" ", displayColumns(prompt))
			}
			first = false
			rows = append(rows, userSurfaceRow(blockID, pad, marker, segment, width, theme, markerWidth))
		}
	}
	if chrome {
		rows = append(rows, userSurfacePadRow(blockID, width, theme))
	}
	return rows
}

func normalizeUserNarrativePrefix(rolePrefix string) string {
	switch strings.TrimSpace(rolePrefix) {
	case "", ">", "▌":
		return userNarrativePrefix
	default:
		if strings.HasSuffix(rolePrefix, " ") {
			return rolePrefix
		}
		return rolePrefix + " "
	}
}

func userNarrativeOuterPad() string {
	// One column so ">" lands on InputInset after the transcript gutter.
	return " "
}

func userNarrativeChrome(theme tuikit.Theme) bool {
	return theme.ComposerBg != nil && !theme.NoColor
}

func userSurfacePadRow(blockID string, width int, theme tuikit.Theme) RenderedRow {
	if width <= 0 {
		width = 1
	}
	return RenderedRow{
		Styled:          userSurfaceFill(theme, width),
		Plain:           "",
		BlockID:         blockID,
		PreWrapped:      true,
		selectionIndent: displayColumns(userNarrativeOuterPad()) + displayColumns(userNarrativePrefix),
	}
}

func userSurfaceRow(blockID, pad, marker, segment string, width int, theme tuikit.Theme, indent int) RenderedRow {
	if width <= 0 {
		width = 1
	}
	prefix := pad + marker
	bodyWidth := maxInt(1, width-displayColumns(prefix))
	plain := prefix + segment
	return RenderedRow{
		Styled:          styleUserSurfaceRow(theme, pad, marker, segment, bodyWidth),
		Plain:           plain,
		BlockID:         blockID,
		PreWrapped:      true,
		selectionIndent: indent,
	}
}

func styleUserSurfaceRow(theme tuikit.Theme, pad, marker, segment string, bodyWidth int) string {
	if !userNarrativeChrome(theme) {
		return pad + marker + segment
	}
	bg := theme.ComposerBg
	padStyle := lipgloss.NewStyle().Background(bg)
	markerStyle := padStyle
	if strings.TrimSpace(marker) != "" {
		markerStyle = theme.PromptStyle().Background(bg)
	}
	textStyle := theme.TextStyle().Background(bg).Width(bodyWidth)
	return padStyle.Render(pad) + markerStyle.Render(marker) + textStyle.Render(segment)
}

func userSurfaceFill(theme tuikit.Theme, width int) string {
	if !userNarrativeChrome(theme) {
		return ""
	}
	return lipgloss.NewStyle().Background(theme.ComposerBg).Render(strings.Repeat(" ", width))
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
