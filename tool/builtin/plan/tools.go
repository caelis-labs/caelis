package plan

import (
	"encoding/json"

	"github.com/OnslaughtSnail/caelis/tool"
)

// planTool implements the PLAN tool for structured plans.
type planTool struct{}

func (*planTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        "PLAN",
		Description: "Replace the visible execution plan with a structured plan.",
		Schema: tool.Schema{
			Type: "object",
			Properties: map[string]tool.Schema{
				"entries": {
					Type: "array",
					Items: &tool.Schema{
						Type: "object",
						Properties: map[string]tool.Schema{
							"content": {Type: "string"},
							"status":  {Type: "string", Enum: []any{"pending", "in_progress", "completed"}},
						},
						Required: []string{"content", "status"},
					},
				},
				"explanation": {Type: "string"},
			},
			Required: []string{"entries"},
		},
	}
}

func (*planTool) Run(_ tool.Context, call tool.Call) (tool.Result, error) {
	entries, _ := call.Args["entries"].([]any)
	explanation, _ := call.Args["explanation"].(string)

	if len(entries) == 0 {
		return tool.Result{Output: "entries is required", IsError: true}, nil
	}

	// Return structured result that runner can persist as a plan event.
	payload := map[string]any{
		"entries":     entries,
		"explanation": explanation,
	}
	data, _ := json.Marshal(payload)
	return tool.Result{Output: string(data)}, nil
}

// All returns all plan built-in tools.
func All() []tool.Tool {
	return []tool.Tool{&planTool{}}
}
