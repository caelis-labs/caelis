package tuiapp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

func toolDisplayArgs(name string, raw map[string]any, fallback ...string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "READ":
		if path := toolPath(raw); path != "" {
			return filepath.Base(path)
		}
	case "LIST":
		if path := toolPath(raw); path != "" {
			return filepath.Base(path)
		}
	case "GLOB":
		if pattern := strings.TrimSpace(asString(raw["pattern"])); pattern != "" {
			return pattern
		}
	case "SEARCH", "RG", "FIND":
		query := toolQuery(raw)
		path := toolPath(raw)
		switch {
		case query != "" && path != "":
			return fmt.Sprintf("%q in %s", query, filepath.Base(path))
		case query != "":
			return fmt.Sprintf("%q", query)
		case path != "":
			return filepath.Base(path)
		}
	case "WRITE", "PATCH":
		if path := toolPath(raw); path != "" {
			return filepath.Base(path)
		}
	case "BASH", "SPAWN", "TASK":
		if name == "TASK" {
			if action := taskControlDisplay(raw); action != "" {
				return action
			}
		}
		if name == "SPAWN" {
			if display := spawnDisplayArgs(raw); display != "" {
				return display
			}
		}
		if command := terminalCommandDisplay(raw); command != "" {
			return normalizeToolDisplayArg(command)
		}
	}
	if summary := genericToolArgs(raw); summary != "" {
		return summary
	}
	return firstTrimmed(fallback...)
}

func toolTitleDisplayArgs(name string, kind string, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "BASH":
		return executeTitleDisplayArgs(title)
	case "READ", "LIST":
		return prefixedTitleDetail(title, "Read", "List")
	case "SEARCH", "RG", "FIND":
		if detail := prefixedTitleDetail(title, "Search", "Searching for:", "Searching"); detail != "" {
			return fmt.Sprintf("%q", detail)
		}
		return title
	case "WRITE", "PATCH":
		return prefixedTitleDetail(title, "Write", "Edit", "Patch", "Delete", "Move")
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "execute":
		return executeTitleDisplayArgs(title)
	case "read":
		return prefixedTitleDetail(title, "Read")
	case "search":
		if detail := prefixedTitleDetail(title, "Search"); detail != "" {
			return fmt.Sprintf("%q", detail)
		}
	case "fetch":
		if detail := prefixedTitleDetail(title, "Fetch", "Searching for:", "Searching"); detail != "" {
			return fmt.Sprintf("%q", detail)
		}
	}
	return title
}

func executeTitleDisplayArgs(title string) string {
	title = strings.TrimSpace(title)
	if strings.EqualFold(title, "Terminal") {
		return ""
	}
	if detail := prefixedTitleDetail(title, "Terminal", "Run", "Running"); detail != "" {
		return detail
	}
	return title
}

func prefixedTitleDetail(title string, prefixes ...string) string {
	title = strings.TrimSpace(title)
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		if strings.HasSuffix(prefix, ":") {
			if len(title) >= len(prefix) && strings.EqualFold(title[:len(prefix)], prefix) {
				return strings.TrimSpace(title[len(prefix):])
			}
			continue
		}
		if len(title) == len(prefix) && strings.EqualFold(title, prefix) {
			return ""
		}
		withSpace := prefix + " "
		if len(title) > len(withSpace) && strings.EqualFold(title[:len(withSpace)], withSpace) {
			detail := strings.TrimSpace(title[len(withSpace):])
			switch strings.ToLower(detail) {
			case "", "file", "files", "repository", "terminal":
				return ""
			default:
				return detail
			}
		}
	}
	return ""
}

func terminalCommandDisplay(raw map[string]any) string {
	if len(raw) == 0 {
		return ""
	}
	if parts := terminalCommandParts(raw["command"]); len(parts) > 0 {
		return commandWithArgsDisplay(parts[0], parts[1:])
	}
	command := firstTrimmed(asString(raw["command"]), asString(raw["cmd"]), asString(raw["executable"]), asString(raw["program"]))
	args := terminalCommandArgs(raw["args"])
	if command == "" {
		if parsed := parsedCommandString(raw); parsed != "" {
			return parsed
		}
		if len(args) == 0 {
			return ""
		}
		return strings.Join(shellDisplayArgs(args), " ")
	}
	if display := commandWithArgsDisplay(command, args); display != "" {
		return display
	}
	return command
}

