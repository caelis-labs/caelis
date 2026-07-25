package tuiapp

import acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"

// applyObservedCommandResults is activity-only repair: terminal transcript
// content remains owned by the RunCommand/Task stream projection. It closes
// only the exact presentation owner named by a terminal Task read/wait
// observation; the Task control invocation completing is not target terminal
// evidence.
func (m *Model) applyObservedCommandResults(results []acpprojector.CommandTaskResult) {
	if m == nil || len(results) == 0 {
		return
	}
	changed := false
	for _, result := range results {
		owner, ok := m.runningActivityTracker.presentationOwner(
			result.Handle,
			result.ParentCallID,
			runningTargetShell,
		)
		if !ok {
			continue
		}
		m.runningActivityTracker.complete(owner.Key)
		changed = true
	}
	if changed {
		m.refreshRunningActivity()
	}
}
