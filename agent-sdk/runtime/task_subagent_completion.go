package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

const (
	subagentCompletionPhasePending = "terminal_pending"
	subagentCompletionPhaseKey     = "completion_phase"
	subagentCompletionResultKey    = "completion_result"
	subagentCompletionTurnSeqKey   = "completion_turn_seq"
)

var errSubagentCompletionIdentityChanged = errors.New("agent-sdk/runtime: subagent completion identity changed")

type pendingSubagentCompletion struct {
	ctx     context.Context
	result  delegation.Result
	turnSeq int64
}

type subagentCompletionDrainOutcome uint8

const (
	subagentCompletionDrainNone subagentCompletionDrainOutcome = iota
	subagentCompletionDrainApplied
	subagentCompletionDrainDeferred
)

// subagentCompletionSink binds the producer callback to one durable Task and
// one Runtime mutation fence. Publishing is synchronous once the Task is ready:
// completion is durable before the producer callback returns. The only
// deferred cases are the bounded spawn-install and Task-operation handoffs,
// which remain owned by taskRuntime and are kicked by those lifecycle edges.
type subagentCompletionSink struct {
	runtime *taskRuntime
	ctx     context.Context
	taskID  string
	turnSeq int64
}

func (s subagentCompletionSink) PublishSubagentCompletion(result delegation.Result) {
	if s.runtime == nil {
		return
	}
	result = delegation.CloneResult(result)
	// The sink is capability-bound to one durable Task. Producer payload is
	// observation data and cannot redirect lifecycle authority.
	result.TaskID = s.taskID
	s.runtime.publishSubagentCompletion(s.ctx, s.turnSeq, result)
}

func newSubagentCompletionSink(ctx context.Context, tm *taskRuntime, taskID string, turnSeq int64) subagentCompletionSink {
	if ctx == nil {
		ctx = context.Background()
	}
	completionCtx := session.ContextWithControlMutation(
		context.WithoutCancel(ctx),
		session.ControlMutationPurposeSubagentCompletion,
	)
	return subagentCompletionSink{
		runtime: tm,
		ctx:     completionCtx,
		taskID:  strings.TrimSpace(taskID),
		turnSeq: max(turnSeq, 1),
	}
}

func (tm *taskRuntime) publishSubagentCompletion(ctx context.Context, turnSeq int64, result delegation.Result) {
	if tm == nil {
		return
	}
	result = delegation.CloneResult(result)
	taskID := strings.TrimSpace(result.TaskID)
	if taskID == "" || result.State == delegation.StateRunning || result.Running {
		return
	}
	switch result.State {
	case delegation.StateCompleted, delegation.StateFailed, delegation.StateCancelled, delegation.StateInterrupted:
	default:
		return
	}
	result = sanitizeSubagentCompletionResult(result)
	if ctx == nil {
		ctx = context.Background()
	}
	completion := pendingSubagentCompletion{ctx: context.WithoutCancel(ctx), result: result, turnSeq: max(turnSeq, 1)}

	tm.mu.Lock()
	task := tm.subagents[taskID]
	if task == nil {
		tm.enqueueSubagentCompletionLocked(taskID, completion)
		tm.mu.Unlock()
		return
	}
	task.mu.Lock()
	ready := task.lifecycleReady
	sessionRef := task.sessionRef
	currentTurnSeq := task.turnSeq
	task.mu.Unlock()
	if currentTurnSeq > completion.turnSeq {
		tm.mu.Unlock()
		return
	}
	key := taskOperationKey(sessionRef, taskID)
	if !ready || currentTurnSeq < completion.turnSeq {
		tm.enqueueSubagentCompletionLocked(taskID, completion)
		tm.mu.Unlock()
		return
	}
	if _, active := tm.operations[key]; active {
		tm.enqueueSubagentCompletionLocked(taskID, completion)
		tm.mu.Unlock()
		return
	}
	tm.operations[key] = struct{}{}
	tm.mu.Unlock()

	err := tm.applySubagentCompletionClaimed(task, completion)

	tm.mu.Lock()
	delete(tm.operations, key)
	permanent := isPermanentSubagentCompletionError(err)
	if err != nil && !permanent {
		// Retain the exact typed terminal result in the Runtime lifecycle queue.
		// A later lifecycle edge retries it; Task read/wait never becomes the
		// mutation trigger.
		tm.enqueueSubagentCompletionLocked(taskID, completion)
	}
	tm.mu.Unlock()
	if err != nil && !permanent {
		tm.startSubagentCompletionRetry()
	} else if err == nil {
		tm.kickPendingSubagentCompletion(taskID)
	}
}

