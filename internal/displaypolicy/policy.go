package displaypolicy

import (
	"encoding/json"
	"maps"
	"strings"
)

const (
	ToolKindRead    = "read"
	ToolKindEdit    = "edit"
	ToolKindSearch  = "search"
	ToolKindExecute = "execute"
	ToolKindOther   = "other"
)

func SemanticToolName(name string, kind string) string {
	name = strings.TrimSpace(name)
	switch strings.ToUpper(name) {
	case "BASH", "SPAWN", "TASK", "READ", "LIST", "GLOB", "SEARCH", "RG", "FIND", "WRITE", "PATCH":
		return strings.ToUpper(name)
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "execute":
		return "BASH"
	case "read":
		return "READ"
	case "search", "fetch":
		return "SEARCH"
	case "edit", "delete", "move":
		return "PATCH"
	default:
		return name
	}
}

func DisplayTerminalID(toolCallID string, name string) (string, bool) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "BASH", "SPAWN":
		if id := strings.TrimSpace(toolCallID); id != "" {
			return id, true
		}
	}
	return "", false
}

func DisplayTerminalInitialOutput(name string, args map[string]any) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "SPAWN":
		agent := strings.TrimSpace(MapString(args, "agent"))
		prompt := strings.TrimSpace(MapString(args, "prompt"))
		switch {
		case agent != "" && prompt != "":
			return "SPAWN agent=" + agent + "\n" + prompt + "\n"
		case agent != "":
			return "SPAWN agent=" + agent + "\n"
		case prompt != "":
			return "SPAWN\n" + prompt + "\n"
		}
	}
	return ""
}

func SummarizeToolCallTitle(name string, args map[string]any) string {
	name = strings.TrimSpace(strings.ToUpper(name))
	switch name {
	case "READ", "WRITE", "PATCH", "SEARCH", "LIST", "GLOB":
		if path := MapString(args, "path"); strings.TrimSpace(path) != "" {
			return strings.TrimSpace(name + " " + path)
		}
	case "BASH", "TASK":
		if command := MapString(args, "command"); strings.TrimSpace(command) != "" {
			return strings.TrimSpace(name + " " + command)
		}
		if action := MapString(args, "action"); strings.TrimSpace(action) != "" {
			if taskID := MapString(args, "task_id"); strings.TrimSpace(taskID) != "" {
				return strings.TrimSpace(name + " " + action + " " + taskID)
			}
			return strings.TrimSpace(name + " " + action)
		}
	case "SPAWN":
		if agent := MapString(args, "agent"); strings.TrimSpace(agent) != "" {
			return strings.TrimSpace(name + " " + agent)
		}
		if prompt := MapString(args, "prompt"); strings.TrimSpace(prompt) != "" {
			return strings.TrimSpace(name + " " + prompt)
		}
	}
	return name
}

func ToolKindForName(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return ToolKindRead
	case "WRITE", "PATCH":
		return ToolKindEdit
	case "SEARCH", "GLOB", "LIST":
		return ToolKindSearch
	case "BASH", "SPAWN", "TASK":
		return ToolKindExecute
	default:
		return ToolKindOther
	}
}

func SpawnDisplayArgs(raw map[string]any) string {
	full := SpawnFullDisplayArgs(raw)
	if full == "" {
		return ""
	}
	return full
}

func SpawnFullDisplayArgs(raw map[string]any) string {
	raw = NormalizeSpawnDisplayRawMap(raw)
	prompt := strings.Join(strings.Fields(NormalizeDisplayArg(MapString(raw, "prompt"))), " ")
	agent := strings.TrimSpace(MapString(raw, "agent"))
	target := spawnDisplayTarget(raw, agent)
	if target == "" {
		return prompt
	}
	if prompt == "" {
		return target
	}
	return target + ": " + prompt
}

func spawnDisplayTarget(raw map[string]any, agent string) string {
	handle := firstNonEmpty(
		spawnDisplayHandle(MapString(raw, "handle")),
		spawnDisplayHandle(MapString(raw, "mention")),
		spawnDisplayHandle(MapString(raw, "task_id")),
	)
	agent = strings.TrimSpace(agent)
	if handle == "" {
		return agent
	}
	if agent != "" && !strings.EqualFold(handle, agent) {
		return handle + "[" + agent + "]"
	}
	return handle
}

