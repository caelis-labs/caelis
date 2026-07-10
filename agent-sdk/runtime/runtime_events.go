package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/userdisplay"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
)

func buildUserEvent(activeSession session.Session, turnID string, input string, displayInput string, parts []model.ContentPart) *session.Event {
	if strings.TrimSpace(input) == "" && len(parts) == 0 {
		return nil
	}
	message, displayText, meta := userdisplay.Resolve(input, displayInput, parts, nil)
	return &session.Event{
		IdempotencyKey: "turn-input:" + strings.TrimSpace(turnID),
		Type:           session.EventTypeUser,
		Visibility:     session.VisibilityCanonical,
		Actor:          session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
		Scope:          ptrScope(defaultScope(activeSession, turnID)),
		Protocol: &session.EventProtocol{
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				Content:       session.ProtocolTextContent(displayText),
			},
		},
		Message: &message,
		Text:    displayText,
		Meta:    meta,
	}
}

func normalizeEvent(activeSession session.Session, turnID string, event *session.Event) *session.Event {
	event = session.CloneEvent(event)
	if event == nil {
		return nil
	}
	if event.Type == "" {
		event.Type = session.EventTypeOf(event)
	}
	if event.Visibility == "" {
		event.Visibility = session.VisibilityCanonical
	}
	if event.Text == "" && event.Message != nil {
		event.Text = event.Message.TextContent()
	}
	if event.Protocol == nil && event.Message == nil {
		switch session.EventTypeOf(event) {
		case session.EventTypeUser:
			event.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeUserMessage),
				Content:       session.ProtocolTextContent(event.Text),
			}}
		case session.EventTypeAssistant:
			event.Protocol = &session.EventProtocol{Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				Content:       session.ProtocolTextContent(event.Text),
			}}
		}
	} else if event.Protocol != nil && event.Message == nil {
		protocol := session.CloneEventProtocol(*event.Protocol)
		if protocol.Update != nil && protocol.Update.Content == nil {
			switch session.EventTypeOf(event) {
			case session.EventTypeUser:
				protocol.Update.SessionUpdate = string(session.ProtocolUpdateTypeUserMessage)
				protocol.Update.Content = session.ProtocolTextContent(event.Text)
			case session.EventTypeAssistant:
				protocol.Update.SessionUpdate = firstNonEmpty(protocol.Update.SessionUpdate, string(session.ProtocolUpdateTypeAgentMessage))
				protocol.Update.Content = session.ProtocolTextContent(event.Text)
			}
			event.Protocol = &protocol
		}
	}
	if strings.TrimSpace(event.SessionID) == "" {
		event.SessionID = strings.TrimSpace(activeSession.SessionID)
	}
	if event.Scope == nil {
		scope := defaultScope(activeSession, turnID)
		event.Scope = &scope
	}
	if event.Scope.TurnID == "" {
		event.Scope.TurnID = strings.TrimSpace(turnID)
	}
	if event.Scope.Controller.Kind == "" {
		event.Scope.Controller = defaultControllerRef(activeSession)
	}
	if event.Actor.Kind == "" {
		event.Actor = defaultActorForEvent(event)
	}
	return event
}

func scopeRuntimeToolFactIdentity(event *session.Event, runID, turnID string, ordinal uint64) bool {
	if event == nil || event.Tool == nil || strings.TrimSpace(event.IdempotencyKey) != "" {
		return false
	}
	eventType := session.EventTypeOf(event)
	if eventType != session.EventTypeToolCall && eventType != session.EventTypeToolResult {
		return false
	}
	toolID := strings.TrimSpace(event.Tool.ID)
	if toolID == "" {
		return false
	}
	event.IdempotencyKey = fmt.Sprintf(
		"%s:%s:%s:%d:%s",
		eventType,
		strings.TrimSpace(runID),
		strings.TrimSpace(turnID),
		ordinal,
		toolID,
	)
	return true
}

