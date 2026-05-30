// Package spawn provides the core-native SPAWN tool declaration.
package spawn

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

const ToolName = "SPAWN"

type Agent struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type Tool struct {
	agents []Agent
}

func New(agents []Agent) *Tool {
	out := make([]Agent, 0, len(agents))
	seen := map[string]struct{}{}
	for _, agent := range agents {
		agent = normalizeAgent(agent)
		key := strings.ToLower(firstNonEmpty(agent.ID, agent.Name))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, agent)
	}
	return &Tool{agents: out}
}

func AgentsFromPlugin(in []plugin.ACPAgentDescriptor) []Agent {
	if len(in) == 0 {
		return nil
	}
	out := make([]Agent, 0, len(in))
	for _, agent := range in {
		name := strings.TrimSpace(agent.Name)
		out = append(out, Agent{
			ID:          name,
			Name:        name,
			Description: strings.TrimSpace(agent.Description),
		})
	}
	return out
}

func (t *Tool) Definition() tool.Definition {
	agents := []Agent(nil)
	if t != nil {
		agents = t.agents
	}
	props := map[string]any{
		"agent": map[string]any{
			"type":        "string",
			"description": agentDescription(agents),
		},
		"prompt": map[string]any{
			"type":        "string",
			"description": "Specific self-contained sub-task for the delegated ACP child agent.",
		},
	}
	if enum := agentNames(agents); len(enum) > 0 {
		props["agent"].(map[string]any)["enum"] = enum
	}
	return tool.Definition{
		Name:        ToolName,
		Description: "Start a delegated ACP child session.",
		InputSchema: map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             []any{"prompt"},
			"additionalProperties": false,
		},
		Meta: map[string]any{
			"caelis.kind": "spawn",
		},
	}
}

func (t *Tool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	return tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    ToolName,
		IsError: true,
		Content: []model.Part{model.NewTextPart("SPAWN must be executed by the runtime spawner")},
	}, fmt.Errorf("tools/spawn: runtime spawner is required")
}

func normalizeAgent(in Agent) Agent {
	out := in
	out.ID = strings.TrimSpace(in.ID)
	out.Name = strings.TrimSpace(in.Name)
	out.Description = strings.TrimSpace(in.Description)
	if out.ID == "" {
		out.ID = out.Name
	}
	if out.Name == "" {
		out.Name = out.ID
	}
	return out
}

func agentNames(agents []Agent) []any {
	out := make([]any, 0, len(agents))
	for _, agent := range agents {
		name := firstNonEmpty(agent.Name, agent.ID)
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func agentDescription(agents []Agent) string {
	if len(agents) == 0 {
		return "Agent name; omit for self when a self ACP agent is configured."
	}
	parts := make([]string, 0, len(agents))
	for _, agent := range agents {
		name := firstNonEmpty(agent.Name, agent.ID)
		if name == "" {
			continue
		}
		if agent.Description != "" {
			parts = append(parts, name+": "+agent.Description)
			continue
		}
		parts = append(parts, name)
	}
	if len(parts) == 0 {
		return "Agent name; omit for self when a self ACP agent is configured."
	}
	return "Agent name from enum; omit for self. Agents: " + strings.Join(parts, "; ") + "."
}

func ResultParts(payload map[string]any) ([]model.Part, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return []model.Part{
		model.NewTextPart(summary(payload)),
		{
			Kind: model.PartJSON,
			JSON: &model.JSONPart{Value: raw},
		},
	}, nil
}

func summary(payload map[string]any) string {
	agent, _ := payload["agent"].(string)
	state, _ := payload["state"].(string)
	taskID, _ := payload["task_id"].(string)
	final, _ := payload["final_message"].(string)
	var lines []string
	header := strings.TrimSpace("spawn " + agent + " " + state)
	if header != "" {
		lines = append(lines, header)
	}
	if taskID != "" {
		lines = append(lines, "task_id: "+taskID)
	}
	if strings.TrimSpace(final) != "" {
		lines = append(lines, "final_message:\n"+strings.TrimRight(final, "\n"))
	}
	if len(lines) == 0 {
		return "spawn completed"
	}
	return strings.Join(lines, "\n\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ tool.Tool = (*Tool)(nil)