// applySubagentCompletionClaimed retries one completion against refreshed
// durable state while the caller owns the Task operation claim.
func (tm *taskRuntime) applySubagentCompletionClaimed(task *subagentTask, completion pendingSubagentCompletion) error {
	var err error
	for range 3 {
		err = tm.completeSubagentTask(completion.ctx, task, completion.turnSeq, completion.result)
		if err == nil {
			return nil
		}
		terminal, reloadErr := tm.reloadSubagentCompletionTask(completion.ctx, task)
		if reloadErr != nil {
			return errors.Join(err, reloadErr)
		}
		if terminal {
			return nil
		}
	}
	return err
}

// drainPendingSubagentCompletionClaimed applies a producer completion queued
// behind the caller's existing Task operation claim. The first completion for
// one turn remains authoritative; transient persistence failures are requeued
// for the claim-release edge and the Runtime retry coordinator.
func (tm *taskRuntime) drainPendingSubagentCompletionClaimed(task *subagentTask) (subagentCompletionDrainOutcome, error) {
	if tm == nil || task == nil {
		return subagentCompletionDrainNone, nil
	}
	taskID := strings.TrimSpace(task.ref.TaskID)
	drained := false
	for {
		tm.mu.Lock()
		completion, ok := tm.completions[taskID]
		if ok {
			delete(tm.completions, taskID)
		}
		tm.mu.Unlock()
		if !ok {
			if drained {
				return subagentCompletionDrainApplied, nil
			}
			return subagentCompletionDrainNone, nil
		}
		drained = true
		err := tm.applySubagentCompletionClaimed(task, completion)
		if err == nil {
			continue
		}
		if isPermanentSubagentCompletionError(err) {
			return subagentCompletionDrainApplied, err
		}
		tm.mu.Lock()
		tm.enqueueSubagentCompletionLocked(taskID, completion)
		tm.mu.Unlock()
		return subagentCompletionDrainDeferred, nil
	}
}

// finishSubagentControlClaimed establishes one point-in-time mutating-control
// result while atomically releasing the Task operation claim. A completion
// that arrived before this boundary is either applied or durably deferred;
// producers arriving after it are ordinary concurrent state changes.
func (tm *taskRuntime) finishSubagentControlClaimed(task *subagentTask) (taskapi.Snapshot, error) {
	if tm == nil || task == nil {
		return taskapi.Snapshot{}, nil
	}
	taskID := strings.TrimSpace(task.ref.TaskID)
	key := taskOperationKey(task.sessionRef, taskID)
	for {
		outcome, err := tm.drainPendingSubagentCompletionClaimed(task)
		if err != nil {
			return task.snapshot(), err
		}
		tm.mu.Lock()
		_, pending := tm.completions[taskID]
		if pending && outcome != subagentCompletionDrainDeferred {
			tm.mu.Unlock()
			continue
		}
		task.mu.Lock()
		snapshot := task.snapshotLocked()
		task.mu.Unlock()
		delete(tm.operations, key)
		tm.mu.Unlock()
		if outcome == subagentCompletionDrainDeferred {
			tm.startSubagentCompletionRetry()
		}
		return snapshot, nil
	}
}

func isPermanentSubagentCompletionError(err error) bool {
	return err != nil &&
		(errors.Is(err, session.ErrSessionNotFound) ||
			errors.Is(err, errSubagentCompletionIdentityChanged))
}

// enqueueSubagentCompletionLocked keeps the first terminal for each turn while
// allowing a newer turn to replace an obsolete pending completion. A bound
// producer must publish exactly once; a conflicting duplicate for the same
// turn fails closed by leaving the first terminal authoritative.
func (tm *taskRuntime) enqueueSubagentCompletionLocked(taskID string, completion pendingSubagentCompletion) {
	if tm == nil {
		return
	}
	if tm.completions == nil {
		tm.completions = map[string]pendingSubagentCompletion{}
	}
	current, exists := tm.completions[taskID]
	if !exists || completion.turnSeq > current.turnSeq {
		tm.completions[taskID] = completion
	}
}

