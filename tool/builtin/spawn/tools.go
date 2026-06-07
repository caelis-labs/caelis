package spawn

import (
	"fmt"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/tool"
)

// spawnTool implements the SPAWN tool for agent delegation.
// It delegates execution to a child agent via a Delegator injected at runtime.
type spawnTool struct {
	delegator agent.SpawnDelegator
}

func New(delegator agent.SpawnDelegator) tool.Tool {
	return &spawnTool{delegator: delegator}
}

func (t *spawnTool) WithSpawnDelegator(delegator agent.SpawnDelegator) tool.Tool {
	clone := *t
	clone.delegator = delegator
	return &clone
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

	result, err := t.delegator.Spawn(ctx, agent.SpawnRequest{
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
