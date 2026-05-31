// Package projector projects core/session canonical events into ACP wire updates.
package projector

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type Update = schema.Update
type SessionNotification = schema.SessionNotification
type RequestPermissionRequest = schema.RequestPermissionRequest

type Projector struct{}

func (Projector) ProjectEvent(event session.Event) ([]Update, error) {
	if event.Approval != nil && event.Approval.Status == session.ApprovalPending {
		return nil, nil
	}
	switch event.Type {
	case session.EventUser:
		return textUpdate(schema.UpdateUserMessage, session.EventText(event)), nil
	case session.EventAssistant:
		return assistantUpdates(event), nil
	case session.EventToolCall:
		if event.Tool == nil {
			return nil, nil
		}
		return []Update{toolCall(*event.Tool)}, nil
	case session.EventToolResult:
		if event.Tool == nil {
			return nil, nil
		}
		update := toolCallUpdate(*event.Tool)
		return []Update{update}, nil
	case session.EventPlan:
		if len(event.Plan) == 0 {
			return nil, nil
		}
		return []Update{planUpdate(event.Plan)}, nil
	default:
		return nil, nil
	}
}

func (p Projector) ProjectNotifications(event session.Event) ([]SessionNotification, error) {
	updates, err := p.ProjectEvent(event)
	if err != nil || len(updates) == 0 {
		return nil, err
	}
	out := make([]SessionNotification, 0, len(updates))
	for _, update := range updates {
		if update == nil {
			continue
		}
		out = append(out, SessionNotification{
			SessionID: strings.TrimSpace(event.SessionID),
			Update:    update,
		})
	}
	return out, nil
}

func (Projector) ProjectPermissionRequest(event session.Event) (*RequestPermissionRequest, bool, error) {
	if event.Approval == nil || event.Approval.Status != session.ApprovalPending {
		return nil, false, nil
	}
	tool := session.ToolEvent{}
	if event.Approval.Tool != nil {
		tool = *event.Approval.Tool
	} else if event.Tool != nil {
		tool = *event.Tool
	}
	options := make([]schema.PermissionOption, 0, len(event.Approval.Options))
	for _, item := range event.Approval.Options {
		options = append(options, schema.PermissionOption{
			OptionID: strings.TrimSpace(item.ID),
			Name:     strings.TrimSpace(item.Name),
			Kind:     strings.TrimSpace(item.Kind),
		})
	}
	update := toolCallUpdate(tool)
	return &schema.RequestPermissionRequest{
		SessionID: strings.TrimSpace(event.SessionID),
		ToolCall:  update,
		Options:   options,
	}, true, nil
}

func assistantUpdates(event session.Event) []Update {
	if event.Message == nil {
		return textUpdate(schema.UpdateAgentMessage, session.EventText(event))
	}
	out := make([]Update, 0, 2)
	if reasoning := reasoningText(*event.Message); reasoning != "" {
		out = append(out, schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentThought,
			Content:       schema.TextContent{Type: "text", Text: reasoning},
		})
	}
	if text := textContent(*event.Message); text != "" {
		out = append(out, schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
		})
	}
	return out
}

func textUpdate(kind string, text string) []Update {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return []Update{schema.ContentChunk{
		SessionUpdate: kind,
		Content:       schema.TextContent{Type: "text", Text: text},
	}}
}

func reasoningText(message model.Message) string {
	var parts []string
	for _, part := range message.Parts {
		if part.Kind == model.PartReasoning && part.Reasoning != nil {
			if text := strings.TrimSpace(part.Reasoning.VisibleText); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func textContent(message model.Message) string {
	return strings.TrimSpace(message.TextContent())
}

func toolCall(tool session.ToolEvent) schema.ToolCall {
	return schema.ToolCall{
		SessionUpdate: schema.UpdateToolCall,
		ToolCallID:    strings.TrimSpace(tool.ID),
		Title:         toolTitle(tool),
		Kind:          toolKind(tool),
		Status:        toolStatus(tool.Status),
		RawInput:      tool.Input,
		Content:       toolContentWithTerminal(tool),
		Locations:     toolLocations(tool.Locations),
		Meta:          toolMeta(tool),
	}
}

func toolCallUpdate(tool session.ToolEvent) schema.ToolCallUpdate {
	title := toolTitle(tool)
	kind := toolKind(tool)
	status := toolStatus(tool.Status)
	return schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(tool.ID),
		Title:         stringPtr(title),
		Kind:          stringPtr(kind),
		Status:        stringPtr(status),
		RawInput:      tool.Input,
		RawOutput:     tool.Output,
		Content:       toolContentWithTerminal(tool),
		Locations:     toolLocations(tool.Locations),
		Meta:          toolMeta(tool),
	}
}

func planUpdate(entries []session.PlanEntry) schema.PlanUpdate {
	out := make([]schema.PlanEntry, 0, len(entries))
	for _, item := range entries {
		out = append(out, schema.PlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: "",
		})
	}
	return schema.PlanUpdate{
		SessionUpdate: schema.UpdatePlan,
		Entries:       out,
	}
}

func toolTitle(tool session.ToolEvent) string {
	if title := strings.TrimSpace(tool.Title); title != "" {
		return title
	}
	if name := strings.TrimSpace(tool.Name); name != "" {
		return name
	}
	return "tool"
}

func toolKind(tool session.ToolEvent) string {
	switch strings.ToLower(strings.TrimSpace(firstNonEmpty(tool.Kind, tool.Name))) {
	case "read", "read_file":
		return schema.ToolKindRead
	case "write", "write_file", "patch", "edit":
		return schema.ToolKindEdit
	case "delete", "remove":
		return schema.ToolKindDelete
	case "move", "rename":
		return schema.ToolKindMove
	case "search", "grep", "glob":
		return schema.ToolKindSearch
	case "shell", "bash", "run_command", "exec":
		return schema.ToolKindExecute
	case "plan", "think":
		return schema.ToolKindThink
	case "fetch", "web":
		return schema.ToolKindFetch
	case "switch", "handoff":
		return schema.ToolKindSwitch
	default:
		return schema.ToolKindOther
	}
}

func toolStatus(status session.ToolStatus) string {
	switch status {
	case session.ToolStarted:
		return schema.ToolStatusPending
	case session.ToolRunning, session.ToolWaitingApproval:
		return schema.ToolStatusInProgress
	case session.ToolCompleted:
		return schema.ToolStatusCompleted
	case session.ToolFailed, session.ToolCancelled:
		return schema.ToolStatusFailed
	default:
		return schema.ToolStatusPending
	}
}

func toolContent(in []session.ToolContent) []schema.ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    strings.TrimSpace(item.Text),
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
		})
	}
	return out
}

