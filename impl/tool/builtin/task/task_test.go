package task

import (
	"strings"
	"testing"
)

func TestTaskDescriptionGuidesContinuingSubagentConversation(t *testing.T) {
	desc := New().Definition().Description
	for _, want := range []string{
		"For RUN_COMMAND, write sends terminal stdin.",
		"For SPAWN, write sends a follow-up prompt only after the child task has completed",
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
	for _, want := range []string{"terminal stdin", "completed SPAWN", "follow-up prompt"} {
		if !strings.Contains(inputDesc, want) {
			t.Fatalf("input description = %q, want %q", inputDesc, want)
		}
	}
}
