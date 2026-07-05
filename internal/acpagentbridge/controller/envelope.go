package controller

import (
	"encoding/json"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type acpEnvelopeParticipantScope struct {
	binding session.ParticipantBinding
	agent   string
	turnID  string
}

func acpEnvelopeFromUpdate(env client.UpdateEnvelope, canonical *session.Event, participant *acpEnvelopeParticipantScope) *eventstream.Envelope {
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
	if participant != nil {
		applyACPParticipantEnvelopeScope(out, participant.binding, participant.agent, participant.turnID)
	}
	return out
}

func applyACPParticipantEnvelopeScope(env *eventstream.Envelope, binding session.ParticipantBinding, agent string, turnID string) {
	if env == nil {
		return
	}
	participantID := strings.TrimSpace(binding.ID)
	env.TurnID = strings.TrimSpace(firstNonEmpty(env.TurnID, turnID))
	env.Scope = eventstream.ScopeParticipant
	env.ScopeID = participantID
	env.ParticipantID = participantID
	env.Actor = strings.TrimSpace(firstNonEmpty(env.Actor, binding.Label, agent, participantID))
	env.Meta = applyACPParticipantDisplayMeta(env.Meta, binding, agent)
}

func applyACPParticipantDisplayMeta(meta map[string]any, binding session.ParticipantBinding, agent string) map[string]any {
	out := metautil.CloneMap(meta)
	if out == nil {
		out = map[string]any{}
	}
	if agent := strings.TrimSpace(agent); agent != "" {
		out["agent"] = agent
	}
	if label := strings.TrimSpace(binding.Label); label != "" {
		out["mention"] = label
		out["handle"] = strings.TrimPrefix(label, "@")
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
			MessageID:     strings.TrimSpace(typed.MessageID),
			Meta:          metautil.CloneMap(typed.Meta),
		}
	case client.ToolCall:
		return typed
	case client.ToolCallUpdate:
		return typed
	case client.PlanUpdate:
		return typed
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
