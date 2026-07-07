package tuikit

import (
	"regexp"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
)

// LineStyle identifies the semantic role of a log line for coloring.
type LineStyle int

const (
	LineStyleDefault      LineStyle = iota
	LineStyleAssistant              // "· " prefix
	LineStyleReasoning              // "› " prefix
	LineStyleUser                   // "▌ " prefix
	LineStyleTool                   // "▸" / "✓" / "? " prefix
	LineStyleWarn                   // "warn:" prefix
	LineStyleError                  // "error:" prefix
	LineStyleNote                   // "note:" prefix
	LineStyleKeyValue               // indented key-value pairs
	LineStyleSection                // top-level header-like text
	LineStyleDiffAdd                // "  +line" (unified diff add)
	LineStyleDiffRemove             // "  -line" (unified diff remove)
	LineStyleDiffHeader             // "  --- old" / "  +++ new"
	LineStyleDiffHunk               // "  @@ -n,m +n,m @@" (hunk header)
	LineStyleTableHeader            // table header row
	LineStyleTableDivider           // table header divider row (using e.g. ───)
)

// DetectLineStyle determines the semantic style of a log line in isolation.
func DetectLineStyle(line string) LineStyle {
	return DetectLineStyleWithContext(line, LineStyleDefault)
}

// DetectLineStyleWithContext determines semantic style considering the
// previous line's style for block continuation. When a line does not have
// its own explicit prefix, it inherits the style of the previous line if
// the previous line was a block-style (assistant, reasoning, tool).
func DetectLineStyleWithContext(line string, prevStyle LineStyle) LineStyle {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return LineStyleDefault
	}

	// Explicit prefix detection first.
	switch {
	case strings.HasPrefix(trimmed, "error:"):
		return LineStyleError
	case strings.HasPrefix(trimmed, "warn:"):
		return LineStyleWarn
	case strings.HasPrefix(trimmed, "! "):
		return LineStyleWarn
	case strings.HasPrefix(trimmed, "note:"):
		return LineStyleNote
	case strings.HasPrefix(trimmed, "· "):
		return LineStyleAssistant
	case strings.HasPrefix(trimmed, "* "):
		return LineStyleAssistant
	case strings.HasPrefix(trimmed, "• "):
		return LineStyleAssistant
	case strings.HasPrefix(trimmed, "› "):
		return LineStyleReasoning
	case strings.HasPrefix(trimmed, "│ ") || strings.HasPrefix(trimmed, "│"):
		return LineStyleReasoning
	case strings.HasPrefix(trimmed, "▌ "):
		return LineStyleUser
	case strings.HasPrefix(trimmed, "> "):
		return LineStyleUser
	case strings.HasPrefix(trimmed, "▸"):
		return LineStyleTool
	case strings.HasPrefix(trimmed, "▾"):
		return LineStyleTool
	case strings.HasPrefix(trimmed, "✓"):
		return LineStyleTool
	case strings.HasPrefix(trimmed, "✗"):
		return LineStyleTool
	case strings.HasPrefix(trimmed, "? "):
		return LineStyleTool
	}

	// Check for diff patterns based on indentation.
	leading := countLeadingSpaces(line)
	if leading >= 2 {
		rest := strings.TrimLeft(line, " \t")
		switch {
		case strings.HasPrefix(rest, "+++ ") || strings.HasPrefix(rest, "--- "):
			return LineStyleDiffHeader
		case strings.HasPrefix(rest, "@@ "):
			return LineStyleDiffHunk
		case len(rest) > 0 && rest[0] == '+':
			return LineStyleDiffAdd
		case len(rest) > 0 && rest[0] == '-':
			return LineStyleDiffRemove
		}
	}

	// Block continuation: if the previous line was a block-style content
	// (assistant, reasoning) and this line has no explicit prefix, treat it
	// as a continuation of that block.
	if isBlockContinuable(prevStyle) {
		return prevStyle
	}

	// Indented key-value
	if leading >= 2 {
		rest := line[leading:]
		keyEnd := strings.IndexAny(rest, " \t")
		if keyEnd > 0 {
			return LineStyleKeyValue
		}
	}

	// Section header: no indentation and no sentence/list structural characters.
	if leading == 0 && !strings.ContainsAny(trimmed, ":：{}[]，。；;、") {
		return LineStyleSection
	}

	return LineStyleDefault
}

// isBlockContinuable returns true for styles whose content can span
// multiple lines without repeating the prefix.
func isBlockContinuable(s LineStyle) bool {
	switch s {
	case LineStyleAssistant, LineStyleReasoning, LineStyleTool:
		return true
	default:
		return false
	}
}

