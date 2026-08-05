package subagent

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

// Registry exposes the spawnable ACP agents available to the runtime.
// App-layer assembly is responsible for registering and wiring the actual ACP
// endpoints or commands behind these descriptors.
type Registry interface {
	Get(context.Context, string) (delegation.Agent, error)
	List(context.Context) ([]delegation.Agent, error)
}

// ApprovalOption is one remote permission option exposed by a child ACP agent.
type ApprovalOption = agent.ApprovalOption

// ApprovalToolCall is the child tool call asking for approval.
type ApprovalToolCall = agent.SubagentApprovalToolCall

// ApprovalRequest is one runtime-owned approval bridge payload for a spawned
// child ACP agent. It is system-controlled and never exposed on the LLM-facing
// SPAWN or TASK results.
type ApprovalRequest = agent.SubagentApprovalRequest

// ApprovalResponse is one bridged child approval outcome.
type ApprovalResponse = agent.ApprovalResponse

// ApprovalRequester bridges child ACP permission requests into the parent
// runtime's approval surface.
type ApprovalRequester = agent.SubagentApprovalRequester

// SpawnContext is the system-controlled parent session context inherited by one
// child ACP agent. None of these fields are exposed on the LLM-facing SPAWN
// tool surface. ApprovalMode is the parent session mode for runners that derive
// child launch configuration from the spawn request; preassembled ACP runners
// may already carry the effective child approval mode in their launch args.
type SpawnContext = agent.SubagentSpawnContext

// Runner drives one spawned ACP child instance. The child itself is expected to
// run in its own session and persist its own transcript independently.
type Runner interface {
	Spawn(context.Context, SpawnContext, delegation.Request) (delegation.Anchor, delegation.Result, error)
	Wait(context.Context, delegation.Anchor, int) (delegation.Result, error)
	Cancel(context.Context, delegation.Anchor) error
}

// MessageRequest carries one trusted Agent message to an existing child. A
// completed child receives Completion for the newly started turn; a running
// child injects the message at its next safe boundary.
type MessageRequest struct {
	agentmessage.Request
	Completion delegation.CompletionSink `json:"-"`
	// Reconnect carries trusted durable execution identity when the Runtime
	// recovered a completed Task but the runner has no process-local child.
	Reconnect *ReconnectRequest `json:"-"`
}

// ReconnectRequest lets a MessageRunner reattach the exact durable child
// Session and Task before accepting a new message-authored Turn. It is Runtime
// recovery context, not a model-facing message argument.
type ReconnectRequest struct {
	Spawn  SpawnContext      `json:"-"`
	Target delegation.Target `json:"-"`
}

// CloneReconnectRequest copies recovery values while preserving injected
// callback interfaces owned by the current Runtime activation.
func CloneReconnectRequest(in *ReconnectRequest) *ReconnectRequest {
	if in == nil {
		return nil
	}
	out := *in
	out.Spawn.SessionRef = session.NormalizeSessionRef(in.Spawn.SessionRef)
	out.Spawn.Session = session.CloneSession(in.Spawn.Session)
	out.Target = delegation.CloneTargetRequest(delegation.TargetRequest{Target: in.Target}).Target
	return &out
}

// MessageRunner is the optional Agent-message extension required by
// SendMessage. Message returns when the runner accepts ownership of delivery;
// target consumption and a newly started child Turn continue asynchronously.
type MessageRunner interface {
	Message(context.Context, delegation.Anchor, MessageRequest) (delegation.Result, error)
}

// PlacementRunner is the optional typed Spawn extension used when Control has
// resolved a model-backed placement that is not an assembled Agent identity.
type PlacementRunner interface {
	SpawnTarget(context.Context, SpawnContext, delegation.TargetRequest) (delegation.Anchor, delegation.Result, error)
}