func terminalCommandParts(raw any) []string {
	switch typed := raw.(type) {
	case []string:
		return terminalCommandArgs(typed)
	case []any:
		return terminalCommandArgs(typed)
	default:
		return nil
	}
}

func terminalCommandArgs(raw any) []string {
	switch typed := raw.(type) {
	case nil:
		return nil
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(asString(item)); trimmed != "" && trimmed != "<nil>" {
				out = append(out, trimmed)
			}
		}
		return out
	case string:
		if trimmed := strings.TrimSpace(typed); trimmed != "" {
			return strings.Fields(trimmed)
		}
	}
	return nil
}

func commandWithArgsDisplay(command string, args []string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if len(args) == 0 {
		return command
	}
	if shellBody := shellLCBody(command, args); shellBody != "" {
		return shellBody
	}
	parts := append([]string{command}, shellDisplayArgs(args)...)
	return strings.Join(parts, " ")
}

func shellLCBody(command string, args []string) string {
	base := filepath.Base(strings.TrimSpace(command))
	switch base {
	case "sh", "bash", "zsh", "fish":
	default:
		return ""
	}
	for i := 0; i+1 < len(args); i++ {
		switch strings.TrimSpace(args[i]) {
		case "-c", "-lc":
			return strings.TrimSpace(args[i+1])
		}
	}
	return ""
}

func shellDisplayArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		out = append(out, shellDisplayArg(arg))
	}
	return out
}

func shellDisplayArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\"'`$\\|&;<>()[]{}*?!") {
		return arg
	}
	return strconv.Quote(arg)
}

func genericToolArgs(raw map[string]any) string {
	query := firstTrimmed(toolQuery(raw), asString(raw["q"]))
	url := toolURL(raw)
	if action, ok := raw["action"].(map[string]any); ok {
		query = firstTrimmed(query, asString(action["query"]))
		url = firstTrimmed(url, asString(action["url"]))
	}
	switch {
	case query != "":
		return strconv.Quote(truncateTailDisplay(query, 96))
	case url != "":
		return truncateTailDisplay(url, 120)
	case toolPath(raw) != "":
		return filepath.Base(toolPath(raw))
	default:
		return ""
	}
}

func toolDisplayOutput(name string, input map[string]any, output map[string]any, fallback string, status string, isErr bool) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	if isErr && (name == "WRITE" || name == "PATCH") {
		if text := mutationErrorDisplay(output, fallback); text != "" {
			return text
		}
	}
	switch name {
	case "READ":
		if summary := readDisplaySummary(input, output); summary != "" {
			return summary
		}
	case "LIST":
		if summary := listDisplaySummary(input, output); summary != "" {
			return summary
		}
	case "GLOB":
		if summary := globDisplaySummary(input, output); summary != "" {
			return summary
		}
	case "SEARCH", "RG", "FIND":
		if summary := searchDisplaySummary(input, output); summary != "" {
			return summary
		}
	case "WRITE", "PATCH":
		if summary := mutationDisplaySummary(input, output); summary != "" {
			return summary
		}
	case "TASK":
		return terminalDisplaySummary(output, isErr)
	case "BASH", "SPAWN":
		if summary := terminalDisplaySummary(output, isErr); summary != "" {
			return summary
		}
		if len(output) > 0 && looksLikeRawToolJSON(fallback) {
			return terminalEmptySummary(name, output, isErr)
		}
	}
	if summary := genericToolOutput(output, isErr); summary != "" {
		return summary
	}
	if isErr {
		if text := strings.TrimSpace(fallback); text != "" {
			return text
		}
	}
	if text := strings.TrimSpace(fallback); text != "" {
		return text
	}
	if transcriptToolStatusFinal(status, isErr) {
		if isErr {
			return "failed"
		}
		return "completed"
	}
	return ""
}

