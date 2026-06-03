package chat

import (
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/internal/displaypolicy"
	"github.com/OnslaughtSnail/caelis/ports/model"
)

func toolResultDisplayOutput(name string, output map[string]any, meta map[string]any) map[string]any {
	out := maps.Clone(output)
	if out == nil {
		out = map[string]any{}
	}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "WRITE", "PATCH":
		for _, key := range []string{
			"created",
			"previous_empty",
			"bytes_written",
			"line_count",
			"added_lines",
			"removed_lines",
			"revision",
			"hunk",
			"diff_hunks",
			"diff_truncated",
		} {
			if value, ok := runtimeToolMeta(meta)[key]; ok {
				out[key] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolResultDisplayText(name string, input map[string]any, output map[string]any, meta map[string]any, status string, isErr bool) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "READ":
		if summary := readResultSummary(input, output); summary != "" {
			return summary
		}
		return toolString(output["content"])
	case "LIST":
		return listResultSummary(input, output)
	case "GLOB":
		return globResultSummary(input, output, meta)
	case "SEARCH", "RG", "FIND":
		return searchResultSummary(input, output, meta)
	case "WRITE", "PATCH":
		if isErr || strings.EqualFold(status, "failed") {
			return firstNonEmpty(toolString(output["error"]), toolString(output["summary"]))
		}
		return mutationResultSummary(input, output)
	case "RUN_COMMAND":
		return terminalResultText(output, status, isErr)
	case "SPAWN":
		return spawnResultText(output, status, isErr)
	case "TASK":
		if toolStatusFinal(status, isErr) {
			if summary := displaypolicy.CleanSubagentFinalOutput(toolString(output["final_message"])); summary != "" {
				return summary
			}
		}
		return terminalResultText(output, status, isErr)
	default:
		return genericResultText(output, isErr)
	}
}

func readResultSummary(input map[string]any, output map[string]any) string {
	path := firstNonEmpty(toolPath(output), toolPath(input))
	if path == "" {
		return ""
	}
	start := toolInt(output["start_line"])
	end := toolInt(output["end_line"])
	if start <= 0 {
		if offset := toolInt(input["offset"]); offset >= 0 {
			start = offset + 1
		}
	}
	if end <= 0 {
		if limit := toolInt(input["limit"]); limit > 0 && start > 0 {
			end = start + limit - 1
		}
	}
	if start > 0 && end > 0 {
		return filepath.Base(path) + " " + strconv.Itoa(start) + "~" + strconv.Itoa(end)
	}
	return filepath.Base(path)
}

func listResultSummary(input map[string]any, output map[string]any) string {
	path := firstNonEmpty(toolPath(output), toolPath(input))
	count := toolInt(output["count"])
	if path == "" && count <= 0 {
		return ""
	}
	if count > 0 {
		return strings.TrimSpace(filepath.Base(path) + " " + pluralize(count, "entry"))
	}
	return filepath.Base(path)
}

func globResultSummary(input map[string]any, output map[string]any, meta map[string]any) string {
	pattern := firstNonEmpty(toolString(input["pattern"]), toolString(output["pattern"]), toolString(runtimeToolMeta(meta)["pattern"]))
	count := toolInt(output["count"])
	switch {
	case pattern != "" && count >= 0:
		return pattern + " " + pluralize(count, "match")
	case pattern != "":
		return pattern
	default:
		return ""
	}
}

func searchResultSummary(input map[string]any, output map[string]any, meta map[string]any) string {
	query := firstNonEmpty(toolString(output["query"]), toolString(input["query"]), toolString(input["pattern"]), toolString(runtimeToolMeta(meta)["query"]))
	count := toolInt(output["count"])
	files := toolInt(output["file_count"])
	if query == "" && count <= 0 {
		return ""
	}
	summary := ""
	if query != "" {
		summary = strconv.Quote(query)
	}
	if count >= 0 {
		summary = strings.TrimSpace(summary + " " + pluralize(count, "hit"))
	}
	if files > 0 {
		summary += " in " + pluralize(files, "file")
	}
	return summary
}

func mutationResultSummary(input map[string]any, output map[string]any) string {
	path := firstNonEmpty(toolPath(output), toolPath(input))
	if path == "" {
		return firstNonEmpty(toolString(output["summary"]), "completed")
	}
	header := filepath.Base(path)
	added := toolInt(output["added_lines"])
	removed := toolInt(output["removed_lines"])
	if added > 0 || removed > 0 {
		header += fmt.Sprintf(" +%d -%d", added, removed)
	}
	if diffLines := mutationDiffLines(output); len(diffLines) > 0 {
		return strings.Join(append([]string{header, "diff / hunk"}, diffLines...), "\n")
	}
	if hunk := strings.TrimSpace(toolString(output["hunk"])); hunk != "" {
		return strings.Join([]string{header, "diff / hunk", hunk}, "\n")
	}
	return header
}

func mutationDiffLines(output map[string]any) []string {
	raw, ok := output["diff_hunks"]
	if !ok || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var hunks []struct {
		Header string   `json:"header"`
		Lines  []string `json:"lines"`
	}
	if err := json.Unmarshal(data, &hunks); err != nil {
		return nil
	}
	lines := make([]string, 0, len(hunks)*4)
	for _, hunk := range hunks {
		if header := strings.TrimSpace(hunk.Header); header != "" {
			lines = append(lines, header)
		}
		lines = append(lines, hunk.Lines...)
	}
	if len(lines) == 0 {
		return nil
	}
	if toolBool(output["diff_truncated"]) {
		lines = append(lines, "@@ diff truncated @@")
	}
	return lines
}

func terminalResultText(output map[string]any, status string, isErr bool) string {
	if !toolStatusFinal(status, isErr) {
		if text := firstNonBlankRaw(
			toolRawString(output["latest_output"]),
			toolRawString(output["output_preview"]),
		); text != "" {
			return text
		}
		return ""
	}
	if text := toolRawString(output["result"]); toolOutputHasNonBlankLine(text) {
		return text
	}
	if errText := toolRawString(output["error"]); toolOutputHasNonBlankLine(errText) {
		return errText
	}
	return ""
}

func spawnResultText(output map[string]any, status string, isErr bool) string {
	if isErr || strings.EqualFold(status, "failed") {
		if stderr := toolRawString(output["stderr"]); toolOutputHasNonBlankLine(stderr) {
			return stderr
		}
		if errText := toolRawString(output["error"]); toolOutputHasNonBlankLine(errText) {
			return errText
		}
	}
	if toolStatusFinal(status, isErr) {
		return displaypolicy.CleanSubagentFinalOutput(firstNonEmpty(
			spawnDisplayText(toolString(output["final_message"])),
			spawnDisplayText(toolString(output["finalMessage"])),
			spawnDisplayText(toolString(output["result"])),
			spawnDisplayText(toolString(output["output"])),
			spawnDisplayText(toolString(output["text"])),
		))
	}
	return firstNonBlankRaw(
		spawnStreamText(toolRawString(output["text"])),
		spawnStreamText(toolRawString(output["stdout"])),
		spawnStreamText(toolRawString(output["output_preview"])),
		spawnStreamText(toolRawString(output["stderr"])),
	)
}

func spawnDisplayText(text string) string {
	return displaypolicy.SpawnDisplayTextCandidate(text)
}

func spawnStreamText(text string) string {
	if text == "" {
		return ""
	}
	candidate := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(candidate, "{") {
		return text
	}
	decoded, remainder, ok := displaypolicy.SplitLeadingJSONObject(candidate)
	if !ok || !displaypolicy.IsSpawnDisplayJSONObject(decoded) {
		return text
	}
	if strings.TrimSpace(remainder) == "" {
		return ""
	}
	return strings.TrimLeft(remainder, "\r\n")
}

func genericResultText(output map[string]any, isErr bool) string {
	if len(output) == 0 {
		return ""
	}
	if isErr {
		return firstNonEmpty(toolString(output["stderr"]), toolString(output["error"]), toolString(output["summary"]))
	}
	return firstNonEmpty(toolString(output["summary"]), toolString(output["result"]), toolString(output["text"]))
}

func toolResultStatusText(status string, isErr bool) string {
	if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
		return "failed"
	}
	if toolStatusFinal(status, isErr) {
		return "completed"
	}
	return ""
}

