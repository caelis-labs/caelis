package tuiapp

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	blockMathPattern        = regexp.MustCompile(`(?ms)(^|\n)\$\$\s*\n?(.*?)\n?\s*\$\$`)
	inlineMathPattern       = regexp.MustCompile(`(^|[^\\$])\$([^\n$]+?)\$`)
	gluedRuleHeadingPattern = regexp.MustCompile(`(?m)(^|\n)([-*_]{3,})(#{1,6}\s)`)
	tableRowBoundaryPattern = regexp.MustCompile(`\|\s+\|`)
)

func normalizeTerminalMarkdown(input string) string {
	if input == "" {
		return ""
	}
	input = normalizeGluedMarkdownBlocks(input)
	output := blockMathPattern.ReplaceAllStringFunc(input, func(match string) string {
		sub := blockMathPattern.FindStringSubmatch(match)
		if len(sub) != 3 {
			return match
		}
		prefix := sub[1]
		body := strings.TrimSpace(sub[2])
		if body == "" {
			return match
		}
		return prefix + body
	})
	return replaceInlineMath(output)
}

func normalizeGluedMarkdownBlocks(input string) string {
	input = gluedRuleHeadingPattern.ReplaceAllString(input, "$1$2\n$3")
	return normalizeInlineMarkdownTables(input)
}

func normalizeInlineMarkdownTables(input string) string {
	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		expanded := expandInlineMarkdownTableLine(line)
		out = append(out, expanded...)
	}
	return strings.Join(out, "\n")
}

func expandInlineMarkdownTableLine(line string) []string {
	if strings.Count(line, "|") < 4 {
		return []string{line}
	}
	expanded := tableRowBoundaryPattern.ReplaceAllString(line, "|\n|")
	parts := strings.Split(expanded, "\n")
	if !containsMarkdownTableSeparator(parts) {
		return []string{line}
	}
	out := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		out = append(out, splitInlineTablePrefix(part)...)
	}
	return out
}

func containsMarkdownTableSeparator(lines []string) bool {
	for _, line := range lines {
		if isTableSeparatorLine(strings.TrimSpace(line)) {
			return true
		}
	}
	return false
}

func splitInlineTablePrefix(line string) []string {
	firstPipe := strings.Index(line, "|")
	if firstPipe <= 0 {
		return []string{line}
	}
	prefix := strings.TrimSpace(line[:firstPipe])
	table := strings.TrimSpace(line[firstPipe:])
	if prefix == "" || !isPotentialTableRow(table) {
		return []string{line}
	}
	return []string{strings.TrimRight(line[:firstPipe], " \t"), table}
}

func replaceInlineMath(text string) string {
	indexes := inlineMathPattern.FindAllStringSubmatchIndex(text, -1)
	if len(indexes) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, idx := range indexes {
		if len(idx) < 6 {
			continue
		}
		body := text[idx[4]:idx[5]]
		if !isInlineMathBody(body) {
			continue
		}
		b.WriteString(text[last:idx[0]])
		b.WriteString(text[idx[2]:idx[3]])
		b.WriteString(body)
		last = idx[1]
	}
	if last == 0 {
		return text
	}
	b.WriteString(text[last:])
	return b.String()
}

func isInlineMathBody(body string) bool {
	body = strings.TrimSpace(body)
	if body == "" {
		return false
	}
	if strings.ContainsAny(body, "\\^_={}()+-*/<>[]") {
		return true
	}
	if strings.ContainsAny(body, " \t") {
		return false
	}
	hasLetter := false
	for _, r := range body {
		if r > unicode.MaxASCII {
			return true
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
			continue
		}
		if (r >= '0' && r <= '9') || r == '.' || r == ',' {
			continue
		}
		return false
	}
	return hasLetter
}
