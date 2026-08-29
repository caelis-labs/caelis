package runtime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

type subagentActivityObserver struct {
	runtime *taskRuntime
	taskID  string
}

func newSubagentActivityObserver(runtime *taskRuntime, taskID string) agent.ChildActivityObserver {
	return subagentActivityObserver{runtime: runtime, taskID: strings.TrimSpace(taskID)}
}

// ObserveChildActivity makes Task a derived output observer. Input admission
// never reaches this method; the first actual frame (or terminal without a
// frame) opens a new observation generation only when no compatibility caller
// already opened one.
func (o subagentActivityObserver) ObserveChildActivity(ctx context.Context, raw agent.ChildActivityEvent) error {
	return o.ObserveChildActivityBatch(ctx, []agent.ChildActivityEvent{raw})
}

// ObserveChildActivityLive applies one already-journaled frame to the
// process-local Task stream without waiting for its Task-store write. The
// durable observer later receives the same cursor and persists the resulting
// current state without appending the frame twice.
func (o subagentActivityObserver) ObserveChildActivityLive(_ context.Context, raw agent.ChildActivityEvent) error {
	if o.runtime == nil || o.taskID == "" {
		return fmt.Errorf("child activity observer is unavailable")
	}
	event := agent.CloneChildActivityEvent(raw)
	if strings.TrimSpace(event.ActivityID) == "" || event.Cursor == 0 {
		return fmt.Errorf("child activity identity and cursor are required")
	}
	if strings.TrimSpace(event.Target.EndpointKey) != o.taskID {
		return fmt.Errorf("child activity endpoint changed")
	}
	if event.Frame == nil || event.Result != nil || event.Gap {
		return fmt.Errorf("live child activity observation requires one frame")
	}
	o.runtime.mu.RLock()
	task := o.runtime.subagents[o.taskID]
	o.runtime.mu.RUnlock()
	if task == nil {
		// Initial Spawn output can precede Task installation. The endpoint journal
		// keeps it for the durable observer; later live cursors remain fenced until
		// that replay catches up.
		return fmt.Errorf("child Task %q is not installed yet", o.taskID)
	}
	if err := validateTaskActivityTarget(task, event.Target); err != nil {
		return err
	}
	_, _, err := o.applyChildActivityEvent(task, event)
	return err
}

