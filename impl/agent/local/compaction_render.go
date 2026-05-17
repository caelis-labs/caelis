package local

import (
	"fmt"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func renderCheckpointCompactionInput(
	baseText string,
	events []*session.Event,
) string {
	var b strings.Builder
	if strings.TrimSpace(baseText) != "" {
		b.WriteString("# Existing Compact Checkpoint (reference only)\n")
		b.WriteString(strings.TrimSpace(baseText))
		b.WriteString("\n\n")
	}
	b.WriteString("# Event Replay Since Last Compact\n")
	for _, event := range events {
		line := renderCompactionEvent(event)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

func normalizeCompactMarkdown(text string) string {
	text = strings.TrimSpace(text)
	text = strings.TrimPrefix(text, "```markdown")
	text = strings.TrimPrefix(text, "```md")
	text = strings.TrimPrefix(text, "```")
	text = strings.TrimSuffix(text, "```")
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToUpper(text), "CONTEXT CHECKPOINT") {
		text = "CONTEXT CHECKPOINT\n\n" + text
	}
	return strings.TrimSpace(text)
}

func renderCompactionEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	text := eventTextForCompaction(event)
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return renderCompactionBlock("User Message", compactText(text, 4000))
	case session.EventTypeAssistant:
		return renderCompactionBlock("Assistant Message", compactText(text, 5000))
	case session.EventTypePlan:
		return renderPlanEventForCompaction(event, text)
	case session.EventTypeToolCall:
		if event.Tool != nil {
			return renderToolPayloadForCompaction("Tool Call", event, event.Tool.Input, 2000)
		}
		if update := session.ProtocolUpdateOf(event); update != nil {
			return renderToolEventForCompaction("Tool Call", event, update, update.RawInput, 2000)
		}
	case session.EventTypeToolResult:
		if event.Tool != nil {
			return renderToolPayloadForCompaction("Tool Result", event, event.Tool.Output, 3500)
		}
		if update := session.ProtocolUpdateOf(event); update != nil {
			return renderToolEventForCompaction("Tool Result", event, update, update.RawOutput, 3500)
		}
		return renderCompactionBlock("Tool Result", compactText(text, 3500))
	case session.EventTypeParticipant:
		if event.Meta != nil {
			return renderCompactionBlock("Participant Update", compactText(renderCompactionValue(event.Meta, 1600), 1800))
		}
	}
	return renderCompactionBlock("Event", compactText(text, 1800))
}

func renderCompactionBlock(title string, body string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Event"
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "## " + title
	}
	return "## " + title + "\n" + body
}

func renderToolEventForCompaction(kind string, event *session.Event, update *session.ProtocolUpdate, payload map[string]any, limit int) string {
	toolName := toolNameForCompaction(event, update)
	lines := []string{}
	if toolName != "" {
		lines = append(lines, "- tool: "+toolName)
	}
	if update != nil {
		if title := strings.TrimSpace(update.Title); title != "" && !strings.EqualFold(title, toolName) {
			lines = append(lines, "- title: "+title)
		}
		if status := strings.TrimSpace(update.Status); status != "" {
			lines = append(lines, "- status: "+status)
		}
		if text := textFromProtocolContent(update.Content); text != "" {
			lines = append(lines, "- content: "+compactText(text, 1200))
		}
	}
	if len(payload) > 0 {
		if rendered := renderCompactionMap(payload, limit); rendered != "" {
			lines = append(lines, "", rendered)
		}
	} else if text := eventTextForCompaction(event); text != "" {
		lines = append(lines, "", compactText(text, limit))
	}
	return renderCompactionBlock(kind, strings.Join(lines, "\n"))
}

