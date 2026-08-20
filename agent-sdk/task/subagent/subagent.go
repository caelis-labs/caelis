package subagent

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
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

// ReconnectRequest lets an endpoint runner reattach the exact durable child
// Session and Task before ordinary input or history access. It is Runtime
// recovery context, not model-facing input.
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

// HistoryRequest identifies one durable child Session whose presentation
// history may be loaded without resuming execution. Anchor and Reconnect are
// trusted Task facts reconstructed by Control, never model input.
type HistoryRequest struct {
	Anchor    delegation.Anchor `json:"-"`
	Reconnect ReconnectRequest  `json:"-"`
}

// CloneHistoryRequest copies a read-only child Session recovery request.
func CloneHistoryRequest(in HistoryRequest) HistoryRequest {
	reconnect := CloneReconnectRequest(&in.Reconnect)
	out := HistoryRequest{Anchor: delegation.CloneAnchor(in.Anchor)}
	if reconnect != nil {
		out.Reconnect = *reconnect
	}
	return out
}

// HistoryRunner is the optional read-only extension used when Control lazily
// opens a terminal child workspace whose transcript is provider-owned. The
// implementation must load the existing Session and must not resume execution
// or derive history from the parent Task result.
type HistoryRunner interface {
	LoadHistory(context.Context, HistoryRequest) (session.LoadedSession, error)
}

// PlacementRunner is the optional typed Spawn extension used when Control has
// resolved a model-backed placement that is not an assembled Agent identity.
type PlacementRunner interface {
	SpawnTarget(context.Context, SpawnContext, delegation.TargetRequest) (delegation.Anchor, delegation.Result, error)
}
