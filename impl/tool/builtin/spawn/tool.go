package spawn

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/caelis-labs/caelis/ports/delegation"
	"github.com/caelis-labs/caelis/ports/tool"
)

const ToolName = "SPAWN"

var allowedArgs = []string{"agent", "prompt"}

func ValidateArgs(args map[string]any) error {
	return tool.RejectUnknownArgs(args, allowedArgs...)
}

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
			"minLength":   1,
			"description": "Specific self-contained sub-task.",
		},
	}
	if enum := agentNames(t.agents); len(enum) > 0 {
		props["agent"].(map[string]any)["enum"] = enum
	}
	return tool.Definition{
		Name:        ToolName,
		Description: "Start a bounded delegated child session for work that can proceed independently. Use it for parallel investigation, isolated review, or a clearly scoped subtask, not for final integration or user-facing judgment. The prompt must be self-contained with goal, scope, constraints, expected output, and whether edits are allowed. To observe or wait for an existing child task, use TASK wait with the returned task_id; do not call SPAWN again.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(false, true, false, true),
	}
}

func (Tool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	args, err := toolutil.DecodeArgs(call)
	if err != nil {
		return tool.Result{}, err
	}
	if err := ValidateArgs(args); err != nil {
		return tool.Result{}, err
	}
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
		return "Agent name; omit for self."
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
		return "Agent name; omit for self."
	}
	return "Agent name from enum; omit for self. Agents: " + strings.Join(parts, "; ") + "."
}

var _ tool.Tool = Tool{}
