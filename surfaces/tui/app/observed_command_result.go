package tuiapp

import "time"

import "github.com/caelis-labs/caelis/control/appserver/taskstream"

// applyObservedCommandResults is activity-only repair: terminal transcript
// content remains owned by the RunCommand/Task stream projection. It closes
// only the exact presentation owner named by a terminal Task read/wait
// observation; the Task control invocation completing is not target terminal
// evidence.
func (m *Model) applyObservedCommandResults(results []taskstream.CommandTaskResult) {
	if m == nil || len(results) == 0 {
		return
	}
	changed := false
	for _, result := range results {
		owner, ok := m.runningHintTracker.presentationOwner(
			result.Handle,
			result.ParentCallID,
			runningTargetShell,
		)
		if !ok {
			continue
		}
		m.runningHintTracker.complete(owner.Key, time.Now())
		changed = true
	}
	if changed {
		m.refreshRunningActivity()
	}
}