// ColorizeLogLine applies lipgloss coloring to a log line based on its style.
func ColorizeLogLine(line string, style LineStyle, theme Theme) string {
	switch style {
	case LineStyleAssistant:
		return colorizeAssistantLine(line, theme)
	case LineStyleReasoning:
		return colorizeReasoningLine(line, theme)
	case LineStyleUser:
		return colorizeUserLine(line, theme)
	case LineStyleTool:
		return colorizeToolLine(line, theme)
	case LineStyleWarn:
		if strings.HasPrefix(strings.TrimSpace(line), "! ") {
			return colorizeWarnLineWithBang(line, theme)
		}
		return colorizeWarnLine(line, theme)
	case LineStyleError:
		return theme.ErrorStyle().Render(LinkifyText(line, theme.LinkStyle()))
	case LineStyleNote:
		return theme.NoteStyle().Render(LinkifyText(line, theme.LinkStyle()))
	case LineStyleKeyValue:
		return colorizeKeyValueLine(line, theme)
	case LineStyleSection:
		prefix := fgStyle(theme.Accent).Render("◆ ")
		return prefix + theme.SectionStyle().Render(LinkifyText(line, theme.LinkStyle()))
	case LineStyleDiffAdd:
		return theme.DiffAddStyle().Render(LinkifyText(line, theme.LinkStyle()))
	case LineStyleDiffRemove:
		return theme.DiffRemoveStyle().Render(LinkifyText(line, theme.LinkStyle()))
	case LineStyleDiffHeader:
		return theme.DiffHeaderStyle().Render(LinkifyText(line, theme.LinkStyle()))
	case LineStyleDiffHunk:
		return theme.DiffHunkStyle().Render(LinkifyText(line, theme.LinkStyle()))
	case LineStyleTableHeader:
		return fgStyle(theme.Accent).Bold(true).Render(LinkifyText(line, theme.LinkStyle()))
	case LineStyleTableDivider:
		return theme.SeparatorStyle().Render(line)
	default:
		return LinkifyText(line, theme.LinkStyle())
	}
}

func colorizeAssistantLine(line string, theme Theme) string {
	if strings.HasPrefix(line, "· ") {
		prefix := theme.AssistantStyle().Render("· ")
		return prefix + theme.TextStyle().Render(LinkifyText(line[len("· "):], theme.LinkStyle()))
	}
	if strings.HasPrefix(line, "* ") {
		prefix := theme.AssistantStyle().Render("* ")
		return prefix + theme.TextStyle().Render(LinkifyText(line[len("* "):], theme.LinkStyle()))
	}
	return theme.TextStyle().Render(LinkifyText(line, theme.LinkStyle()))
}

func colorizeReasoningLine(line string, theme Theme) string {
	if strings.HasPrefix(line, "› ") {
		return theme.ReasoningStyle().Render("› ") + theme.ReasoningStyle().Render(strings.TrimPrefix(line, "› "))
	}
	if strings.HasPrefix(line, "│ ") {
		return theme.ReasoningStyle().Render("│ ") + theme.ReasoningStyle().Render(strings.TrimPrefix(line, "│ "))
	}
	return theme.ReasoningStyle().Render(line)
}

func colorizeUserLine(line string, theme Theme) string {
	return theme.UserStyle().Render(line)
}

func colorizeWarnLine(line string, theme Theme) string {
	content := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "warn:"))
	if content == "" {
		return theme.WarnStyle().Render("! ")
	}
	return theme.WarnStyle().Render("! ") + theme.TextStyle().Render(LinkifyText(content, theme.LinkStyle()))
}

func styleUserMentions(text string, theme Theme) string {
	runes := []rune(text)
	if len(runes) == 0 {
		return ""
	}
	var out strings.Builder
	for i := 0; i < len(runes); {
		if runes[i] == '@' {
			j := i + 1
			for j < len(runes) && isUserMentionRune(runes[j]) {
				j++
			}
			if j > i+1 {
				out.WriteString(theme.UserMentionStyle().Render(string(runes[i:j])))
				i = j
				continue
			}
		}
		start := i
		for i < len(runes) && runes[i] != '@' {
			i++
		}
		out.WriteString(theme.UserStyle().Render(string(runes[start:i])))
	}
	return out.String()
}

func isUserMentionRune(r rune) bool {
	if unicode.IsSpace(r) {
		return false
	}
	switch r {
	case ',', '，', '。', ':', '：', ';', '；', '!', '?', '！', '？', '"', '\'', '(', ')', '[', ']', '{', '}', '<', '>', '|':
		return false
	default:
		return true
	}
}

