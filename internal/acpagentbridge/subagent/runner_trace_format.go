package subagent

import (
	"fmt"
	"path/filepath"
	"strings"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	acpschema "github.com/caelis-labs/caelis/protocol/acp/schema"
)

func childNarrativeTraceText(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return text
}

func childToolCallTraceText(update client.ToolCall) string {
	line := childToolTraceLine(update.Title, update.Kind, update.RawInput, update.Status)
	return normalizeTraceLine(line)
}

func childToolCallUpdateTraceText(update client.ToolCallUpdate) string {
	if childToolUpdateStatusIsQuiet(derefString(update.Status)) {
		return ""
	}
	if childToolUpdateHasTerminalOutput(update) {
		return ""
	}
	line := childToolTraceLine(derefString(update.Title), derefString(update.Kind), update.RawInput, derefString(update.Status))
	return normalizeTraceLine(line)
}

func childToolTraceLine(title string, kind string, rawInput any, status string) string {
	label, summarized := childToolDisplayLabel(title, kind, rawInput)
	if label == "" {
		label = "Tool"
	}
	if !summarized {
		if summary := childToolInputSummary(rawInput); shouldAppendChildToolSummary(label, summary, title, kind) {
			label += " " + summary
		}
	}
	if suffix := childToolStatusSuffix(status); suffix != "" && !containsFold(label, suffix) {
		label += " " + suffix
	}
	return label
}

func childToolUpdateHasTerminalOutput(update client.ToolCallUpdate) bool {
	return childTerminalContentText(update.Content) != ""
}

func childTerminalContentText(content []client.ToolCallContent) string {
	var out strings.Builder
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		out.WriteString(acpschema.ExtractTextValue(item.Content))
	}
	return out.String()
}

func childToolUpdateStatusIsQuiet(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "succeeded", "success", "done":
		return true
	default:
		return false
	}
}

func childToolDisplayLabel(title string, kind string, rawInput any) (string, bool) {
	title = strings.TrimSpace(title)
	name, detail := splitChildToolTitle(title)
	if name == "" {
		name = childToolNameFromKind(kind)
	}
	if name == "" {
		return childToolLabel(title, kind), false
	}
	if summary := childToolInputSummaryForName(name, rawInput); summary != "" {
		return strings.TrimSpace(name + " " + summary), true
	}
	if detail = compactChildToolDetail(name, detail); detail != "" {
		return strings.TrimSpace(name + " " + detail), true
	}
	if title != "" && !strings.EqualFold(name, title) {
		return childToolLabel(title, kind), false
	}
	return name, false
}

func splitChildToolTitle(title string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(title))
	if len(fields) == 0 {
		return "", ""
	}
	name := childCanonicalToolName(fields[0])
	if name == "" {
		return "", ""
	}
	return name, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(title), fields[0]))
}

func childToolNameFromKind(kind string) string {
	return childCanonicalToolName(kind)
}

func childCanonicalToolName(name string) string {
	if canonical, ok := names.Resolve(name); ok {
		return canonical
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "rg":
		return "RG"
	case "find":
		return "FIND"
	default:
		return ""
	}
}

func childToolLabel(title string, kind string) string {
	if text := strings.TrimSpace(title); text != "" {
		return text
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return ""
	}
	switch strings.ToLower(kind) {
	case "think":
		return "Think"
	case "execute":
		return "Run"
	}
	parts := strings.FieldsFunc(kind, func(r rune) bool {
		return r == '_' || r == '-' || r == ' '
	})
	if len(parts) == 0 {
		return kind
	}
	for i, part := range parts {
		if part == "" {
			continue
		}
		parts[i] = strings.ToUpper(part[:1]) + strings.ToLower(part[1:])
	}
	return strings.Join(parts, " ")
}

func childToolStatusSuffix(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "succeeded", "success":
		return "completed"
	case "failed", "error":
		return "failed"
	case "cancelled", "canceled":
		return "cancelled"
	case "interrupted":
		return "interrupted"
	default:
		return ""
	}
}

func childToolInputSummary(raw any) string {
	values := acpschema.NormalizeRawMap(raw)
	if len(values) == 0 {
		return ""
	}
	if query := rawValueString(values, "query"); query != "" {
		if scope := firstNonEmpty(rawValueString(values, "path"), rawValueString(values, "cwd"), rawValueString(values, "directory")); scope != "" {
			return truncateTraceText(quoteTraceValue(query)+" in "+scope, 140)
		}
		return truncateTraceText(quoteTraceValue(query), 140)
	}
	if pattern := rawValueString(values, "pattern"); pattern != "" {
		if scope := firstNonEmpty(rawValueString(values, "path"), rawValueString(values, "cwd"), rawValueString(values, "directory")); scope != "" {
			return truncateTraceText(quoteTraceValue(pattern)+" in "+scope, 140)
		}
		return truncateTraceText(quoteTraceValue(pattern), 140)
	}
	for _, key := range []string{"path", "file_path", "filePath", "uri", "url", "command", "cmd"} {
		if value := rawValueString(values, key); value != "" {
			return truncateTraceText(value, 140)
		}
	}
	if prompt := rawValueString(values, "prompt"); prompt != "" {
		if agent := rawValueString(values, "agent"); agent != "" {
			return truncateTraceText(agent+": "+prompt, 140)
		}
		return truncateTraceText(prompt, 140)
	}
	if text := rawValueString(values, "text"); text != "" {
		return truncateTraceText(text, 140)
	}
	return ""
}

