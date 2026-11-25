package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const ToolName = "Plan"

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
		Name:         ToolName,
		Description:  "Maintain a short multi-step execution checklist so you and the user can track remaining goals. Skip one-step work and pure investigation/write-ups. When updating: keep prior step texts, advance status, and insert or append steps as needed; do not overwrite or delete history. At most one in_progress entry.",
		OutputSchema: map[string]any{"type": "object"},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"explanation": map[string]any{
					"type":        "string",
					"description": "Optional. Why the plan changed (status/insert/append), not progress narration.",
				},
				"entries": map[string]any{
					"type":        "array",
					"description": "Full current plan. Preserve prior steps; update status and insert/append only.",
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
	}
}

func (t Tool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	result, _, err := t.CallWithUpdate(call)
	return result, err
}

// CallWithUpdate returns the normalized plan alongside its model-visible acknowledgment.
func (Tool) CallWithUpdate(call tool.Call) (tool.Result, Args, error) {
	args, err := decodeArgs(call.Input)
	if err != nil {
		return tool.Result{}, Args{}, err
	}
	args.Entries, err = normalizeEntries(args.Entries)
	if err != nil {
		return tool.Result{}, Args{}, err
	}
	args.Explanation = strings.TrimSpace(args.Explanation)
	result := tool.Result{
		Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`{"updated":true}`)}},
	}
	return result, args, nil
}

func decodeArgs(raw json.RawMessage) (Args, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return Args{}, nil
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return Args{}, fmt.Errorf("decode args for %q: %w", ToolName, err)
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
		return nil, fmt.Errorf("%s entries must be an array", ToolName)
	}
	entries := make([]Entry, 0, len(items))
	for idx, item := range items {
		values, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s entries[%d] must be an object", ToolName, idx)
		}
		if err := tool.RejectUnknownArgs(values, "content", "status"); err != nil {
			return nil, fmt.Errorf("%s entries[%d]: %w", ToolName, idx, err)
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
		return "", fmt.Errorf("%s %s must be string", ToolName, key)
	}
	return value, nil
}

func requiredPlanEntryString(values map[string]any, idx int, key string) (string, error) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return "", fmt.Errorf("%s entries[%d].%s is required", ToolName, idx, key)
	}
	value, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s entries[%d].%s must be string", ToolName, idx, key)
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
			return nil, fmt.Errorf("%q entries.content is required", ToolName)
		}
		status := normalizeStatus(item.Status)
		if status == "" {
			return nil, fmt.Errorf("%q entries.status must be pending, in_progress, or completed", ToolName)
		}
		if status == StatusInProgress {
			inProgress++
		}
		out = append(out, Entry{Content: content, Status: status})
	}
	if inProgress > 1 {
		return nil, fmt.Errorf("%q allows at most one in_progress entry", ToolName)
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