func colorizeToolLine(line string, theme Theme) string {
	trimmed := strings.TrimSpace(line)

	if prefix, rest, ok := splitToolLifecyclePrefix(trimmed); ok {
		parts := strings.SplitN(rest, " ", 2)
		if len(parts) == 0 {
			return theme.ToolStyle().Render(line)
		}
		toolName := parts[0]
		suffix := ""
		if len(parts) == 2 {
			switch prefix {
			case "✓":
				suffix = " " + renderToolResultSuffix(toolName, parts[1], theme)
			case "✗":
				suffix = " " + theme.ToolErrorStyle().Render(LinkifyText(parts[1], theme.LinkStyle()))
			default:
				suffix = " " + renderToolCallSuffix(toolName, parts[1], theme)
			}
		}
		prefixStyle := theme.ToolStyle()
		nameStyle := theme.ToolNameStyle()
		switch prefix {
		case "✓":
			prefixStyle = theme.AssistantStyle()
		case "✗":
			prefixStyle = theme.ToolErrorStyle()
			nameStyle = theme.ToolErrorStyle()
		}
		return prefixStyle.Render(prefix+" ") + nameStyle.Render(toolName) + suffix
	}

	// Approval prompt: "? ..."
	if strings.HasPrefix(trimmed, "? ") {
		return lipgloss.NewStyle().Foreground(theme.Warning).Bold(true).Render(line)
	}

	return theme.ToolOutputStyle().Render(LinkifyText(line, theme.LinkStyle()))
}

func renderToolCallSuffix(toolName string, suffix string, theme Theme) string {
	if !strings.EqualFold(toolName, "PATCH") && !strings.EqualFold(toolName, "WRITE") {
		return theme.ToolArgsStyle().Render(LinkifyText(suffix, theme.LinkStyle()))
	}
	fields := strings.Fields(strings.TrimSpace(suffix))
	if len(fields) < 3 {
		return theme.ToolArgsStyle().Render(LinkifyText(suffix, theme.LinkStyle()))
	}
	added := fields[len(fields)-2]
	removed := fields[len(fields)-1]
	if !strings.HasPrefix(added, "+") || !strings.HasPrefix(removed, "-") {
		return theme.ToolArgsStyle().Render(LinkifyText(suffix, theme.LinkStyle()))
	}
	target := strings.TrimSpace(strings.Join(fields[:len(fields)-2], " "))
	if target == "" {
		return theme.ToolArgsStyle().Render(LinkifyText(suffix, theme.LinkStyle()))
	}
	targetText := theme.ToolArgsStyle().Render(LinkifyText(target, theme.LinkStyle()))
	addedText := lipgloss.NewStyle().Foreground(theme.DiffAddFg).Render(added)
	removedText := lipgloss.NewStyle().Foreground(theme.DiffRemoveFg).Render(removed)
	return targetText + " " + addedText + " " + removedText
}

func renderToolResultSuffix(toolName string, suffix string, theme Theme) string {
	if !strings.EqualFold(toolName, "PATCH") && !strings.EqualFold(toolName, "WRITE") {
		return renderToolMetricSuffix(suffix, theme)
	}
	fields := strings.Fields(strings.TrimSpace(suffix))
	if len(fields) != 2 || !strings.HasPrefix(fields[0], "+") || !strings.HasPrefix(fields[1], "-") {
		return theme.ToolResultStyle().Render(LinkifyText(suffix, theme.LinkStyle()))
	}
	added := lipgloss.NewStyle().Foreground(theme.DiffAddFg).Render(fields[0])
	removed := lipgloss.NewStyle().Foreground(theme.DiffRemoveFg).Render(fields[1])
	return added + " " + removed
}

func renderToolMetricSuffix(suffix string, theme Theme) string {
	fields := strings.Fields(strings.TrimSpace(suffix))
	if len(fields) == 0 {
		return ""
	}
	out := make([]string, 0, len(fields))
	low := theme.ToolResultStyle()
	high := theme.TextStyle().Bold(true)
	for _, field := range fields {
		if isMetricToken(field) {
			out = append(out, high.Render(field))
			continue
		}
		out = append(out, low.Render(LinkifyText(field, theme.LinkStyle())))
	}
	return strings.Join(out, " ")
}

func splitToolLifecyclePrefix(line string) (prefix string, rest string, ok bool) {
	for _, candidate := range []string{"▸", "▾", "✓", "✗"} {
		withSpace := candidate + " "
		if strings.HasPrefix(line, withSpace) {
			return candidate, strings.TrimSpace(strings.TrimPrefix(line, withSpace)), true
		}
	}
	return "", "", false
}

