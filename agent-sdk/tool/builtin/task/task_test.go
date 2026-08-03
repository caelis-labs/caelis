package task

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestTaskDescriptionSummarizesPurposeAndWhenToUse(t *testing.T) {
	desc := New().Definition().Description
	for _, want := range []string{
		"Control asynchronous work",
		"RunCommand or Spawn",
		"after receiving a task handle",
		"inspect progress or results",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("TASK description missing %q:\n%s", want, desc)
		}
	}
	for _, forbidden := range []string{
		"tool-call ID",
		"harness task",
		"running/waiting SPAWN",
		"task_id",
		"output_preview",
		"final_message",
		"terminal stdin",
		"state=running",
	} {
		if strings.Contains(desc, forbidden) {
			t.Fatalf("TASK description contains parameter-level guidance %q:\n%s", forbidden, desc)
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
		"RunCommand briefly waits for output",
		"Spawn returns output_preview or exact final_message",
		"wait may stay running for up to one minute",
		"multiple handles return after any result",
		"write sends stdin only to RunCommand",
		"cancel stops work",
	} {
		if !strings.Contains(actionDesc, want) {
			t.Fatalf("action description = %q, want %q", actionDesc, want)
		}
	}
	handle, _ := props["handle"].(map[string]any)
	handleDesc, _ := handle["description"].(string)
	for _, want := range []string{
		"Pass one Session-scoped handle returned by RunCommand or Spawn",
		"Only wait and cancel accept multiple comma-separated handles",
	} {
		if !strings.Contains(handleDesc, want) {
			t.Fatalf("handle description = %q, want %q", handleDesc, want)
		}
	}
	input, _ := props["input"].(map[string]any)
	inputDesc, _ := input["description"].(string)
	for _, want := range []string{"Required only for write", "terminal stdin", "Spawn tasks reject write", "SendMessage"} {
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
