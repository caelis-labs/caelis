package spawn

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/delegation"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const ToolName = "SPAWN"

type Tool struct {
	agents []delegation.Agent
}

func New(agents []delegation.Agent) Tool {
	out := make([]delegation.Agent, 0, len(agents))
	for _, one := range agents {
		normalized := delegation.NormalizeAgent(one)
		if normalized.Name == "" {
			continue
		}
		out = append(out, normalized)
	}
	return Tool{agents: out}
}

func (t Tool) Definition() tool.Definition {
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
	return tool.Definition{
		Name:        ToolName,
		Description: "Delegate a sub-task to self or one registered ACP agent. SPAWN starts a child session and returns a task handle; use that handle with TASK wait, cancel, or write for follow-up control.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
	}
}

func (Tool) Call(context.Context, tool.Call) (tool.Result, error) {
	return tool.Result{}, fmt.Errorf("tool: SPAWN must be executed by the runtime wrapper")
}

func agentNames(agents []delegation.Agent) []string {
	out := make([]string, 0, len(agents))
	for _, one := range agents {
		if name := strings.TrimSpace(one.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

func agentDescription(agents []delegation.Agent) string {
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

var _ tool.Tool = Tool{}
