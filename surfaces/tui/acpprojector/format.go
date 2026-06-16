package acpprojector

import (
	"encoding/json"
	"fmt"
	"strings"
)

type ToolContent struct {
	Type       string
	Content    any
	TerminalID string
	Path       string
	OldText    *string
	NewText    string
}

func FormatToolStart(name string, args map[string]any) string {
	return sanitizeToolDisplayText(FormatToolArgsValue(name, args))
}

func FormatToolContent(content []ToolContent) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch strings.TrimSpace(item.Type) {
		case "content":
			if text := toolContentText(item.Content); text != "" {
				parts = append(parts, text)
			}
		case "diff":
			if text := toolDiffText(item); text != "" {
				parts = append(parts, text)
			}
		default:
			continue
		}
	}
	return strings.Join(parts, "\n")
}

func toolDiffText(item ToolContent) string {
	oldText := ""
	if item.OldText != nil {
		oldText = *item.OldText
	}
	lines := buildUnifiedLines(oldText, item.NewText)
	if len(lines) == 0 {
		return ""
	}
	if header := toolDiffHeader(item.Path, lines); header != "" {
		return header + "\n" + strings.Join(lines, "\n")
	}
	return strings.Join(lines, "\n")
}

func toolDiffHeader(path string, lines []string) string {
	path = diffDisplayPath(path)
	if path == "" {
		return ""
	}
	added, removed := 0, 0
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	if added == 0 && removed == 0 {
		return path
	}
	return fmt.Sprintf("%s +%d -%d", path, added, removed)
}

func buildUnifiedLines(oldText string, newText string) []string {
	oldLines := splitUnifiedContentLines(oldText)
	newLines := splitUnifiedContentLines(newText)
	rows := buildUnifiedRows(oldLines, newLines)
	return unifiedHunkLines(rows)
}

type unifiedRow struct {
	kind  byte
	oldNo int
	newNo int
	text  string
}

type indexPair struct {
	a int
	b int
}

func buildUnifiedRows(oldLines []string, newLines []string) []unifiedRow {
	pairs := unifiedLinePairs(oldLines, newLines)
	rows := make([]unifiedRow, 0, len(oldLines)+len(newLines))
	oldIdx, newIdx := 0, 0
	for _, pair := range pairs {
		rows = appendUnifiedChangedRows(rows, oldLines, newLines, oldIdx, pair.a, newIdx, pair.b)
		rows = append(rows, unifiedRow{
			kind:  ' ',
			oldNo: pair.a + 1,
			newNo: pair.b + 1,
			text:  oldLines[pair.a],
		})
		oldIdx = pair.a + 1
		newIdx = pair.b + 1
	}
	rows = appendUnifiedChangedRows(rows, oldLines, newLines, oldIdx, len(oldLines), newIdx, len(newLines))
	return rows
}

func appendUnifiedChangedRows(rows []unifiedRow, oldLines []string, newLines []string, oldStart int, oldEnd int, newStart int, newEnd int) []unifiedRow {
	for idx := oldStart; idx < oldEnd; idx++ {
		rows = append(rows, unifiedRow{kind: '-', oldNo: idx + 1, text: oldLines[idx]})
	}
	for idx := newStart; idx < newEnd; idx++ {
		rows = append(rows, unifiedRow{kind: '+', newNo: idx + 1, text: newLines[idx]})
	}
	return rows
}

func unifiedLinePairs(oldLines []string, newLines []string) []indexPair {
	const maxCells = 250000
	prefix, suffix := commonLineAffixCounts(oldLines, newLines)
	pairs := make([]indexPair, 0, prefix+suffix)
	for idx := 0; idx < prefix; idx++ {
		pairs = append(pairs, indexPair{a: idx, b: idx})
	}
	oldCore := oldLines[prefix : len(oldLines)-suffix]
	newCore := newLines[prefix : len(newLines)-suffix]
	if len(oldCore) > 0 && len(newCore) > 0 && len(oldCore) <= maxCells/len(newCore) {
		for _, pair := range lcsLinePairs(oldCore, newCore) {
			pairs = append(pairs, indexPair{a: prefix + pair.a, b: prefix + pair.b})
		}
	}
	for idx := 0; idx < suffix; idx++ {
		oldIndex := len(oldLines) - suffix + idx
		newIndex := len(newLines) - suffix + idx
		pairs = append(pairs, indexPair{a: oldIndex, b: newIndex})
	}
	return pairs
}

func unifiedHunkLines(rows []unifiedRow) []string {
	ranges := unifiedHunkRanges(rows)
	if len(ranges) == 0 {
		return nil
	}
	out := make([]string, 0, len(rows)+len(ranges))
	for _, item := range ranges {
		hunkRows := rows[item[0] : item[1]+1]
		out = append(out, unifiedHunkHeader(hunkRows))
		for _, row := range hunkRows {
			out = append(out, string(row.kind)+row.text)
		}
	}
	return out
}

func unifiedHunkRanges(rows []unifiedRow) [][2]int {
	const mergeContextGap = 1
	ranges := make([][2]int, 0)
	lastChange := -1
	for idx, row := range rows {
		if row.kind == ' ' {
			continue
		}
		if len(ranges) == 0 || idx-lastChange > mergeContextGap+1 {
			ranges = append(ranges, [2]int{idx, idx})
		} else {
			ranges[len(ranges)-1][1] = idx
		}
		lastChange = idx
	}
	return ranges
}