func (tm *taskRuntime) startSubagentCompletionRetry() {
	if tm == nil {
		return
	}
	tm.mu.Lock()
	if tm.completionWorker {
		tm.mu.Unlock()
		return
	}
	tm.completionWorker = true
	tm.mu.Unlock()

	go func() {
		delay := 25 * time.Millisecond
		for {
			timer := time.NewTimer(delay)
			<-timer.C
			tm.mu.Lock()
			taskIDs := tm.retryableSubagentCompletionTaskIDsLocked()
			if len(taskIDs) == 0 {
				// Check pending ownership and retire the coordinator atomically.
				// A publisher that enqueues retryable work after this unlock
				// observes no worker and starts the next owner itself. Early
				// spawn/operation handoffs remain queued for their lifecycle
				// edge and do not keep an idle retry goroutine alive.
				tm.completionWorker = false
				tm.mu.Unlock()
				return
			}
			tm.mu.Unlock()
			for _, taskID := range taskIDs {
				tm.kickPendingSubagentCompletion(taskID)
			}
			delay = min(delay*2, 800*time.Millisecond)
		}
	}()
}

func (tm *taskRuntime) retryableSubagentCompletionTaskIDsLocked() []string {
	if tm == nil {
		return nil
	}
	taskIDs := make([]string, 0, len(tm.completions))
	for taskID := range tm.completions {
		task := tm.subagents[taskID]
		if task == nil {
			continue
		}
		task.mu.Lock()
		ready := task.lifecycleReady
		ref := task.sessionRef
		task.mu.Unlock()
		if !ready {
			continue
		}
		if _, active := tm.operations[taskOperationKey(ref, taskID)]; active {
			continue
		}
		taskIDs = append(taskIDs, taskID)
	}
	return taskIDs
}

func (tm *taskRuntime) reloadSubagentCompletionTask(ctx context.Context, task *subagentTask) (bool, error) {
	if tm == nil || tm.store == nil || task == nil {
		return false, fmt.Errorf("agent-sdk/runtime: subagent completion store is unavailable")
	}
	current, err := tm.store.Get(ctx, task.ref.TaskID)
	if err != nil {
		return false, err
	}
	if current == nil || current.Kind != taskapi.KindSubagent ||
		strings.TrimSpace(current.Session.SessionID) != strings.TrimSpace(task.sessionRef.SessionID) {
		return false, fmt.Errorf("%w for task %q", errSubagentCompletionIdentityChanged, task.ref.TaskID)
	}
	durable := tm.rehydrateSubagentTask(current)
	task.mu.Lock()
	copySubagentCompletionObservationLocked(durable, task)
	task.mu.Unlock()
	durable.mu.Lock()
	durableRunning := durable.running
	resolvableUnknown := subagentCompletionCanResolveUnknownLocked(durable)
	durable.mu.Unlock()
	if !durableRunning && !resolvableUnknown {
		durable.ensureTerminalStreamFrameLocked()
		if err := tm.appendStagedSideSubagentFinalEvent(ctx, durable); err != nil {
			return true, err
		}
		snapshot := durable.snapshot()
		if shouldDropInactiveSubagentTask(snapshot) {
			if err := tm.updateSubagentParticipant(ctx, durable, "updated"); err != nil {
				return true, err
			}
		}
		tm.publishStagedSubagentCompletion(task, durable)
		if shouldDropInactiveSubagentTask(snapshot) {
			tm.mu.Lock()
			if tm.subagents[task.ref.TaskID] == task {
				delete(tm.subagents, task.ref.TaskID)
			}
			tm.mu.Unlock()
		}
		return true, nil
	}
	task.mu.Lock()
	task.revision = durable.revision
	task.lease = taskapi.CloneLease(durable.lease)
	task.state = durable.state
	task.running = durable.running
	task.result = durable.result
	task.metadata = durable.metadata
	task.turnSeq = durable.turnSeq
	task.lifecycleReady = true
	task.mu.Unlock()
	return false, nil
}

