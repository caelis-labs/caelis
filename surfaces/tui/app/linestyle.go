package tuiapp

import (
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

type inlineStyleState struct {
	bold   bool
	italic bool
	code   bool
	strike bool
}

type inlineSpan struct {
	Text  string
	State inlineStyleState
}

func renderInlineMarkdown(text string, base lipgloss.Style, theme tuikit.Theme) string {
	if text == "" {
		return ""
	}
	if !hasInlineMarkdownMarkers(text) {
		return renderInlineText(text, inlineStyleState{}, base, theme)
	}
	return renderInlineSpans(parseInlineMarkdownSpans(text), base, theme)
}

func renderInlineSpans(spans []inlineSpan, base lipgloss.Style, theme tuikit.Theme) string {
	var out strings.Builder
	for _, span := range spans {
		out.WriteString(renderInlineText(span.Text, span.State, base, theme))
	}
	return out.String()
}

func parseInlineMarkdownSpans(text string) []inlineSpan {
	return parseInlineMarkdownSpansWithState(text, inlineStyleState{})
}

func parseInlineMarkdownSpansWithState(text string, state inlineStyleState) []inlineSpan {
	if text == "" {
		return nil
	}
	spans := make([]inlineSpan, 0, 4)
	var literal strings.Builder
	flushLiteral := func() {
		if literal.Len() == 0 {
			return
		}
		spans = appendInlineSpan(spans, literal.String(), state)
		literal.Reset()
	}
	for i := 0; i < len(text); {
		switch {
		case text[i] == '\\':
			if i+1 < len(text) && isEscapableInlineChar(text[i+1]) && !isLikelyPathSeparatorEscape(text, i) {
				literal.WriteByte(text[i+1])
				i += 2
				continue
			}
			literal.WriteByte(text[i])
			i++
		case text[i] == '`':
			inner, next, ok := parseInlineCodeSpan(text, i)
			if !ok {
				literal.WriteByte(text[i])
				i++
				continue
			}
			flushLiteral()
			nextState := state
			nextState.code = true
			spans = appendInlineSpan(spans, inner, nextState)
			i = next
		case strings.HasPrefix(text[i:], "**"):
			inner, next, ok := parseInlineDelimited(text, i, "**")
			if !ok {
				literal.WriteString("**")
				i += 2
				continue
			}
			flushLiteral()
			nextState := state
			nextState.bold = true
			spans = append(spans, parseInlineMarkdownSpansWithState(inner, nextState)...)
			i = next
		case strings.HasPrefix(text[i:], "__"):
			inner, next, ok := parseInlineDelimited(text, i, "__")
			if !ok {
				literal.WriteString("__")
				i += 2
				continue
			}
			flushLiteral()
			nextState := state
			nextState.bold = true
			spans = append(spans, parseInlineMarkdownSpansWithState(inner, nextState)...)
			i = next
		case strings.HasPrefix(text[i:], "~~"):
			inner, next, ok := parseInlineDelimited(text, i, "~~")
			if !ok {
				literal.WriteString("~~")
				i += 2
				continue
			}
			flushLiteral()
			nextState := state
			nextState.strike = true
			spans = append(spans, parseInlineMarkdownSpansWithState(inner, nextState)...)
			i = next
		case text[i] == '*' && canOpenEmphasis(text, i):
			inner, next, ok := parseInlineDelimited(text, i, "*")
			if !ok {
				literal.WriteByte(text[i])
				i++
				continue
			}
			flushLiteral()
			nextState := state
			nextState.italic = true
			spans = append(spans, parseInlineMarkdownSpansWithState(inner, nextState)...)
			i = next
		case text[i] == '_' && canOpenEmphasis(text, i):
			inner, next, ok := parseInlineDelimited(text, i, "_")
			if !ok {
				literal.WriteByte(text[i])
				i++
				continue
			}
			flushLiteral()
			nextState := state
			nextState.italic = true
			spans = append(spans, parseInlineMarkdownSpansWithState(inner, nextState)...)
			i = next
		default:
			j := i + 1
			for j < len(text) && !isInlineMarkerStart(text, j) {
				j++
			}
			literal.WriteString(text[i:j])
			i = j
		}
	}
	flushLiteral()
	return spans
}

func hasInlineMarkdownMarkers(text string) bool {
	return strings.ContainsAny(text, "\\`*_~")
}

func stripInlineMarkdown(text string) string {
	if text == "" {
		return ""
	}
	return inlineSpansPlain(parseInlineMarkdownSpans(text))
}

func inlineSpansPlain(spans []inlineSpan) string {
	var out strings.Builder
	for _, span := range spans {
		out.WriteString(span.Text)
	}
	return out.String()
}

func renderInlineMarkdownWrappedSegments(text string, base lipgloss.Style, theme tuikit.Theme, width int) ([]string, []string) {
	if width <= 0 {
		width = 1
	}
	if text == "" {
		return []string{""}, []string{""}
	}
	spans := parseInlineMarkdownSpans(text)
	if len(spans) == 0 {
		return []string{""}, []string{""}
	}
	return wrapInlineSpans(spans, base, theme, width)
}

func wrapInlineSpans(spans []inlineSpan, base lipgloss.Style, theme tuikit.Theme, width int) ([]string, []string) {
	if width <= 0 {
		width = 1
	}
	styledSegments := make([]string, 0, 2)
	plainSegments := make([]string, 0, 2)
	lineSpans := make([]inlineSpan, 0, len(spans))
	lineWidth := 0
	flushLine := func() {
		if len(lineSpans) == 0 {
			styledSegments = append(styledSegments, "")
			plainSegments = append(plainSegments, "")
			lineWidth = 0
			return
		}
		styledSegments = append(styledSegments, renderInlineSpans(lineSpans, base, theme))
		plainSegments = append(plainSegments, inlineSpansPlain(lineSpans))
		lineSpans = lineSpans[:0]
		lineWidth = 0
	}
	for _, span := range spans {
		for _, cluster := range splitGraphemeClusters(span.Text) {
			clusterWidth := graphemeWidth(cluster)
			if clusterWidth < 0 {
				clusterWidth = 0
			}
			if clusterWidth > 0 && strings.TrimSpace(cluster) == "" {
				if lineWidth == 0 {
					continue
				}
				if lineWidth+clusterWidth > width {
					flushLine()
					continue
				}
			}
			if lineWidth > 0 && clusterWidth > 0 && lineWidth+clusterWidth > width {
				flushLine()
			}
			lineSpans = appendInlineSpan(lineSpans, cluster, span.State)
			lineWidth += clusterWidth
		}
	}
	if len(lineSpans) > 0 || len(styledSegments) == 0 {
		flushLine()
	}
	return styledSegments, plainSegments
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
	for searchFrom := from; searchFrom <= len(text)-len(delim); searchFrom++ {
		end := findInlineDelimiterEnd(text, searchFrom, delim)
		if end < 0 {
			return "", start, false
		}
		inner := text[from:end]
		if validInlineDelimitedBody(inner, delim) {
			return inner, end + len(delim), true
		}
		searchFrom = end
	}
	return "", start, false
}

func parseInlineCodeSpan(text string, start int) (string, int, bool) {
	if start < 0 || start >= len(text) || text[start] != '`' {
		return "", start, false
	}
	run := countInlineMarkerRun(text, start, '`')
	from := start + run
	for i := from; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		found := countInlineMarkerRun(text, i, '`')
		if found == run {
			inner := text[from:i]
			if !validInlineDelimitedBody(inner, "`") {
				return "", start, false
			}
			return inner, i + found, true
		}
		i += found
	}
	return "", start, false
}

