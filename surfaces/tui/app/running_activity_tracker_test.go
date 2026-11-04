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

func TestRunningHintTrackerDoesNotRestoreToolsSupersededByModelProgress(t *testing.T) {
	tracker := newRunningHintTracker()
	tracker.beginTurn(time.Unix(100, 0))
	tracker.start("old", runningPhaseToolWait, runningTargetShell, time.Unix(101, 0), "old")
	// No terminal arrived for old. The foreground has since progressed through
	// another model step and a newer tool, whose completion cannot revive it.
	tracker.setFocus(runningPhaseThinking, "", "new-model-step", time.Unix(102, 0))
	tracker.start("new", runningPhaseToolWait, runningTargetShell, time.Unix(103, 0), "new")
	tracker.complete("new", time.Unix(104, 0))
	if got := tracker.visible(true); got.Phase != runningPhaseModelWait {
		t.Fatalf("superseded tool revived: %#v", got)
	}
	// A fresh progress update can still prove that an independent activity is
	// alive; this is presentation convergence, not fabricated tool completion.
	tracker.start("old", runningPhaseToolWait, runningTargetShell, time.Unix(105, 0), "old")
	tracker.start("other", runningPhaseToolWait, runningTargetShell, time.Unix(106, 0), "other")
	tracker.complete("other", time.Unix(107, 0))
	if got := tracker.visible(true); got.CallID != "old" {
		t.Fatalf("fresh concurrent progress was discarded: %#v", got)
	}
}

func TestRunningHintTrackerRetainsCurrentParallelTools(t *testing.T) {
	tracker := newRunningHintTracker()
	tracker.beginTurn(time.Unix(100, 0))
	tracker.start("a", runningPhaseToolWait, runningTargetShell, time.Unix(101, 0), "a")
	tracker.start("b", runningPhaseToolWait, runningTargetShell, time.Unix(102, 0), "b")
	tracker.complete("b", time.Unix(103, 0))
	if got := tracker.visible(true); got.CallID != "a" {
		t.Fatalf("current parallel activity was hidden: %#v", got)
	}
	tracker.start("approval", runningPhaseReview, "", time.Unix(104, 0), "review")
	tracker.setFocus(runningPhaseResponding, "", "response", time.Unix(105, 0))
	if got := tracker.visible(true); got.Phase != runningPhaseReview {
		t.Fatalf("model progress displaced a valid review: %#v", got)
	}
}

func TestRunningHintTrackerToolResultOnlySettlesMatchingReviewIdentity(t *testing.T) {
	tracker := newRunningHintTracker()
	tracker.beginTurn(time.Unix(100, 0))
	tracker.start("approval:older:shared", runningPhaseReview, "", time.Unix(101, 0), "shared")
	tracker.start("approval:current:shared", runningPhaseReview, "", time.Unix(102, 0), "shared")
	tracker.completeTool("tool:older:shared", "shared", time.Unix(103, 0))
	if got := tracker.visible(true); got.Key != "approval:current:shared" {
		t.Fatalf("other Turn's terminal cleared a valid review: %#v", got)
	}
}

func TestRunningHintTrackerReplacesStaleCompactWithForegroundTool(t *testing.T) {
	t.Parallel()

	tracker := newRunningHintTracker()
	tracker.beginTurn(time.Unix(100, 0))
	compactAt := time.Unix(101, 0)
	tracker.setCompact(compactAt)

	toolKey := "tool:turn-2:command-1"
	tracker.start(toolKey, runningPhaseToolWait, runningTargetShell, time.Unix(102, 0), "command-1")
	if got := tracker.visible(true); got.Phase != runningPhaseToolWait || got.CallID != "command-1" {
		t.Fatalf("activity after tool start = %#v, want latest foreground tool", got)
	}

	tracker.complete(toolKey, time.Unix(103, 0))
	if got := tracker.visible(true); got.Phase != runningPhaseModelWait {
		t.Fatalf("activity after tool completion = %#v, want waiting after tool completion", got)
	}

	tracker.completeCompact(time.Unix(104, 0))
	if got := tracker.visible(true); got.Phase != runningPhaseModelWait {
		t.Fatalf("activity after compact completion = %#v, want model wait", got)
	}
}

func TestRunningHintTrackerRestoresForegroundAdvancedDuringCompaction(t *testing.T) {
	t.Parallel()

	t.Run("tool", func(t *testing.T) {
		tracker := newRunningHintTracker()
		tracker.beginTurn(time.Unix(100, 0))
		tracker.setCompact(time.Unix(101, 0))
		tracker.start("tool:turn-1:wait-1", runningPhaseToolWait, runningTargetSubagent, time.Unix(102, 0), "wait-1")

		tracker.completeCompact(time.Unix(103, 0))
		if got := tracker.visible(true); got.Key != "tool:turn-1:wait-1" || got.Target != runningTargetSubagent {
			t.Fatalf("activity after compact completion = %#v, want current foreground tool", got)
		}
	})

	t.Run("narrative", func(t *testing.T) {
		tracker := newRunningHintTracker()
		tracker.beginTurn(time.Unix(100, 0))
		tracker.setFocus(runningPhaseThinking, "", "reasoning:before-compact", time.Unix(101, 0))
		tracker.setCompact(time.Unix(102, 0))
		tracker.setFocus(runningPhaseResponding, "", "response:after-compact", time.Unix(103, 0))

		tracker.completeCompact(time.Unix(104, 0))
		if got := tracker.visible(true); got.Key != "response:after-compact" || got.Phase != runningPhaseResponding {
			t.Fatalf("activity after compact completion = %#v, want current foreground narrative", got)
		}
	})
}
