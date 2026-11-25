package plan

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestPlanToolReturnsNormalizedEntries(t *testing.T) {
	t.Parallel()

	raw, _ := json.Marshal(map[string]any{
		"explanation": "keep focus",
		"entries": []map[string]any{
			{"content": "Read code", "status": "completed"},
			{"content": "Implement fix", "status": "in_progress"},
		},
	})
	result, update, err := (Tool{}).CallWithUpdate(tool.Call{
		ID:    "call-1",
		Name:  ToolName,
		Input: raw,
	})
	if err != nil {
		t.Fatalf("Call() error = %v", err)
	}
	structured, ok := result.Structured()
	if len(result.Content) != 1 || !ok {
		t.Fatalf("result.Content = %+v, want single json part", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal(structured, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := payload["updated"]; got != true {
		t.Fatalf("updated = %#v, want true", got)
	}
	if len(update.Entries) != 2 || update.Entries[1] != (Entry{Content: "Implement fix", Status: StatusInProgress}) || update.Explanation != "keep focus" {
		t.Fatalf("typed update = %+v", update)
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

func TestPlanDefinitionGuidesStableUpdates(t *testing.T) {
	t.Parallel()

	def := New().Definition()
	for _, want := range []string{
		"multi-step execution checklist",
		"insert or append",
		"do not overwrite or delete history",
		"At most one in_progress",
	} {
		if !strings.Contains(def.Description, want) {
			t.Fatalf("Description missing %q: %q", want, def.Description)
		}
	}
	properties, _ := def.InputSchema["properties"].(map[string]any)
	entries, _ := properties["entries"].(map[string]any)
	if got, _ := entries["description"].(string); !strings.Contains(got, "insert/append") {
		t.Fatalf("entries description = %q, want insert/append guidance", got)
	}
}
