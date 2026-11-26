package eventstream

import "strings"

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

// SetSessionResultSequence records a committed result on a terminal Envelope
// whose narrative may have been delivered through a separate lane.
func SetSessionResultSequence(env *Envelope, seq uint64) {
	if !IsTurnTerminalLifecycle(*env) {
		return
	}
	lifecycle := *env.Lifecycle
	lifecycle.ResultThroughSeq = seq
	env.Lifecycle = &lifecycle
}

// TerminalSessionResultSequence returns the Host's confirmed result at Turn end.
func TerminalSessionResultSequence(env Envelope) uint64 {
	if !IsTurnTerminalLifecycle(env) {
		return 0
	}
	return env.Lifecycle.ResultThroughSeq
}
