package task

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestTaskDescriptionGuidesContinuingSubagentConversation(t *testing.T) {
	desc := New().Definition().Description
	for _, want := range []string{
		"For RunCommand, write sends terminal stdin.",
		"For Spawn, write sends a follow-up prompt only after the child task has completed",
		"Always wait before relying on a task result.",
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

func TestTaskSchemaUsesYieldTimeForWaitBudget(t *testing.T) {
	def := New().Definition()
	props, _ := def.InputSchema["properties"].(map[string]any)
	if _, ok := props["wait_until_done"]; ok {
		t.Fatalf("wait_until_done property unexpectedly exposed: %#v", props["wait_until_done"])
	}
	yield, _ := props["yield_time_ms"].(map[string]any)
	if got := yield["minimum"]; got != -1 {
		t.Fatalf("yield_time_ms minimum = %#v, want -1", got)
	}
	desc, _ := yield["description"].(string)
	for _, want := range []string{"0 uses the default 7000 ms", "-1 waits as long as possible", "positive values use that exact budget"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("yield_time_ms description = %q, want %q", desc, want)
		}
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
		"task_id":    "task-1",
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
