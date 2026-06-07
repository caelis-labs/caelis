package agent

import (
	"context"

	"github.com/OnslaughtSnail/caelis/tool"
)

// Hook observes runner-owned invocation and tool lifecycle operations.
type Hook interface {
	BeforeInvocation(context.Context, InvocationHook) error
	AfterInvocation(context.Context, InvocationHookResult) error
	BeforeTool(context.Context, ToolHook) error
	AfterTool(context.Context, ToolHookResult) error
}

// InvocationHook describes one runner invocation.
type InvocationHook struct {
	AgentName    string
	SessionID    string
	InvocationID string
	Branch       string
	Metadata     map[string]any
}

// InvocationHookResult describes one completed runner invocation.
type InvocationHookResult struct {
	InvocationHook
	Error error
}

// ToolHook describes one tool call passing through the runner executor chain.
type ToolHook struct {
	AgentName    string
	SessionID    string
	InvocationID string
	ToolName     string
	CallID       string
	Args         map[string]any
	Metadata     map[string]any
}

// ToolHookResult describes one completed tool call.
type ToolHookResult struct {
	ToolHook
	Result tool.Result
	Error  error
}