func taskControlDisplay(raw map[string]any) string {
	action := strings.ToUpper(strings.TrimSpace(asString(raw["action"])))
	switch action {
	case "WAIT":
		if ms := displayInt(raw["yield_time_ms"]); ms > 0 {
			return "WAIT " + formatDurationMS(ms)
		}
		return "WAIT"
	case "CANCEL":
		return "CANCEL"
	case "WRITE":
		if input := formatTaskWriteInput(asString(raw["input"])); input != "" {
			return "WRITE " + input
		}
		return "WRITE"
	case "":
		return ""
	default:
		return action
	}
}

func spawnDisplayArgs(raw map[string]any) string {
	if len(raw) == 0 {
		return ""
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	return normalizeToolDisplayArg(string(data))
}

func genericToolOutput(output map[string]any, isErr bool) string {
	if len(output) == 0 {
		return ""
	}
	if isErr {
		if stderr := strings.TrimSpace(asString(output["stderr"])); stderr != "" {
			return stderr
		}
		if errText := strings.TrimSpace(asString(output["error"])); errText != "" {
			return errText
		}
		if summary := strings.TrimSpace(asString(output["summary"])); summary != "" {
			return summary
		}
	}
	return firstTrimmed(
		asString(output["text"]),
		asString(output["stdout"]),
		asString(output["result"]),
		asString(output["output"]),
		asString(output["output_preview"]),
		asString(output["stderr"]),
		asString(output["error"]),
		asString(output["summary"]),
	)
}

func mutationErrorDisplay(output map[string]any, fallback string) string {
	if text := genericToolOutput(output, true); text != "" && !isGenericFailureText(text) {
		return text
	}
	if text := strings.TrimSpace(fallback); text != "" && !isGenericFailureText(text) {
		return text
	}
	if text := genericToolOutput(output, true); text != "" {
		return text
	}
	return strings.TrimSpace(fallback)
}

func isGenericFailureText(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "", "failed", "error":
		return true
	default:
		return false
	}
}

func formatTaskWriteInput(input string) string {
	input = normalizeToolDisplayArg(input)
	if input == "" {
		return ""
	}
	return strconv.Quote(input)
}

func normalizeToolDisplayArg(input string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(input, "\r\n", "\n"), "\r", "\n"))
}

func formatDurationMS(ms int) string {
	if ms%1000 == 0 {
		return strconv.Itoa(ms/1000) + " s"
	}
	return strconv.Itoa(ms) + " ms"
}

func toolDisplayPanelOutput(name string, output string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "WRITE", "PATCH":
		lines := strings.Split(strings.TrimSpace(output), "\n")
		if len(lines) >= 2 && strings.EqualFold(strings.TrimSpace(lines[1]), "diff / hunk") {
			return strings.Join(lines[1:], "\n")
		}
	}
	return output
}

func toolDisplayTaskID(input map[string]any, output map[string]any) string {
	return firstTrimmed(asString(output["task_id"]), asString(input["task_id"]))
}

func toolDisplayResultHeader(name string, output string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ", "LIST", "GLOB", "SEARCH", "RG", "FIND", "WRITE", "PATCH":
	default:
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && trimmed != "diff / hunk" {
			return trimmed
		}
	}
	return ""
}

func readDisplaySummary(input map[string]any, output map[string]any) string {
	path := firstTrimmed(toolPath(output), toolPath(input))
	if path == "" {
		return ""
	}
	start := displayInt(output["start_line"])
	end := displayInt(output["end_line"])
	if start <= 0 {
		if offset := displayInt(input["offset"]); offset >= 0 {
			start = offset + 1
		}
	}
	if end <= 0 {
		if limit := displayInt(input["limit"]); limit > 0 && start > 0 {
			end = start + limit - 1
		}
	}
	if start > 0 && end > 0 {
		return filepath.Base(path) + " " + strconv.Itoa(start) + "~" + strconv.Itoa(end)
	}
	return filepath.Base(path)
}

