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

func TestRunningHintTrackerKeepsCompactBoundaryAcrossToolStartAndCompletion(t *testing.T) {
	t.Parallel()

	tracker := newRunningHintTracker()
	tracker.beginTurn(time.Unix(100, 0))
	compactAt := time.Unix(101, 0)
	tracker.setCompact(compactAt)

	toolKey := "tool:turn-2:command-1"
	tracker.start(toolKey, runningPhaseToolWait, runningTargetShell, time.Unix(102, 0), "command-1")
	if got := tracker.visible(true); got.Phase != runningPhaseCompact || !got.StartedAt.Equal(compactAt) {
		t.Fatalf("activity after tool start = %#v, want stable compact boundary", got)
	}

	tracker.complete(toolKey, time.Unix(103, 0))
	if got := tracker.visible(true); got.Phase != runningPhaseCompact {
		t.Fatalf("activity after tool completion = %#v, want compact until typed completion", got)
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
