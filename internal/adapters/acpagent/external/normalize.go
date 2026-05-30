package external

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type wireSessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

type wireUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
}

func eventsFromSessionUpdate(cfg Config, raw json.RawMessage) ([]session.Event, error) {
	var note wireSessionNotification
	if err := json.Unmarshal(raw, &note); err != nil {
		return nil, formatDecodeError("session/update", raw, err)
	}
	updateType, err := updateType(note.Update)
	if err != nil {
		return nil, err
	}
	switch updateType {
	case schema.UpdateUserMessage:
		var update schema.ContentChunk
		if err := json.Unmarshal(note.Update, &update); err != nil {
			return nil, formatDecodeError("user message update", note.Update, err)
		}
		return []session.Event{messageEvent(cfg, note.SessionID, session.EventUser, model.RoleUser, contentText(update.Content))}, nil
	case schema.UpdateAgentMessage:
		var update schema.ContentChunk
		if err := json.Unmarshal(note.Update, &update); err != nil {
			return nil, formatDecodeError("agent message update", note.Update, err)
		}
		return []session.Event{messageEvent(cfg, note.SessionID, session.EventAssistant, model.RoleAssistant, contentText(update.Content))}, nil
	case schema.UpdateAgentThought:
		var update schema.ContentChunk
		if err := json.Unmarshal(note.Update, &update); err != nil {
			return nil, formatDecodeError("agent thought update", note.Update, err)
		}
		text := contentText(update.Content)
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		message := model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewReasoningPart(text, model.ReasoningVisible)},
		}
		return []session.Event{baseEvent(cfg, note.SessionID, session.EventAssistant, session.ActorRef{Kind: session.ActorParticipant, ID: cfg.AgentID, Name: cfg.AgentName}, &message)}, nil
	case schema.UpdateToolCall:
		var update schema.ToolCall
		if err := json.Unmarshal(note.Update, &update); err != nil {
			return nil, formatDecodeError("tool call update", note.Update, err)
		}
		return []session.Event{toolCallEvent(cfg, note.SessionID, update)}, nil
	case schema.UpdateToolCallInfo:
		var update schema.ToolCallUpdate
		if err := json.Unmarshal(note.Update, &update); err != nil {
			return nil, formatDecodeError("tool call info update", note.Update, err)
		}
		return []session.Event{toolUpdateEvent(cfg, note.SessionID, update)}, nil
	case schema.UpdatePlan:
		var update schema.PlanUpdate
		if err := json.Unmarshal(note.Update, &update); err != nil {
			return nil, formatDecodeError("plan update", note.Update, err)
		}
		return []session.Event{planEvent(cfg, note.SessionID, update)}, nil
	default:
		return nil, nil
	}
}

func updateType(raw json.RawMessage) (string, error) {
	var update wireUpdate
	if err := json.Unmarshal(raw, &update); err != nil {
		return "", formatDecodeError("update envelope", raw, err)
	}
	return strings.TrimSpace(update.SessionUpdate), nil
}

func messageEvent(cfg Config, acpSessionID string, eventType session.EventType, role model.Role, text string) session.Event {
	if strings.TrimSpace(text) == "" {
		return session.Event{}
	}
	message := model.Message{Role: role, Parts: []model.Part{model.NewTextPart(text)}}
	return baseEvent(cfg, acpSessionID, eventType, actorForRole(cfg, role), &message)
}

func toolCallEvent(cfg Config, acpSessionID string, update schema.ToolCall) session.Event {
	tool := session.ToolEvent{
		ID:        strings.TrimSpace(update.ToolCallID),
		Name:      strings.TrimSpace(firstNonEmpty(update.Kind, update.Title)),
		Kind:      strings.TrimSpace(update.Kind),
		Title:     strings.TrimSpace(update.Title),
		Status:    toolStatus(update.Status),
		Input:     anyMap(update.RawInput),
		Output:    anyMap(update.RawOutput),
		Content:   toolContent(update.Content),
		Locations: toolLocations(update.Locations),
		Meta:      update.Meta,
	}
	return session.Event{
		SessionID:  strings.TrimSpace(acpSessionID),
		Type:       session.EventToolCall,
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      session.ActorRef{Kind: session.ActorTool, ID: tool.ID, Name: tool.Name},
		Scope:      eventScope(cfg, acpSessionID, schema.UpdateToolCall),
		Tool:       &tool,
	}
}

func toolUpdateEvent(cfg Config, acpSessionID string, update schema.ToolCallUpdate) session.Event {
	tool := session.ToolEvent{
		ID:        strings.TrimSpace(update.ToolCallID),
		Name:      strings.TrimSpace(firstNonEmpty(ptrValue(update.Kind), ptrValue(update.Title))),
		Kind:      strings.TrimSpace(ptrValue(update.Kind)),
		Title:     strings.TrimSpace(ptrValue(update.Title)),
		Status:    toolStatus(ptrValue(update.Status)),
		Input:     anyMap(update.RawInput),
		Output:    anyMap(update.RawOutput),
		Content:   toolContent(update.Content),
		Locations: toolLocations(update.Locations),
		Meta:      update.Meta,
	}
	eventType := session.EventToolCall
	if tool.Status == session.ToolCompleted || tool.Status == session.ToolFailed || tool.Status == session.ToolCancelled {
		eventType = session.EventToolResult
	}
	return session.Event{
		SessionID:  strings.TrimSpace(acpSessionID),
		Type:       eventType,
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      session.ActorRef{Kind: session.ActorTool, ID: tool.ID, Name: tool.Name},
		Scope:      eventScope(cfg, acpSessionID, schema.UpdateToolCallInfo),
		Tool:       &tool,
	}
}

