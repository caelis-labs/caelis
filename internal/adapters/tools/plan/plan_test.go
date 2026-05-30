package plan

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

func TestPlanToolReturnsCanonicalPlanMetadata(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"explanation": "keep focus",
		"entries": []map[string]any{
			{"content": "Read code", "status": "completed"},
			{"content": "Implement fix", "status": "in_progress"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := New().Call(context.Background(), tool.Call{ID: "call-1", Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != ToolName || len(result.Content) != 1 || result.Content[0].Kind != model.PartJSON {
		t.Fatalf("result = %#v, want json update result", result)
	}
	entries, ok := result.Meta["plan_entries"].([]session.PlanEntry)
	if !ok || len(entries) != 2 {
		t.Fatalf("plan_entries = %#v, want typed entries", result.Meta["plan_entries"])
	}
	if entries[1].Content != "Implement fix" || entries[1].Status != "in_progress" {
		t.Fatalf("entries = %#v, want normalized plan", entries)
	}
	if result.Meta["explanation"] != "keep focus" {
		t.Fatalf("explanation = %#v, want keep focus", result.Meta["explanation"])
	}
}

func TestPlanToolRejectsMultipleInProgressEntries(t *testing.T) {
	raw, err := json.Marshal(map[string]any{
		"entries": []map[string]any{
			{"content": "one", "status": "in_progress"},
			{"content": "two", "status": "in_progress"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New().Call(context.Background(), tool.Call{Input: raw}); err == nil {
		t.Fatal("Call error = nil, want validation error")
	}
}
