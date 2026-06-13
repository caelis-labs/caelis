package task

import (
	"strings"
	"testing"
)

func TestTaskDescriptionGuidesContinuingSubagentConversation(t *testing.T) {
	desc := New().Definition().Description
	for _, want := range []string{
		"continue a running/waiting SPAWN child-agent conversation",
		"Continue an existing child agent with TASK write instead of spawning a replacement",
	} {
		if !strings.Contains(desc, want) {
			t.Fatalf("TASK description missing %q:\n%s", want, desc)
		}
	}
}