func isMetricToken(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	hasDigit := false
	for _, r := range value {
		if unicode.IsDigit(r) {
			hasDigit = true
			continue
		}
		switch r {
		case '+', '-', '~', ':', ',', '.', '/', '%':
			continue
		default:
			return false
		}
	}
	return hasDigit
}

var (
	reSuccess     = regexp.MustCompile(`\b(ok|success|seatbelt sandbox)\b`)
	reError       = regexp.MustCompile(`\b(failed|error)\b`)
	reWarning     = regexp.MustCompile(`\b(warn|warning|manual)\b`)
	reAuto        = regexp.MustCompile(`\b(auto(-[a-zA-Z0-9_-]+)?)\b`)
	reLevel       = regexp.MustCompile(`\[(high|max|pro)\]`)
	rePlaceholder = regexp.MustCompile(`(<[a-zA-Z0-9_-]+>|\[[a-zA-Z0-9_-]+\])`)
	reTokenUsage  = regexp.MustCompile(`\b(\d+(\.\d+)?[kKmMgG]?\s*/\s*\d+(\.\d+)?[kKmMgG]?)\b`)
)

func highlightStatusValue(val string, theme Theme) string {
	val = reLevel.ReplaceAllStringFunc(val, func(m string) string {
		return fgStyle(theme.Accent).Bold(true).Render(m)
	})
	val = reTokenUsage.ReplaceAllStringFunc(val, func(m string) string {
		return fgStyle(theme.Info).Render(m)
	})
	val = rePlaceholder.ReplaceAllStringFunc(val, func(m string) string {
		if strings.Contains(m, "\x1b") {
			return m
		}
		return quietStyle(theme, theme.MutedText).Render(m)
	})
	val = reSuccess.ReplaceAllStringFunc(val, func(m string) string {
		if strings.Contains(m, "\x1b") {
			return m
		}
		return fgStyle(theme.Success).Render(m)
	})
	val = reError.ReplaceAllStringFunc(val, func(m string) string {
		if strings.Contains(m, "\x1b") {
			return m
		}
		return fgStyle(theme.Error).Render(m)
	})
	val = reWarning.ReplaceAllStringFunc(val, func(m string) string {
		if strings.Contains(m, "\x1b") {
			return m
		}
		return fgStyle(theme.Warning).Render(m)
	})
	val = reAuto.ReplaceAllStringFunc(val, func(m string) string {
		if strings.Contains(m, "\x1b") {
			return m
		}
		return fgStyle(theme.Success).Render(m)
	})
	return val
}

func colorizeKeyValueLine(line string, theme Theme) string {
	rest := strings.TrimLeft(line, " \t")
	leading := len(line) - len(rest)
	keyEnd := strings.IndexAny(rest, " \t")
	if keyEnd <= 0 {
		return line
	}
	key := rest[:keyEnd]
	val := rest[keyEnd:]

	var keyStyled string
	if strings.HasPrefix(key, "/") {
		keyStyled = fgStyle(theme.Accent).Bold(true).Render(key)
	} else {
		keyStyled = theme.KeyLabelStyle().Render(key)
	}

	valStyled := highlightStatusValue(val, theme)

	return strings.Repeat(" ", leading) + keyStyled + valStyled
}

func countLeadingSpaces(s string) int {
	n := 0
	for _, ch := range s {
		switch ch {
		case ' ':
			n++
		case '\t':
			n += 4
		default:
			return n
		}
	}
	return n
}

// IsRetryLine returns true if the line looks like a retry/error log message
// (e.g., "! llm request failed, retrying in 2s (1/5): ...").
func IsRetryLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "! ") && !strings.HasPrefix(trimmed, "warn:") {
		return false
	}
	lower := strings.ToLower(trimmed)
	return strings.Contains(lower, "retry") || strings.Contains(lower, "retrying")
}

// IsLogLine returns true if the line is a system/tool log rather than
// narrative content (assistant/user/reasoning).
func IsLogLine(style LineStyle) bool {
	switch style {
	case LineStyleTool, LineStyleWarn, LineStyleError, LineStyleNote:
		return true
	default:
		return false
	}
}

// colorizeWarnLineWithBang handles lines that already start with "! ".
func colorizeWarnLineWithBang(line string, theme Theme) string {
	content := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "!"))
	if content == "" {
		return theme.WarnStyle().Render("! ")
	}
	return theme.WarnStyle().Render("! ") + theme.TextStyle().Render(LinkifyText(content, theme.LinkStyle()))
}