func spawnDisplayHandle(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	if value == "" {
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

func SpawnDisplayInputForResult(input map[string]any, output map[string]any) map[string]any {
	output = NormalizeSpawnDisplayRawMap(output)
	merged := NormalizeSpawnDisplayRawMap(input)
	if merged == nil {
		merged = map[string]any{}
	}
	for _, key := range []string{"agent", "prompt", "handle", "mention", "task_id"} {
		if strings.TrimSpace(MapString(merged, key)) != "" {
			continue
		}
		if value, ok := output[key]; ok {
			merged[key] = value
		}
	}
	return merged
}

func NormalizeSpawnDisplayRawMap(raw map[string]any) map[string]any {
	if len(raw) == 0 {
		return maps.Clone(raw)
	}
	out := maps.Clone(raw)
	for _, key := range []string{"text", "result", "output", "summary", "content", "stdout", "output_preview", "tool_output", "toolOutput", "raw_output", "rawOutput", "message"} {
		text := MapString(out, key)
		decoded, remainder, ok := SplitLeadingJSONObject(text)
		if !ok || !IsSpawnDisplayJSONObject(decoded) {
			continue
		}
		for decodedKey, value := range decoded {
			if _, exists := out[decodedKey]; !exists {
				out[decodedKey] = value
			}
		}
		if strings.TrimSpace(remainder) != "" {
			out[key] = strings.TrimSpace(remainder)
		} else {
			delete(out, key)
		}
	}
	return out
}

func SplitLeadingJSONObject(text string) (map[string]any, string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "{") {
		return nil, text, false
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	var decoded map[string]any
	if err := decoder.Decode(&decoded); err != nil || len(decoded) == 0 {
		return nil, text, false
	}
	offset := int(decoder.InputOffset())
	if offset < 0 || offset > len(text) {
		return nil, text, false
	}
	return decoded, strings.TrimSpace(text[offset:]), true
}

func IsSpawnDisplayJSONObject(decoded map[string]any) bool {
	if len(decoded) == 0 {
		return false
	}
	for _, key := range []string{"agent", "prompt", "task_id", "handle", "mention", "terminal_id", "running", "supports_input", "supports_cancel"} {
		if _, ok := decoded[key]; ok {
			return true
		}
	}
	return false
}

func SpawnDisplayTextCandidate(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	decoded, remainder, ok := SplitLeadingJSONObject(text)
	if !ok || !IsSpawnDisplayJSONObject(decoded) {
		return text
	}
	return strings.TrimSpace(remainder)
}

func SanitizeSpawnHeaderArgs(args string) string {
	args = strings.TrimSpace(args)
	if strings.EqualFold(args, "SPAWN") {
		return ""
	}
	for _, prefix := range []string{"SPAWN ", "spawn "} {
		if strings.HasPrefix(args, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(args, prefix))
		}
	}
	return args
}

func ToolTaskID(input map[string]any, output map[string]any, meta map[string]any) string {
	return firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "target_id"),
		MapString(output, "handle"),
		MapString(output, "task_id"),
		MapString(input, "task_id"),
	)
}

func ToolTaskAction(input map[string]any, output map[string]any, meta map[string]any) string {
	return strings.ToLower(firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "action"),
		MapString(output, "action"),
		MapString(input, "action"),
	))
}

func ToolTaskInput(input map[string]any, output map[string]any, meta map[string]any) string {
	return firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "input"),
		MapString(output, "input"),
		MapString(input, "input"),
	)
}

func ToolTaskTargetKind(input map[string]any, output map[string]any, meta map[string]any) string {
	return strings.ToLower(firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "target_kind"),
		MapString(output, "target_kind"),
		MapString(output, "kind"),
		MapString(input, "target_kind"),
	))
}

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

func NormalizeDisplayArg(input string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(input, "\r\n", "\n"), "\r", "\n"))
}

func MapString(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func MetaString(meta map[string]any, path ...string) string {
	var current any = meta
	for _, key := range path {
		obj, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current, ok = obj[key]
		if !ok {
			return ""
		}
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
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
