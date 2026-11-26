package eventstream

import (
	"strconv"
	"strings"
)

const sessionResultSequenceKey = "session_result_seq"

// SessionResultSequence identifies a committed, nonempty main Assistant
// response. Replaying thought, tools and transient text cannot advance it.
func SessionResultSequence(env Envelope) uint64 {
	if !env.Final || env.Position == nil || env.Position.Durable == nil ||
		env.Scope != "" && env.Scope != ScopeMain || !HasAssistantContent(env) {
		return 0
	}
	if env.Delivery == nil || env.Delivery.Mode != DeliveryCanonical && env.Delivery.Mode != DeliveryMirror {
		return 0
	}
	return env.Position.Durable.Seq
}

// HasAssistantContent reports whether the Envelope displays main narrative.
func HasAssistantContent(env Envelope) bool {
	if env.Kind != KindSessionUpdate || env.Scope != "" && env.Scope != ScopeMain {
		return false
	}
	update, ok := env.Update.(ContentChunk)
	if !ok || UpdateType(update) != UpdateAgentMessage {
		return false
	}
	switch content := update.Content.(type) {
	case TextContent:
		return strings.TrimSpace(content.Text) != ""
	case map[string]any:
		if content["type"] == "text" {
			text, _ := content["text"].(string)
			return strings.TrimSpace(text) != ""
		}
		return content["type"] != nil
	default:
		return update.Content != nil
	}
}

// SetSessionResultSequence annotates the producer's terminal Envelope with a
// committed result already presented through the Turn's live narrative lane.
func SetSessionResultSequence(env *Envelope, seq uint64) {
	if env.Meta == nil {
		env.Meta = make(map[string]any)
	}
	delete(env.Meta, sessionResultSequenceKey)
	if seq != 0 {
		env.Meta[sessionResultSequenceKey] = strconv.FormatUint(seq, 10)
	}
}

// TerminalSessionResultSequence returns the Host's confirmed result at Turn end.
func TerminalSessionResultSequence(env Envelope) uint64 {
	if !IsTurnTerminalLifecycle(env) {
		return 0
	}
	raw, _ := env.Meta[sessionResultSequenceKey].(string)
	seq, _ := strconv.ParseUint(raw, 10, 64)
	return seq
}
