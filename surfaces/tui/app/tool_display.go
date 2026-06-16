package tuiapp

import (
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/internal/displaypolicy"
)

func toolDisplayArgs(name string, raw map[string]any, fallback ...string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "READ":
		if path := toolPath(raw); path != "" {
			return path
		}
	case "LIST":
		if path := toolPath(raw); path != "" {
			return path
		}
		if strings.EqualFold(parsedCommandType(raw), "list_files") {
			if cwd := strings.TrimSpace(asString(raw["cwd"])); cwd != "" {
				return cwd
			}
		}
		if metadataOnlyToolArgs(raw) {
			return ""
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
			return fmt.Sprintf("%q in %s", query, compactPathDisplay(path))
		case query != "":
			return fmt.Sprintf("%q", query)
		case path != "":
			return compactPathDisplay(path)
		}
	case "WRITE", "PATCH":
		if path := toolPath(raw); path != "" {
			return filepath.Base(path)
		}
	case "RUN_COMMAND", "SPAWN", "TASK":
		if name == "TASK" {
			if action := taskControlDisplay(raw); action != "" {
				return action
			}
			if action := taskControlDisplayFallback(fallback...); action != "" {
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

func metadataOnlyToolArgs(raw map[string]any) bool {
	if len(raw) == 0 {
		return false
	}
	for key := range raw {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "metadata", "_meta":
			continue
		default:
			return false
		}
	}
	return true
}

func compactPathDisplay(path string) string {
	return compactPathDisplayWithBase(path, currentWorkingDirectory())
}

func compactPathDisplayWithBase(target string, base string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if rel, ok := compactPathRelativeToBase(target, base); ok {
		return rel
	}
	if baseName := displayPathBase(target); baseName != "" {
		return baseName
	}
	return target
}

func currentWorkingDirectory() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

func compactPathRelativeToBase(target string, base string) (string, bool) {
	target = strings.TrimSpace(target)
	base = strings.TrimSpace(base)
	if target == "" || base == "" || !isAbsoluteDisplayPath(target) {
		return "", false
	}
	targetClean := cleanDisplayPathForCompare(target)
	baseClean := cleanDisplayPathForCompare(base)
	if targetClean == "" || baseClean == "" {
		return "", false
	}
	targetCmp := targetClean
	baseCmp := baseClean
	if displayPathLooksWindows(target) || displayPathLooksWindows(base) {
		targetCmp = strings.ToLower(targetCmp)
		baseCmp = strings.ToLower(baseCmp)
	}
	if targetCmp == baseCmp {
		return displayPathBase(base), true
	}
	prefix := baseCmp
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	if !strings.HasPrefix(targetCmp, prefix) {
		return "", false
	}
	rel := strings.TrimPrefix(targetClean[len(baseClean):], "/")
	rel = strings.TrimSpace(rel)
	if rel == "" || rel == "." {
		return displayPathBase(base), true
	}
	if strings.Contains(target, `\`) || strings.Contains(base, `\`) {
		rel = strings.ReplaceAll(rel, "/", `\`)
	}
	return rel, true
}

func isAbsoluteDisplayPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 &&
		isASCIILetter(value[0]) &&
		value[1] == ':' &&
		(value[2] == '\\' || value[2] == '/')
}

func displayPathLooksWindows(value string) bool {
	value = strings.TrimSpace(value)
	return strings.Contains(value, `\`) ||
		(len(value) >= 2 && isASCIILetter(value[0]) && value[1] == ':')
}

func cleanDisplayPathForCompare(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	if value == "" {
		return ""
	}
	return pathpkg.Clean(value)
}

func displayPathBase(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	trimmed := strings.TrimRight(value, `/\`)
	if trimmed == "" {
		return value
	}
	if idx := strings.LastIndexAny(trimmed, `/\`); idx >= 0 && idx+1 < len(trimmed) {
		return trimmed[idx+1:]
	}
	return trimmed
}

func isASCIILetter(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

func toolDisplayFullArgs(name string, raw map[string]any) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "SPAWN":
		return spawnFullDisplayArgs(raw)
	default:
		return ""
	}
}

func refinedToolDisplayName(semanticName string, kind string, title string, raw map[string]any) string {
	if !strings.EqualFold(strings.TrimSpace(kind), "search") && !strings.EqualFold(strings.TrimSpace(semanticName), "SEARCH") {
		return ""
	}
	switch parsedCommandType(raw) {
	case "list_files":
		return "LIST"
	}
	return ""
}

func toolTitleDisplayArgs(name string, kind string, title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "RUN_COMMAND":
		return executeTitleDisplayArgs(title)
	case "READ", "LIST":
		return prefixedTitleDetail(title, "Read", "List")
	case "SEARCH", "RG", "FIND":
		if detail := prefixedSearchTitleDetail(title); detail != "" {
			return fmt.Sprintf("%q", detail)
		}
		if genericSearchTitle(title) {
			return ""
		}
		return title
	case "WRITE", "PATCH":
		return compactMutationTitleDetail(prefixedTitleDetail(title, "Write", "Edit", "Patch", "Delete", "Move"))
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "execute":
		return executeTitleDisplayArgs(title)
	case "read":
		return prefixedTitleDetail(title, "Read")
	case "search":
		if detail := prefixedSearchTitleDetail(title); detail != "" {
			return fmt.Sprintf("%q", detail)
		}
		if genericSearchTitle(title) {
			return ""
		}
	case "fetch":
		if detail := prefixedTitleDetail(title, "Fetch", "Searching for:"); detail != "" {
			return fmt.Sprintf("%q", detail)
		}
	case "edit", "delete", "move":
		return compactMutationTitleDetail(prefixedTitleDetail(title, "Write", "Edit", "Patch", "Delete", "Move"))
	}
	return title
}

func compactMutationTitleDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	parts := strings.Split(detail, ",")
	if len(parts) > 1 {
		out := make([]string, 0, len(parts))
		changed := false
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			compacted := compactMutationTitleDetail(trimmed)
			if compacted != trimmed {
				changed = true
			}
			if compacted != "" {
				out = append(out, compacted)
			}
		}
		if changed && len(out) > 0 {
			return strings.Join(out, ", ")
		}
		return detail
	}
	pathPart, rest, ok, tagged := splitLeadingPathHeaderParts(detail)
	if !ok {
		if isLikelyDisplayPath(detail) {
			if base := displayPathBase(detail); base != "" {
				return base
			}
		}
		return detail
	}
	base := displayPathBase(pathPart)
	if base == "" || base == pathPart {
		if tagged {
			return pathPart + rest
		}
		return detail
	}
	return base + rest
}

func prefixedSearchTitleDetail(title string) string {
	detail := prefixedTitleDetail(title, "Search", "Find", "Searching for:", "Finding:")
	if genericSearchTitle(detail) {
		return ""
	}
	return detail
}

func genericSearchTitle(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "search", "find", "rg", "grep", "search files", "find files", "search repository", "find repository":
		return true
	default:
		return false
	}
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
		return compactPathDisplay(toolPath(raw))
	default:
		return ""
	}
}

func taskControlDisplay(raw map[string]any) string {
	action := strings.ToUpper(strings.TrimSpace(asString(raw["action"])))
	handle := taskHandleDisplay(asString(raw["task_id"]))
	switch action {
	case "WAIT":
		duration := ""
		if ms := taskWaitDurationMS(raw); ms >= 0 {
			duration = formatDurationMS(ms)
		}
		if handle == "" && duration == "" {
			if targetKind := taskTargetKindDisplay(asString(raw["target_kind"])); targetKind != "" {
				return "Wait " + targetKind
			}
		}
		parts := []string{"Wait"}
		if handle != "" {
			parts = append(parts, handle)
		}
		if duration != "" {
			parts = append(parts, duration)
		}
		return strings.Join(parts, " ")
	case "CANCEL":
		if handle != "" {
			return "Cancel " + handle
		}
		return "Cancel"
	case "WRITE":
		rawInput := asString(raw["input"])
		input := normalizeTaskWriteDisplayInput(rawInput)
		if handle != "" && input != "" {
			return "Write " + handle + ": " + input
		}
		if strings.TrimSpace(rawInput) != "" {
			return "Write " + formatTaskWriteInput(rawInput)
		}
		if handle != "" {
			return "Write " + handle
		}
		return "Write"
	case "":
		return ""
	default:
		return strings.ToUpper(action[:1]) + strings.ToLower(action[1:])
	}
}

func taskWaitDurationMS(raw map[string]any) int {
	if value, ok := raw["yield_time_ms"]; ok {
		if ms := displayInt(value); ms == -1 {
			return -1
		} else if ms > 0 {
			return ms
		}
	}
	if ms := displayInt(raw["effective_yield_time_ms"]); ms >= 0 {
		return ms
	}
	return -1
}

func taskControlDisplayFallback(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		if strings.EqualFold(fields[0], "TASK") {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		action := strings.ToUpper(fields[0])
		details := make([]string, 0, len(fields)-1)
		if len(fields) > 1 {
			if detail := taskHandleDisplay(fields[1]); detail != "" {
				details = append(details, detail)
			}
		}
		if len(fields) > 2 {
			details = append(details, fields[2:]...)
		}
		detail := strings.TrimSpace(strings.Join(details, " "))
		switch action {
		case "WAIT":
			if detail != "" {
				return "Wait " + detail
			}
			return "Wait"
		case "CANCEL":
			if detail != "" {
				return "Cancel " + detail
			}
			return "Cancel"
		case "WRITE":
			return "Write"
		}
	}
	return ""
}

func taskDisplayArgsWithTaskID(args string, taskID string) string {
	handle := taskHandleDisplay(taskID)
	if handle == "" {
		return args
	}
	verb, detail := splitTaskAction(args)
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "wait", "cancel", "write":
	default:
		return args
	}
	detail = taskDetailWithDisplayHandle(verb, detail, handle)
	if detail == "" {
		return verb
	}
	return verb + " " + detail
}

func taskDetailWithDisplayHandle(verb string, detail string, handle string) string {
	detail = strings.TrimSpace(detail)
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return detail
	}
	if detail == "" {
		return handle
	}
	if before, after, ok := strings.Cut(detail, ":"); ok && taskHandleDisplay(before) != "" {
		return handle + ":" + after
	}
	if strings.EqualFold(strings.TrimSpace(verb), "write") {
		return taskWriteDetailWithDisplayHandle(detail, handle)
	}
	fields := strings.Fields(detail)
	if len(fields) == 0 {
		return handle
	}
	if taskHandleDisplay(fields[0]) != "" && !looksLikeTaskDuration(fields[0]) {
		fields[0] = handle
		return strings.Join(fields, " ")
	}
	return strings.TrimSpace(handle + " " + detail)
}

func taskWriteDetailWithDisplayHandle(detail string, handle string) string {
	if detail == "" {
		return handle
	}
	fields := strings.Fields(detail)
	if len(fields) == 1 && isTaskHandleDetail(fields[0]) {
		return handle
	}
	return handle + ": " + detail
}

func looksLikeTaskDuration(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if strings.HasSuffix(value, "ms") {
		value = strings.TrimSuffix(value, "ms")
	} else if strings.HasSuffix(value, "s") {
		value = strings.TrimSuffix(value, "s")
	} else {
		return false
	}
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func spawnDisplayArgs(raw map[string]any) string {
	full := displaypolicy.SpawnDisplayArgs(raw)
	if full == "" {
		return ""
	}
	return truncateReasoningPreviewMiddle(full, 96)
}

func spawnFullDisplayArgs(raw map[string]any) string {
	return displaypolicy.SpawnFullDisplayArgs(raw)
}

func spawnDisplayInputForResult(input map[string]any, output map[string]any) map[string]any {
	return displaypolicy.SpawnDisplayInputForResult(input, output)
}

func taskDisplayInputForResult(input map[string]any, output map[string]any) map[string]any {
	if len(output) == 0 {
		return input
	}
	out := cloneAnyMap(input)
	if out == nil {
		out = map[string]any{}
	}
	for _, key := range []string{"yield_time_ms", "effective_yield_time_ms", "yield_time_ms_defaulted", "target_kind"} {
		if value, ok := output[key]; ok {
			out[key] = value
		}
	}
	return out
}

func normalizeTaskWriteDisplayInput(input string) string {
	input = normalizeToolDisplayArg(input)
	if input == "" {
		return ""
	}
	return strings.Join(strings.Fields(input), " ")
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
	if ms <= 0 {
		return "0ms"
	}
	if ms%1000 == 0 {
		return strconv.Itoa(ms/1000) + "s"
	}
	return strconv.Itoa(ms) + "ms"
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

func toolDisplayTaskID(input map[string]any, output map[string]any, meta map[string]any) string {
	return displaypolicy.ToolTaskID(input, output, meta)
}

func toolDisplayTaskAction(input map[string]any, output map[string]any, meta map[string]any) string {
	return displaypolicy.ToolTaskAction(input, output, meta)
}

func toolDisplayTaskInput(input map[string]any, output map[string]any, meta map[string]any) string {
	return displaypolicy.ToolTaskInput(input, output, meta)
}

func toolDisplayTaskTargetKind(input map[string]any, output map[string]any, meta map[string]any) string {
	return displaypolicy.ToolTaskTargetKind(input, output, meta)
}

func toolDisplaySummaryOutput(name string, output map[string]any, meta map[string]any) map[string]any {
	out := cloneAnyMap(output)
	if out == nil {
		out = map[string]any{}
	}
	toolMeta := eventRuntimeToolMeta(meta)
	if len(toolMeta) == 0 {
		if len(out) == 0 {
			return nil
		}
		return out
	}
	var keys []string
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		keys = []string{"path", "file_path", "start_line", "end_line", "next_offset", "has_more"}
	case "LIST":
		keys = []string{"path", "count", "total_count"}
	case "GLOB":
		keys = []string{"pattern", "count", "total_count"}
	case "SEARCH", "RG", "FIND":
		keys = []string{"query", "pattern", "count", "file_count"}
	default:
		if len(out) == 0 {
			return nil
		}
		return out
	}
	for _, key := range keys {
		if _, exists := out[key]; exists {
			continue
		}
		if value, ok := toolMeta[key]; ok {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func toolDisplayStructuredSummary(name string, input map[string]any, output map[string]any, meta map[string]any) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return readDisplaySummary(input, output)
	case "LIST":
		return listDisplaySummary(input, output)
	case "GLOB":
		return globDisplaySummary(input, output)
	case "SEARCH", "RG", "FIND":
		return searchDisplaySummary(input, output)
	default:
		return ""
	}
}

func taskHandleDisplay(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	if value == "" {
		return ""
	}
	if displayStringAllDigits(value) {
		return ""
	}
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "task-") || strings.Contains(lower, "-task-") {
		return ""
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	return value
}

func taskTargetKindDisplay(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "command":
		return "command"
	case "subagent":
		return "subagent"
	default:
		return ""
	}
}

func displayStringAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func toolDisplayResultHeader(name string, output string) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "READ", "LIST", "GLOB", "WRITE", "PATCH":
	default:
		return ""
	}
	mutationDiff := (name == "WRITE" || name == "PATCH") && toolOutputLooksLikeMutationDiff(output)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if mutationDiff && mutationDiffLine(trimmed) {
			continue
		}
		if !mutationDiff && strings.EqualFold(trimmed, "diff / hunk") {
			continue
		}
		if trimmed != "" {
			return compactToolResultHeaderPath(name, trimmed)
		}
	}
	return ""
}

func compactToolResultHeaderPath(name string, header string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ", "LIST":
		pathPart, rest, ok, tagged := splitLeadingPathHeaderParts(header)
		if !ok {
			return header
		}
		compact := displayPathBase(pathPart)
		if compact == "" || compact == pathPart {
			if tagged {
				return pathPart + rest
			}
			return header
		}
		return compact + rest
	case "GLOB":
	default:
		return header
	}
	pathPart, rest, ok, tagged := splitLeadingPathHeaderParts(header)
	if !ok || !isAbsoluteDisplayPath(pathPart) {
		if tagged {
			return pathPart + rest
		}
		return header
	}
	compact := compactPathDisplay(pathPart)
	if compact == "" || compact == pathPart {
		if tagged {
			return pathPart + rest
		}
		return header
	}
	return compact + rest
}

func splitLeadingPathHeader(header string) (pathPart string, rest string, ok bool) {
	pathPart, rest, ok, _ = splitLeadingPathHeaderParts(header)
	return pathPart, rest, ok
}

func splitLeadingPathHeaderParts(header string) (pathPart string, rest string, ok bool, tagged bool) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", "", false, false
	}
	if pathPart, rest, ok := splitLeadingPathTagHeader(header); ok {
		return pathPart, rest, true, true
	}
	fields := strings.Fields(header)
	if len(fields) == 0 {
		return "", "", false, false
	}
	pathPart = strings.TrimRight(fields[0], ",")
	if !isLikelyDisplayPath(pathPart) {
		return "", "", false, false
	}
	idx := strings.Index(header, fields[0])
	if idx < 0 {
		return "", "", false, false
	}
	rest = header[idx+len(fields[0]):]
	if strings.HasSuffix(fields[0], ",") {
		rest = "," + rest
	}
	return pathPart, rest, true, false
}

func splitLeadingPathTagHeader(header string) (pathPart string, rest string, ok bool) {
	const openTag = "<path>"
	const closeTag = "</path>"
	lower := strings.ToLower(strings.TrimSpace(header))
	if !strings.HasPrefix(lower, openTag) {
		return "", "", false
	}
	closeOffset := strings.Index(lower[len(openTag):], closeTag)
	if closeOffset < 0 {
		return "", "", false
	}
	closeStart := len(openTag) + closeOffset
	pathPart = strings.TrimSpace(header[len(openTag):closeStart])
	if pathPart == "" {
		return "", "", false
	}
	rest = header[closeStart+len(closeTag):]
	return pathPart, rest, true
}

func isLikelyDisplayPath(value string) bool {
	value = strings.TrimSpace(value)
	return isAbsoluteDisplayPath(value) || strings.ContainsAny(value, `/\`)
}

func toolOutputLooksLikeMutationDiff(output string) bool {
	nonEmpty := 0
	hasScaffold := false
	hasAdd := false
	hasRemove := false
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		nonEmpty++
		switch {
		case strings.EqualFold(trimmed, "diff / hunk"):
			hasScaffold = true
			continue
		case isDiffHunkHeader(trimmed):
			hasScaffold = true
			continue
		case strings.HasPrefix(trimmed, "+"):
			hasAdd = true
			continue
		case strings.HasPrefix(trimmed, "-"):
			hasRemove = true
			continue
		}
		return false
	}
	return nonEmpty > 0 && (hasScaffold || (hasAdd && hasRemove))
}

func mutationDiffLine(trimmed string) bool {
	if strings.EqualFold(trimmed, "diff / hunk") {
		return true
	}
	if isDiffHunkHeader(trimmed) {
		return true
	}
	return strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-")
}

func isDiffHunkHeader(trimmed string) bool {
	_, _, _, _, ok := parseDiffHunkHeader(trimmed)
	return ok
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
		return displayPathBase(path) + " " + strconv.Itoa(start) + "~" + strconv.Itoa(end)
	}
	return displayPathBase(path)
}

func listDisplaySummary(input map[string]any, output map[string]any) string {
	path := firstTrimmed(toolPath(output), toolPath(input))
	count := displayInt(output["count"])
	if path == "" && count <= 0 {
		return ""
	}
	if count > 0 {
		return strings.TrimSpace(displayPathBase(path) + " " + pluralizeUnit(count, "entry"))
	}
	return displayPathBase(path)
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

func parsedCommandType(raw map[string]any) string {
	for _, entry := range parsedCommandEntries(raw["parsed_cmd"]) {
		if value := strings.ToLower(strings.TrimSpace(asString(entry["type"]))); value != "" && value != "<nil>" {
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
