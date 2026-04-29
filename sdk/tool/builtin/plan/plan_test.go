package plan

import (
	"context"
	"encoding/json"
	"testing"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestPlanToolReturnsNormalizedEntries(t *testing.T) {
	t.Parallel()

	tool := New()
	raw, _ := json.Marshal(map[string]any{
		"explanation": "keep focus",
		"entries": []map[string]any{
			{"content": "Read code", "status": "completed"},
			{"content": "Implement fix", "status": "in_progress"},
		},
	})
	result, err := tool.Call(context.Background(), sdktool.Call{
		ID:    "call-1",
		Name:  ToolName,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	if got := result.Name; got != ToolName {
		t.Fatalf("result.Name = %q, want %q", got, ToolName)
	}
	if len(result.Content) != 1 || result.Content[0].Kind != sdkmodel.PartKindJSON {
		t.Fatalf("result.Content = %+v, want single json part", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	entries, _ := payload["entries"].([]any)
	if got, want := len(entries), 2; got != want {
		t.Fatalf("len(entries) = %d, want %d", got, want)
	}
}