func (tm *taskRuntime) kickPendingSubagentCompletion(taskID string) {
	if tm == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	tm.mu.Lock()
	completion, ok := tm.completions[taskID]
	if ok {
		delete(tm.completions, taskID)
	}
	tm.mu.Unlock()
	if ok {
		tm.publishSubagentCompletion(completion.ctx, completion.turnSeq, completion.result)
	}
}

func (tm *taskRuntime) completeSubagentTask(ctx context.Context, task *subagentTask, turnSeq int64, result delegation.Result) error {
	if task == nil {
		return fmt.Errorf("agent-sdk/runtime: subagent completion requires a task")
	}
	result = sanitizeSubagentCompletionResult(result)
	if taskID := strings.TrimSpace(result.TaskID); taskID != "" && taskID != strings.TrimSpace(task.ref.TaskID) {
		return fmt.Errorf("agent-sdk/runtime: subagent completion task_id %q does not match %q", taskID, task.ref.TaskID)
	}
	switch result.State {
	case delegation.StateCompleted, delegation.StateFailed, delegation.StateCancelled, delegation.StateInterrupted:
	default:
		return fmt.Errorf("agent-sdk/runtime: subagent completion has invalid terminal state %q", result.State)
	}

	task.mu.Lock()
	if task.turnSeq != max(turnSeq, 1) {
		task.mu.Unlock()
		return nil
	}
	if !task.running && !subagentCompletionCanResolveUnknownLocked(task) {
		task.ensureTerminalStreamFrameLocked()
		task.mu.Unlock()
		return tm.appendSideSubagentFinalEvent(ctx, task)
	}
	current := task.entrySnapshot(tm.runtime.now())
	desired := tm.rehydrateSubagentTask(current)
	copySubagentCompletionObservationLocked(desired, task)
	task.mu.Unlock()

	desired.lifecycleReady = true
	desired.applyResultLocked(result, false)
	desired.seedStreamFromResult(result)
	desired.ensureTerminalStreamFrameLocked()
	intent := taskapi.CloneEntry(current)
	setSubagentCompletionIntent(intent, max(turnSeq, 1), result)
	if err := tm.persistStagedTaskEntry(ctx, intent); err != nil {
		return err
	}
	desired.revision = intent.Revision
	desired.lease = taskapi.CloneLease(intent.Lease)
	if err := tm.appendCompletionSideSubagentFinalEvent(ctx, desired); err != nil {
		return err
	}
	snapshot := desired.snapshot()
	if shouldDropInactiveSubagentTask(snapshot) {
		if err := tm.updateSubagentParticipant(ctx, desired, "updated"); err != nil {
			return err
		}
	}
	applyContinuePhaseToMetadata(&desired.metadata, continuePhaseNone, "", agent.ContextTransfer{}, "", 0, "")
	clearSubagentCompletionIntentTask(desired)
	entry := desired.entrySnapshot(tm.runtime.now())
	applyContinuePhaseToEntry(entry, continuePhaseNone, "", agent.ContextTransfer{}, "", 0, "")
	clearSubagentCompletionIntentEntry(entry)
	if err := tm.persistStagedTaskEntry(ctx, entry); err != nil {
		return err
	}
	desired.revision = entry.Revision
	desired.lease = taskapi.CloneLease(entry.Lease)
	tm.publishStagedSubagentCompletion(task, desired)
	if shouldDropInactiveSubagentTask(snapshot) {
		tm.mu.Lock()
		if tm.subagents[task.ref.TaskID] == task {
			delete(tm.subagents, task.ref.TaskID)
		}
		tm.mu.Unlock()
	}
	return nil
}

// subagentCompletionCanResolveUnknownLocked distinguishes a provisional
// recovery fallback from an authoritative producer terminal. Continue/Cancel
// recovery records unknown outcome because ownership was lost, not because a
// child terminal was observed; a capability-bound completion for the same turn
// is stronger evidence and may still converge that Task to its real terminal.
// The caller holds task.mu and has already matched turn_seq.
func subagentCompletionCanResolveUnknownLocked(task *subagentTask) bool {
	if task == nil || task.state != taskapi.StateUnknownOutcome {
		return false
	}
	if normalizeContinuePhase(taskStringValue(task.metadata["continue_phase"])) == continuePhaseUnknownOutcome {
		return true
	}
	if normalizeSubagentCancelPhase(taskStringValue(task.metadata["cancel_phase"])) != subagentCancelPhaseNone {
		return true
	}
	return taskStringValue(task.metadata[subagentCompletionPhaseKey]) == subagentCompletionPhasePending
}

