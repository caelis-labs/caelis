package acpingress

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/impl/agent/acp/internal/acpconvert"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	acpschema "github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

type VisibilityPolicy func(updateType string, eventType session.EventType) session.Visibility

type Options struct {
	Clock        func() time.Time
	At           time.Time
	Scope        session.EventScope
	Actor        session.ActorRef
	Meta         map[string]any
	TextOverride string
	Visibility   VisibilityPolicy
}

func ControllerVisibility(updateType string, _ session.EventType) session.Visibility {
	switch strings.TrimSpace(updateType) {
	case client.UpdateUserMessage:
		return session.VisibilityCanonical
	default:
		return session.VisibilityUIOnly
	}
}

func UIOnlyVisibility(string, session.EventType) session.Visibility {
	return session.VisibilityUIOnly
}

func NormalizeUpdate(update client.Update, opts Options) *session.Event {
	switch typed := update.(type) {
	case client.ContentChunk:
		return normalizeContentChunk(typed, opts)
	case client.ToolCall:
		return normalizeToolCall(typed, opts)
	case client.ToolCallUpdate:
		return normalizeToolCallUpdate(typed, opts)
	case client.PlanUpdate:
		return normalizePlanUpdate(typed, opts)
	case client.UsageUpdate:
		// usage_update is a standard ACP client-stream payload, but Caelis does
		// not yet have a durable canonical source for ACP size/used semantics.
		// Controller live streams pass it through as eventstream.Envelope only.
		return nil
	default:
		return nil
	}
}

func ContentChunkText(chunk client.ContentChunk) string {
	var text client.TextChunk
	if err := json.Unmarshal(chunk.Content, &text); err == nil {
		if text.Text != "" {
			return text.Text
		}
		return acpschema.TextFromRawContent(chunk.Content)
	}
	return acpschema.TextFromRawContent(chunk.Content)
}

func normalizeContentChunk(chunk client.ContentChunk, opts Options) *session.Event {
	updateType := strings.TrimSpace(chunk.SessionUpdate)
	text := ContentChunkText(chunk)
	if opts.TextOverride != "" {
		text = opts.TextOverride
	}
	if text == "" {
		return nil
	}
	eventType := session.EventTypeAssistant
	actor := session.CloneActorRef(opts.Actor)
	if updateType == client.UpdateUserMessage {
		eventType = session.EventTypeUser
		actor = session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
	}
	event := baseEvent(updateType, eventType, text, actor, opts)
	msg := messageForContentChunk(chunk, text)
	event.Message = &msg
	event.Protocol = normalizedProtocol(&session.EventProtocol{
		Update: &session.ProtocolUpdate{
			SessionUpdate: updateType,
			Content:       append(json.RawMessage(nil), chunk.Content...),
			MessageID:     strings.TrimSpace(chunk.MessageID),
			Meta:          metautil.CloneMap(chunk.Meta),
		},
	})
	return event
}

func normalizeToolCall(call client.ToolCall, opts Options) *session.Event {
	updateType := strings.TrimSpace(call.SessionUpdate)
	protocolTool := &session.ProtocolToolCall{
		ID:       strings.TrimSpace(call.ToolCallID),
		Name:     acpconvert.ToolDisplayName(call.Kind, call.Title),
		Kind:     strings.TrimSpace(call.Kind),
		Title:    strings.TrimSpace(call.Title),
		Status:   firstNonEmpty(strings.TrimSpace(call.Status), acpschema.ToolStatusPending),
		RawInput: acpconvert.ToolRawInput(call.RawInput),
		Content:  acpconvert.ToolContent(call.Content),
	}
	event := baseEvent(
		updateType,
		session.EventTypeToolCall,
		firstNonEmpty(strings.TrimSpace(call.Title), strings.TrimSpace(call.Kind), "tool call"),
		session.CloneActorRef(opts.Actor),
		opts,
	)
	update := acpconvert.ToolProtocolUpdate(updateType, protocolTool, call.Meta)
	update.Locations = protocolLocations(call.Locations)
	event.Protocol = normalizedProtocol(&session.EventProtocol{Update: update})
	if event.Visibility == session.VisibilityCanonical {
		return nil
	}
	return event
}

