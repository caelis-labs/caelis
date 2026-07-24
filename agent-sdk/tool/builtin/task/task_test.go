package task

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestTaskDescriptionGuidesInteractiveCommandsAndSubagentContinuation(t *testing.T) {
	desc := New().Definition().Description
	for _, want := range []string{
		"read observes new output without waiting for exit",
		"accepts exactly one RunCommand handle",
		"read does not support Spawn",
		"RunCommand write sends terminal stdin then briefly awaits its response",
		"Completed Spawn write sends a follow-up prompt",
		"Wait observes either target for at most one minute",
		"may return state=running",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("TASK description missing %q:\n%s", want, desc)
		}
	}
	for _, forbidden := range []string{"tool-call ID", "harness task", "running/waiting SPAWN", "task_id", "action=wait"} {
		if strings.Contains(desc, forbidden) {
			t.Fatalf("TASK description contains irrelevant identifier guidance %q:\n%s", forbidden, desc)
		}
	}
}

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
	actionDesc, _ := action["description"].(string)
	for _, want := range []string{
		"wait: one or more RunCommand or Spawn handles",
		"read: exactly one RunCommand handle only, never Spawn",
		"write: exactly one handle",
		"cancel: one or more handles",
	} {
		if !strings.Contains(actionDesc, want) {
			t.Fatalf("action description = %q, want %q", actionDesc, want)
		}
	}
	handle, _ := props["handle"].(map[string]any)
	handleDesc, _ := handle["description"].(string)
	if !strings.Contains(handleDesc, "Only wait and cancel accept comma-separated handles") {
		t.Fatalf("handle description = %q, want batch action restriction", handleDesc)
	}
	input, _ := props["input"].(map[string]any)
	inputDesc, _ := input["description"].(string)
	for _, want := range []string{"terminal stdin", "completed Spawn", "follow-up prompt"} {
		if !strings.Contains(inputDesc, want) {
			t.Fatalf("input description = %q, want %q", inputDesc, want)
		}
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
