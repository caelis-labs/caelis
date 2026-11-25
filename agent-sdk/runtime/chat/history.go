package chat

import (
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func messagesFromContextChecked(ctx agent.Context) ([]model.Message, error) {
	if ctx == nil {
		return nil, nil
	}
	events := make([]*session.Event, 0, ctx.Events().Len())
	for event := range ctx.Events().All() {
		events = append(events, event)
	}
	return messagesFromEventsChecked(ctx.Session(), events)
}

func promptSummaryID(event *session.Event) string {
	if session.EventTypeOf(event) == session.EventTypeCompact && session.EventText(event) != "" {
		data, _ := session.CheckpointDataFromEvent(event)
		if data.Purpose != session.CheckpointReplacement {
			return event.ID
		}
	}
	return ""
}

func messagesFromEventsChecked(activeSession session.Session, events []*session.Event) ([]model.Message, error) {
	ordered, err := session.OrderToolFacts(events)
	if err != nil {
		return nil, err
	}
	events = ordered
	out := make([]model.Message, 0, len(events))
	for i := 0; i < len(events); {
		event := events[i]
		if event == nil || !session.IsMainInvocationVisibleEvent(event) {
			i++
			continue
		}
		event = eventWithParticipantContextMeta(event, activeSession)
		if session.EventTypeOf(event) == session.EventTypeToolCall {
			if message, next, ok := toolCallMessageFromEventRun(events, i); ok {
				out = append(out, message)
				i = next
				continue
			}
		}
		message, ok := messageFromInvocationEvent(event)
		if !ok {
			i++
			continue
		}
		out = append(out, message)
		i++
	}
	return normalizeToolCallHistory(out), nil
}

func eventWithParticipantContextMeta(event *session.Event, activeSession session.Session) *session.Event {
	if event == nil || event.Scope == nil {
		return event
	}
	participantID := strings.TrimSpace(event.Scope.Participant.ID)
	if participantID == "" {
		return event
	}
	binding, ok := chatParticipantBinding(activeSession, participantID)
	if !ok {
		return event
	}
	agent := strings.TrimSpace(binding.AgentName)
	label := strings.TrimSpace(binding.Label)
	if agent == "" && label == "" {
		return event
	}
	if stringFromFlatMap(event.Meta, "agent") != "" &&
		stringFromFlatMap(event.Meta, "mention") != "" &&
		stringFromFlatMap(event.Meta, "handle") != "" {
		return event
	}
	cloned := session.CloneEvent(event)
	if cloned.Meta == nil {
		cloned.Meta = map[string]any{}
	}
	if agent != "" && stringFromFlatMap(cloned.Meta, "agent") == "" {
		cloned.Meta["agent"] = agent
	}
	if label != "" {
		if stringFromFlatMap(cloned.Meta, "mention") == "" {
			cloned.Meta["mention"] = label
		}
		if stringFromFlatMap(cloned.Meta, "handle") == "" {
			cloned.Meta["handle"] = strings.TrimPrefix(label, "@")
		}
	}
	return cloned
}

func chatParticipantBinding(activeSession session.Session, participantID string) (session.ParticipantBinding, bool) {
	participantID = strings.TrimSpace(participantID)
	if participantID == "" {
		return session.ParticipantBinding{}, false
	}
	for _, item := range activeSession.Participants {
		if strings.TrimSpace(item.ID) == participantID {
			return session.CloneParticipantBinding(item), true
		}
	}
	return session.ParticipantBinding{}, false
}

func toolCallMessageFromEventRun(events []*session.Event, start int) (model.Message, int, bool) {
	if start < 0 || start >= len(events) {
		return model.Message{}, start + 1, false
	}
	firstMessage, _ := session.ModelMessageOf(events[start])
	if firstCalls := firstMessage.ToolCalls(); len(firstCalls) > 0 {
		known := map[string]struct{}{}
		for _, call := range firstCalls {
			if id := strings.TrimSpace(call.ID); id != "" {
				known[id] = struct{}{}
			}
		}
		next := start + 1
		for next < len(events) {
			event := events[next]
			if event == nil || !session.IsMainInvocationVisibleEvent(event) || session.EventTypeOf(event) != session.EventTypeToolCall {
				break
			}
			call, ok := toolCallFromEventTool(event)
			if !ok {
				break
			}
			if _, exists := known[strings.TrimSpace(call.ID)]; !exists {
				break
			}
			next++
		}
		return firstMessage, next, true
	}
	calls := make([]model.ToolCall, 0, 1)
	text := ""
	next := start
	for next < len(events) {
		event := events[next]
		if event == nil || !session.IsMainInvocationVisibleEvent(event) || session.EventTypeOf(event) != session.EventTypeToolCall {
			break
		}
		if text == "" {
			text = strings.TrimSpace(session.EventText(event))
		}
		call, ok := toolCallFromEventTool(event)
		if !ok {
			if message, ok := session.ModelMessageOf(event); ok {
				for _, item := range message.ToolCalls() {
					if strings.TrimSpace(item.ID) != "" && strings.TrimSpace(item.Name) != "" {
						calls = append(calls, item)
					}
				}
			}
			next++
			continue
		}
		calls = append(calls, call)
		next++
	}
	if len(calls) == 0 {
		return model.Message{}, start + 1, false
	}
	return model.MessageFromToolCalls(model.RoleAssistant, calls, text), next, true
}

func toolCallFromEventTool(event *session.Event) (model.ToolCall, bool) {
	if event == nil || event.Tool == nil {
		return model.ToolCall{}, false
	}
	call := model.ToolCall{
		ID:   strings.TrimSpace(event.Tool.ID),
		Name: session.CanonicalToolName(event, nil),
		Args: string(mustJSON(event.Tool.Input)),
	}
	if call.ID == "" || call.Name == "" {
		return model.ToolCall{}, false
	}
	return call, true
}

func normalizeToolCallHistory(messages []model.Message) []model.Message {
	if len(messages) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(messages))
	for i := 0; i < len(messages); {
		start := i
		message := messages[i]
		i++
		calls := message.ToolCalls()
		if len(calls) == 0 {
			if len(message.ToolResults()) == 0 {
				out = append(out, message)
			}
			continue
		}
		if !toolCallsHaveValidArgs(calls) {
			continue
		}
		required := make(map[string]string, len(calls))
		for _, call := range calls {
			required[strings.TrimSpace(call.ID)] = call.Name
		}
		valid := len(required) == len(calls)
		for i < len(messages) && len(required) > 0 {
			results := messages[i].ToolResults()
			if len(results) == 0 {
				break
			}
			for _, result := range results {
				id := strings.TrimSpace(result.ToolUseID)
				name, ok := required[id]
				if !ok || result.Name != "" && result.Name != name {
					valid = false
				}
				delete(required, id)
			}
			i++
		}
		if valid && len(required) == 0 {
			out = append(out, messages[start:i]...)
		}
	}
	return out
}

