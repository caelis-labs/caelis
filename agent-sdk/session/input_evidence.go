package session

import "github.com/caelis-labs/caelis/agent-sdk/model"

// InputEvidence retains authenticated submitted content independently of the
// effective Message. Hook-derived text never expands original user authority.
// Legacy events without this field use their original Message as evidence.
type InputEvidence struct {
	Message model.Message `json:"message"`
	Source  ActorRef      `json:"source"`
	Hooks   []string      `json:"hooks,omitempty"`
}

// OriginalInputMessage returns detached authorization evidence, never a hook's
// effective view. Non-input facts have no original input.
func OriginalInputMessage(e *Event) *model.Message {
	if e == nil || (EventTypeOf(e) != EventTypeUser && EventTypeOf(e) != EventTypeContext) {
		return nil
	}
	if e.InputEvidence != nil {
		m := model.CloneMessage(e.InputEvidence.Message)
		return &m
	}
	if e.Message == nil {
		return nil
	}
	m := model.CloneMessage(*e.Message)
	return &m
}
