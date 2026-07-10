package subagent

import (
	"context"

	agent "github.com/caelis-labs/caelis/agent-sdk"
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
	Continue(context.Context, delegation.Anchor, delegation.ContinueRequest) (delegation.Result, error)
	Wait(context.Context, delegation.Anchor, int) (delegation.Result, error)
	Cancel(context.Context, delegation.Anchor) error
}
