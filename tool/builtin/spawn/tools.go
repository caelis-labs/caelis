package spawn

import (
	"fmt"

	"github.com/OnslaughtSnail/caelis/tool"
)

// spawnTool implements the SPAWN tool for agent delegation.
// It delegates execution to a child agent via a Delegator injected at runtime.
type spawnTool struct {
	delegator Delegator
}

// Delegator is the contract for spawning child agent sessions.
// Runner implements this to create child invocations.
type Delegator interface {
	// Spawn starts a child agent session and returns a handle.
	Spawn(ctx tool.Context, req SpawnRequest) (SpawnResult, error)
}

// SpawnRequest is the input to Delegator.Spawn.
type SpawnRequest struct {
	AgentName string
	Prompt    string
	RunID     string
}

// SpawnResult is the output of Delegator.Spawn.
type SpawnResult struct {
	HandleID     string
	FinalMessage string
}

func New(delegator Delegator) tool.Tool {
	return &spawnTool{delegator: delegator}
}

func (*spawnTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "SPAWN",
		Description: "Start a bounded delegated child session.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"agent":  {Type: "string", Description: "Agent name"},
				"prompt": {Type: "string", Description: "Self-contained sub-task prompt"},
			},
			Required: []string{"prompt"},
		},
		Metadata: map[string]any{
			"requires_delegation": true,
		},
	}
}

func (t *spawnTool) Run(ctx tool.Context, call tool.Call) (tool.Result, error) {
	prompt, _ := call.Args["prompt"].(string)
	if prompt == "" {
		return tool.Result{Output: "prompt is required", IsError: true}, nil
	}
	agentName, _ := call.Args["agent"].(string)

	if t.delegator == nil {
		return tool.Result{Output: "SPAWN: no delegator configured", IsError: true}, nil
	}

	result, err := t.delegator.Spawn(ctx, SpawnRequest{
		AgentName: agentName,
		Prompt:    prompt,
		RunID:     ctx.InvocationID(),
	})
	if err != nil {
		return tool.Result{Output: fmt.Sprintf("SPAWN error: %v", err), IsError: true}, nil
	}

	return tool.Result{
		Output: result.FinalMessage,
		Metadata: map[string]any{
			"handle_id": result.HandleID,
		},
	}, nil
}

// All returns all spawn built-in tools (requires delegator to be set).
func All() []tool.Tool {
	return []tool.Tool{&spawnTool{}}
}