func messageFromInvocationEvent(event *session.Event) (model.Message, bool) {
	if event == nil || !session.IsMainInvocationVisibleEvent(event) {
		return model.Message{}, false
	}
	if event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return messageFromDurableEvent(event)
	}
	text := strings.TrimSpace(session.EventText(event))
	if text == "" {
		return model.Message{}, false
	}
	label := participantInvocationLabel(*event)
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		return model.NewTextMessage(model.RoleUser, fmt.Sprintf("User to %s: %s", label, text)), true
	case session.EventTypeAssistant:
		if prefix := participantAssistantContextPrefix(*event); prefix != "" {
			return model.NewTextMessage(model.RoleAssistant, prefix+text), true
		}
		return model.NewTextMessage(model.RoleAssistant, fmt.Sprintf("Assistant(%s): %s", label, text)), true
	default:
		return messageFromDurableEvent(event)
	}
}

func messageFromDurableEvent(event *session.Event) (model.Message, bool) {
	if message, ok := session.ModelMessageOf(event); ok {
		return message, true
	}
	switch session.EventTypeOf(event) {
	case session.EventTypeUser:
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			return model.NewTextMessage(model.RoleUser, text), true
		}
	case session.EventTypeCompact:
		// Compact checkpoints retain Runtime/System provenance in the internal
		// overlay. Providers receive user-role history for compatibility with
		// APIs that reject or discard system-role messages mid-conversation.
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			return model.NewTextMessage(model.RoleUser, text), true
		}
	case session.EventTypeAssistant:
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			return model.NewTextMessage(model.RoleAssistant, text), true
		}
	case session.EventTypeToolCall:
		call, ok := toolCallFromEventTool(event)
		if !ok {
			return model.Message{}, false
		}
		return model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), true
	case session.EventTypeToolResult:
		toolPayload := event.Tool
		if toolPayload == nil {
			return model.Message{}, false
		}
		name := session.CanonicalToolName(event, nil)
		if toolPayload.ID == "" || name == "" {
			return model.Message{}, false
		}
		message := model.Message{
			Role: model.RoleTool,
			Parts: []model.Part{model.NewToolResultJSONPart(
				strings.TrimSpace(toolPayload.ID),
				name,
				toolResultContextPayload(toolPayload),
				strings.EqualFold(strings.TrimSpace(toolPayload.Status), "failed"),
			)},
		}
		return message, true
	}
	return model.Message{}, false
}

