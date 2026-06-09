package tuiapp

import (
	"strings"
	"testing"
)

func TestSubagentFinalStreamBypassesSmoothing(t *testing.T) {
	m := newGatewayEventTestModel()

	updated, _ := m.Update(SubagentStartMsg{SpawnID: "spawn-1", Agent: "reviewer"})
	m = updated.(*Model)

	finalText := strings.Repeat("final child output ", 80)
	updated, _ = m.enqueueSubagentDelta("spawn-1", "assistant", finalText, true)
	m = updated.(*Model)

	if got := len(m.streamSmoothing); got != 0 {
		t.Fatalf("streamSmoothing entries after final subagent stream = %d, want 0", got)
	}
	state := m.subagentSessions["spawn-1"]
	if state == nil {
		t.Fatal("missing subagent session state")
	}
	if !strings.Contains(snapshotTranscriptModel(m), "final child output") {
		t.Fatalf("completed subagent output was not flushed into transcript:\n%s", snapshotTranscriptModel(m))
	}
}

func TestSubagentEmptyFinalStreamFlushesPendingSmoothing(t *testing.T) {
	m := newGatewayEventTestModel()

	updated, _ := m.Update(SubagentStartMsg{SpawnID: "spawn-1", Agent: "reviewer"})
	m = updated.(*Model)

	updated, _ = m.enqueueSubagentDelta("spawn-1", "assistant", "pending child output", false)
	m = updated.(*Model)
	if got := len(m.streamSmoothing); got != 1 {
		t.Fatalf("streamSmoothing entries after non-final subagent stream = %d, want 1", got)
	}

	updated, _ = m.enqueueSubagentDelta("spawn-1", "assistant", "", true)
	m = updated.(*Model)

	if got := len(m.streamSmoothing); got != 0 {
		t.Fatalf("streamSmoothing entries after empty final subagent stream = %d, want 0", got)
	}
	if !strings.Contains(snapshotTranscriptModel(m), "pending child output") {
		t.Fatalf("pending subagent output was not flushed into transcript:\n%s", snapshotTranscriptModel(m))
	}
}
