package agentsdk

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ModelRequestResolver supplies a complete configuration for the next unsent
// model request, including later tool rounds in the same Run. Returned models,
// tools and their definitions must remain immutable while that request and its
// tools execute. The Runtime applies its ordinary capability, policy, journal
// and lifecycle wrappers to each snapshot.
type ModelRequestResolver func(context.Context) (ModelRequestSnapshot, error)

// ModelRequestSnapshot replaces the model-facing configuration as one unit.
// Empty instructions, tools, reasoning and service tier are explicit values;
// they do not inherit the previous snapshot or AgentSpec metadata. Revision is
// an opaque embedding-owned identity and never enters the model prompt.
type ModelRequestSnapshot struct {
	Revision      string
	Model         model.LLM
	Tools         []tool.Tool
	DeferredTools tool.Source
	Instructions  []model.Part
	Reasoning     model.ReasoningConfig
	Request       ModelRequestOptions
	// Admit runs immediately before each actual provider attempt, including
	// retries, after the request is built. The embedding serializes this hook
	// with durable configuration updates. Returning ErrModelRequestSnapshotStale
	// rebuilds the unsent request without adding or changing canonical messages.
	// A successful admission pins this snapshot through response tool execution;
	// the hook must not retain its update lock for the provider stream lifetime.
	Admit func(context.Context, ModelRequestAdmission) error
}

// ModelRequestAdmission identifies one attempt at the provider dispatch boundary.
// RequestID is fresh for every actual attempt. Runtime supplies the canonical
// TurnID; a standalone chat Agent has no Runtime Turn and leaves it empty.
// Successful admission establishes local dispatch order, not remote receipt.
type ModelRequestAdmission struct {
	RequestID string
	TurnID    string
}

// ErrModelRequestSnapshotStale asks the chat loop to resolve another complete
// snapshot. It is not a provider failure and must bypass transport retries.
var ErrModelRequestSnapshotStale error = modelRequestSnapshotStaleError{}

type modelRequestSnapshotStaleError struct{}

func (modelRequestSnapshotStaleError) Error() string {
	return "agent-sdk: model request snapshot is stale"
}

func (modelRequestSnapshotStaleError) Retryable() bool { return false }

// CloneModelRequestSnapshot detaches the snapshot's containers. Models, tool
// callables, deferred sources and admission hooks retain their identity.
func CloneModelRequestSnapshot(in ModelRequestSnapshot) ModelRequestSnapshot {
	out := in
	out.Tools = append([]tool.Tool(nil), in.Tools...)
	out.Instructions = model.CloneParts(in.Instructions)
	out.Request = in.Request.WithDefaults(ModelRequestOptions{})
	return out
}