func setSubagentCompletionIntent(entry *taskapi.Entry, turnSeq int64, result delegation.Result) {
	if entry == nil {
		return
	}
	if entry.Spec == nil {
		entry.Spec = map[string]any{}
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	result = delegation.CloneResult(result)
	result.TaskID = strings.TrimSpace(entry.TaskID)
	result.Running = false
	result.Yielded = false
	intent := map[string]any{
		"task_id": result.TaskID,
		"state":   string(result.State),
	}
	if value := strings.TrimSpace(result.OutputPreview); value != "" {
		intent["output_preview"] = value
	}
	if value := strings.TrimSpace(result.Error); value != "" {
		intent["error"] = value
	}
	if result.State == delegation.StateCompleted && taskOutputHasNonBlankLine(result.Result) {
		intent["result"] = result.Result
	}
	entry.Spec[subagentCompletionPhaseKey] = subagentCompletionPhasePending
	entry.Spec[subagentCompletionTurnSeqKey] = max(turnSeq, 1)
	entry.Spec[subagentCompletionResultKey] = intent
	entry.Metadata[subagentCompletionPhaseKey] = subagentCompletionPhasePending
	entry.Metadata[subagentCompletionTurnSeqKey] = max(turnSeq, 1)
}

func clearSubagentCompletionIntentTask(task *subagentTask) {
	if task == nil || task.metadata == nil {
		return
	}
	delete(task.metadata, subagentCompletionPhaseKey)
	delete(task.metadata, subagentCompletionTurnSeqKey)
}

func clearSubagentCompletionIntentEntry(entry *taskapi.Entry) {
	if entry == nil {
		return
	}
	for _, values := range []map[string]any{entry.Spec, entry.Metadata} {
		delete(values, subagentCompletionPhaseKey)
		delete(values, subagentCompletionTurnSeqKey)
		delete(values, subagentCompletionResultKey)
	}
}

func subagentCompletionIntent(entry *taskapi.Entry) (delegation.Result, int64, bool) {
	if entry == nil ||
		firstNonEmpty(
			taskSpecString(entry.Spec, subagentCompletionPhaseKey),
			taskStringValue(entry.Metadata[subagentCompletionPhaseKey]),
		) != subagentCompletionPhasePending {
		return delegation.Result{}, 0, false
	}
	raw, ok := entry.Spec[subagentCompletionResultKey].(map[string]any)
	if !ok {
		return delegation.Result{}, 0, false
	}
	state := delegation.State(taskStringValue(raw["state"]))
	switch state {
	case delegation.StateCompleted,
		delegation.StateFailed,
		delegation.StateCancelled,
		delegation.StateInterrupted:
	default:
		return delegation.Result{}, 0, false
	}
	turnSeq, ok := taskInt64Value(firstNonNil(
		entry.Spec[subagentCompletionTurnSeqKey],
		entry.Metadata[subagentCompletionTurnSeqKey],
	))
	if !ok || turnSeq <= 0 {
		return delegation.Result{}, 0, false
	}
	result := delegation.Result{
		TaskID:        strings.TrimSpace(entry.TaskID),
		State:         state,
		OutputPreview: taskStringValue(raw["output_preview"]),
		Error:         taskStringValue(raw["error"]),
		Result:        taskRawStringValue(raw["result"]),
	}
	return sanitizeSubagentCompletionResult(result), turnSeq, true
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// sanitizeSubagentCompletionResult is the durable trust boundary for public
// Runner implementations. Only the bounded diagnostics produced by maintained
// Runtime adapters may cross into Task/Session persistence; arbitrary transport
// errors, paths, headers, and credentials collapse to a fixed state fallback.
func sanitizeSubagentCompletionResult(result delegation.Result) delegation.Result {
	result = delegation.CloneResult(result)
	result.TaskID = strings.TrimSpace(result.TaskID)
	result.Running = false
	result.Yielded = false
	switch result.State {
	case delegation.StateCompleted:
		result.Error = ""
	case delegation.StateFailed:
		switch diagnostic := strings.TrimSpace(result.Error); diagnostic {
		case "subagent prompt timed out",
			"subagent authentication required",
			"subagent connection closed",
			"subagent prompt is unsupported",
			"subagent prompt was invalid",
			"subagent prompt was rejected",
			"subagent prompt failed":
			result.Error = diagnostic
		default:
			result.Error = "subagent failed"
		}
		result.OutputPreview = result.Error
		result.Result = ""
	case delegation.StateInterrupted:
		switch diagnostic := strings.TrimSpace(result.Error); diagnostic {
		case "interrupted", "subagent interrupted", "subagent cancellation failed":
			result.Error = diagnostic
		default:
			result.Error = "subagent interrupted"
		}
		result.OutputPreview = result.Error
		result.Result = ""
	case delegation.StateCancelled:
		result.Error = ""
		result.OutputPreview = ""
		result.Result = ""
	}
	return result
}

// copySubagentCompletionObservationLocked transfers the transient stream view
// into a detached completion staging task. The caller holds source.mu; staged
// notifications are intentionally disconnected from live Wait/Read observers.
func copySubagentCompletionObservationLocked(staged *subagentTask, source *subagentTask) {
	if staged == nil || source == nil {
		return
	}
	staged.stdout = source.stdout
	staged.stderr = source.stderr
	staged.stdoutCursor = source.stdoutCursor
	staged.stderrCursor = source.stderrCursor
	staged.streamFrames = make([]stream.Frame, len(source.streamFrames))
	for index, frame := range source.streamFrames {
		staged.streamFrames[index] = stream.CloneFrame(frame)
	}
	staged.streamFrameSizes = append([]int(nil), source.streamFrameSizes...)
	staged.streamEventBase = source.streamEventBase
	staged.streamOutputCursor = source.streamOutputCursor
	staged.streamBytes = source.streamBytes
	staged.streamTerminalFramed = source.streamTerminalFramed
	staged.streamChanged = nil
}

// publishStagedSubagentCompletion makes a fully committed terminal lifecycle
// visible once. Task CAS, Side final dialogue, participant checkpoint, and any
// participant status update have already completed on staged.
func (tm *taskRuntime) publishStagedSubagentCompletion(task *subagentTask, staged *subagentTask) {
	if tm == nil || task == nil || staged == nil {
		return
	}
	task.mu.Lock()
	if task.turnSeq != staged.turnSeq ||
		(!task.running && !subagentCompletionCanResolveUnknownLocked(task)) {
		task.mu.Unlock()
		return
	}
	task.state = staged.state
	task.running = staged.running
	task.revision = staged.revision
	task.lease = taskapi.CloneLease(staged.lease)
	task.result = session.CloneState(staged.result)
	task.metadata = session.CloneState(staged.metadata)
	task.stdout = staged.stdout
	task.stderr = staged.stderr
	task.stdoutCursor = staged.stdoutCursor
	task.stderrCursor = staged.stderrCursor
	task.streamFrames = make([]stream.Frame, len(staged.streamFrames))
	for index, frame := range staged.streamFrames {
		task.streamFrames[index] = stream.CloneFrame(frame)
	}
	task.streamFrameSizes = append([]int(nil), staged.streamFrameSizes...)
	task.streamEventBase = staged.streamEventBase
	task.streamOutputCursor = staged.streamOutputCursor
	task.streamBytes = staged.streamBytes
	task.streamTerminalFramed = staged.streamTerminalFramed
	task.lifecycleReady = true
	task.notifyStreamChangeLocked()
	task.mu.Unlock()
}

func (tm *taskRuntime) markSubagentLifecycleReady(task *subagentTask) {
	if tm == nil || task == nil {
		return
	}
	task.mu.Lock()
	task.lifecycleReady = true
	if !task.running {
		task.ensureTerminalStreamFrameLocked()
	}
	taskID := task.ref.TaskID
	task.mu.Unlock()
	tm.kickPendingSubagentCompletion(taskID)
}