func childToolInputSummaryForName(name string, raw any) string {
	values := acpschema.NormalizeRawMap(raw)
	if len(values) == 0 {
		return ""
	}
	name = names.CanonicalOrSelf(name)
	switch name {
	case names.Read:
		if path := childRawPath(values); path != "" {
			return childDisplayPathBase(path)
		}
	case names.List:
		if path := firstNonEmpty(childRawPath(values), rawValueString(values, "cwd"), rawValueString(values, "directory")); path != "" {
			return childDisplayPathBase(path)
		}
	case names.Glob:
		pattern := rawValueString(values, "pattern")
		scope := firstNonEmpty(childRawPath(values), rawValueString(values, "cwd"), rawValueString(values, "directory"))
		switch {
		case pattern != "" && scope != "":
			return truncateTraceText(pattern+" in "+childDisplayPathBase(scope), 80)
		case pattern != "":
			return truncateTraceText(pattern, 80)
		case scope != "":
			return childDisplayPathBase(scope)
		}
	case names.Grep, "RG", "FIND":
		query := firstNonEmpty(rawValueString(values, "query"), rawValueString(values, "pattern"), rawValueString(values, "text"))
		scope := firstNonEmpty(childRawPath(values), rawValueString(values, "cwd"), rawValueString(values, "directory"))
		switch {
		case query != "" && scope != "":
			return truncateTraceText(quoteTraceValue(query)+" in "+childDisplayPathBase(scope), 100)
		case query != "":
			return truncateTraceText(quoteTraceValue(query), 100)
		case scope != "":
			return childDisplayPathBase(scope)
		}
	case names.Write, names.Patch:
		if path := childRawPath(values); path != "" {
			return childDisplayPathBase(path)
		}
	case names.WebSearch:
		if query := rawValueString(values, "query"); query != "" {
			return truncateTraceText(quoteTraceValue(query), 100)
		}
	case names.WebFetch:
		if url := rawValueString(values, "url"); url != "" {
			return truncateTraceText(url, 100)
		}
	}
	return ""
}

func compactChildToolDetail(name string, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	name = names.CanonicalOrSelf(name)
	switch name {
	case names.Read, names.List, names.Write, names.Patch:
		if path, rest, ok := childLeadingPath(detail); ok {
			return strings.TrimSpace(childDisplayPathBase(path) + rest)
		}
	case names.Glob, names.Grep, "RG", "FIND":
		if before, after, ok := strings.Cut(detail, " in "); ok {
			if path, rest, pathOK := childLeadingPath(after); pathOK {
				return strings.TrimSpace(before + " in " + childDisplayPathBase(path) + rest)
			}
		}
		if path, rest, ok := childLeadingPath(detail); ok {
			return strings.TrimSpace(childDisplayPathBase(path) + rest)
		}
	}
	return truncateTraceText(detail, 100)
}

func childRawPath(values map[string]any) string {
	return firstNonEmpty(
		rawValueString(values, "path"),
		rawValueString(values, "file_path"),
		rawValueString(values, "filePath"),
		rawValueString(values, "filepath"),
		rawValueString(values, "target"),
		rawValueString(values, "source"),
	)
}

func childLeadingPath(text string) (string, string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", "", false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return "", "", false
	}
	if !childLooksLikePath(fields[0]) {
		return "", "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	if rest != "" {
		rest = " " + rest
	}
	return fields[0], rest, true
}

func childLooksLikePath(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && (strings.ContainsAny(value, `/\`) || strings.HasPrefix(value, "."))
}

func childDisplayPathBase(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	trimmed := strings.TrimRight(path, `/\`)
	if trimmed == "" {
		return path
	}
	if strings.Contains(trimmed, `\`) {
		parts := strings.FieldsFunc(trimmed, func(r rune) bool { return r == '\\' || r == '/' })
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return filepath.Base(trimmed)
}

func shouldAppendChildToolSummary(label string, summary string, title string, kind string) bool {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false
	}
	if containsFold(label, summary) || containsFold(label, strings.Trim(summary, `"`)) {
		return false
	}
	if strings.TrimSpace(title) != "" && strings.EqualFold(strings.TrimSpace(kind), "execute") {
		return false
	}
	return true
}

func rawValueString(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	value, ok := values[key]
	if !ok {
		return ""
	}
	text := acpschema.ExtractTextValue(value)
	if text == "" {
		text = strings.TrimSpace(fmt.Sprint(value))
	}
	if text == "<nil>" {
		return ""
	}
	return strings.TrimSpace(text)
}

func quoteTraceValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.ContainsAny(value, " \t") && !strings.HasPrefix(value, "\"") {
		return `"` + value + `"`
	}
	return value
}

func truncateTraceText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "..."
}

func containsFold(text string, part string) bool {
	text = strings.ToLower(strings.TrimSpace(text))
	part = strings.ToLower(strings.TrimSpace(part))
	return text != "" && part != "" && strings.Contains(text, part)
}

func normalizeTraceLine(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return text + "\n"
}