func normalizeToolCallUpdate(update client.ToolCallUpdate, opts Options) *session.Event {
	updateType := strings.TrimSpace(update.SessionUpdate)
	status := derefString(update.Status)
	eventType := toolEventTypeFromStatus(status)
	protocolTool := &session.ProtocolToolCall{
		ID:        strings.TrimSpace(update.ToolCallID),
		Name:      acpconvert.ToolDisplayName(derefString(update.Kind), derefString(update.Title)),
		Kind:      derefString(update.Kind),
		Title:     derefString(update.Title),
		Status:    status,
		RawInput:  acpconvert.ToolRawInput(update.RawInput),
		RawOutput: acpconvert.ToolRawOutput(update.RawOutput),
		Content:   acpconvert.ToolContent(update.Content),
	}
	event := baseEvent(
		updateType,
		eventType,
		firstNonEmpty(derefString(update.Title), derefString(update.Kind), "tool update"),
		session.CloneActorRef(opts.Actor),
		opts,
	)
	protocolUpdate := acpconvert.ToolProtocolUpdate(updateType, protocolTool, update.Meta)
	protocolUpdate.Locations = protocolLocations(update.Locations)
	event.Protocol = normalizedProtocol(&session.EventProtocol{Update: protocolUpdate})
	if event.Visibility == session.VisibilityCanonical {
		return nil
	}
	return event
}

func normalizePlanUpdate(update client.PlanUpdate, opts Options) *session.Event {
	updateType := strings.TrimSpace(update.SessionUpdate)
	event := baseEvent(
		updateType,
		session.EventTypePlan,
		"plan updated",
		session.CloneActorRef(opts.Actor),
		opts,
	)
	entries := planEntries(update.Entries)
	event.Protocol = normalizedProtocol(&session.EventProtocol{
		Update: &session.ProtocolUpdate{
			SessionUpdate: updateType,
			Entries:       entries,
		},
	})
	if event.Visibility == session.VisibilityCanonical {
		return nil
	}
	return event
}

func baseEvent(updateType string, eventType session.EventType, text string, actor session.ActorRef, opts Options) *session.Event {
	scope := session.CloneEventScope(opts.Scope)
	scope.ACP.EventType = strings.TrimSpace(updateType)
	visibility := session.VisibilityUIOnly
	if opts.Visibility != nil {
		if selected := opts.Visibility(updateType, eventType); selected != "" {
			visibility = selected
		}
	}
	return &session.Event{
		Type:       eventType,
		Visibility: visibility,
		Time:       eventTime(opts),
		Actor:      actor,
		Scope:      &scope,
		Text:       text,
		Meta:       metautil.CloneMap(opts.Meta),
	}
}

func eventTime(opts Options) time.Time {
	if !opts.At.IsZero() {
		return opts.At
	}
	if opts.Clock != nil {
		return opts.Clock()
	}
	return time.Now()
}

func messageForContentChunk(chunk client.ContentChunk, text string) model.Message {
	role := model.RoleAssistant
	if strings.TrimSpace(chunk.SessionUpdate) == client.UpdateUserMessage {
		role = model.RoleUser
	}
	if strings.TrimSpace(chunk.SessionUpdate) == client.UpdateAgentThought {
		return model.NewReasoningMessage(role, text, model.ReasoningVisibilityVisible)
	}
	return model.NewTextMessage(role, text)
}

func protocolLocations(in []client.ToolCallLocation) []session.ProtocolToolCallLocation {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.ProtocolToolCallLocation, 0, len(in))
	for _, item := range in {
		var line *int
		if item.Line != nil {
			value := *item.Line
			line = &value
		}
		out = append(out, session.ProtocolToolCallLocation{
			Path: strings.TrimSpace(item.Path),
			Line: line,
		})
	}
	return out
}

func planEntries(in []client.PlanEntry) []session.ProtocolPlanEntry {
	out := make([]session.ProtocolPlanEntry, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: strings.TrimSpace(item.Priority),
		})
	}
	return out
}

func normalizedProtocol(protocol *session.EventProtocol) *session.EventProtocol {
	if protocol == nil {
		return nil
	}
	normalized := session.CloneEventProtocol(*protocol)
	return &normalized
}

func toolEventTypeFromStatus(status string) session.EventType {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled":
		return session.EventTypeToolResult
	default:
		return session.EventTypeToolCall
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func derefString(in *string) string {
	if in == nil {
		return ""
	}
	return strings.TrimSpace(*in)
}
