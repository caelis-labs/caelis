package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/internal/toolutil"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

const ToolName = "PLAN"

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

type Args struct {
	Explanation string  `json:"explanation,omitempty"`
	Entries     []Entry `json:"entries"`
}

type Tool struct{}

func New() tool.Tool { return Tool{} }

func (Tool) Definition() tool.Definition {
	return tool.Definition{
		Name:        ToolName,
		Description: "Replace the visible execution plan with the complete current plan. Use it for multi-step, risky, or ambiguous tasks, and skip it for trivial one-step work. Keep entries short, outcome-oriented, and statused with at most one in_progress entry.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"explanation": map[string]any{
					"type":        "string",
					"description": "Why the plan changed.",
				},
				"entries": map[string]any{
					"type":        "array",
					"description": "Complete current plan.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{"type": "string", "minLength": 1},
							"status": map[string]any{
								"type": "string",
								"enum": []string{string(StatusPending), string(StatusInProgress), string(StatusCompleted)},
							},
						},
						"required":             []string{"content", "status"},
						"additionalProperties": false,
					},
				},
			},
			"required":             []string{"entries"},
			"additionalProperties": false,
		},
		Metadata: toolutil.AnnotationMetadata(false, false, true, false),
	}
}

func (Tool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	args, err := decodeArgs(call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	entries, err := normalizeEntries(args.Entries)
	if err != nil {
		return tool.Result{}, err
	}
	payload := map[string]any{
		"updated": true,
	}
	meta := map[string]any{
		"caelis": map[string]any{
			"version": 1,
			"runtime": map[string]any{
				"tool": map[string]any{
					"updated":     true,
					"entries":     entriesToAny(entries),
					"explanation": strings.TrimSpace(args.Explanation),
				},
			},
		},
	}
	raw, _ := json.Marshal(payload)
	return tool.Result{
		ID:       strings.TrimSpace(call.ID),
		Name:     ToolName,
		Content:  []model.Part{model.NewJSONPart(raw)},
		Metadata: meta,
	}, nil
}

func decodeArgs(raw json.RawMessage) (Args, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Args{}, nil
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return Args{}, fmt.Errorf("tool: decode args for %q: %w", ToolName, err)
	}
	if err := tool.RejectUnknownArgs(values, "explanation", "entries"); err != nil {
		return Args{}, err
	}
	explanation, err := optionalPlanString(values, "explanation")
	if err != nil {
		return Args{}, err
	}
	entries, err := decodePlanEntries(values["entries"])
	if err != nil {
		return Args{}, err
	}
	return Args{Explanation: explanation, Entries: entries}, nil
}

func decodePlanEntries(raw any) ([]Entry, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("tool: %s entries must be an array", ToolName)
	}
	entries := make([]Entry, 0, len(items))
	for idx, item := range items {
		values, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool: %s entries[%d] must be an object", ToolName, idx)
		}
		if err := tool.RejectUnknownArgs(values, "content", "status"); err != nil {
			return nil, fmt.Errorf("tool: %s entries[%d]: %w", ToolName, idx, err)
		}
		content, err := requiredPlanEntryString(values, idx, "content")
		if err != nil {
			return nil, err
		}
		status, err := requiredPlanEntryString(values, idx, "status")
		if err != nil {
			return nil, err
		}
		entries = append(entries, Entry{Content: content, Status: Status(status)})
	}
	return entries, nil
}

func optionalPlanString(values map[string]any, key string) (string, error) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("tool: %s %s must be string", ToolName, key)
	}
	return value, nil
}

func requiredPlanEntryString(values map[string]any, idx int, key string) (string, error) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return "", fmt.Errorf("tool: %s entries[%d].%s is required", ToolName, idx, key)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("tool: %s entries[%d].%s must be string", ToolName, idx, key)
	}
	return value, nil
}

func normalizeEntries(entries []Entry) ([]Entry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	out := make([]Entry, 0, len(entries))
	inProgress := 0
	for _, item := range entries {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			return nil, fmt.Errorf("tool: %q entries.content is required", ToolName)
		}
		status := normalizeStatus(item.Status)
		if status == "" {
			return nil, fmt.Errorf("tool: %q entries.status must be pending, in_progress, or completed", ToolName)
		}
		if status == StatusInProgress {
			inProgress++
		}
		out = append(out, Entry{Content: content, Status: status})
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("tool: %q allows at most one in_progress entry", ToolName)
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

func entriesToAny(entries []Entry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, item := range entries {
		out = append(out, map[string]any{
			"content": item.Content,
			"status":  string(item.Status),
		})
	}
	return out
}
