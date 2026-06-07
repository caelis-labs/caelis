package tool

import (
	"context"
)

// Tool is the contract for a runnable tool. Implementations live in
// tool/builtin/ or are registered externally.
type Tool interface {
	// Definition returns the tool's name, description, and schema.
	Definition() Definition

	// Run executes the tool call and returns a result.
	Run(Context, Call) (Result, error)
}

// Executor runs model-requested tool calls through the runtime-owned execution
// chain. Runners use this to keep policy, approval, sandbox, observer, and
// truncation semantics outside individual agents.
type Executor interface {
	Execute(context.Context, Call) (Result, error)
}

// Toolset is a named group of tools that can be loaded together.
type Toolset interface {
	// Name returns the toolset name.
	Name() string

	// Tools returns the tools in this toolset.
	Tools(context.Context) ([]Tool, error)
}

// Registry resolves tool names to Tool instances.
type Registry interface {
	// Lookup returns a tool by name.
	Lookup(context.Context, string) (Tool, bool, error)

	// List returns all registered tools.
	List(context.Context) ([]Tool, error)
}
