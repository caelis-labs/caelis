package tuiapp

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/charmbracelet/x/ansi"
)

func summarizeSubagentTerminalPanelText(text string, final bool) string {
	if final {
		// A completed Final Message is canonical Markdown, not a lifecycle
		// preview. Do not filter blank lines, repeated text, fences, or rules.
		return display.CleanSubagentFinalOutput(text)
	}
	lines := subagentTerminalSignalLines(text, final)
	if len(lines) == 0 {
		return ""
	}
	if !final && len(lines) > acpTerminalPanelMaxLines {
		lines = lines[len(lines)-acpTerminalPanelMaxLines:]
	}
	return strings.Join(lines, "\n")
}

func subagentTerminalSignalLines(text string, final bool) []string {
	text = sanitizeRenderableText(text)
	rawLines := splitRenderableLines(text)
	lines := make([]string, 0, len(rawLines))
	seen := map[string]struct{}{}
	toolLineIndex := map[string]int{}
	for _, raw := range rawLines {
		line, ok := cleanSubagentTerminalPreviewLine(raw, final)
		if !ok || line == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(line), "completed") {
			continue
		}
		if signal, ok := parseSubagentTerminalToolSignalLine(line); ok {
			if idx, exists := toolLineIndex[signal.Key]; exists {
				if signal.Status == "failed" {
					lines[idx] = signal.Display
				}
				continue
			}
			toolLineIndex[signal.Key] = len(lines)
			lines = append(lines, signal.Display)
			continue
		}
		key := normalizeSubagentTerminalPreviewLineKey(line)
		if key != "" {
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
		}
		lines = append(lines, line)
	}
	return lines
}

type subagentTerminalToolSignalLine struct {
	Key     string
	Display string
	Status  string
}

func parseSubagentTerminalToolSignalLine(line string) (subagentTerminalToolSignalLine, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return subagentTerminalToolSignalLine{}, false
	}
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return subagentTerminalToolSignalLine{}, false
	}
	rawName := fields[0]
	displayName := toolSignalDisplayVerb(rawName)
	if displayName == "" {
		return subagentTerminalToolSignalLine{}, false
	}
	detail := strings.TrimSpace(strings.TrimPrefix(line, rawName))
	status := ""
	for _, candidate := range []string{"completed", "failed"} {
		if strings.EqualFold(detail, candidate) {
			detail = ""
			status = candidate
			break
		}
		suffix := " " + candidate
		if len(detail) > len(suffix) && strings.HasSuffix(strings.ToLower(detail), suffix) {
			detail = strings.TrimSpace(detail[:len(detail)-len(suffix)])
			status = candidate
			break
		}
	}
	detail = compactSubagentTerminalToolSignalDetail(rawName, detail)
	display := displayName
	if detail != "" {
		display += " " + detail
	}
	if status == "failed" {
		display += " failed"
	}
	identityName := names.CanonicalOrSelf(rawName)
	key := identityName + "\x00" + normalizeSubagentTerminalPreviewLineKey(detail)
	if key == "\x00" {
		key = identityName
	}
	return subagentTerminalToolSignalLine{
		Key:     key,
		Display: display,
		Status:  status,
	}, true
}

func compactSubagentTerminalToolSignalDetail(name string, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	info, known := names.Lookup(name)
	pathLike := known && info.TitleStyle == names.TitlePath
	if !pathLike {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "rg", "find":
			pathLike = true
		}
	}
	if pathLike {
		if pathPart, rest, ok, _ := splitLeadingPathHeaderParts(detail); ok && isLikelyDisplayPath(pathPart) {
			if compact := compactPathDisplay(pathPart); compact != "" {
				return compact + rest
			}
		}
	}
	return detail
}

func cleanSubagentTerminalPreviewLine(raw string, final bool) (string, bool) {
	line := strings.TrimSpace(ansi.Strip(sanitizeRenderableText(raw)))
	if line == "" {
		return "", true
	}
	if preview, ok := subagentTerminalJSONPreviewLine(line); ok {
		line = strings.TrimSpace(preview)
		if line == "" {
			return "", true
		}
	}
	if isSubagentTerminalProtocolNoise(line) {
		return "", true
	}
	if final {
		line = strings.TrimSpace(display.CleanSubagentFinalOutput(line))
	}
	line = strings.TrimSpace(line)
	return line, line != ""
}

func subagentTerminalJSONPreviewLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil || len(payload) == 0 {
		return "", false
	}
	for _, key := range []string{"error", "final_message", "finalMessage", "message", "summary", "result", "output", "text"} {
		value := strings.TrimSpace(asString(payload[key]))
		if value == "" || isSubagentTerminalProtocolValue(value) {
			continue
		}
		if key == "error" {
			return "error: " + value, true
		}
		return value, true
	}
	return "", true
}

func isSubagentTerminalProtocolNoise(line string) bool {
	trimmed := strings.TrimSpace(line)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
		return true
	}
	if isSubagentMarkdownRuleLine(trimmed) || isSubagentMarkdownTableRuleLine(trimmed) {
		return true
	}
	if strings.HasPrefix(lower, "data: {") ||
		strings.HasPrefix(lower, "event: ") ||
		strings.HasPrefix(lower, "jsonrpc") ||
		strings.Contains(lower, "session/update") ||
		strings.Contains(lower, "tool_call") ||
		strings.Contains(lower, "tool_result") ||
		strings.Contains(lower, "terminal_id") ||
		strings.Contains(lower, "supports_input") ||
		strings.Contains(lower, "supports_cancel") {
		return true
	}
	return false
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

func isSubagentMarkdownTableRuleLine(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return false
	}
	body := strings.Trim(line, "| ")
	if body == "" {
		return false
	}
	for _, r := range body {
		if r != '-' && r != ':' && r != '|' && r != ' ' && r != '\t' {
			return false
		}
	}
	return true
}

func isSubagentTerminalProtocolValue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "running", "completed", "pending", "started", "done", "tool_call", "tool_result", "session_update":
		return true
	default:
		return false
	}
}

func normalizeSubagentTerminalPreviewLineKey(line string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(line)), " "))
}
