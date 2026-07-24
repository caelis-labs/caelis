package spawn

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const ToolName = names.Spawn

var allowedArgs = []string{"agent", "prompt"}

func ValidateArgs(args map[string]any) error {
	return tool.RejectUnknownArgs(args, allowedArgs...)
}

type Tool struct {
	agents  []delegation.Agent
	targets map[string]Target
}

// Target is the typed execution placement behind one model-visible Spawn
// selector.
type Target = delegation.Target

// Resolver resolves one validated model-visible selector before the durable
// Spawn intent is written.
type Resolver interface {
	ResolveTarget(string) (Target, error)
}

func New(agents []delegation.Agent) Tool {
	return NewWithTargets(agents, nil)
}

// NewWithTargets builds a Spawn tool with stable model-visible selectors and
// optional concrete execution placements. Missing placements execute the
// selector directly, preserving the generic SDK behavior.
func NewWithTargets(agents []delegation.Agent, targets map[string]Target) Tool {
	out := make([]delegation.Agent, 0, len(agents))
	for _, one := range agents {
		normalized := delegation.NormalizeAgent(one)
		if normalized.Name == "" {
			continue
		}
		out = append(out, normalized)
	}
	resolved := make(map[string]Target, len(targets))
	for selector, raw := range targets {
		target := normalizeTarget(raw)
		if target.Selector == "" {
			target.Selector = strings.TrimSpace(selector)
		}
		if delegation.ValidateTarget(target) != nil {
			continue
		}
		resolved[strings.ToLower(target.Selector)] = target
	}
	return Tool{agents: out, targets: resolved}
}

// ResolveTarget resolves one already-validated model-visible selector to its
// concrete execution placement.
func (t Tool) ResolveTarget(selector string) (Target, error) {
	selector = strings.TrimSpace(selector)
	if target, ok := t.targets[strings.ToLower(selector)]; ok {
		return cloneTarget(target), nil
	}
	for _, agent := range t.agents {
		if strings.EqualFold(agent.Name, selector) {
			name := strings.TrimSpace(agent.Name)
			return Target{Selector: name, Placement: delegation.Placement{Kind: delegation.PlacementAgent, Agent: name}}, nil
		}
	}
	return Target{}, fmt.Errorf("tool: Spawn agent %q is not available", selector)
}

func normalizeTarget(target Target) Target {
	return delegation.NormalizeTarget(target)
}

func cloneTarget(target Target) Target {
	return delegation.NormalizeTarget(target)
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
		Description: "Start a bounded delegated child session for work that can proceed independently. Use it for parallel investigation, isolated review, or a clearly scoped subtask, not for final integration or user-facing judgment. The prompt must be self-contained with goal, scope, constraints, expected output, and whether edits are allowed. To observe or wait for an existing child task, use Task wait with the returned handle; do not call Spawn again.",
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
	return tool.Result{}, fmt.Errorf("tool: Spawn must be executed by the runtime wrapper")
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
var _ Resolver = Tool{}
