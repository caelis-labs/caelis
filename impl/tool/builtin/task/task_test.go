package task

import (
	"strings"
	"testing"
)

func TestTaskDescriptionGuidesContinuingSubagentConversation(t *testing.T) {
	desc := New().Definition().Description
	for _, want := range []string{
		"continue a running/waiting SPAWN child-agent conversation",
		"wait_until_done=true",
		"instead of repeated short waits",
		"Continue an existing child agent with TASK write instead of spawning a replacement",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("TASK description missing %q:\n%s", want, desc)
		}
	}
}

func TestTaskSchemaExposesWaitUntilDoneParameter(t *testing.T) {
	def := New().Definition()
	props, _ := def.InputSchema["properties"].(map[string]any)
	waitUntilDone, _ := props["wait_until_done"].(map[string]any)
	if got := waitUntilDone["type"]; got != "boolean" {
		t.Fatalf("wait_until_done type = %#v, want boolean", got)
	}
	yield, _ := props["yield_time_ms"].(map[string]any)
	desc, _ := yield["description"].(string)
	if !strings.Contains(desc, "300000") {
		t.Fatalf("yield_time_ms description = %q, want bounded default", desc)
	}
}
