package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestPlanToolReturnsNormalizedEntries(t *testing.T) {
	t.Parallel()

	planTool := New()
	raw, _ := json.Marshal(map[string]any{
		"explanation": "keep focus",
		"entries": []map[string]any{
			{"content": "Read code", "status": "completed"},
			{"content": "Implement fix", "status": "in_progress"},
		},
	})
	result, err := planTool.Call(context.Background(), tool.Call{
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
	if len(result.Content) != 1 || result.Content[0].Kind != model.PartKindJSON {
		t.Fatalf("result.Content = %+v, want single json part", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(result.Content[0].JSONValue(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := payload["updated"]; got != true {
		t.Fatalf("updated = %#v, want true", got)
	}
	caelis, _ := result.Metadata["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	entries, _ := toolMeta["entries"].([]map[string]any)
	if got, want := len(entries), 2; got != want {
		t.Fatalf("len(metadata entries) = %d, want %d", got, want)
	}
}

func TestPlanToolRejectsNonObjectEntries(t *testing.T) {
	t.Parallel()

	raw, _ := json.Marshal(map[string]any{
		"entries": []any{"not an entry"},
	})
	_, err := New().Call(context.Background(), tool.Call{
		Name:  ToolName,
		Input: raw,
	})
	if err == nil {
		t.Fatal("Call() error = nil, want non-object entry rejection")
	}
	if got, want := err.Error(), "entries[0]"; !strings.Contains(got, want) {
		t.Fatalf("Call() error = %v, want %q", err, want)
	}
}
