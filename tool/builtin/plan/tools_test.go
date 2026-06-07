package plan

import (
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/tool"
)

func TestPlanToolRequiresEntries(t *testing.T) {
	result, err := All()[0].Run(nil, tool.Call{Args: map[string]any{}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !result.IsError || result.Output != "entries is required" {
		t.Fatalf("result = %#v, want required entries error", result)
	}
}

func TestPlanToolReturnsStructuredPayload(t *testing.T) {
	entries := []any{
		map[string]any{"content": "write test", "status": "in_progress"},
		map[string]any{"content": "make pass", "status": "pending"},
	}
	result, err := All()[0].Run(nil, tool.Call{Args: map[string]any{
		"entries":     entries,
		"explanation": "tdd",
	}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.IsError {
		t.Fatalf("result is error: %#v", result)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Output), &payload); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if payload["explanation"] != "tdd" {
		t.Fatalf("explanation = %#v, want tdd", payload["explanation"])
	}
	if got, ok := payload["entries"].([]any); !ok || len(got) != 2 {
		t.Fatalf("entries = %#v, want two entries", payload["entries"])
	}
}

func TestPlanToolDefinition(t *testing.T) {
	def := All()[0].Definition()
	if def.Name != "PLAN" {
		t.Fatalf("name = %q, want PLAN", def.Name)
	}
	if len(def.Schema.Required) != 1 || def.Schema.Required[0] != "entries" {
		t.Fatalf("required = %#v, want entries", def.Schema.Required)
	}
}
