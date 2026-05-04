package core

import "strings"

// CleanSubagentFinalOutput normalizes markdown-heavy ACP final text into a
// dense terminal-panel summary. It intentionally stays small and lossy: the
// child transcript remains the source of rich formatting.
func CleanSubagentFinalOutput(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	lines := make([]string, 0, len(strings.Split(text, "\n")))
	for _, raw := range strings.Split(text, "\n") {
		line, ok := cleanSubagentFinalLine(raw)
		if !ok || line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func cleanSubagentFinalLine(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", true
	}
	if isSubagentMarkdownRuleLine(line) {
		return "", true
	}
	if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
		return "", false
	}
	if table, ok := cleanSubagentMarkdownTableLine(line); ok {
		return table, table != ""
	}
	for _, prefix := range []string{"# ", "## ", "### ", "#### ", "##### ", "###### ", "> "} {
		if strings.HasPrefix(line, prefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			break
		}
	}
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			line = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			break
		}
	}
	if idx := strings.Index(line, ". "); idx > 0 {
		numbered := true
		for _, r := range line[:idx] {
			if r < '0' || r > '9' {
				numbered = false
				break
			}
		}
		if numbered {
			line = strings.TrimSpace(line[idx+2:])
		}
	}
	for _, marker := range []string{"**", "__", "`"} {
		line = strings.ReplaceAll(line, marker, "")
	}
	return strings.TrimSpace(line), true
}

func isSubagentMarkdownRuleLine(line string) bool {
	line = strings.TrimSpace(line)
	if len(line) < 3 {
		return false
	}
	for _, marker := range []rune{'-', '*', '_'} {
		all := true
		for _, r := range line {
			if r != marker && r != ' ' && r != '\t' {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

func cleanSubagentMarkdownTableLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
		return "", false
	}
	cells := strings.Split(strings.Trim(trimmed, "|"), "|")
	cleaned := make([]string, 0, len(cells))
	separator := true
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			continue
		}
		onlyRuleChars := true
		for _, r := range cell {
			if r != '-' && r != ':' && r != ' ' && r != '\t' {
				onlyRuleChars = false
				break
			}
		}
		if !onlyRuleChars {
			separator = false
		}
		cell = strings.Trim(cell, "-: ")
		for _, marker := range []string{"**", "__", "`"} {
			cell = strings.ReplaceAll(cell, marker, "")
		}
		if cell != "" {
			cleaned = append(cleaned, cell)
		}
	}
	if separator || len(cleaned) == 0 {
		return "", true
	}
	return strings.Join(cleaned, "  "), true
}