func toolStatusFinal(status string, isErr bool) bool {
	if isErr {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled", "terminated":
		return true
	default:
		return false
	}
}

func toolResultTerminalID(call model.ToolCall, output map[string]any, meta map[string]any) string {
	return firstNonEmpty(
		toolString(output["terminal_id"]),
		stringFromNestedMap(meta, "caelis", "runtime", "task", "terminal_id"),
		strings.TrimSpace(call.ID),
	)
}

func runtimeToolMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	return toolMeta
}

func toolPath(values map[string]any) string {
	return firstNonEmpty(toolString(values["path"]), toolString(values["target"]), toolString(values["source"]))
}

func toolString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func toolRawString(value any) string {
	text, _ := value.(string)
	return text
}

func firstNonBlankRaw(values ...string) string {
	for _, value := range values {
		if toolOutputHasNonBlankLine(value) {
			return value
		}
	}
	return ""
}

func toolOutputHasNonBlankLine(text string) bool {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func toolInt(value any) int {
	if intValue, ok := intValue(value); ok {
		return intValue
	}
	if text := toolString(value); text != "" {
		if parsed, err := strconv.Atoi(text); err == nil {
			return parsed
		}
	}
	return -1
}

func toolBool(value any) bool {
	typed, _ := value.(bool)
	return typed
}

func pluralize(count int, unit string) string {
	if count == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(count) + " " + unit + "s"
}
