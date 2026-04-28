package subagent

import (
	"context"

	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

// Registry exposes the spawnable ACP agents available to the runtime.
// App-layer assembly is responsible for registering and wiring the actual ACP
// endpoints or commands behind these descriptors.
type Registry interface {
	Get(context.Context, string) (sdkdelegation.Agent, error)
	List(context.Context) ([]sdkdelegation.Agent, error)
}

// ApprovalOption is one remote permission option exposed by a child ACP agent.
type ApprovalOption struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// ApprovalToolCall is the child tool call asking for approval.
type ApprovalToolCall struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name,omitempty"`
	Kind     string         `json:"kind,omitempty"`
	Title    string         `json:"title,omitempty"`
	Status   string         `json:"status,omitempty"`
	RawInput map[string]any `json:"raw_input,omitempty"`
}

// ApprovalRequest is one runtime-owned approval bridge payload for a spawned
// child ACP agent. It is system-controlled and never exposed on the LLM-facing
// SPAWN or TASK results.
type ApprovalRequest struct {
	SessionRef sdksession.SessionRef `json:"session_ref,omitempty"`
	Session    sdksession.Session    `json:"session,omitempty"`
	TaskID     string                `json:"task_id,omitempty"`
	Agent      string                `json:"agent,omitempty"`
	Mode       string                `json:"mode,omitempty"`
	ToolCall   ApprovalToolCall      `json:"tool_call,omitempty"`
	Options    []ApprovalOption      `json:"options,omitempty"`
}

// ApprovalResponse is one bridged child approval outcome.
type ApprovalResponse struct {
	Outcome  string `json:"outcome,omitempty"`
	OptionID string `json:"option_id,omitempty"`
	Approved bool   `json:"approved,omitempty"`
}

// ApprovalRequester bridges child ACP permission requests into the parent
// runtime's approval surface.
type ApprovalRequester interface {
	RequestSubagentApproval(context.Context, ApprovalRequest) (ApprovalResponse, error)
}

// SpawnContext is the system-controlled parent session context inherited by one
// child ACP agent. None of these fields are exposed on the LLM-facing SPAWN
// tool surface.
type SpawnContext struct {
	SessionRef        sdksession.SessionRef `json:"session_ref,omitempty"`
	Session           sdksession.Session    `json:"session,omitempty"`
	CWD               string                `json:"cwd,omitempty"`
	TaskID            string                `json:"task_id,omitempty"`
	Mode              string                `json:"mode,omitempty"`
	ApprovalRequester ApprovalRequester     `json:"-"`
	Streams           sdkstream.Sink        `json:"-"`
}

// Runner drives one spawned ACP child instance. The child itself is expected to
// run in its own session and persist its own transcript independently.
type Runner interface {
	Spawn(context.Context, SpawnContext, sdkdelegation.Request) (sdkdelegation.Anchor, sdkdelegation.Result, error)
	Continue(context.Context, sdkdelegation.Anchor, sdkdelegation.ContinueRequest) (sdkdelegation.Result, error)
	Wait(context.Context, sdkdelegation.Anchor, int) (sdkdelegation.Result, error)
	Cancel(context.Context, sdkdelegation.Anchor) error
}