func defaultScope(activeSession session.Session, turnID string) session.EventScope {
	return session.EventScope{
		TurnID:     strings.TrimSpace(turnID),
		Controller: defaultControllerRef(activeSession),
	}
}

func defaultControllerRef(activeSession session.Session) session.ControllerRef {
	binding := session.CloneControllerBinding(activeSession.Controller)
	kind := binding.Kind
	if kind == "" {
		kind = session.ControllerKindKernel
	}
	return session.ControllerRef{
		Kind:    kind,
		ID:      binding.ControllerID,
		EpochID: binding.EpochID,
	}
}

func defaultActorForEvent(event *session.Event) session.ActorRef {
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
	case session.EventTypeToolResult:
		name := ""
		if event.Message != nil {
			if parts := event.Message.ToolResults(); len(parts) > 0 {
				name = parts[0].Name
			}
		}
		return session.ActorRef{Kind: session.ActorKindTool, Name: strings.TrimSpace(name)}
	case session.EventTypeNotice, session.EventTypeLifecycle, session.EventTypeSystem:
		return session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"}
	default:
		return session.ActorRef{Kind: session.ActorKindController}
	}
}

func ptrScope(scope session.EventScope) *session.EventScope {
	return &scope
}

func (r *Runtime) handlePlanEvent(
	ctx context.Context,
	ref session.SessionRef,
	turnID string,
	event *session.Event,
) (*session.Event, bool, error) {
	entries, explanation, ok := planEntriesFromEvent(event)
	if !ok {
		return nil, false, nil
	}
	planEvent := &session.Event{
		Type:       session.EventTypePlan,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindController},
		Scope: &session.EventScope{
			TurnID: strings.TrimSpace(turnID),
			Source: "tool_result",
		},
		PlanPayload: &session.EventPlanPayload{Entries: entriesToPlanPayload(entries)},
		Text:        strings.TrimSpace(explanation),
	}
	normalized := normalizeEvent(session.Session{}, turnID, planEvent)
	normalized.Scope.Controller = event.Scope.Controller
	planIdentity := stableToolFactIdentity(event)
	if planIdentity == "" {
		return nil, true, errors.New("agent-sdk/runtime: plan result requires a stable tool identity")
	}
	normalized.IdempotencyKey = "plan-state:" + planIdentity
	batch, ok := r.sessions.(session.EventBatchStateService)
	if !ok {
		return nil, true, fmt.Errorf("agent-sdk/runtime: session service must support atomic plan event/state commit")
	}
	persisted, err := batch.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
		SessionRef:     ref,
		MutationGuard:  session.RuntimeMutationGuard(ctx),
		TransactionID:  normalized.IdempotencyKey,
		MutationDigest: "plan-state-v1",
		Events:         []*session.Event{normalized},
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			if state == nil {
				state = map[string]any{}
			}
			state["plan"] = map[string]any{
				"version":     1,
				"entries":     entriesToState(entries),
				"explanation": explanation,
			}
			return state, nil
		},
	})
	if err != nil {
		return nil, true, err
	}
	if len(persisted) != 1 {
		return nil, true, fmt.Errorf("agent-sdk/runtime: atomic plan commit returned %d events", len(persisted))
	}
	return persisted[0], true, nil
}

func stableToolFactIdentity(event *session.Event) string {
	if event == nil {
		return ""
	}
	if identity := strings.TrimSpace(event.IdempotencyKey); identity != "" {
		return identity
	}
	if event.Tool == nil {
		return ""
	}
	return strings.TrimSpace(event.Tool.ID)
}

