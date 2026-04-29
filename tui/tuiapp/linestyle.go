package tuiapp

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

type inlineStyleState struct {
	bold   bool
	italic bool
	code   bool
	strike bool
}

func renderInlineMarkdown(text string, base lipgloss.Style, theme tuikit.Theme) string {
	if text == "" {
		return ""
	}
	if !hasInlineMarkdownMarkers(text) {
		return renderInlineText(text, inlineStyleState{}, base, theme)
	}
	return renderInlineMarkdownWithState(text, base, theme, inlineStyleState{})
}

func renderInlineMarkdownWithState(text string, base lipgloss.Style, theme tuikit.Theme, state inlineStyleState) string {
	if text == "" {
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		switch {
		case text[i] == '`':
			inner, next, ok := parseInlineDelimited(text, i, "`")
			if !ok {
				out.WriteString(renderInlineText(text[i:i+1], inlineStyleState{}, base, theme))
				i++
				continue
			}
			nextState := state
			nextState.code = true
			out.WriteString(renderInlineText(inner, nextState, base, theme))
			i = next
		case strings.HasPrefix(text[i:], "**"):
			inner, next, ok := parseInlineDelimited(text, i, "**")
			if !ok {
				out.WriteString(renderInlineText(text[i:i+1], state, base, theme))
				i++
				continue
			}
			nextState := state
			nextState.bold = true
			out.WriteString(renderInlineMarkdownWithState(inner, base, theme, nextState))
			i = next
		case strings.HasPrefix(text[i:], "__"):
			inner, next, ok := parseInlineDelimited(text, i, "__")
			if !ok {
				out.WriteString(renderInlineText(text[i:i+1], state, base, theme))
				i++
				continue
			}
			nextState := state
			nextState.bold = true
			out.WriteString(renderInlineMarkdownWithState(inner, base, theme, nextState))
			i = next
		case strings.HasPrefix(text[i:], "~~"):
			inner, next, ok := parseInlineDelimited(text, i, "~~")
			if !ok {
				out.WriteString(renderInlineText(text[i:i+1], state, base, theme))
				i++
				continue
			}
			nextState := state
			nextState.strike = true
			out.WriteString(renderInlineMarkdownWithState(inner, base, theme, nextState))
			i = next
		case text[i] == '*' && canOpenEmphasis(text, i):
			inner, next, ok := parseInlineDelimited(text, i, "*")
			if !ok {
				out.WriteString(renderInlineText(text[i:i+1], state, base, theme))
				i++
				continue
			}
			nextState := state
			nextState.italic = true
			out.WriteString(renderInlineMarkdownWithState(inner, base, theme, nextState))
			i = next
		case text[i] == '_' && canOpenEmphasis(text, i):
			inner, next, ok := parseInlineDelimited(text, i, "_")
			if !ok {
				out.WriteString(renderInlineText(text[i:i+1], state, base, theme))
				i++
				continue
			}
			nextState := state
			nextState.italic = true
			out.WriteString(renderInlineMarkdownWithState(inner, base, theme, nextState))
			i = next
		default:
			j := i + 1
			for j < len(text) && !isInlineMarkerStart(text, j) {
				j++
			}
			out.WriteString(renderInlineText(text[i:j], state, base, theme))
			i = j
		}
	}
	return out.String()
}

func hasInlineMarkdownMarkers(text string) bool {
	return strings.ContainsAny(text, "`*_~")
}

func stripInlineMarkdown(text string) string {
	if text == "" {
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(text); {
		switch {
		case text[i] == '`':
			inner, next, ok := parseInlineDelimited(text, i, "`")
			if !ok {
				out.WriteByte(text[i])
				i++
				continue
			}
			out.WriteString(inner)
			i = next
		case strings.HasPrefix(text[i:], "**"):
			inner, next, ok := parseInlineDelimited(text, i, "**")
			if !ok {
				out.WriteByte(text[i])
				i++
				continue
			}
			out.WriteString(stripInlineMarkdown(inner))
			i = next
		case strings.HasPrefix(text[i:], "__"):
			inner, next, ok := parseInlineDelimited(text, i, "__")
			if !ok {
				out.WriteByte(text[i])
				i++
				continue
			}
			out.WriteString(stripInlineMarkdown(inner))
			i = next
		case strings.HasPrefix(text[i:], "~~"):
			inner, next, ok := parseInlineDelimited(text, i, "~~")
			if !ok {
				out.WriteByte(text[i])
				i++
				continue
			}
			out.WriteString(stripInlineMarkdown(inner))
			i = next
		case text[i] == '*' && canOpenEmphasis(text, i):
			inner, next, ok := parseInlineDelimited(text, i, "*")
			if !ok {
				out.WriteByte(text[i])
				i++
				continue
			}
			out.WriteString(stripInlineMarkdown(inner))
			i = next
		case text[i] == '_' && canOpenEmphasis(text, i):
			inner, next, ok := parseInlineDelimited(text, i, "_")
			if !ok {
				out.WriteByte(text[i])
				i++
				continue
			}
			out.WriteString(stripInlineMarkdown(inner))
			i = next
		default:
			j := i + 1
			for j < len(text) && !isInlineMarkerStart(text, j) {
				j++
			}
			out.WriteString(text[i:j])
			i = j
		}
	}
	return out.String()
}

func renderInlineText(text string, state inlineStyleState, base lipgloss.Style, theme tuikit.Theme) string {
	if text == "" {
		return ""
	}
	style := base
	switch {
	case state.code:
		style = theme.MarkdownInlineCodeStyle()
	case state.bold:
		style = style.Bold(true)
	}
	if state.italic && !state.code {
		style = style.Italic(true)
	}
	if state.strike {
		style = style.Strikethrough(true)
	}
	return style.Render(tuikit.LinkifyText(text, theme.LinkStyle()))
}

func parseInlineDelimited(text string, start int, delim string) (string, int, bool) {
	if !strings.HasPrefix(text[start:], delim) {
		return "", start, false
	}
	from := start + len(delim)
	for end := from; end <= len(text)-len(delim); end++ {
		if !strings.HasPrefix(text[end:], delim) {
			continue
		}
		inner := text[from:end]
		if !validInlineDelimitedBody(inner, delim) {
			continue
		}
		return inner, end + len(delim), true
	}
	return "", start, false
}

func validInlineDelimitedBody(inner, delim string) bool {
	if inner == "" || strings.Contains(inner, "\n") {
		return false
	}
	if delim == "*" || delim == "_" {
		return strings.TrimSpace(inner) != ""
	}
	return true
}

func canOpenEmphasis(text string, idx int) bool {
	if idx+1 >= len(text) {
		return false
	}
	next := rune(text[idx+1])
	if unicode.IsSpace(next) {
		return false
	}
	if text[idx] == '_' {
		if idx > 0 && isWordByte(text[idx-1]) {
			return false
		}
		if idx+1 < len(text) && isWordByte(text[idx+1]) && idx > 0 && isWordByte(text[idx-1]) {
			return false
		}
	}
	return true
}

func isInlineMarkerStart(text string, idx int) bool {
	switch text[idx] {
	case '`', '*', '_', '~':
		return true
	default:
		return false
	}
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
