package tuiapp

import (
	"testing"
	"time"
)

func TestRunningHintTrackerPreservesPhaseStartAcrossMatchingUpdates(t *testing.T) {
	t.Parallel()

	tracker := newRunningHintTracker()
	turnStartedAt := time.Unix(100, 0)
	tracker.beginTurn(turnStartedAt)
	if got := tracker.visible(true); got.Phase != runningPhaseModelWait || !got.StartedAt.Equal(turnStartedAt) {
		t.Fatalf("initial activity = %#v, want model waiting from Turn start", got)
	}

	thinkingAt := time.Unix(101, 0)
	tracker.setFocus(runningPhaseThinking, "", "reasoning:message-1", thinkingAt)
	tracker.setFocus(runningPhaseThinking, "", "reasoning:message-1", time.Unix(102, 0))
	if got := tracker.visible(true); !got.StartedAt.Equal(thinkingAt) {
		t.Fatalf("continued thinking = %#v, want original phase clock", got)
	}

	respondingAt := time.Unix(103, 0)
	tracker.setFocus(runningPhaseResponding, "", "response:message-1", respondingAt)
	if got := tracker.visible(true); got.Phase != runningPhaseResponding || !got.StartedAt.Equal(respondingAt) {
		t.Fatalf("response activity = %#v, want a new response clock", got)
	}
}

func TestRunningHintTrackerObservedOwnerCandidatesFailClosedOnConflictingCorrelations(t *testing.T) {
	t.Parallel()

	tracker := newRunningHintTracker()
	tracker.start(
		"tool:turn-1:spawn-1",
		runningPhaseToolWait,
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
		runningPhaseToolWait,
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

func TestRunningHintTrackerPresentationOwnerNormalizesHandle(t *testing.T) {
	t.Parallel()

	tracker := newRunningHintTracker()
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

func TestRunningHintTrackerPresentationOwnerFailsClosedOnIdentityMismatch(t *testing.T) {
	t.Parallel()

	tracker := newRunningHintTracker()
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
