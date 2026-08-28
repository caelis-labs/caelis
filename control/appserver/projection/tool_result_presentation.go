package projection

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func projectedToolResultContent(toolCallID string, name string, input, output, meta map[string]any, status string) []schema.ToolCallContent {
	profile, known := projectedBuiltinToolProfile(name)
	displayOutput := projectedToolDisplayOutput(profile, known, output, meta)
	if known && profile.result == projectedResultTask && projectedSuppressTaskControlContent(display.ToolTaskAction(input, displayOutput, meta)) {
		return nil
	}
	isErr := strings.EqualFold(strings.TrimSpace(status), schema.ToolStatusFailed)
	text := projectedToolResultText(profile, known, input, displayOutput, meta, status, isErr)
	if strings.TrimSpace(text) == "" && projectedSuccessfulEmptyTerminal(profile, known, status, isErr) {
		return nil
	}
	if strings.TrimSpace(text) == "" {
		text = projectedToolResultStatusText(status, isErr)
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	contentType := "content"
	terminalID := ""
	if known && profile.terminalKnown {
		contentType = "terminal"
		terminalID = firstNonEmpty(
			projectedToolString(displayOutput["terminal_id"]),
			projectedNestedString(meta, "caelis", "runtime", "task", "terminal_id"),
			strings.TrimSpace(toolCallID),
		)
	}
	return []schema.ToolCallContent{{
		Type:       contentType,
		Content:    schema.TextContent{Type: "text", Text: text},
		TerminalID: terminalID,
	}}
}

func projectedToolDisplayOutput(profile projectedToolProfile, known bool, output, meta map[string]any) map[string]any {
	out := cloneAnyMap(output)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := projectedRuntimeToolMeta(meta)
	if known {
		for _, key := range projectedSummaryMetadataKeys(profile.result) {
			if _, exists := out[key]; exists {
				continue
			}
			if value, exists := toolMeta[key]; exists {
				out[key] = value
			}
		}
		if profile.result == projectedResultMutation {
			for _, key := range []string{
				"created", "previous_empty", "bytes_written", "line_count", "added_lines", "removed_lines",
				"revision", "hunk", "diff_hunks", "diff_truncated",
			} {
				if value, exists := toolMeta[key]; exists {
					out[key] = value
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func projectedSummaryMetadataKeys(style projectedToolResultStyle) []string {
	switch style {
	case projectedResultRead:
		return []string{"path", "file_path", "start_line", "end_line", "next_offset", "has_more"}
	case projectedResultGlob:
		return []string{"pattern", "count", "total_count"}
	case projectedResultSearch:
		return []string{"pattern", "query", "count", "file_count"}
	case projectedResultWebSearch:
		return []string{"query", "provider", "model", "status", "answer", "results", "message"}
	case projectedResultWebFetch:
		return []string{"url", "final_url", "title", "status", "status_code", "content_type", "format", "message"}
	default:
		return nil
	}
}

func projectedToolResultText(profile projectedToolProfile, known bool, input, output, meta map[string]any, status string, isErr bool) string {
	if !known {
		return projectedGenericResultText(output, isErr)
	}
	switch profile.result {
	case projectedResultRead:
		if summary := projectedReadResultSummary(input, output); summary != "" {
			return summary
		}
		return projectedToolString(output["content"])
	case projectedResultGlob:
		return projectedGlobResultSummary(input, output, meta)
	case projectedResultSearch:
		return projectedSearchResultSummary(input, output, meta)
	case projectedResultWebSearch:
		return display.WebSearchSummary(input, output)
	case projectedResultWebFetch:
		return display.WebFetchSummary(input, output)
	case projectedResultMutation:
		if isErr || strings.EqualFold(status, schema.ToolStatusFailed) {
			return firstNonEmpty(projectedToolString(output["error"]), projectedToolString(output["summary"]))
		}
		return projectedMutationResultSummary(input, output)
	case projectedResultCommand:
		return projectedTerminalResultText(output, status, isErr)
	case projectedResultSpawn:
		return projectedSpawnResultText(output, status, isErr)
	case projectedResultTask:
		observedStatus := firstNonEmpty(projectedToolString(output["state"]), status)
		if projectedToolStatusFinal(observedStatus, isErr) {
			if summary := display.CleanSubagentFinalOutput(projectedToolString(output["final_message"])); summary != "" {
				return summary
			}
		}
		return projectedTerminalResultText(output, observedStatus, isErr)
	default:
		return projectedGenericResultText(output, isErr)
	}
}

func projectedReadResultSummary(input, output map[string]any) string {
	path := firstNonEmpty(projectedToolPath(output), projectedToolPath(input))
	if path == "" {
		return ""
	}
	start := projectedToolInt(output["start_line"])
	end := projectedToolInt(output["end_line"])
	if start <= 0 {
		if offset := projectedToolInt(input["offset"]); offset >= 0 {
			start = offset + 1
		}
	}
	if end <= 0 {
		if limit := projectedToolInt(input["limit"]); limit > 0 && start > 0 {
			end = start + limit - 1
		}
	}
	if start > 0 && end > 0 {
		return filepath.Base(path) + " " + strconv.Itoa(start) + "~" + strconv.Itoa(end)
	}
	return filepath.Base(path)
}

func projectedGlobResultSummary(input, output, meta map[string]any) string {
	toolMeta := projectedRuntimeToolMeta(meta)
	pattern := firstNonEmpty(projectedToolString(input["pattern"]), projectedToolString(output["pattern"]), projectedToolString(toolMeta["pattern"]))
	count := projectedFirstToolInt(output["count"], toolMeta["count"])
	switch {
	case pattern != "" && count >= 0:
		return pattern + " " + display.Pluralize(count, "match")
	case pattern != "":
		return pattern
	default:
		return ""
	}
}

func projectedSearchResultSummary(input, output, meta map[string]any) string {
	toolMeta := projectedRuntimeToolMeta(meta)
	query := firstNonEmpty(projectedToolString(output["pattern"]), projectedToolString(input["pattern"]), projectedToolString(toolMeta["pattern"]), projectedToolString(output["query"]), projectedToolString(input["query"]), projectedToolString(toolMeta["query"]))
	count := projectedFirstToolInt(output["count"], toolMeta["count"])
	if query == "" && count <= 0 {
		return ""
	}
	summary := ""
	if query != "" {
		summary = strconv.Quote(query)
	}
	if count >= 0 {
		summary = strings.TrimSpace(summary + " " + display.Pluralize(count, "hit"))
	}
	return summary
}

func projectedMutationResultSummary(input, output map[string]any) string {
	path := firstNonEmpty(projectedToolPath(output), projectedToolPath(input))
	if path == "" {
		return firstNonEmpty(projectedToolString(output["summary"]), "completed")
	}
	header := filepath.Base(path)
	added := projectedToolInt(output["added_lines"])
	removed := projectedToolInt(output["removed_lines"])
	if added > 0 || removed > 0 {
		header += fmt.Sprintf(" +%d -%d", added, removed)
	}
	if lines := projectedMutationDiffLines(output); len(lines) > 0 {
		return strings.Join(append([]string{header, "diff / hunk"}, lines...), "\n")
	}
	if hunk := strings.TrimSpace(projectedToolString(output["hunk"])); hunk != "" {
		return strings.Join([]string{header, "diff / hunk", hunk}, "\n")
	}
	return header
}

func projectedMutationDiffLines(output map[string]any) []string {
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
	if json.Unmarshal(data, &hunks) != nil {
		return nil
	}
	lines := make([]string, 0, len(hunks)*4)
	for _, hunk := range hunks {
		if header := strings.TrimSpace(hunk.Header); header != "" {
			lines = append(lines, header)
		}
		lines = append(lines, hunk.Lines...)
	}
	if len(lines) > 0 && projectedToolBool(output["diff_truncated"]) {
		lines = append(lines, "@@ diff truncated @@")
	}
	return lines
}

func projectedTerminalResultText(output map[string]any, status string, isErr bool) string {
	if !projectedToolStatusFinal(status, isErr) {
		return projectedFirstNonBlankRaw(projectedToolRawString(output["latest_output"]), projectedToolRawString(output["output_preview"]))
	}
	if text := projectedToolRawString(output["result"]); projectedOutputHasNonBlankLine(text) {
		return text
	}
	if text := projectedToolRawString(output["error"]); projectedOutputHasNonBlankLine(text) {
		return text
	}
	if isErr || strings.EqualFold(status, schema.ToolStatusFailed) {
		if exitCode := projectedToolInt(output["exit_code"]); exitCode > 0 {
			return "exit " + strconv.Itoa(exitCode)
		}
	}
	return ""
}

func projectedSpawnResultText(output map[string]any, status string, isErr bool) string {
	if isErr || strings.EqualFold(status, schema.ToolStatusFailed) {
		if text := projectedToolRawString(output["stderr"]); projectedOutputHasNonBlankLine(text) {
			return text
		}
		if text := projectedToolRawString(output["error"]); projectedOutputHasNonBlankLine(text) {
			return text
		}
	}
	if projectedToolStatusFinal(status, isErr) {
		return display.CleanSubagentFinalOutput(firstNonEmpty(
			display.SpawnDisplayTextCandidate(projectedToolString(output["final_message"])),
			display.SpawnDisplayTextCandidate(projectedToolString(output["finalMessage"])),
			display.SpawnDisplayTextCandidate(projectedToolString(output["result"])),
			display.SpawnDisplayTextCandidate(projectedToolString(output["output"])),
			display.SpawnDisplayTextCandidate(projectedToolString(output["text"])),
		))
	}
	return projectedFirstNonBlankRaw(
		projectedSpawnStreamText(projectedToolRawString(output["text"])),
		projectedSpawnStreamText(projectedToolRawString(output["stdout"])),
		projectedSpawnStreamText(projectedToolRawString(output["output_preview"])),
		projectedSpawnStreamText(projectedToolRawString(output["stderr"])),
	)
}

func projectedSpawnStreamText(text string) string {
	if text == "" {
		return ""
	}
	candidate := strings.TrimLeft(text, " \t\r\n")
	decoded, remainder, ok := display.SplitLeadingJSONObject(candidate)
	if !strings.HasPrefix(candidate, "{") || !ok || !display.IsSpawnDisplayJSONObject(decoded) {
		return text
	}
	if strings.TrimSpace(remainder) == "" {
		return ""
	}
	return strings.TrimLeft(remainder, "\r\n")
}

func projectedGenericResultText(output map[string]any, isErr bool) string {
	if isErr {
		return firstNonEmpty(projectedToolString(output["stderr"]), projectedToolString(output["error"]), projectedToolString(output["summary"]))
	}
	return firstNonEmpty(projectedToolString(output["summary"]), projectedToolString(output["result"]), projectedToolString(output["text"]))
}

func projectedToolResultStatusText(status string, isErr bool) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if isErr || normalized == schema.ToolStatusFailed {
		return "failed"
	}
	switch normalized {
	case schema.ToolStatusCompleted, "interrupted", "terminated":
		return normalized
	case "cancelled", "canceled":
		return "cancelled"
	default:
		return ""
	}
}

func projectedSuppressTaskControlContent(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "wait", "read", "cancel":
		return true
	default:
		return false
	}
}

func projectedSuccessfulEmptyTerminal(profile projectedToolProfile, known bool, status string, isErr bool) bool {
	return known && profile.terminalKnown && profile.terminalPanel && !isErr && strings.EqualFold(strings.TrimSpace(status), schema.ToolStatusCompleted)
}

func projectedToolStatusFinal(status string, isErr bool) bool {
	if isErr {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case schema.ToolStatusCompleted, schema.ToolStatusFailed, "interrupted", "cancelled", "canceled", "terminated":
		return true
	default:
		return false
	}
}

func projectedRuntimeToolMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	return toolMeta
}

func projectedToolPath(values map[string]any) string {
	return firstNonEmpty(projectedToolString(values["path"]), projectedToolString(values["target"]), projectedToolString(values["source"]))
}

func projectedToolString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func projectedToolRawString(value any) string {
	text, _ := value.(string)
	return text
}

func projectedNestedString(values map[string]any, path ...string) string {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	return projectedToolString(current)
}

func projectedToolInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		parsed, err := strconv.Atoi(string(typed))
		if err == nil {
			return parsed
		}
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed
		}
	}
	return -1
}

func projectedFirstToolInt(values ...any) int {
	for _, value := range values {
		if parsed := projectedToolInt(value); parsed >= 0 {
			return parsed
		}
	}
	return -1
}

func projectedToolBool(value any) bool {
	typed, _ := value.(bool)
	return typed
}

func projectedFirstNonBlankRaw(values ...string) string {
	for _, value := range values {
		if projectedOutputHasNonBlankLine(value) {
			return value
		}
	}
	return ""
}

func projectedOutputHasNonBlankLine(text string) bool {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}
