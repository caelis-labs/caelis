package delegation

import (
	"fmt"
	"strings"
	"time"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
)

// Agent is the LLM-visible descriptor of one spawnable ACP agent.
// Name is the unique identifier used by SPAWN. Description is optional.
type Agent struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// PlacementKind identifies the reusable execution backend captured for a
// delegated child. Product-specific profile selection is resolved before this
// value enters the Runtime.
type PlacementKind = sdkplacement.Kind

const (
	// PlacementAgent runs an already assembled external or built-in Agent.
	PlacementAgent = sdkplacement.KindAgent
	// PlacementModel runs one configured model through the host's local child
	// endpoint factory.
	PlacementModel = sdkplacement.KindModel
)

// Placement is the typed durable execution decision behind a model-visible
// Spawn selector. ConfigFingerprint lets the host fail closed when a prepared
// Spawn is recovered after its referenced configuration changed.
type Placement = sdkplacement.Placement

// Target combines one stable model-visible selector with its resolved durable
// execution placement.
type Target struct {
	Selector  string    `json:"selector,omitempty"`
	Placement Placement `json:"placement,omitempty"`
}

// State identifies one delegated child lifecycle state.
type State string

const (
	StateRunning         State = "running"
	StateCompleted       State = "completed"
	StateFailed          State = "failed"
	StateCancelled       State = "cancelled"
	StateInterrupted     State = "interrupted"
	StateWaitingApproval State = "waiting_approval"
)

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

// Request describes one delegated child prompt. System-controlled execution
// details such as workspace, timeout, model, and prompt scaffolding are
// deliberately excluded from the LLM-visible SPAWN surface.
type Request struct {
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// TargetRequest carries a typed durable execution placement to runners that
// implement the optional subagent.PlacementRunner extension.
type TargetRequest struct {
	Target Target `json:"target,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// ContinueRequest describes a prompt appended to an existing child session.
// YieldTimeMS belongs to the TASK control plane, not the SPAWN tool surface.
type ContinueRequest struct {
	// Agent is retained for SDK compatibility and is derived from the Anchor by
	// the Runtime.
	// Deprecated: use the Agent carried by Anchor.
	Agent       string `json:"agent,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	YieldTimeMS int    `json:"yield_time_ms,omitempty"`
}

// Result captures one delegated child summary visible to runtime and the
// calling agent. The child transcript remains in its own session.
type Result struct {
	TaskID        string    `json:"task_id,omitempty"`
	State         State     `json:"state,omitempty"`
	Running       bool      `json:"running,omitempty"`
	Yielded       bool      `json:"yielded,omitempty"`
	OutputPreview string    `json:"output_preview,omitempty"`
	Result        string    `json:"result,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

func NormalizeAgent(in Agent) Agent {
	return Agent{
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
	}
}

func CloneAnchor(in Anchor) Anchor {
	return Anchor{
		TaskID:    strings.TrimSpace(in.TaskID),
		SessionID: strings.TrimSpace(in.SessionID),
		Agent:     strings.TrimSpace(in.Agent),
		AgentID:   strings.TrimSpace(in.AgentID),
	}
}

func CloneRequest(in Request) Request {
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
	return Target{Selector: name, Placement: Placement{Kind: PlacementAgent, Agent: name}}
}

// ValidateTarget rejects incomplete or unknown execution placements before a
// durable external-effect claim is written.
func ValidateTarget(raw Target) error {
	target := NormalizeTarget(raw)
	if target.Selector == "" {
		return fmt.Errorf("agent-sdk/task/delegation: target selector is required")
	}
	if err := sdkplacement.Validate(target.Placement); err != nil {
		return fmt.Errorf("agent-sdk/task/delegation: %w", err)
	}
	return nil
}

func CloneContinueRequest(in ContinueRequest) ContinueRequest {
	out := in
	out.Agent = strings.TrimSpace(in.Agent)
	out.Prompt = strings.TrimSpace(in.Prompt)
	return out
}

func CloneResult(in Result) Result {
	out := in
	out.State = State(strings.TrimSpace(string(in.State)))
	out.TaskID = strings.TrimSpace(in.TaskID)
	out.OutputPreview = strings.TrimSpace(in.OutputPreview)
	out.Result = strings.TrimSpace(in.Result)
	return out
}

// NormalizePlacement returns one canonical detached placement.
func NormalizePlacement(in Placement) Placement {
	return sdkplacement.Normalize(in)
}

// NormalizeTarget returns one canonical detached target.
func NormalizeTarget(in Target) Target {
	return Target{
		Selector:  strings.TrimSpace(in.Selector),
		Placement: NormalizePlacement(in.Placement),
	}
}
