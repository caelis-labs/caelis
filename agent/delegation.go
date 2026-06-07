package agent

import "github.com/OnslaughtSnail/caelis/tool"

// SpawnDelegator starts delegated child agent sessions for model-visible
// delegation tools. Runner implementations own session and lifecycle details.
type SpawnDelegator interface {
	// Spawn starts a child agent session and returns a handle.
	Spawn(ctx tool.Context, req SpawnRequest) (SpawnResult, error)
}

// SpawnRequest describes a delegated child-agent task.
type SpawnRequest struct {
	AgentName string
	Prompt    string
	RunID     string
}

// SpawnResult describes the child task handle returned to the model.
type SpawnResult struct {
	HandleID     string
	FinalMessage string
}