func toolResultContextPayload(toolPayload *session.EventTool) map[string]any {
	if toolPayload == nil {
		return map[string]any{}
	}
	if len(toolPayload.Output) > 0 {
		return session.CloneState(toolPayload.Output)
	}
	var diffs []session.EventToolContent
	for _, item := range toolPayload.Content {
		if item.Type == "diff" && item.Changes != nil {
			diffs = append(diffs, session.EventToolContent{
				Type:    "diff",
				Changes: session.CloneProtocolDiffChanges(item.Changes),
				Patch:   session.CloneProtocolDiffPatch(item.Patch),
				Meta:    session.CloneState(item.Meta),
			})
		}
	}
	if len(diffs) > 0 {
		payload := map[string]any{"content": diffs}
		if text := eventToolContentText(toolPayload.Content); text != "" {
			payload["result"] = text
		}
		return payload
	}
	if text := eventToolContentText(toolPayload.Content); text != "" {
		return map[string]any{"result": text}
	}
	return map[string]any{}
}

func eventToolContentText(content []session.EventToolContent) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		switch strings.TrimSpace(item.Type) {
		case "content", "terminal":
			if item.Text != "" {
				parts = append(parts, item.Text)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func participantInvocationLabel(event session.Event) string {
	if mention := strings.TrimSpace(stringFromFlatMap(event.Meta, "mention")); mention != "" {
		return mention
	}
	if handle := strings.TrimSpace(stringFromFlatMap(event.Meta, "handle")); handle != "" {
		if strings.HasPrefix(handle, "@") {
			return handle
		}
		return "@" + handle
	}
	if agent := strings.TrimSpace(stringFromFlatMap(event.Meta, "agent")); agent != "" {
		return agent
	}
	if name := strings.TrimSpace(event.Actor.Name); name != "" && !strings.EqualFold(name, "user") {
		return name
	}
	if id := strings.TrimSpace(event.Actor.ID); id != "" && event.Actor.Kind == session.ActorKindParticipant {
		return id
	}
	if id := strings.TrimSpace(event.Actor.ID); id != "" {
		return id
	}
	if event.Scope != nil {
		if id := strings.TrimSpace(event.Scope.Participant.ID); id != "" {
			return id
		}
	}
	return "participant"
}

func participantAssistantContextPrefix(event session.Event) string {
	if event.Scope == nil {
		return ""
	}
	participant := event.Scope.Participant
	if strings.TrimSpace(participant.ID) == "" || participant.Role != session.ParticipantRoleSidecar {
		return ""
	}
	agent := stableAgentContextValue(participantAgentType(event))
	if agent == "" {
		agent = stableAgentContextValue(string(participant.Kind))
	}
	if agent == "" {
		agent = "agent"
	}
	handle := stableAgentHandleValue(participantHandle(event))
	if handle != "" {
		return fmt.Sprintf("[agent_source agent=%s handle=%s]\n", agent, handle)
	}
	return fmt.Sprintf("[agent_source agent=%s]\n", agent)
}

func participantAgentType(event session.Event) string {
	if agent := strings.TrimSpace(stringFromFlatMap(event.Meta, "agent")); agent != "" {
		return agent
	}
	if event.Scope.Participant.Kind != "" {
		return string(event.Scope.Participant.Kind)
	}
	return ""
}

func participantHandle(event session.Event) string {
	for _, value := range []string{
		stringFromFlatMap(event.Meta, "mention"),
		stringFromFlatMap(event.Meta, "handle"),
		event.Actor.Name,
	} {
		if text := strings.TrimSpace(value); text != "" && !strings.EqualFold(text, "user") {
			return text
		}
	}
	if event.Actor.Kind == session.ActorKindParticipant {
		if id := strings.TrimSpace(event.Actor.ID); id != "" {
			return id
		}
	}
	if event.Scope != nil {
		return strings.TrimSpace(event.Scope.Participant.ID)
	}
	return ""
}

func stableAgentContextValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, "_")
}

func stableAgentHandleValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	value = strings.Join(fields, "_")
	value = strings.TrimPrefix(value, "@")
	if value == "" {
		return ""
	}
	return "@" + value
}

func stringFromFlatMap(values map[string]any, key string) string {
	if len(values) == 0 {
		return ""
	}
	text, _ := values[key].(string)
	return strings.TrimSpace(text)
}
