package acpprojector

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func FormatToolStart(name string, args map[string]any) string {
	return sanitizeToolDisplayText(FormatToolArgsValue(name, args))
}

func FormatToolContent(content []session.ProtocolToolCallContent) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch strings.TrimSpace(item.Type) {
		case "content", "terminal":
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

func toolDiffText(item session.ProtocolToolCallContent) string {
	oldText := ""
	if item.OldText != nil {
		oldText = *item.OldText
	}
	lines := contentDiffLines(oldText, item.NewText)
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n")
}

func contentDiffLines(oldText string, newText string) []string {
	oldLines := splitContentDiffLines(oldText)
	newLines := splitContentDiffLines(newText)
	rows := buildContentDiffRows(oldLines, newLines)
	return contentDiffHunkLines(rows)
}

type contentDiffRow struct {
	kind  byte
	oldNo int
	newNo int
	text  string
}

type contentDiffLinePair struct {
	oldIndex int
	newIndex int
}

func buildContentDiffRows(oldLines []string, newLines []string) []contentDiffRow {
	pairs := contentDiffLinePairs(oldLines, newLines)
	rows := make([]contentDiffRow, 0, len(oldLines)+len(newLines))
	oldIdx, newIdx := 0, 0
	for _, pair := range pairs {
		rows = appendContentChangedRows(rows, oldLines, newLines, oldIdx, pair.oldIndex, newIdx, pair.newIndex)
		rows = append(rows, contentDiffRow{
			kind:  ' ',
			oldNo: pair.oldIndex + 1,
			newNo: pair.newIndex + 1,
			text:  oldLines[pair.oldIndex],
		})
		oldIdx = pair.oldIndex + 1
		newIdx = pair.newIndex + 1
	}
	rows = appendContentChangedRows(rows, oldLines, newLines, oldIdx, len(oldLines), newIdx, len(newLines))
	return rows
}

func appendContentChangedRows(rows []contentDiffRow, oldLines []string, newLines []string, oldStart int, oldEnd int, newStart int, newEnd int) []contentDiffRow {
	for idx := oldStart; idx < oldEnd; idx++ {
		rows = append(rows, contentDiffRow{kind: '-', oldNo: idx + 1, text: oldLines[idx]})
	}
	for idx := newStart; idx < newEnd; idx++ {
		rows = append(rows, contentDiffRow{kind: '+', newNo: idx + 1, text: newLines[idx]})
	}
	return rows
}

func contentDiffLinePairs(oldLines []string, newLines []string) []contentDiffLinePair {
	const maxCells = 250000
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	pairs := make([]contentDiffLinePair, 0, prefix+suffix)
	for idx := 0; idx < prefix; idx++ {
		pairs = append(pairs, contentDiffLinePair{oldIndex: idx, newIndex: idx})
	}
	oldCore := oldLines[prefix : len(oldLines)-suffix]
	newCore := newLines[prefix : len(newLines)-suffix]
	if len(oldCore) > 0 && len(newCore) > 0 && len(oldCore) <= maxCells/len(newCore) {
		dp := make([][]int, len(oldCore)+1)
		for i := range dp {
			dp[i] = make([]int, len(newCore)+1)
		}
		for i := len(oldCore) - 1; i >= 0; i-- {
			for j := len(newCore) - 1; j >= 0; j-- {
				if oldCore[i] == newCore[j] {
					dp[i][j] = dp[i+1][j+1] + 1
				} else if dp[i+1][j] >= dp[i][j+1] {
					dp[i][j] = dp[i+1][j]
				} else {
					dp[i][j] = dp[i][j+1]
				}
			}
		}
		for oldIdx, newIdx := 0, 0; oldIdx < len(oldCore) && newIdx < len(newCore); {
			switch {
			case oldCore[oldIdx] == newCore[newIdx]:
				pairs = append(pairs, contentDiffLinePair{oldIndex: prefix + oldIdx, newIndex: prefix + newIdx})
				oldIdx++
				newIdx++
			case dp[oldIdx+1][newIdx] >= dp[oldIdx][newIdx+1]:
				oldIdx++
			default:
				newIdx++
			}
		}
	}
	for idx := 0; idx < suffix; idx++ {
		oldIndex := len(oldLines) - suffix + idx
		newIndex := len(newLines) - suffix + idx
		pairs = append(pairs, contentDiffLinePair{oldIndex: oldIndex, newIndex: newIndex})
	}
	return pairs
}

func contentDiffHunkLines(rows []contentDiffRow) []string {
	ranges := contentDiffHunkRanges(rows)
	if len(ranges) == 0 {
		return nil
	}
	out := make([]string, 0, len(rows)+len(ranges))
	for _, item := range ranges {
		hunkRows := rows[item[0] : item[1]+1]
		out = append(out, contentDiffHunkHeader(hunkRows))
		for _, row := range hunkRows {
			out = append(out, string(row.kind)+row.text)
		}
	}
	return out
}

func contentDiffHunkRanges(rows []contentDiffRow) [][2]int {
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

func contentDiffHunkHeader(rows []contentDiffRow) string {
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

func splitContentDiffLines(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.TrimSuffix(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
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

func MarshalToolInput(args map[string]any) string {
	return marshalToolInput(args)
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

func marshalToolInput(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(data)
}
