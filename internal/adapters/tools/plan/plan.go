// Package plan provides a core-native execution plan tool.
package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

const ToolName = "update_plan"

type Status string

const (
	StatusPending    Status = "pending"
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
)

type Entry struct {
	Content string `json:"content"`
	Status  Status `json:"status"`
}

type Input struct {
	Explanation string  `json:"explanation,omitempty"`
	Entries     []Entry `json:"entries"`
}

type Tool struct{}

func New() Tool {
	return Tool{}
}

func (Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Replace the current execution plan.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"explanation": map[string]any{
					"type":        "string",
					"description": "Short reason why the plan changed.",
				},
				"entries": map[string]any{
					"type":        "array",
					"description": "Complete current plan.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{"type": "string"},
							"status": map[string]any{
								"type": "string",
								"enum": []string{
									string(StatusPending),
									string(StatusInProgress),
									string(StatusCompleted),
								},
							},
						},
						"required":             []any{"content", "status"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []any{"entries"},
			"additionalProperties": false,
		},
		Meta: map[string]any{
			"caelis.permission": "state",
			"caelis.kind":       "plan",
		},
	}
}

func (Tool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if ctx != nil {
		select {
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		default:
		}
	}
	var input Input
	if len(call.Input) > 0 {
		if err := json.Unmarshal(call.Input, &input); err != nil {
			return tool.Result{}, fmt.Errorf("tools/plan: invalid json input: %w", err)
		}
	}
	entries, err := normalizeEntries(input.Entries)
	if err != nil {
		return tool.Result{}, err
	}
	payload := map[string]any{"updated": true}
	raw, err := json.Marshal(payload)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		ID:   strings.TrimSpace(call.ID),
		Name: ToolName,
		Content: []model.Part{{
			Kind: model.PartJSON,
			JSON: &model.JSONPart{Value: raw},
		}},
		Meta: map[string]any{
			"plan_entries": entries,
			"explanation":  strings.TrimSpace(input.Explanation),
		},
	}, nil
}

func normalizeEntries(entries []Entry) ([]session.PlanEntry, error) {
	out := make([]session.PlanEntry, 0, len(entries))
	inProgress := 0
	for _, item := range entries {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			return nil, fmt.Errorf("tools/plan: entries.content is required")
		}
		status := normalizeStatus(item.Status)
		if status == "" {
			return nil, fmt.Errorf("tools/plan: entries.status must be pending, in_progress, or completed")
		}
		if status == StatusInProgress {
			inProgress++
		}
		out = append(out, session.PlanEntry{
			Content: content,
			Status:  string(status),
		})
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("tools/plan: at most one entry may be in_progress")
	}
	return out, nil
}

func normalizeStatus(value Status) Status {
	switch strings.TrimSpace(string(value)) {
	case string(StatusPending):
		return StatusPending
	case string(StatusInProgress):
		return StatusInProgress
	case string(StatusCompleted):
		return StatusCompleted
	default:
		return ""
	}
}

var _ tool.Tool = Tool{}