// ObserveChildActivityBatch applies one ordered endpoint-journal batch and
// advances its durable cursor with one Task write. The child execution result
// remains authoritative even when the batch begins with a recoverable activity
// observation gap.
func (o subagentActivityObserver) ObserveChildActivityBatch(ctx context.Context, raw []agent.ChildActivityEvent) error {
	if o.runtime == nil || o.taskID == "" {
		return fmt.Errorf("child activity observer is unavailable")
	}
	if len(raw) == 0 {
		return nil
	}
	events := make([]agent.ChildActivityEvent, 0, len(raw))
	var previousCursor uint64
	terminalSeen := false
	for _, item := range raw {
		event := agent.CloneChildActivityEvent(item)
		if strings.TrimSpace(event.ActivityID) == "" || event.Cursor == 0 {
			return fmt.Errorf("child activity identity and cursor are required")
		}
		if strings.TrimSpace(event.Target.EndpointKey) != o.taskID {
			return fmt.Errorf("child activity endpoint changed")
		}
		if previousCursor != 0 && event.Cursor <= previousCursor {
			return fmt.Errorf("child activity batch cursor is not increasing")
		}
		if terminalSeen {
			return fmt.Errorf("child activity batch continued after terminal result")
		}
		terminalSeen = event.Result != nil
		previousCursor = event.Cursor
		events = append(events, event)
	}
	o.runtime.mu.RLock()
	task := o.runtime.subagents[o.taskID]
	o.runtime.mu.RUnlock()
	if task == nil {
		// Spawn may emit before its post-spawn Task record becomes live. The
		// runner journal retains the event and retries after installation.
		return fmt.Errorf("child Task %q is not installed yet", o.taskID)
	}
	release, claimed := o.runtime.tryClaimSubagentOperation(task.sessionRef, o.taskID)
	if !claimed {
		return fmt.Errorf("child Task %q observation is busy", o.taskID)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	for _, event := range events {
		if err := validateTaskActivityTarget(task, event.Target); err != nil {
			return err
		}
	}
	ctx = context.WithoutCancel(ctx)

	var terminal *delegation.Result
	var terminalGeneration int64
	var observedCursor uint64
	for _, event := range events {
		generation, pending, err := o.applyChildActivityEvent(task, event)
		if err != nil {
			return err
		}
		if !pending {
			continue
		}
		observedCursor = max(observedCursor, event.Cursor)
		if event.Result != nil {
			result := delegation.CloneResult(*event.Result)
			terminal = &result
			terminalGeneration = generation
		}
	}

	if observedCursor == 0 {
		return nil
	}
	if terminal == nil {
		entry, persistedCursor := observedSubagentActivitySnapshot(task, o.runtime.runtime.now())
		if err := o.runtime.persistObservedSubagentTurn(ctx, task, entry); err != nil {
			return err
		}
		if cursor, ok := taskInt64Value(entry.Metadata[subagentActivityCursorMeta]); ok && cursor > 0 {
			persistedCursor = max(persistedCursor, uint64(cursor))
		}
		task.mu.Lock()
		task.activityDurableCursor = max(task.activityDurableCursor, persistedCursor)
		task.mu.Unlock()
		return nil
	}
	result := delegation.CloneResult(*terminal)
	result.TaskID = o.taskID
	switch result.State {
	case delegation.StateCompleted,
		delegation.StateFailed,
		delegation.StateCancelled,
		delegation.StateInterrupted,
		delegation.StateUnknownOutcome:
	default:
		result.State = delegation.StateUnknownOutcome
		result.Error = "child activity returned a non-terminal result"
	}
	result.Running = false
	result.Yielded = false
	// Completion persists lifecycle, result, output frames, and this terminal
	// activity cursor in one Task transaction. Persisting a running cursor first
	// would let restart skip the still-uncommitted terminal event.
	done := newObservedSubagentCompletionSink(ctx, o.runtime, o.taskID, terminalGeneration).enqueue(result)
	// Enqueue while the observation claim is still present. release atomically
	// transfers that claim to the queued completion before another Task control
	// operation can enter the child runner.
	release()
	released = true
	if done != nil {
		<-done
	}
	task.mu.Lock()
	task.activityDurableCursor = max(task.activityDurableCursor, observedCursor)
	task.mu.Unlock()
	return nil
}

// applyChildActivityEvent is the single process-local application boundary for
// live preview and durable journal replay. pending reports whether the cursor
// still needs a durable acknowledgement even when its frame was already
// applied by the live path.
func (o subagentActivityObserver) applyChildActivityEvent(
	task *subagentTask,
	event agent.ChildActivityEvent,
) (generation int64, pending bool, err error) {
	if task == nil {
		return 0, false, fmt.Errorf("child Task is unavailable")
	}
	task.activityApplyMu.Lock()
	defer task.activityApplyMu.Unlock()

	task.mu.Lock()
	if event.Cursor <= task.activityDurableCursor {
		generation = max(task.activityGeneration, 1)
		task.mu.Unlock()
		return generation, false, nil
	}
	activityOpened := false
	if task.activityID != strings.TrimSpace(event.ActivityID) {
		initialGeneration := event.Initial && task.turnSeq == 1 && task.activityID == ""
		if !initialGeneration && (!task.running || task.activityID != "") {
			beginObservedSubagentActivityLocked(task)
		}
		task.activityID = strings.TrimSpace(event.ActivityID)
		task.activityGeneration = max(task.turnSeq, 1)
		activityOpened = true
	}
	generation = max(task.activityGeneration, 1)
	alreadyApplied := event.Cursor <= task.activityCursor
	task.mu.Unlock()
	if activityOpened {
		// Follow observers park after a terminal Task snapshot. Wake them only
		// after a real frame (or a durable terminal-only event) proves that a new
		// child activity exists.
		o.runtime.notifyTaskStreamActivity(task.sessionRef.SessionID, task.ref.TaskID)
	}

	if !alreadyApplied {
		if event.Gap {
			task.markSubagentActivityObservationGap(event.Dropped)
		}
		if event.Frame != nil {
			frame := activityFrameForGeneration(*event.Frame, task, generation, event.ActivityID)
			task.applyStreamFrames([]stream.Frame{frame})
		}
	}

	task.mu.Lock()
	if event.Cursor > task.activityCursor {
		task.activityCursor = event.Cursor
	}
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata[subagentActivityIDMeta] = task.activityID
	task.metadata[subagentActivityGenerationMeta] = generation
	task.metadata[subagentActivityCursorMeta] = int64(task.activityCursor)
	task.mu.Unlock()
	return generation, true, nil
}

func observedSubagentActivitySnapshot(task *subagentTask, now time.Time) (*taskapi.Entry, uint64) {
	if task == nil {
		return nil, 0
	}
	task.activityApplyMu.Lock()
	defer task.activityApplyMu.Unlock()
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata[subagentActivityIDMeta] = task.activityID
	task.metadata[subagentActivityGenerationMeta] = max(task.activityGeneration, 1)
	task.metadata[subagentActivityCursorMeta] = int64(task.activityCursor)
	return task.entrySnapshot(now), task.activityCursor
}

const observedSubagentTurnPersistAttempts = 4

// persistObservedSubagentTurn converges a non-terminal Task observation. A
// CAS conflict may rebase this local view, but it never repeats the child input
// or invalidates target-owned execution.
func (tm *taskRuntime) persistObservedSubagentTurn(ctx context.Context, task *subagentTask, entry *taskapi.Entry) error {
	if tm == nil || task == nil || entry == nil {
		return nil
	}
	var lastErr error
	for range observedSubagentTurnPersistAttempts {
		lastErr = tm.persistTaskEntryWithConflictInvalidation(ctx, entry, false)
		if lastErr == nil {
			return nil
		}
		var conflict *taskapi.RevisionConflictError
		if !errors.As(lastErr, &conflict) || tm.store == nil {
			return lastErr
		}
		current, loadErr := tm.store.Get(context.WithoutCancel(ctx), entry.TaskID)
		if loadErr != nil || current == nil {
			return errors.Join(lastErr, loadErr)
		}
		desiredTurn := taskTurnSeqFromSpec(entry.Spec)
		currentTurn := taskTurnSeqFromSpec(current.Spec)
		if currentTurn > desiredTurn {
			return lastErr
		}
		rebased := tm.rebaseObservedSubagentTask(task, current)
		if rebased == nil {
			return lastErr
		}
		*entry = *rebased
	}
	return lastErr
}

// rebaseObservedSubagentTask preserves fields committed by concurrent Task
// observers while retaining this child activity's lifecycle and output view.
func (tm *taskRuntime) rebaseObservedSubagentTask(task *subagentTask, current *taskapi.Entry) *taskapi.Entry {
	if tm == nil || task == nil || current == nil {
		return nil
	}
	task.activityApplyMu.Lock()
	task.mu.Lock()
	liveMetadata := session.CloneState(task.metadata)
	mergedMetadata := session.CloneState(current.Metadata)
	if mergedMetadata == nil {
		mergedMetadata = map[string]any{}
	}
	for key, value := range liveMetadata {
		mergedMetadata[key] = value
	}
	for _, key := range []string{
		"final_event_persisted", subagentCancelPhaseKey, subagentCancelTurnSeqKey,
		"continue_phase", "continue_prompt", "continue_context", "continue_digest", "continue_turn_seq", "continue_reason",
	} {
		if _, kept := liveMetadata[key]; !kept {
			delete(mergedMetadata, key)
		}
	}
	task.metadata = mergedMetadata
	task.revision = current.Revision
	task.lease = taskapi.CloneLease(current.Lease)
	if cursor, ok := taskInt64Value(current.Metadata[subagentStreamEventCursorMeta]); ok {
		liveCursor := task.streamEventBase + int64(len(task.streamFrames))
		if cursor > liveCursor {
			task.streamEventBase += cursor - liveCursor
		}
	}
	if cursor, ok := taskInt64Value(current.Metadata[subagentStreamOutputCursorMeta]); ok {
		task.streamOutputCursor = max(task.streamOutputCursor, cursor)
	}
	rebased := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	task.activityApplyMu.Unlock()

	mergedSpec := session.CloneState(current.Spec)
	if mergedSpec == nil {
		mergedSpec = map[string]any{}
	}
	for key, value := range rebased.Spec {
		mergedSpec[key] = value
	}
	for _, key := range []string{subagentCancelPhaseKey, subagentCancelTurnSeqKey} {
		if _, kept := rebased.Spec[key]; !kept {
			delete(mergedSpec, key)
		}
	}
	for _, key := range []string{"continue_phase", "continue_digest", "continue_turn_seq"} {
		delete(mergedSpec, key)
	}
	rebased.Spec = mergedSpec
	return rebased
}

func beginObservedSubagentActivityLocked(task *subagentTask) {
	if task == nil {
		return
	}
	task.turnSeq++
	if task.turnSeq <= 0 {
		task.turnSeq = 1
	}
	task.stdout = ""
	task.stderr = ""
	task.contextUsage = nil
	task.stdoutCursor = 0
	task.stderrCursor = 0
	task.streamTerminalFramed = false
	task.applyResult(delegation.Result{
		TaskID: task.ref.TaskID, State: delegation.StateRunning, Running: true,
	})
	// Cancellation phases are owned by one child Turn. Preserve the journal
	// when this first observation opens its preselected generation; a strictly
	// later activity, or a legacy unscoped phase, clears it for a fresh effect.
	cancelPhase := subagentCancelPhase(taskStringValue(task.metadata[subagentCancelPhaseKey]))
	cancelTurnSeq, cancelTurnScoped := subagentCancelTurnSeq(task.metadata)
	if cancelPhase == subagentCancelPhaseNone || !cancelTurnScoped || cancelTurnSeq != task.turnSeq {
		delete(task.metadata, subagentCancelPhaseKey)
		delete(task.metadata, subagentCancelTurnSeqKey)
	}
	delete(task.metadata, "final_event_persisted")
}

func activityFrameForGeneration(frame stream.Frame, task *subagentTask, generation int64, activityID string) stream.Frame {
	frame = stream.CloneFrame(frame)
	frame.ActivityID = strings.TrimSpace(activityID)
	frame.Ref.TaskID = strings.TrimSpace(task.ref.TaskID)
	frame.Ref.SessionID = strings.TrimSpace(task.sessionRef.SessionID)
	frame.Ref.TerminalID = subagentTurnID(task.ref.TaskID, generation)
	if frame.Event != nil {
		if frame.Event.Scope == nil {
			frame.Event.Scope = &session.EventScope{}
		}
		frame.Event.Scope.TurnID = subagentTurnID(task.ref.TaskID, generation)
	}
	return frame
}

func validateTaskActivityTarget(task *subagentTask, target agent.ChildEndpointRef) error {
	if task == nil {
		return fmt.Errorf("child activity Task is unavailable")
	}
	target = agent.NormalizeChildEndpointRef(target)
	task.mu.Lock()
	want := agent.ChildEndpointRef{
		ParticipantID: strings.TrimSpace(task.anchor.AgentID),
		SessionID:     strings.TrimSpace(task.anchor.SessionID),
		EndpointKey:   strings.TrimSpace(task.ref.TaskID),
		Role:          subagentParticipantRole(task),
		Placement:     placement.Normalize(task.target.Placement),
	}
	task.mu.Unlock()
	if target.ParticipantID != want.ParticipantID || target.SessionID != want.SessionID ||
		target.EndpointKey != want.EndpointKey || target.Role != want.Role ||
		!reflect.DeepEqual(target.Placement, want.Placement) {
		return fmt.Errorf("child activity endpoint no longer matches its Task")
	}
	return nil
}

func subagentActivityCursor(task *subagentTask) uint64 {
	if task == nil {
		return 0
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.activityDurableCursor
}

var (
	_ agent.ChildActivityObserver      = subagentActivityObserver{}
	_ agent.ChildActivityBatchObserver = subagentActivityObserver{}
	_ agent.ChildActivityLiveObserver  = subagentActivityObserver{}
)
