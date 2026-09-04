package runtime

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

// seedStreamFromResult is retained as a lifecycle helper while the old
// Runtime-owned stream cache is removed. It records only the canonical Final
// Result used by Task read/wait fallback; transient deltas are emitted through
// the producer-only output observer and retained by Control.
func (t *subagentTask) seedStreamFromResult(result delegation.Result) {
	if t == nil || result.State != delegation.StateCompleted {
		return
	}
	t.retainCompletedFinalLocked(result.Result)
}

// retainCompletedFinalLocked protects the latest completed child response in
// the canonical Task result fallback. Historical/replay output belongs to the
// Control spool or, if that trace is unavailable, to ACP session/load.
func (t *subagentTask) retainCompletedFinalLocked(text string) {
	if t == nil || !taskOutputHasNonBlankLine(text) {
		return
	}
	turnSeq := max(t.turnSeq, 1)
	if t.latestFinalTurnSeq == turnSeq && t.latestFinalText == text {
		return
	}
	t.latestFinalText = text
	t.latestFinalTurnSeq = turnSeq
	t.latestFinalAt = time.Now()
	t.latestFinalActivityID = strings.TrimSpace(t.activityID)
}