func toolContentWithTerminal(tool session.ToolEvent) []schema.ToolCallContent {
	out := toolContent(tool.Content)
	terminalID := terminalIDFromTool(tool)
	if terminalID == "" || hasTerminalContent(out) {
		return out
	}
	return append(out, schema.ToolCallContent{
		Type:       "terminal",
		TerminalID: terminalID,
	})
}

func hasTerminalContent(content []schema.ToolCallContent) bool {
	for _, item := range content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			return true
		}
	}
	return false
}

func toolMeta(tool session.ToolEvent) map[string]any {
	meta := maps.Clone(tool.Meta)
	terminalID := terminalIDFromTool(tool)
	if terminalID == "" {
		return meta
	}
	if meta == nil {
		meta = map[string]any{}
	}
	if _, ok := meta["terminal_info"]; !ok {
		info := map[string]any{"terminal_id": terminalID}
		if taskID := firstNonEmpty(anyString(tool.Output["task_id"]), anyString(runtimeTaskValue(tool.Meta, "task_id"))); taskID != "" {
			info["task_id"] = taskID
		}
		if name := strings.TrimSpace(tool.Name); name != "" {
			info["tool"] = name
		}
		if command := firstNonEmpty(anyString(tool.Input["command"]), anyString(tool.Output["command"])); command != "" {
			info["command"] = command
		}
		if cwd := firstNonEmpty(anyString(tool.Input["cwd"]), anyString(tool.Input["workdir"])); cwd != "" {
			info["cwd"] = cwd
		}
		meta["terminal_info"] = info
	}
	if output := terminalOutputText(tool); output != "" {
		meta["terminal_output"] = map[string]any{
			"terminal_id": terminalID,
			"data":        output,
		}
	}
	if exitCode, ok := terminalExitCode(tool); ok {
		meta["terminal_exit"] = map[string]any{
			"terminal_id": terminalID,
			"exit_code":   exitCode,
		}
	}
	return meta
}

func terminalIDFromTool(tool session.ToolEvent) string {
	for _, item := range tool.Content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
				return terminalID
			}
		}
	}
	return firstNonEmpty(
		anyString(tool.Output["terminal_id"]),
		anyString(tool.Output["task_id"]),
		anyString(runtimeTaskValue(tool.Meta, "terminal_id")),
		anyString(runtimeTaskValue(tool.Meta, "task_id")),
	)
}

func terminalOutputText(tool session.ToolEvent) string {
	var parts []string
	if stdout := anyText(tool.Output["stdout"]); stdout != "" {
		parts = append(parts, stdout)
	}
	if stderr := anyText(tool.Output["stderr"]); stderr != "" {
		parts = append(parts, stderr)
	}
	for _, item := range tool.Content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if text := strings.TrimRight(item.Text, "\n"); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func terminalExitCode(tool session.ToolEvent) (int, bool) {
	if code, ok := anyInt(tool.Output["exit_code"]); ok {
		return code, true
	}
	return anyInt(runtimeTaskValue(tool.Meta, "exit_code"))
}

func runtimeTaskValue(meta map[string]any, key string) any {
	if len(meta) == 0 {
		return nil
	}
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	task, _ := runtimeMeta["task"].(map[string]any)
	if len(task) == 0 {
		return nil
	}
	return task[strings.TrimSpace(key)]
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return strings.TrimSpace(typed.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func anyText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func anyInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return int(n), true
		}
	case string:
		var n json.Number = json.Number(strings.TrimSpace(typed))
		if parsed, err := n.Int64(); err == nil {
			return int(parsed), true
		}
	}
	return 0, false
}

func toolLocations(in []session.ToolLocation) []schema.ToolCallLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallLocation, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallLocation{
			Path: strings.TrimSpace(item.Path),
			Line: item.Line,
		})
	}
	return out
}

func rawInputMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{"raw": strings.TrimSpace(string(raw))}
	}
	return out
}

func stringPtr(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