func planEvent(cfg Config, acpSessionID string, update schema.PlanUpdate) session.Event {
	entries := make([]session.PlanEntry, 0, len(update.Entries))
	for _, entry := range update.Entries {
		entries = append(entries, session.PlanEntry{
			Content: strings.TrimSpace(entry.Content),
			Status:  strings.TrimSpace(entry.Status),
		})
	}
	return session.Event{
		SessionID:  strings.TrimSpace(acpSessionID),
		Type:       session.EventPlan,
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      actor(cfg),
		Scope:      eventScope(cfg, acpSessionID, schema.UpdatePlan),
		Plan:       entries,
	}
}

func permissionEvent(cfg Config, req schema.RequestPermissionRequest, status session.ApprovalStatus, optionID string, reason string) session.Event {
	tool := session.ToolEvent{
		ID:     strings.TrimSpace(req.ToolCall.ToolCallID),
		Name:   strings.TrimSpace(firstNonEmpty(ptrValue(req.ToolCall.Kind), ptrValue(req.ToolCall.Title))),
		Kind:   strings.TrimSpace(ptrValue(req.ToolCall.Kind)),
		Title:  strings.TrimSpace(ptrValue(req.ToolCall.Title)),
		Status: session.ToolWaitingApproval,
		Input:  anyMap(req.ToolCall.RawInput),
		Output: anyMap(req.ToolCall.RawOutput),
		Meta:   req.ToolCall.Meta,
	}
	options := make([]session.ApprovalOption, 0, len(req.Options))
	for _, option := range req.Options {
		options = append(options, session.ApprovalOption{
			ID:   strings.TrimSpace(option.OptionID),
			Name: strings.TrimSpace(option.Name),
			Kind: strings.TrimSpace(option.Kind),
		})
	}
	return session.Event{
		SessionID:  strings.TrimSpace(req.SessionID),
		Type:       session.EventApproval,
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      actor(cfg),
		Scope:      eventScope(cfg, req.SessionID, schema.MethodSessionReqPermission),
		Tool:       &tool,
		Approval: &session.ApprovalEvent{
			ID:       "approval-" + tool.ID,
			Status:   status,
			Tool:     &tool,
			Options:  options,
			Decision: strings.TrimSpace(optionID),
			Reason:   strings.TrimSpace(reason),
		},
	}
}

func baseEvent(cfg Config, acpSessionID string, eventType session.EventType, actor session.ActorRef, message *model.Message) session.Event {
	return session.Event{
		SessionID:  strings.TrimSpace(acpSessionID),
		Type:       eventType,
		Visibility: session.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      actor,
		Scope:      eventScope(cfg, acpSessionID, string(eventType)),
		Message:    message,
	}
}

func eventScope(cfg Config, acpSessionID string, acpEventType string) *session.EventScope {
	return &session.EventScope{
		Source: "external_acp",
		Participant: session.ParticipantBinding{
			ID:        strings.TrimSpace(cfg.AgentID),
			Kind:      session.ParticipantACP,
			Role:      session.ParticipantDelegated,
			AgentName: strings.TrimSpace(cfg.AgentName),
			Label:     strings.TrimSpace(cfg.AgentName),
			SessionID: strings.TrimSpace(acpSessionID),
			Source:    "external_acp",
		},
		ACP: session.ACPRef{
			SessionID: strings.TrimSpace(acpSessionID),
			EventType: strings.TrimSpace(acpEventType),
		},
	}
}

func actorForRole(cfg Config, role model.Role) session.ActorRef {
	if role == model.RoleUser {
		return session.ActorRef{Kind: session.ActorUser, ID: "user", Name: "user"}
	}
	return actor(cfg)
}

func actor(cfg Config) session.ActorRef {
	return session.ActorRef{
		Kind: session.ActorParticipant,
		ID:   strings.TrimSpace(cfg.AgentID),
		Name: strings.TrimSpace(cfg.AgentName),
	}
}

func contentText(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case schema.TextContent:
		return strings.TrimSpace(typed.Text)
	case map[string]any:
		if text, ok := typed["text"].(string); ok {
			return strings.TrimSpace(text)
		}
		if content, ok := typed["content"].(string); ok {
			return strings.TrimSpace(content)
		}
	case []any:
		var parts []string
		for _, item := range typed {
			if text := contentText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	raw, _ := json.Marshal(value)
	return strings.TrimSpace(string(raw))
}

func toolStatus(status string) session.ToolStatus {
	switch strings.TrimSpace(status) {
	case schema.ToolStatusPending:
		return session.ToolStarted
	case schema.ToolStatusInProgress:
		return session.ToolRunning
	case schema.ToolStatusCompleted:
		return session.ToolCompleted
	case schema.ToolStatusFailed:
		return session.ToolFailed
	default:
		return session.ToolRunning
	}
}

func toolContent(in []schema.ToolCallContent) []session.ToolContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.ToolContent, 0, len(in))
	for _, item := range in {
		out = append(out, session.ToolContent{
			Type:       strings.TrimSpace(item.Type),
			Text:       contentText(item.Content),
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
		})
	}
	return out
}

func toolLocations(in []schema.ToolCallLocation) []session.ToolLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.ToolLocation, 0, len(in))
	for _, item := range in {
		out = append(out, session.ToolLocation{
			Path: strings.TrimSpace(item.Path),
			Line: item.Line,
		})
	}
	return out
}

func anyMap(value any) map[string]any {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return typed
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return map[string]any{"value": value}
		}
		out := map[string]any{}
		if err := json.Unmarshal(raw, &out); err != nil {
			return map[string]any{"value": value}
		}
		return out
	}
}

func ptrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
