package tuiapp

import (
	"testing"
	"time"
)

func TestRunningActivityObservedOwnerCandidatesFailClosedOnConflictingCorrelations(t *testing.T) {
	t.Parallel()

	tracker := newRunningActivityTracker()
	tracker.start(
		"tool:turn-1:spawn-1",
		runningPhaseWait,
		runningTargetSubagent,
		time.Unix(1, 0),
		"spawn-1",
	)
	tracker.observeOwner("alpha", runningActivityOwner{
		Key:    "tool:turn-1:spawn-1",
		CallID: "spawn-1",
		Target: runningTargetSubagent,
	})
	tracker.start(
		"tool:turn-1:spawn-2",
		runningPhaseWait,
		runningTargetSubagent,
		time.Unix(2, 0),
		"spawn-2",
	)
	tracker.observeOwner("beta", runningActivityOwner{
		Key:    "tool:turn-1:spawn-2",
		CallID: "spawn-2",
		Target: runningTargetSubagent,
	})

	if candidates := tracker.observedOwnerCandidates("alpha", "spawn-2"); len(candidates) != 0 {
		t.Fatalf("candidates = %#v, want conflicting handle and parent call to resolve no owner", candidates)
	}
	if len(tracker.active) != 2 {
		t.Fatalf("active = %#v, want owner resolution to leave activities unchanged", tracker.active)
	}
}

func TestRunningActivityPresentationOwnerNormalizesHandle(t *testing.T) {
	t.Parallel()

	tracker := newRunningActivityTracker()
	tracker.observeOwner("@Command-3", runningActivityOwner{
		Key:     "tool:turn-1:command-1",
		CallID:  "command-1",
		BlockID: "block-1",
		Target:  runningTargetShell,
	})

	owner, ok := tracker.presentationOwner("command-3", "command-1", runningTargetShell)
	if !ok || owner.BlockID != "block-1" {
		t.Fatalf("presentationOwner() = %#v, %v; want normalized command owner", owner, ok)
	}
	if !sameTaskHandle("@COMMAND-3", "command-3") {
		t.Fatal("sameTaskHandle() did not normalize case and the display prefix")
	}
}

func TestRunningActivityPresentationOwnerFailsClosedOnIdentityMismatch(t *testing.T) {
	t.Parallel()

	tracker := newRunningActivityTracker()
	tracker.observeOwner("command-3", runningActivityOwner{
		Key:     "tool:turn-1:command-1",
		CallID:  "command-1",
		BlockID: "block-1",
		Target:  runningTargetShell,
	})
	tracker.observeOwner("command-4", runningActivityOwner{
		Key:     "tool:turn-1:command-2",
		CallID:  "command-2",
		BlockID: "block-2",
		Target:  runningTargetShell,
	})

	if owner, ok := tracker.presentationOwner("command-3", "command-2", runningTargetShell); ok {
		t.Fatalf("presentationOwner() = %#v, want conflicting handle and parent call to fail closed", owner)
	}
	if owner, ok := tracker.presentationOwner("", "command-2", runningTargetSubagent); ok {
		t.Fatalf("presentationOwner() = %#v, want target mismatch to fail closed", owner)
	}
}
