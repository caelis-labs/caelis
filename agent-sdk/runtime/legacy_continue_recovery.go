package runtime

import (
	"context"
	"strings"

	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

// These values were persisted by the retired Task-write continuation saga.
// Keep this compatibility reader until every supported upgrade source predates
// no Task store that may contain continue_phase. It must never issue a remote
// effect; new Agent communication is owned exclusively by SendMessage.
const (
	legacyContinuePrepared       = "continue_prepared"
	legacyContinuePending        = "continue_pending"
	legacyContinuePostEffect     = "continue_post_effect"
	legacyContinueUnknownOutcome = "continue_unknown_outcome"

	legacyContinueRecoveryMeta = "legacy_continue_recovery"
)

const (
	legacyContinueInterruptedDiagnostic = "legacy subagent continuation was not delivered before upgrade"
	legacyContinueUnknownDiagnostic     = "legacy subagent continuation outcome could not be confirmed"
)

// recoverLegacySubagentContinue converges one retired continuation record
// without reviving Task input or repeating the remote effect. handled remains
// true even when no write is required so generic orphan recovery cannot
// overwrite an explicit legacy unknown outcome.
func (r *Runtime) recoverLegacySubagentContinue(ctx context.Context, entry *taskapi.Entry) (handled bool, err error) {
	if r == nil || r.tasks == nil || entry == nil || entry.Kind != taskapi.KindSubagent {
		return false, nil
	}
	phase := legacyContinuePhaseOfEntry(entry)
	if phase == "" {
		return false, nil
	}

	next := taskapi.CloneEntry(entry)
	next.SupportsInput = false
	switch phase {
	case legacyContinuePrepared:
		next = interruptedSubagentEntry(next, legacyContinueInterruptedDiagnostic)
	case legacyContinuePending, legacyContinueUnknownOutcome:
		markLegacyContinueUnknown(next)
	case legacyContinuePostEffect:
		// A post-effect record already contains the remote result. Preserve a
		// proven terminal value; a still-running record lost its observer across
		// restart and therefore has an unknown outcome, not an interruption.
		if next.Running {
			markLegacyContinueUnknown(next)
		} else {
			normalizeSubagentEntryResult(next, next.FailureDiagnostic)
			if next.State == taskapi.StateCompleted {
				legacyTask := r.tasks.rehydrateSubagentTask(next)
				if err := r.tasks.appendSideSubagentFinalEvent(ctx, legacyTask); err != nil {
					return true, err
				}
				if r.tasks.store != nil {
					if persisted, loadErr := r.tasks.store.Get(context.WithoutCancel(ctx), next.TaskID); loadErr == nil && persisted != nil {
						next = persisted
					}
				}
			}
		}
	default:
		return false, nil
	}
	clearLegacyContinueState(next, phase)
	return true, r.tasks.persistTaskEntry(ctx, next)
}

func legacyContinuePhaseOfEntry(entry *taskapi.Entry) string {
	if entry == nil {
		return ""
	}
	phase := firstNonEmpty(
		taskStringValue(entry.Metadata["continue_phase"]),
		taskSpecString(entry.Spec, "continue_phase"),
	)
	switch strings.TrimSpace(phase) {
	case legacyContinuePrepared, legacyContinuePending, legacyContinuePostEffect, legacyContinueUnknownOutcome:
		return strings.TrimSpace(phase)
	default:
		return ""
	}
}

func markLegacyContinueUnknown(entry *taskapi.Entry) {
	if entry == nil {
		return
	}
	entry.Running = false
	entry.State = taskapi.StateUnknownOutcome
	entry.SupportsInput = false
	normalizeSubagentEntryResult(entry, legacyContinueUnknownDiagnostic)
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	entry.Metadata["state"] = string(taskapi.StateUnknownOutcome)
}

func clearLegacyContinueState(entry *taskapi.Entry, recoveredPhase string) {
	if entry == nil {
		return
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	entry.Metadata[legacyContinueRecoveryMeta] = strings.TrimSpace(recoveredPhase)
	for _, key := range []string{
		"continue_phase", "continue_prompt", "continue_context", "continue_digest", "continue_turn_seq", "continue_reason",
	} {
		delete(entry.Metadata, key)
	}
	for _, key := range []string{"continue_phase", "continue_digest", "continue_turn_seq"} {
		delete(entry.Spec, key)
	}
}
