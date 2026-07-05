package delegation

import (
	"strings"
	"time"
)

// Agent is the LLM-visible descriptor of one spawnable ACP agent.
// Name is the unique identifier used by SPAWN. Description is optional.
type Agent struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
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
	Agent     string `json:"agent,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
}

// Request describes one delegated child prompt. System-controlled execution
// details such as workspace, timeout, model, and prompt scaffolding are
// deliberately excluded from the LLM-visible SPAWN surface.
type Request struct {
	Agent  string `json:"agent,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// ContinueRequest describes a prompt appended to an existing child session.
// YieldTimeMS belongs to the TASK control plane, not the SPAWN tool surface.
type ContinueRequest struct {
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
