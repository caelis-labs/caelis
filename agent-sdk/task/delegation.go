package task

import (
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
)

// DelegationAgent is the LLM-visible descriptor of one spawnable ACP agent.
// Name is the unique identifier used by SPAWN. Description is optional.
type DelegationAgent struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Target combines one stable model-visible selector with its resolved durable
// execution placement.
type Target struct {
	Selector  string              `json:"selector,omitempty"`
	Placement placement.Placement `json:"placement,omitempty"`
}

// Anchor identifies one delegated child instance for system/runtime use.
// It is not intended to be exposed to the LLM-facing tool result surface.
type Anchor struct {
	TaskID    string `json:"task_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Agent is legacy diagnostic data only. Runtime execution and validation
	// must use the frozen Target.Placement and never compare this value with a
	// model-visible selector.
	// Deprecated: ignored by Runtime and adapters.
	Agent   string `json:"agent,omitempty"`
	AgentID string `json:"agent_id,omitempty"`
}

// DelegationRequest describes one delegated child prompt. System-controlled
// execution details such as workspace, timeout, model, and prompt scaffolding
// are deliberately excluded from the LLM-visible SPAWN surface.
type DelegationRequest struct {
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// TargetRequest carries a typed durable execution placement to runners that
// implement the optional PlacementRunner extension.
type TargetRequest struct {
	Target Target `json:"target,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// CompletionSink receives the terminal result owned by one child execution
// turn. Producers publish from their independent completion path after
// Spawn/Message returns and after releasing their own locks. Publish does not
// return until Runtime has durably converged the terminal result.
type CompletionSink interface {
	PublishSubagentCompletion(DelegationResult)
}

// DelegationResult captures one collaborating Agent summary visible to Runtime
// and the calling Agent. The collaborator transcript remains in its own Session.
type DelegationResult struct {
	TaskID string `json:"task_id,omitempty"`
	// State is authoritative for Runtime. StateRunning and StateWaitingApproval
	// describe live activities. All other valid values are terminal Task states.
	State   State `json:"state,omitempty"`
	Running bool  `json:"running,omitempty"`
	Yielded bool  `json:"yielded,omitempty"`
	// SupportsSteering allows Agent input while the current Turn is running.
	SupportsSteering bool   `json:"supports_steering"`
	OutputPreview    string `json:"output_preview,omitempty"`
	// Error is one bounded, non-sensitive operation summary for a terminal
	// delegated child. Producers must not include raw provider errors, stderr,
	// credentials, or local paths. It remains distinct from Result, which is
	// reserved for canonical assistant output.
	Error     string    `json:"error,omitempty"`
	Result    string    `json:"result,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// NormalizeDelegationAgent returns a normalized descriptor copy.
func NormalizeDelegationAgent(in DelegationAgent) DelegationAgent {
	return DelegationAgent{
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
	}
}

// CloneAnchor returns a normalized copy of an instance anchor.
func CloneAnchor(in Anchor) Anchor {
	return Anchor{
		TaskID:    strings.TrimSpace(in.TaskID),
		SessionID: strings.TrimSpace(in.SessionID),
		Agent:     strings.TrimSpace(in.Agent),
		AgentID:   strings.TrimSpace(in.AgentID),
	}
}

// CloneDelegationRequest returns a trimmed copy of a spawn prompt request.
func CloneDelegationRequest(in DelegationRequest) DelegationRequest {
	out := in
	out.Agent = strings.TrimSpace(in.Agent)
	out.Prompt = strings.TrimSpace(in.Prompt)
	return out
}

// CloneTargetRequest returns a canonical detached typed Spawn request.
func CloneTargetRequest(in TargetRequest) TargetRequest {
	return TargetRequest{Target: NormalizeTarget(in.Target), Prompt: strings.TrimSpace(in.Prompt)}
}

// AgentTarget returns a target that directly executes one assembled Agent.
func AgentTarget(name string) Target {
	name = strings.TrimSpace(name)
	return Target{Selector: name, Placement: placement.Placement{Kind: placement.KindAgent, Agent: name}}
}

// ValidateTarget rejects incomplete or unknown execution placements before a
// durable external-effect claim is written.
func ValidateTarget(raw Target) error {
	target := NormalizeTarget(raw)
	if target.Selector == "" {
		return fmt.Errorf("agent-sdk/task: target selector is required")
	}
	if err := placement.Validate(target.Placement); err != nil {
		return fmt.Errorf("agent-sdk/task: %w", err)
	}
	return nil
}

// CloneDelegationResult trims text, canonicalizes the state, and retains Error
// only for terminal failures.
func CloneDelegationResult(in DelegationResult) DelegationResult {
	out := in
	out.State = State(strings.ToLower(strings.TrimSpace(string(in.State))))
	out.TaskID = strings.TrimSpace(in.TaskID)
	out.OutputPreview = strings.TrimSpace(in.OutputPreview)
	out.Error = strings.TrimSpace(in.Error)
	out.Result = strings.TrimSpace(in.Result)
	if out.State != StateFailed && out.State != StateInterrupted && out.State != StateUnknownOutcome {
		out.Error = ""
	}
	return out
}

// NormalizeTarget returns one canonical detached target.
func NormalizeTarget(in Target) Target {
	return Target{
		Selector:  strings.TrimSpace(in.Selector),
		Placement: placement.Normalize(in.Placement),
	}
}
