package spawn

import (
	"context"
	"fmt"
	"strings"

	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

const ToolName = "SPAWN"

type Tool struct {
	agents []sdkdelegation.Agent
}

func New(agents []sdkdelegation.Agent) Tool {
	out := make([]sdkdelegation.Agent, 0, len(agents))
	for _, one := range agents {
		normalized := sdkdelegation.NormalizeAgent(one)
		if normalized.Name == "" {
			continue
		}
		out = append(out, normalized)
	}
	return Tool{agents: out}
}

func (t Tool) Definition() sdktool.Definition {
	props := map[string]any{
		"agent": map[string]any{
			"type":        "string",
			"description": agentDescription(t.agents),
		},
		"prompt": map[string]any{
			"type":        "string",
			"description": "The sub-task for the selected agent. Keep it specific and self-contained.",
		},
	}
	if enum := agentNames(t.agents); len(enum) > 0 {
		props["agent"].(map[string]any)["enum"] = enum
	}
	return sdktool.Definition{
		Name:        ToolName,
		Description: "Delegate a sub-task to self or one registered ACP agent. SPAWN starts a child session and returns task_id plus handle metadata; use TASK wait, cancel, or write for follow-up control.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
	}
}

func (Tool) Call(context.Context, sdktool.Call) (sdktool.Result, error) {
	return sdktool.Result{}, fmt.Errorf("tool: SPAWN must be executed by the runtime wrapper")
}

func agentNames(agents []sdkdelegation.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, one := range agents {
		if name := strings.TrimSpace(one.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func agentDescription(agents []sdkdelegation.Agent) string {
	if len(agents) == 0 {
		return "Optional ACP agent name. Omit to use self."
	}
	parts := make([]string, 0, len(agents))
	for _, one := range agents {
		name := strings.TrimSpace(one.Name)
		if name == "" {
			continue
		}
		if desc := strings.TrimSpace(one.Description); desc != "" {
			parts = append(parts, name+": "+desc)
			continue
		}
		parts = append(parts, name)
	}
	if len(parts) == 0 {
		return "Optional ACP agent name. Omit to use self."
	}
	return "Optional ACP agent name. Available agents include self plus attached external ACP agents: " + strings.Join(parts, "; ") + ". Omit to use self."
}

var _ sdktool.Tool = Tool{}