func findInlineDelimiterEnd(text string, from int, delim string) int {
	if delim == "" {
		return -1
	}
	for end := from; end <= len(text)-len(delim); end++ {
		if text[end] == '\\' && end+1 < len(text) && isEscapableInlineChar(text[end+1]) {
			end++
			continue
		}
		if !strings.HasPrefix(text[end:], delim) {
			continue
		}
		if len(delim) == 1 {
			if end > 0 && text[end-1] == delim[0] {
				continue
			}
			if end+1 < len(text) && text[end+1] == delim[0] {
				continue
			}
		}
		return end
	}
	return -1
}

func countInlineMarkerRun(text string, start int, marker byte) int {
	i := start
	for i < len(text) && text[i] == marker {
		i++
	}
	return i - start
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
	case '\\', '`', '*', '_', '~':
		return true
	default:
		return false
	}
}

func isEscapableInlineChar(ch byte) bool {
	switch ch {
	case '\\', '`', '*', '_', '~', '[', ']', '(', ')', '#', '+', '-', '.', '!', '|':
		return true
	default:
		return false
	}
}

func isLikelyPathSeparatorEscape(text string, idx int) bool {
	if idx <= 0 || idx+1 >= len(text) {
		return false
	}
	next := text[idx+1]
	if !isEscapableInlineChar(next) {
		return false
	}
	tokenStart := strings.LastIndexAny(text[:idx], " \t\r\n\"'") + 1
	prefix := text[tokenStart:idx]
	if hasLikelyPathSeparatorPrefix(prefix) {
		return true
	}
	return next == '*' && idx+2 < len(text) && strings.ContainsAny(text[idx+2:idx+3], `./\`)
}

func hasLikelyPathSeparatorPrefix(prefix string) bool {
	if strings.ContainsAny(prefix, `:/`) {
		return true
	}
	for i := 0; i < len(prefix); i++ {
		if prefix[i] != '\\' {
			continue
		}
		prevPath := i > 0 && isPathSegmentByte(prefix[i-1])
		nextPath := i+1 < len(prefix) && isPathSegmentByte(prefix[i+1])
		if prevPath || nextPath {
			return true
		}
	}
	return false
}

func isPathSegmentByte(ch byte) bool {
	return isWordByte(ch) || strings.ContainsRune(".-_$[]#@", rune(ch))
}

func appendInlineSpan(spans []inlineSpan, text string, state inlineStyleState) []inlineSpan {
	if text == "" {
		return spans
	}
	if len(spans) > 0 && sameInlineStyleState(spans[len(spans)-1].State, state) {
		spans[len(spans)-1].Text += text
		return spans
	}
	return append(spans, inlineSpan{Text: text, State: state})
}

func sameInlineStyleState(a, b inlineStyleState) bool {
	return a.bold == b.bold &&
		a.italic == b.italic &&
		a.code == b.code &&
		a.strike == b.strike
}

func isWordByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
