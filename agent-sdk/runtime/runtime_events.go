package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
)

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
	if event.Tool != nil && event.Tool.Execution.ItemID != "" {
		if event.Tool.Execution.SessionID == "" {
			event.Tool.Execution.SessionID = activeSession.SessionID
		}
		if event.Tool.Execution.TurnID == "" {
			event.Tool.Execution.TurnID = turnID
		}
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
	if !session.ActorRefHasIdentity(event.Scope.Executor) {
		event.Scope.Executor = session.ControllerExecutor(activeSession.Controller)
	}
	if event.Actor.Kind == "" {
		event.Actor = defaultActorForEvent(event)
	}
	if event.Actor.Kind == session.ActorKindController && strings.TrimSpace(event.Actor.Name) == "" {
		event.Actor.Name = event.Scope.Executor.Name
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
	if event.Tool.Execution.ItemID != "" {
		event.IdempotencyKey = fmt.Sprintf("tool:%s:%s:%s", turnID, event.Tool.Execution.ItemID, eventType)
		event.ID = event.IdempotencyKey
		return true
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
		Executor:   session.ControllerExecutor(activeSession.Controller),
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
		if event.Tool != nil && event.Tool.Name != "" {
			return session.ActorRef{Kind: session.ActorKindTool, Name: event.Tool.Name}
		}
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
	planState := map[string]any{
		"version":     1,
		"turn_id":     turnID,
		"entries":     entriesToState(entries),
		"explanation": explanation,
	}
	planMutation, err := json.Marshal(planState)
	if err != nil {
		return nil, true, fmt.Errorf("agent-sdk/runtime: encode plan state mutation: %w", err)
	}
	persisted, err := batch.AppendEventsAndUpdateState(ctx, session.AppendEventsAndUpdateStateRequest{
		SessionRef:     ref,
		MutationGuard:  session.RuntimeMutationGuard(ctx),
		TransactionID:  normalized.IdempotencyKey,
		MutationDigest: "plan-state-v2:" + string(planMutation),
		Events:         []*session.Event{normalized},
		UpdateState: func(_ []*session.Event, state map[string]any) (map[string]any, error) {
			if state == nil {
				state = map[string]any{}
			}
			state["plan"] = session.CloneState(planState)
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

// planEntriesFromEvent consumes the built-in Plan tool's observation.
// Tool results may be transformed for the model without granting plan authority.
func planEntriesFromEvent(event *session.Event) ([]plan.Entry, string, bool) {
	if event == nil || event.Type != session.EventTypeToolResult || event.Tool == nil ||
		event.Tool.Name != plan.ToolName || event.Tool.Outcome != session.ToolOutcomeSucceeded {
		return nil, "", false
	}
	observed, _ := nestedValue(event.Meta, "caelis", "runtime", "tool").(map[string]any)
	rawEntries, present := observed["entries"]
	if observed["updated"] != true || !present {
		return nil, "", false
	}
	data, err := json.Marshal(rawEntries)
	if err != nil {
		return nil, "", false
	}
	var entries []plan.Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, "", false
	}
	explanation, _ := observed["explanation"].(string)
	return entries, strings.TrimSpace(explanation), true
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

// A failed write may already have committed. Read back the original ID/key and
// compare through the Store's full-payload idempotency contract. Absence is not
// permission to replay with a new identity. This function never writes.
func (r *Runtime) reconcileEventAppend(ctx context.Context, ref session.SessionRef, want *session.Event) (*session.Event, error) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	loaded, err := r.sessions.LoadSession(cleanup, session.LoadSessionRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil {
		return nil, err
	}
	for _, event := range loaded.Events {
		if event.ID != want.ID && (want.IdempotencyKey == "" || event.IdempotencyKey != want.IdempotencyKey) {
			continue
		}
		prepared, err := session.PrepareEventsForAppend(session.PrepareEventsForAppendRequest{
			SessionID: ref.SessionID, Events: []*session.Event{want}, ExistingEvents: []*session.Event{event}, Now: r.now(),
		})
		if err != nil {
			return nil, err
		}
		if len(prepared.Persisted) == 0 && len(prepared.Events) == 1 {
			return prepared.Events[0], nil
		}
	}
	return nil, nil
}
