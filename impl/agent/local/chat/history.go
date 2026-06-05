package chat

import (
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func messagesFromContext(ctx agent.Context) []model.Message {
	if ctx == nil {
		return nil
	}
	activeSession := ctx.Session()
	events := make([]*session.Event, 0, ctx.Events().Len())
	for event := range ctx.Events().All() {
		events = append(events, event)
	}
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
	return normalizeToolCallHistory(out)
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
	if first := events[start]; first != nil {
		firstMessage, ok := session.ModelMessageOf(first)
		firstCalls := firstMessage.ToolCalls()
		if !ok || len(firstCalls) == 0 {
			goto collectToolRun
		}
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
		return model.CloneMessage(firstMessage), next, true
	}
collectToolRun:
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
	toolPayload := session.EventToolProjection(event)
	if toolPayload == nil {
		return model.ToolCall{}, false
	}
	call := model.ToolCall{
		ID:   strings.TrimSpace(toolPayload.ID),
		Name: toolNameFromEvent(event),
		Args: string(mustJSON(toolPayload.Input)),
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
	for i := 0; i < len(messages); i++ {
		calls := messages[i].ToolCalls()
		if len(calls) == 0 {
			if len(messages[i].ToolResults()) > 0 {
				continue
			}
			out = append(out, messages[i])
			continue
		}
		required := map[string]struct{}{}
		for _, call := range calls {
			if id := strings.TrimSpace(call.ID); id != "" {
				required[id] = struct{}{}
			}
		}
		if len(required) == 0 {
			continue
		}
		if !toolCallsHaveValidArgs(calls) {
			next := i + 1
			for next < len(messages) && len(messages[next].ToolResults()) > 0 {
				next++
			}
			i = next - 1
			continue
		}
		run := []model.Message{messages[i]}
		next := i + 1
		valid := true
		for len(required) > 0 {
			if next >= len(messages) {
				valid = false
				break
			}
			results := messages[next].ToolResults()
			if len(results) == 0 {
				valid = false
				break
			}
			matched := false
			for _, result := range results {
				if id := strings.TrimSpace(result.ToolUseID); id != "" {
					if _, ok := required[id]; ok {
						delete(required, id)
						matched = true
					}
				}
			}
			if !matched {
				valid = false
				break
			}
			run = append(run, messages[next])
			next++
		}
		if valid {
			out = append(out, run...)
		}
		for next < len(messages) && len(messages[next].ToolResults()) > 0 {
			next++
		}
		i = next - 1
	}
	return out
}

func messageFromInvocationEvent(event *session.Event) (model.Message, bool) {
	if event == nil || !session.IsMainInvocationVisibleEvent(event) {
		return model.Message{}, false
	}
	if event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "" {
		if message, ok := session.ModelMessageOf(event); ok {
			return message, true
		}
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
		if message, ok := session.ModelMessageOf(event); ok {
			return message, true
		}
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
	case session.EventTypeAssistant:
		if text := strings.TrimSpace(session.EventText(event)); text != "" {
			return model.NewTextMessage(model.RoleAssistant, text), true
		}
	case session.EventTypeToolCall:
		toolPayload := session.EventToolProjection(event)
		if toolPayload == nil {
			return model.Message{}, false
		}
		call := model.ToolCall{
			ID:   strings.TrimSpace(toolPayload.ID),
			Name: toolNameFromEvent(event),
			Args: string(mustJSON(toolPayload.Input)),
		}
		if call.ID == "" || call.Name == "" {
			return model.Message{}, false
		}
		return model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), true
	case session.EventTypeToolResult:
		toolPayload := session.EventToolProjection(event)
		if toolPayload == nil {
			return model.Message{}, false
		}
		name := toolNameFromEvent(event)
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
		return maps.Clone(toolPayload.Output)
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

func toolNameFromEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if name := strings.TrimSpace(stringFromNestedMap(event.Meta, "caelis", "runtime", "tool", "name")); name != "" {
		return name
	}
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		if name := strings.TrimSpace(toolPayload.Name); name != "" {
			return name
		}
	}
	return ""
}

func stringFromNestedMap(values map[string]any, path ...string) string {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[key]
	}
	text, _ := current.(string)
	return strings.TrimSpace(text)
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
	if source := strings.ToLower(strings.TrimSpace(event.Scope.Source)); strings.HasPrefix(source, "slash_") {
		return strings.TrimPrefix(source, "slash_")
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
