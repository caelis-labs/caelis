package evalharness

import (
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/charmbracelet/x/ansi"
)

type EventTraceEntry struct {
	Visibility       string          `json:"visibility,omitempty"`
	Type             string          `json:"type,omitempty"`
	Role             string          `json:"role,omitempty"`
	Text             string          `json:"text,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	MessageToolCalls []ToolCallTrace `json:"message_tool_calls,omitempty"`
	Tool             *ToolTrace      `json:"tool,omitempty"`
	Notice           *NoticeTrace    `json:"notice,omitempty"`
}

type ToolCallTrace struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Args string `json:"args,omitempty"`
}

type ToolTrace struct {
	ID      string         `json:"id,omitempty"`
	Name    string         `json:"name,omitempty"`
	Status  string         `json:"status,omitempty"`
	Input   map[string]any `json:"input,omitempty"`
	Output  map[string]any `json:"output,omitempty"`
	Content []ContentTrace `json:"content,omitempty"`
}

type ContentTrace struct {
	Type       string `json:"type,omitempty"`
	Text       string `json:"text,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
	Path       string `json:"path,omitempty"`
	NewText    string `json:"new_text,omitempty"`
}

type NoticeTrace struct {
	Level string `json:"level,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Text  string `json:"text,omitempty"`
}

func EventTrace(events []*session.Event) []EventTraceEntry {
	out := make([]EventTraceEntry, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		entry := EventTraceEntry{
			Visibility: visibilityTraceValue(event.Visibility),
			Type:       string(event.Type),
			Text:       strings.TrimSpace(session.EventText(event)),
		}
		if message, ok := session.ModelMessageOf(event); ok {
			entry.Role = string(message.Role)
			entry.Reasoning = strings.TrimSpace(reasoningText(message))
			entry.MessageToolCalls = toolCallTrace(message.ToolCalls())
		}
		if toolPayload := session.EventToolProjection(event); toolPayload != nil {
			entry.Tool = &ToolTrace{
				ID:      strings.TrimSpace(toolPayload.ID),
				Name:    strings.TrimSpace(toolPayload.Name),
				Status:  strings.TrimSpace(toolPayload.Status),
				Input:   cloneAnyMap(toolPayload.Input),
				Output:  cloneAnyMap(toolPayload.Output),
				Content: contentTrace(toolPayload.Content),
			}
		}
		if notice, ok := session.NoticeOf(event); ok {
			entry.Notice = &NoticeTrace{
				Level: strings.TrimSpace(notice.Level),
				Kind:  strings.TrimSpace(notice.Kind),
				Text:  strings.TrimSpace(notice.Text),
			}
		}
		out = append(out, entry)
	}
	return out
}

func CanonicalEvents(events []*session.Event) []*session.Event {
	var out []*session.Event
	for _, event := range events {
		if event == nil || event.Visibility == session.VisibilityUIOnly {
			continue
		}
		out = append(out, session.CanonicalizeEvent(event))
	}
	return out
}

func NormalizeFrame(frame string) string {
	frame = ansi.Strip(frame)
	frame = strings.ReplaceAll(frame, "\r\n", "\n")
	frame = strings.ReplaceAll(frame, "\r", "\n")
	lines := strings.Split(frame, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

func cloneSessionEvent(event *session.Event) *session.Event {
	return session.CloneEvent(event)
}

func visibilityTraceValue(visibility session.Visibility) string {
	if visibility == "" || visibility == session.VisibilityCanonical {
		return "canonical"
	}
	return string(visibility)
}

func reasoningText(message model.Message) string {
	var parts []string
	for _, part := range message.Parts {
		if part.Reasoning == nil {
			continue
		}
		text := ""
		if part.Reasoning.VisibleText != nil {
			text = strings.TrimSpace(*part.Reasoning.VisibleText)
		}
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func toolCallTrace(calls []model.ToolCall) []ToolCallTrace {
	if len(calls) == 0 {
		return nil
	}
	out := make([]ToolCallTrace, 0, len(calls))
	for _, call := range calls {
		out = append(out, ToolCallTrace{
			ID:   strings.TrimSpace(call.ID),
			Name: strings.TrimSpace(call.Name),
			Args: strings.TrimSpace(call.Args),
		})
	}
	return out
}

func contentTrace(content []session.EventToolContent) []ContentTrace {
	if len(content) == 0 {
		return nil
	}
	out := make([]ContentTrace, 0, len(content))
	for _, item := range content {
		out = append(out, ContentTrace{
			Type:       strings.TrimSpace(item.Type),
			Text:       strings.TrimSpace(item.Text),
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			NewText:    strings.TrimSpace(item.NewText),
		})
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
