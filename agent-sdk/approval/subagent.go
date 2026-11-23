package approval

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// SubagentRequest is one child permission request bridged into the
// parent runtime approval surface.
type SubagentRequest struct {
	// Approval preserves independent endpoint context when no tool invocation
	// exists. The legacy ToolCall/Options projection is used only when nil.
	Approval     *session.ProtocolApproval `json:"approval,omitempty"`
	Origin       Origin                    `json:"origin"`
	SessionRef   session.SessionRef        `json:"session_ref,omitempty"`
	Session      session.Session           `json:"session,omitempty"`
	TaskID       string                    `json:"task_id,omitempty"`
	ParentCallID string                    `json:"parent_call_id,omitempty"`
	Agent        string                    `json:"agent,omitempty"`
	Mode         string                    `json:"mode,omitempty"`
	ToolCall     session.ProtocolToolCall  `json:"tool_call,omitempty"`
	Options      []Option                  `json:"options,omitempty"`
}

// SubagentRequester bridges child permission requests into the parent
// runtime's approval surface.
type SubagentRequester interface {
	RequestSubagentApproval(context.Context, SubagentRequest) (Response, error)
}
