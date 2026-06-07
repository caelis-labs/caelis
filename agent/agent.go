package agent

import (
	"context"
	"fmt"
	"iter"

	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
)

// Agent is the contract for an executable agent. Implementations include
// llmagent (model/tool loop) and workflow agents (deferred).
type Agent interface {
	// Name returns the agent's unique name.
	Name() string

	// Description returns a human-readable description.
	Description() string

	// Run executes the agent and streams session events.
	Run(InvocationContext) iter.Seq2[session.Event, error]

	// SubAgents returns child agents in the agent tree.
	SubAgents() []Agent

	// FindAgent returns a child agent by name, or nil.
	FindAgent(name string) Agent
}

// Prepareable is optionally implemented by agents that need runner
// to wire dependencies (LLM, tools, tool context) before Run.
// Prepare returns a new prepared agent instance — the original is
// not mutated. This ensures concurrent Run calls are safe.
type Prepareable interface {
	// Prepare returns a new agent with the given dependencies wired.
	Prepare(PrepareRequest) Agent
}

// PrepareRequest carries the dependencies that runner wires into an agent.
type PrepareRequest struct {
	// LLM is the resolved model instance.
	LLM model.LLM
	// Tools are the resolved tool instances.
	Tools []tool.Tool
	// ToolContext provides sandbox/session info to tools during execution.
	ToolContext tool.Context
}

// InvocationContext provides runtime context for an agent invocation.
type InvocationContext interface {
	context.Context

	// Agent returns the current agent.
	Agent() Agent

	// Session returns the current session.
	Session() session.Session

	// InvocationID returns a unique identifier for this invocation.
	InvocationID() string

	// Branch returns the session branch name.
	Branch() string

	// UserMessage returns the user message that triggered this invocation.
	UserMessage() model.Message

	// PriorMessages returns the reconstructed model context from prior
	// session events. This is the durable replay — the agent must include
	// these messages before the current user message in its model request.
	PriorMessages() []model.Message

	// RunConfig returns the runtime configuration.
	RunConfig() *RunConfig

	// EndInvocation signals that the agent should stop.
	EndInvocation()

	// Ended returns true if EndInvocation was called.
	Ended() bool
}

// ToolContext extends tool.Context with agent-level information.
type ToolContext interface {
	tool.Context

	// AgentName returns the name of the agent running the tool.
	AgentName() string

	// InvocationID returns the current invocation identifier.
	InvocationID() string
}

// RunConfig holds runtime configuration for an agent invocation.
type RunConfig struct {
	// MaxModelCalls limits the number of model calls per invocation.
	MaxModelCalls int

	// MaxToolCalls limits the number of tool calls per invocation.
	MaxToolCalls int

	// Metadata is arbitrary key-value metadata.
	Metadata map[string]any
}

// DefaultRunConfig returns a RunConfig with sensible defaults.
func DefaultRunConfig() *RunConfig {
	return &RunConfig{
		MaxModelCalls: 100,
		MaxToolCalls:  100,
		Metadata:      make(map[string]any),
	}
}

// Validate checks that the run config has sensible values.
func (rc *RunConfig) Validate() error {
	if rc == nil {
		return nil
	}
	if rc.MaxModelCalls < 0 {
		return fmt.Errorf("run config: MaxModelCalls must be non-negative")
	}
	if rc.MaxToolCalls < 0 {
		return fmt.Errorf("run config: MaxToolCalls must be non-negative")
	}
	return nil
}

// ApprovalRequester is the contract for requesting user approval
// when a tool call requires it (e.g., escalated sandbox permissions).
// Implementations include gateway-level approval (TUI channel, auto-review)
// and test mocks.
type ApprovalRequester interface {
	// RequestApproval asks the user (or auto-reviewer) to approve a tool call.
	// Blocks until a decision is made or context is cancelled.
	RequestApproval(context.Context, ApprovalRequest) (ApprovalResponse, error)
}

// ApprovalRequest describes a tool call pending approval.
type ApprovalRequest struct {
	ToolName string
	CallID   string
	Args     map[string]any
	Reason   string
	Session  session.Session
	RunID    string
}

// ApprovalResponse is the user's decision.
type ApprovalResponse struct {
	Approved bool
	Reason   string
}
