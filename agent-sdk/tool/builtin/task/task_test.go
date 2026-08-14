package task

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestTaskSchemaUsesServiceOwnedObservationBudgets(t *testing.T) {
	def := New().Definition()
	props, _ := def.InputSchema["properties"].(map[string]any)
	if _, ok := props["handle"]; !ok {
		t.Fatalf("handle property missing: %#v", props)
	}
	if _, ok := props["task_id"]; ok {
		t.Fatalf("task_id property unexpectedly exposed: %#v", props)
	}
	if _, ok := props["wait_until_done"]; ok {
		t.Fatalf("wait_until_done property unexpectedly exposed: %#v", props["wait_until_done"])
	}
	if _, ok := props["yield_time_ms"]; ok {
		t.Fatalf("yield_time_ms property unexpectedly exposed: %#v", props["yield_time_ms"])
	}
	action, _ := props["action"].(map[string]any)
	enum, _ := action["enum"].([]string)
	if !slices.Contains(enum, "read") {
		t.Fatalf("action enum = %#v, want read", enum)
	}
}

func TestTaskCallRejectsInputOutsideWrite(t *testing.T) {
	t.Parallel()

	raw, _ := json.Marshal(map[string]any{
		"action": "wait",
		"handle": "zuri",
		"input":  "status",
	})
	_, err := New().Call(context.Background(), tool.Call{Name: ToolName, Input: raw})
	if err == nil || !strings.Contains(err.Error(), "valid only when action is write") {
		t.Fatalf("Task wait with input error = %v, want input/write validation", err)
	}
}

func TestTaskWriteSupportsExactTerminalInput(t *testing.T) {
	t.Parallel()

	if err := ValidateArgs(map[string]any{
		"action": "write", "handle": "zuri", "input": "\x1b[A", "append_newline": false,
	}); err != nil {
		t.Fatalf("ValidateArgs(exact terminal input) error = %v", err)
	}
	if err := ValidateArgs(map[string]any{
		"action": "write", "handle": "zuri", "input": "\n", "append_newline": false,
	}); err != nil {
		t.Fatalf("ValidateArgs(exact newline) error = %v", err)
	}
	if err := ValidateArgs(map[string]any{
		"action": "read", "handle": "zuri", "append_newline": false,
	}); err == nil || !strings.Contains(err.Error(), "valid only") {
		t.Fatalf("ValidateArgs(read append_newline) error = %v, want write-only rejection", err)
	}
}

func TestTaskCallRequiresRuntimeWrapper(t *testing.T) {
	t.Parallel()

	_, err := New().Call(context.Background(), tool.Call{Name: ToolName})
	if err == nil {
		t.Fatal("TASK Call() error = nil, want runtime wrapper error")
	}
	if !strings.Contains(err.Error(), "runtime wrapper") {
		t.Fatalf("TASK Call() error = %v, want runtime wrapper mention", err)
	}
}

func TestTaskCallRejectsUnknownArgsBeforeRuntimeWrapperError(t *testing.T) {
	t.Parallel()

	raw, _ := json.Marshal(map[string]any{
		"action":     "wait",
		"handle":     "zuri",
		"unexpected": true,
	})
	_, err := New().Call(context.Background(), tool.Call{Name: ToolName, Input: raw})
	if err == nil {
		t.Fatal("TASK Call() error = nil, want unknown arg rejection")
	}
	if strings.Contains(err.Error(), "runtime wrapper") || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("TASK Call() error = %v, want unknown arg rejection before runtime wrapper error", err)
	}
}

func TestTaskCallRejectsLegacyYieldTime(t *testing.T) {
	t.Parallel()

	raw, _ := json.Marshal(map[string]any{
		"action":        "wait",
		"handle":        "zuri",
		"yield_time_ms": -1,
	})
	_, err := New().Call(context.Background(), tool.Call{Name: ToolName, Input: raw})
	if err == nil {
		t.Fatal("TASK Call() error = nil, want legacy arg rejection")
	}
	if strings.Contains(err.Error(), "runtime wrapper") || !strings.Contains(err.Error(), "yield_time_ms") {
		t.Fatalf("TASK Call() error = %v, want legacy arg rejection before runtime wrapper error", err)
	}
}