func listDisplaySummary(input map[string]any, output map[string]any) string {
	path := firstTrimmed(toolPath(output), toolPath(input))
	count := displayInt(output["count"])
	if path == "" && count <= 0 {
		return ""
	}
	if count > 0 {
		return strings.TrimSpace(filepath.Base(path) + " " + pluralizeUnit(count, "entry"))
	}
	return filepath.Base(path)
}

func globDisplaySummary(input map[string]any, output map[string]any) string {
	pattern := firstTrimmed(asString(input["pattern"]), asString(output["pattern"]))
	count := displayInt(output["count"])
	switch {
	case pattern != "" && count >= 0:
		return pattern + " " + pluralizeUnit(count, "match")
	case pattern != "":
		return pattern
	default:
		return ""
	}
}

func searchDisplaySummary(input map[string]any, output map[string]any) string {
	query := firstTrimmed(toolResultQuery(output), toolQuery(input))
	count := displayInt(output["count"])
	files := displayInt(output["file_count"])
	if query == "" && count <= 0 {
		return ""
	}
	summary := ""
	if query != "" {
		summary = fmt.Sprintf("%q", query)
	}
	if count >= 0 {
		summary = strings.TrimSpace(summary + " " + pluralizeUnit(count, "hit"))
	}
	if files > 0 {
		summary += " in " + pluralizeUnit(files, "file")
	}
	return summary
}

func mutationDisplaySummary(input map[string]any, output map[string]any) string {
	path := firstTrimmed(toolPath(output), toolPath(input))
	if path == "" {
		return ""
	}
	added := displayInt(output["added_lines"])
	removed := displayInt(output["removed_lines"])
	header := filepath.Base(path)
	if added > 0 || removed > 0 {
		header += fmt.Sprintf(" +%d -%d", added, removed)
	}
	if diffLines := mutationStructuredDiffLines(output); len(diffLines) > 0 {
		return strings.Join(append([]string{header, "diff / hunk"}, diffLines...), "\n")
	}
	oldText := strings.TrimSpace(asString(input["old"]))
	newText := strings.TrimSpace(asString(input["new"]))
	if hunk := strings.TrimSpace(asString(output["hunk"])); hunk != "" || oldText != "" || newText != "" {
		if hunk == "" {
			hunk = mutationSyntheticHunk(output)
		}
		diffLines := []string{header, "diff / hunk", hunk}
		if oldText != "" {
			diffLines = append(diffLines, prefixDiffLines("-", oldText)...)
		}
		if newText != "" {
			diffLines = append(diffLines, prefixDiffLines("+", newText)...)
		}
		return strings.Join(diffLines, "\n")
	}
	if strings.EqualFold(strings.TrimSpace(asString(output["created"])), "true") || displayBool(output["created"]) || displayBool(output["previous_empty"]) {
		if content := asString(input["content"]); content != "" {
			diffLines := []string{header, "diff / hunk", writeCreateHunk(content)}
			diffLines = append(diffLines, prefixDiffLines("+", strings.TrimRight(content, "\n"))...)
			return strings.Join(diffLines, "\n")
		}
	}
	return header
}

type mutationDisplayDiffHunk struct {
	Header string   `json:"header"`
	Lines  []string `json:"lines"`
}

func mutationStructuredDiffLines(output map[string]any) []string {
	raw, exists := output["diff_hunks"]
	if !exists || raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var hunks []mutationDisplayDiffHunk
	if err := json.Unmarshal(data, &hunks); err != nil {
		return nil
	}
	lines := make([]string, 0, len(hunks)*4)
	for _, hunk := range hunks {
		header := strings.TrimSpace(hunk.Header)
		if header == "" && len(hunk.Lines) == 0 {
			continue
		}
		if header != "" {
			lines = append(lines, header)
		}
		lines = append(lines, hunk.Lines...)
	}
	if len(lines) == 0 {
		return nil
	}
	if displayBool(output["diff_truncated"]) {
		lines = append(lines, "@@ diff truncated @@")
	}
	return lines
}

func mutationSyntheticHunk(output map[string]any) string {
	if replaced := displayInt(output["replaced"]); replaced > 0 {
		return "@@ repeated replacement: " + pluralizeUnit(replaced, "match") + " @@"
	}
	return "@@ changed @@"
}