func unifiedHunkHeader(rows []unifiedRow) string {
	oldStart, oldLines := 0, 0
	newStart, newLines := 0, 0
	for _, row := range rows {
		if row.kind != '+' {
			if oldStart == 0 {
				oldStart = row.oldNo
			}
			oldLines++
		}
		if row.kind != '-' {
			if newStart == 0 {
				newStart = row.newNo
			}
			newLines++
		}
	}
	if oldStart == 0 {
		oldStart = previousDiffLine(newStart)
	}
	if newStart == 0 {
		newStart = previousDiffLine(oldStart)
	}
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldLines, newStart, newLines)
}

func previousDiffLine(line int) int {
	if line <= 1 {
		return 0
	}
	return line - 1
}

func splitUnifiedContentLines(text string) []string {
	if text == "" {
		return nil
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	if normalized == "" {
		return nil
	}
	return strings.Split(normalized, "\n")
}

func commonLineAffixCounts(oldLines, newLines []string) (int, int) {
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	suffix := 0
	for prefix+suffix < len(oldLines) &&
		prefix+suffix < len(newLines) &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	return prefix, suffix
}

func lcsLinePairs(oldLines, newLines []string) []indexPair {
	n := len(oldLines)
	m := len(newLines)
	if n == 0 || m == 0 {
		return nil
	}
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = maxInt(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	pairs := make([]indexPair, 0, dp[0][0])
	i, j := 0, 0
	for i < n && j < m {
		if oldLines[i] == newLines[j] {
			pairs = append(pairs, indexPair{a: i, b: j})
			i++
			j++
			continue
		}
		if dp[i+1][j] >= dp[i][j+1] {
			i++
		} else {
			j++
		}
	}
	return pairs
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func diffDisplayPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.TrimRight(path, `/\`)
	if path == "" {
		return ""
	}
	if idx := strings.LastIndexAny(path, `/\`); idx >= 0 && idx+1 < len(path) {
		return path[idx+1:]
	}
	return path
}

func toolContentText(raw any) string {
	switch typed := raw.(type) {
	case nil:
		return ""
	case json.RawMessage:
		if len(typed) == 0 {
			return ""
		}
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return ""
		}
		return toolContentText(decoded)
	case map[string]any:
		if typeText, _ := typed["type"].(string); !strings.EqualFold(strings.TrimSpace(typeText), "text") {
			return ""
		}
		text, _ := typed["text"].(string)
		return text
	default:
		rawJSON, err := json.Marshal(typed)
		if err != nil || len(rawJSON) == 0 {
			return ""
		}
		var decoded struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(rawJSON, &decoded); err != nil {
			return ""
		}
		if !strings.EqualFold(strings.TrimSpace(decoded.Type), "text") {
			return ""
		}
		return decoded.Text
	}
}

func FormatToolArgsValue(name string, raw any) string {
	return sanitizeToolDisplayText(toolArgsWithName(name, raw))
}

func sanitizeToolDisplayText(text string) string {
	text = strings.TrimSpace(text)
	switch strings.ToLower(text) {
	case "", "null", "{}", "[]", "map[]":
		return ""
	default:
		return text
	}
}

func toolArgsWithName(name string, raw any) string {
	values, ok := raw.(map[string]any)
	if !ok || len(values) == 0 {
		if value := strings.TrimSpace(primaryValue(raw)); value != "" {
			return truncateInline(value, 120)
		}
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(name), "LIST") && listArgsHaveNoDisplayValue(values) {
		return ""
	}
	kind := strings.ToLower(strings.TrimSpace(asString(values["kind"])))
	switch kind {
	case "search":
		if query := strings.TrimSpace(firstNonEmpty(asString(values["query"]), asString(values["pattern"]), asString(values["text"]))); query != "" {
			return `for "` + truncateInline(query, 96) + `"`
		}
	case "edit":
		if path := strings.TrimSpace(firstNonEmpty(asString(values["path"]), asString(values["target"]))); path != "" {
			return truncateInline(path, 120)
		}
	case "read", "delete", "move":
		if path := strings.TrimSpace(firstNonEmpty(asString(values["path"]), asString(values["source"]), asString(values["target"]))); path != "" {
			return truncateInline(path, 120)
		}
	case "execute":
		if command := strings.TrimSpace(firstNonEmpty(asString(values["command"]), asString(values["cmd"]))); command != "" {
			return truncateInline(command, 120)
		}
	case "fetch":
		if url := strings.TrimSpace(firstNonEmpty(asString(values["url"]), asString(values["uri"]))); url != "" {
			return truncateInline(url, 120)
		}
	}
	if value := strings.TrimSpace(primaryValue(values)); value != "" {
		return truncateInline(value, 120)
	}
	return ""
}

func listArgsHaveNoDisplayValue(values map[string]any) bool {
	for _, key := range []string{"path", "target", "source", "cwd"} {
		if strings.TrimSpace(asString(values[key])) != "" {
			return false
		}
	}
	return true
}

func primaryValue(raw any) string {
	switch typed := raw.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case map[string]any:
		for _, key := range []string{"path", "command", "query", "url", "target", "source", "text"} {
			if value := strings.TrimSpace(asString(typed[key])); value != "" {
				return value
			}
		}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Sprint(raw)
	}
	return string(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func asString(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		if v == nil {
			return ""
		}
		return fmt.Sprint(v)
	}
}

func truncateInline(input string, limit int) string {
	input = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(input, "\n", " "), "\t", " "))
	if limit <= 0 || len([]rune(input)) <= limit {
		return input
	}
	runes := []rune(input)
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}
