package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
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

func New() sdktool.Tool { return Tool{} }

func (Tool) Definition() sdktool.Definition {
	return sdktool.Definition{
		Name:        ToolName,
		Description: "Replace the current execution plan for non-trivial multi-step work. Keep steps concise and provide the full current list.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"explanation": map[string]any{
					"type":        "string",
					"description": "Optional short note explaining why the plan changed.",
				},
				"entries": map[string]any{
					"type":        "array",
					"description": "The complete current plan.",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{"type": "string"},
							"status": map[string]any{
								"type": "string",
								"enum": []string{string(StatusPending), string(StatusInProgress), string(StatusCompleted)},
							},
						},
						"required": []string{"content", "status"},
					},
				},
			},
			"required": []string{"entries"},
		},
	}
}

func (Tool) Call(_ context.Context, call sdktool.Call) (sdktool.Result, error) {
	args, err := decodeArgs(call.Input)
	if err != nil {
		return sdktool.Result{}, err
	}
	entries, err := normalizeEntries(args.Entries)
	if err != nil {
		return sdktool.Result{}, err
	}
	result := map[string]any{
		"message":     "Plan updated",
		"entries":     entriesToAny(entries),
		"explanation": strings.TrimSpace(args.Explanation),
	}
	raw, _ := json.Marshal(result)
	return sdktool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    ToolName,
		Content: []sdkmodel.Part{sdkmodel.NewJSONPart(raw)},
		Meta:    result,
	}, nil
}

func decodeArgs(raw json.RawMessage) (Args, error) {
	var args Args
	if err := json.Unmarshal(raw, &args); err != nil {
		return Args{}, fmt.Errorf("tool: decode args for %q: %w", ToolName, err)
	}
	return args, nil
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