func planEntriesFromEvent(event *session.Event) ([]plan.Entry, string, bool) {
	if event == nil {
		return nil, "", false
	}
	name := strings.TrimSpace(planToolNameFromEvent(event))
	if name == "" {
		if message, ok := session.ModelMessageOf(event); ok {
			results := message.ToolResults()
			if len(results) > 0 {
				name = strings.TrimSpace(results[0].Name)
			}
		}
	}
	if name == "" && event.Message != nil {
		if results := event.Message.ToolResults(); len(results) > 0 {
			name = strings.TrimSpace(results[0].Name)
		}
	}
	if !strings.EqualFold(name, plan.ToolName) {
		return nil, "", false
	}

	payload := map[string]any{}
	if toolPayload := session.EventToolProjection(event); toolPayload != nil && len(toolPayload.Output) > 0 {
		payload = session.CloneState(toolPayload.Output)
	}
	if len(payload) == 0 {
		if update := session.ProtocolUpdateOf(event); update != nil && len(update.RawOutput) > 0 {
			payload = session.CloneState(update.RawOutput)
		}
	}
	if len(payload) == 0 {
		message, ok := session.ModelMessageOf(event)
		if !ok {
			if event.Message != nil {
				message = *event.Message
				ok = true
			}
		}
		if ok {
			results := message.ToolResults()
			if len(results) == 0 {
				return nil, "", false
			}
			result := results[0]
			if len(result.Content) > 0 && result.Content[0].Kind == model.PartKindJSON && result.Content[0].JSON != nil {
				_ = json.Unmarshal(result.Content[0].JSONValue(), &payload)
			}
		}
	}
	entries := planEntriesFromAny(payload["entries"])
	explanation := strings.TrimSpace(stringValue(payload["explanation"]))
	if len(entries) == 0 {
		entries = planEntriesFromAny(nestedValue(event.Meta, "caelis", "runtime", "tool", "entries"))
	}
	if explanation == "" {
		explanation = nestedString(event.Meta, "caelis", "runtime", "tool", "explanation")
	}
	return entries, explanation, true
}

func planToolNameFromEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if name := nestedString(event.Meta, "caelis", "runtime", "tool", "name"); name != "" {
		return name
	}
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		if name := strings.TrimSpace(toolPayload.Name); name != "" {
			return name
		}
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		if title := strings.Fields(strings.TrimSpace(update.Title)); len(title) > 0 {
			return title[0]
		}
		return strings.TrimSpace(update.Kind)
	}
	return ""
}

func nestedString(values map[string]any, path ...string) string {
	current := nestedValue(values, path...)
	text, _ := current.(string)
	return strings.TrimSpace(text)
}

func nestedValue(values map[string]any, path ...string) any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}

func planEntriesFromAny(raw any) []plan.Entry {
	if raw == nil {
		return nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var rows []struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil
	}
	entries := make([]plan.Entry, 0, len(rows))
	for _, row := range rows {
		content := row.Content
		status := row.Status
		content = strings.TrimSpace(content)
		status = strings.TrimSpace(status)
		if content == "" || status == "" {
			continue
		}
		entries = append(entries, plan.Entry{
			Content: content,
			Status:  plan.Status(status),
		})
	}
	return entries
}

func entriesToProtocol(entries []plan.Entry) []session.ProtocolPlanEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]session.ProtocolPlanEntry, 0, len(entries))
	for _, item := range entries {
		out = append(out, session.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(string(item.Status)),
			Priority: "medium",
		})
	}
	return out
}

func entriesToPlanPayload(entries []plan.Entry) []session.EventPlanEntry {
	if len(entries) == 0 {
		return nil
	}
	out := make([]session.EventPlanEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, session.EventPlanEntry{
			Content:  strings.TrimSpace(entry.Content),
			Status:   strings.TrimSpace(string(entry.Status)),
			Priority: "medium",
		})
	}
	return out
}

func entriesToState(entries []plan.Entry) []map[string]any {
	if len(entries) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(entries))
	for _, item := range entries {
		out = append(out, map[string]any{
			"content": strings.TrimSpace(item.Content),
			"status":  strings.TrimSpace(string(item.Status)),
		})
	}
	return out
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}
