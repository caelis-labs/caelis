package acp

import (
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func acpEnvelopeFromUpdate(env client.UpdateEnvelope, canonical *session.Event) *eventstream.Envelope {
	update := schemaUpdateFromClientUpdate(env)
	if update == nil {
		return nil
	}
	out := &eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: strings.TrimSpace(env.SessionID),
		Update:    update,
		Meta:      eventstream.UpdateMeta(update),
	}
	if canonical != nil {
		out.OccurredAt = canonical.Time
		out.Actor = strings.TrimSpace(canonical.Actor.Name)
		if canonical.Scope != nil {
			out.TurnID = strings.TrimSpace(canonical.Scope.TurnID)
			out.Scope = eventstream.ScopeMain
			out.ScopeID = strings.TrimSpace(canonical.SessionID)
			if id := strings.TrimSpace(canonical.Scope.Participant.ID); id != "" {
				out.Scope = eventstream.ScopeParticipant
				out.ScopeID = id
				out.ParticipantID = id
			}
		}
	}
	return out
}

func schemaUpdateFromClientUpdate(env client.UpdateEnvelope) schema.Update {
	switch typed := env.Update.(type) {
	case nil:
		return nil
	case client.ContentChunk:
		return schema.ContentChunk{
			SessionUpdate: strings.TrimSpace(typed.SessionUpdate),
			Content:       rawContentValue(typed.Content),
		}
	case client.ToolCall:
		return schema.ToolCall(typed)
	case client.ToolCallUpdate:
		return schema.ToolCallUpdate(typed)
	case client.PlanUpdate:
		return schema.PlanUpdate(typed)
	case client.CurrentModeUpdate:
		return schema.RawUpdate{SessionUpdate: strings.TrimSpace(typed.SessionUpdate), Raw: cloneRaw(env.Raw)}
	case client.ConfigOptionUpdate:
		return schema.RawUpdate{SessionUpdate: strings.TrimSpace(typed.SessionUpdate), Raw: cloneRaw(env.Raw)}
	case client.SessionInfoUpdate:
		return schema.RawUpdate{SessionUpdate: strings.TrimSpace(typed.SessionUpdate), Raw: cloneRaw(env.Raw)}
	case client.AvailableCommandsUpdate:
		return schema.RawUpdate{SessionUpdate: strings.TrimSpace(typed.SessionUpdate), Raw: cloneRaw(env.Raw)}
	case client.RawUpdate:
		raw := cloneRaw(typed.Raw)
		if len(raw) == 0 {
			raw = cloneRaw(env.Raw)
		}
		return schema.RawUpdate{SessionUpdate: strings.TrimSpace(typed.SessionUpdate), Raw: raw}
	case schema.Update:
		return typed
	default:
		return schema.RawUpdate{SessionUpdate: sessionUpdateTypeFromRaw(env.Raw), Raw: cloneRaw(env.Raw)}
	}
}

func rawContentValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return cloneRaw(raw)
	}
	return out
}

func sessionUpdateTypeFromRaw(raw json.RawMessage) string {
	var probe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	_ = json.Unmarshal(raw, &probe)
	return strings.TrimSpace(probe.SessionUpdate)
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