func renderToolPayloadForCompaction(kind string, event *session.Event, payload map[string]any, limit int) string {
	toolName := toolNameForCompaction(event, nil)
	lines := []string{}
	if toolName != "" {
		lines = append(lines, "- tool: "+toolName)
	}
	if event != nil && event.Tool != nil {
		if title := strings.TrimSpace(event.Tool.Title); title != "" && !strings.EqualFold(title, toolName) {
			lines = append(lines, "- title: "+title)
		}
		if status := strings.TrimSpace(event.Tool.Status); status != "" {
			lines = append(lines, "- status: "+status)
		}
		if text := textFromEventToolContent(event.Tool.Content); text != "" {
			lines = append(lines, "- content: "+compactText(text, 1200))
		}
	}
	if len(payload) > 0 {
		if rendered := renderCompactionMap(payload, limit); rendered != "" {
			lines = append(lines, "", rendered)
		}
	} else if text := eventTextForCompaction(event); text != "" {
		lines = append(lines, "", compactText(text, limit))
	}
	return renderCompactionBlock(kind, strings.Join(lines, "\n"))
}

func renderCompactionValue(value any, limit int) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return compactText(typed, limit)
	case map[string]any:
		return renderCompactionMap(typed, limit)
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			text := renderCompactionValue(item, max(limit/2, 200))
			if text == "" {
				continue
			}
			items = append(items, "- "+strings.ReplaceAll(text, "\n", "\n  "))
		}
		return compactText(strings.Join(items, "\n"), limit)
	default:
		return compactText(stringifyAny(value), limit)
	}
}

func renderCompactionMap(values map[string]any, limit int) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if strings.TrimSpace(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		text := renderCompactionValue(values[key], max(limit/len(keys), 240))
		if text == "" {
			continue
		}
		if strings.Contains(text, "\n") {
			lines = append(lines, key+":\n  "+strings.ReplaceAll(text, "\n", "\n  "))
		} else {
			lines = append(lines, key+": "+text)
		}
	}
	return compactText(strings.Join(lines, "\n"), limit)
}

func textFromProtocolContent(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return strings.TrimSpace(text)
		}
		if content, ok := typed["content"].(string); ok {
			return strings.TrimSpace(content)
		}
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := textFromProtocolContent(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case []session.ProtocolToolCallContent:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := textFromProtocolContent(item.Content); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return ""
}

func textFromEventToolContent(content []session.EventToolContent) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if text := strings.TrimSpace(item.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func toolNameForCompaction(event *session.Event, update *session.ProtocolUpdate) string {
	if event != nil {
		if runtimeMeta := nestedMap(event.Meta, "caelis", "runtime", "tool"); len(runtimeMeta) > 0 {
			if name := strings.TrimSpace(stringifyAny(runtimeMeta["name"])); name != "" {
				return name
			}
		}
		if event.Tool != nil {
			if name := strings.TrimSpace(event.Tool.Name); name != "" {
				return name
			}
		}
		if event.Protocol != nil && event.Protocol.ToolCall != nil {
			if name := strings.TrimSpace(event.Protocol.ToolCall.Name); name != "" {
				return name
			}
		}
	}
	if update != nil {
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			return title[0]
		}
		if kind := strings.TrimSpace(update.Kind); kind != "" {
			return kind
		}
	}
	return "tool"
}

func renderPlanEventForCompaction(event *session.Event, fallback string) string {
	lines := make([]string, 0, 8)
	if text := strings.TrimSpace(fallback); text != "" {
		lines = append(lines, compactText(text, 1000))
	}
	for _, entry := range planEntriesForCompaction(event) {
		content := strings.TrimSpace(entry.Content)
		if content == "" {
			continue
		}
		status := strings.TrimSpace(entry.Status)
		if status == "" {
			status = "unknown"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s", status, content))
	}
	if len(lines) == 0 {
		return renderCompactionBlock("Plan Update", "")
	}
	return renderCompactionBlock("Plan Update", strings.Join(lines, "\n"))
}

func planEntriesForCompaction(event *session.Event) []session.ProtocolPlanEntry {
	if event == nil || event.Protocol == nil {
		return nil
	}
	if event.Protocol.Plan != nil && len(event.Protocol.Plan.Entries) > 0 {
		return event.Protocol.Plan.Entries
	}
	if event.Protocol.Update != nil && len(event.Protocol.Update.Entries) > 0 {
		return event.Protocol.Update.Entries
	}
	return nil
}