func prefixDiffLines(prefix string, text string) []string {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, prefix+line)
	}
	return out
}

func writeCreateHunk(content string) string {
	lines := 0
	if strings.TrimSpace(content) != "" {
		lines = strings.Count(strings.TrimRight(content, "\n"), "\n") + 1
	}
	return "@@ -0,0 +1," + strconv.Itoa(lines) + " @@"
}

func terminalDisplaySummary(output map[string]any, isErr bool) string {
	if isErr {
		if stderr := strings.TrimSpace(asString(output["stderr"])); stderr != "" {
			return stderr
		}
		if errText := strings.TrimSpace(asString(output["error"])); errText != "" {
			return errText
		}
	}
	if text := asString(output["text"]); strings.TrimSpace(text) != "" {
		return text
	}
	return firstTrimmed(asString(output["stdout"]), asString(output["result"]), asString(output["output_preview"]), asString(output["stderr"]))
}

func terminalEmptySummary(name string, output map[string]any, isErr bool) string {
	if isErr {
		if stderr := strings.TrimSpace(asString(output["stderr"])); stderr != "" {
			return stderr
		}
	}
	if text := asString(output["text"]); strings.TrimSpace(text) != "" {
		return text
	}
	return firstTrimmed(asString(output["stdout"]), asString(output["output_preview"]), asString(output["result"]))
}

func looksLikeRawToolJSON(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "{") && (strings.Contains(trimmed, `"session_id"`) || strings.Contains(trimmed, `"supports_input"`) || strings.Contains(trimmed, `"task_id"`))
}

func toolPath(raw map[string]any) string {
	path := firstTrimmed(
		asString(raw["path"]),
		asString(raw["file_path"]),
		asString(raw["filePath"]),
		asString(raw["filepath"]),
		asString(raw["target"]),
		parsedCommandField(raw, "path"),
		parsedCommandField(raw, "name"),
	)
	if path != "" {
		return path
	}
	source := strings.TrimSpace(asString(raw["source"]))
	if source != "" && !isTransportSourceValue(source) {
		return source
	}
	return ""
}

func toolQuery(raw map[string]any) string {
	return firstTrimmed(
		asString(raw["query"]),
		asString(raw["pattern"]),
		asString(raw["text"]),
		parsedCommandField(raw, "query"),
		parsedCommandField(raw, "pattern"),
		parsedCommandField(raw, "text"),
	)
}

func toolResultQuery(raw map[string]any) string {
	return firstTrimmed(
		asString(raw["query"]),
		asString(raw["pattern"]),
		parsedCommandField(raw, "query"),
		parsedCommandField(raw, "pattern"),
	)
}

func toolURL(raw map[string]any) string {
	return firstTrimmed(asString(raw["url"]), asString(raw["uri"]), asString(raw["href"]))
}

func parsedCommandString(raw map[string]any) string {
	return parsedCommandField(raw, "cmd")
}

func parsedCommandField(raw map[string]any, key string) string {
	for _, entry := range parsedCommandEntries(raw["parsed_cmd"]) {
		if value := strings.TrimSpace(asString(entry[key])); value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func parsedCommandEntries(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []map[string]any:
		return typed
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				out = append(out, entry)
			}
		}
		return out
	case map[string]any:
		return []map[string]any{typed}
	default:
		return nil
	}
}

func isTransportSourceValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "unified_exec_startup", "unified_exec", "tool_call", "tool_call_update":
		return true
	default:
		return false
	}
}

func firstTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" && trimmed != "<nil>" {
			return trimmed
		}
	}
	return ""
}

func displayBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return false
	}
}

func displayInt(value any) int {
	switch typed := value.(type) {
	case nil:
		return -1
	case int:
		return typed
	case int8:
		return int(typed)
	case int16:
		return int(typed)
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case uint:
		return int(typed)
	case uint8:
		return int(typed)
	case uint16:
		return int(typed)
	case uint32:
		return int(typed)
	case uint64:
		return int(typed)
	case float64:
		return int(typed)
	case float32:
		return int(typed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err == nil {
			return parsed
		}
	}
	return -1
}
