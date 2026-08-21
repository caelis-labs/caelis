package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

const (
	subagentStreamEventCursorMeta   = "stream_event_cursor"
	subagentStreamOutputCursorMeta  = "stream_output_cursor"
	subagentFinalResponseCursorMeta = "final_response_cursor"
	subagentActivityIDMeta          = "child_activity_id"
	subagentActivityGenerationMeta  = "child_activity_generation"
	subagentActivityCursorMeta      = "child_activity_cursor"
)

func subagentSpawnTaskID(ref session.SessionRef, spawnID string) (string, error) {
	spawnID = strings.TrimSpace(spawnID)
	if spawnID == "" {
		return randomTaskID()
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(ref.SessionID) + "\x00" + spawnID))
	return hex.EncodeToString(sum[:taskIDRandomBytes]), nil
}

func resolveSpawnAgent(session session.Session, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || strings.EqualFold(requested, "self") {
		return "self", nil
	}
	return requested, nil
}

func (r *Runtime) buildDelegatedSpawnPromptContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
) (agent.ContextTransfer, uint64, error) {
	return r.buildParticipantPromptContext(ctx, activeSession, ref, session.ParticipantBinding{
		ID:             "spawn",
		Label:          "spawn",
		ContextSyncSeq: 0,
	})
}

func (r *Runtime) buildSideSubagentPromptContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	target string,
	sinceSeq uint64,
) (agent.ContextTransfer, uint64, error) {
	if r == nil || r.controllerContextRouter == nil {
		return agent.ContextTransfer{}, 0, fmt.Errorf("agent-sdk/runtime: controller context router is unavailable")
	}
	route, err := r.controllerContextRouter.ParticipantContext(ctx, controller.ParticipantContextRequest{
		SessionRef: session.NormalizeSessionRef(ref),
		Session:    session.CloneSession(activeSession),
		Binding: session.ParticipantBinding{
			ID:             strings.TrimSpace(target),
			Label:          strings.TrimSpace(target),
			ContextSyncSeq: sinceSeq,
		},
	})
	if err != nil {
		return agent.ContextTransfer{}, 0, err
	}
	return agent.CloneContextTransfer(route.Context), route.SyncSeq, nil
}

func isSideSubagentTask(task *subagentTask) bool {
	return subagentParticipantRole(task) == session.ParticipantRoleSidecar
}

func subagentParticipantRole(task *subagentTask) session.ParticipantRole {
	if task != nil {
		role := session.ParticipantRole(strings.TrimSpace(taskStringValue(task.metadata["participant_role"])))
		if role == session.ParticipantRoleSidecar {
			return role
		}
	}
	return session.ParticipantRoleDelegated
}

func normalizeSubagentParticipantRole(role session.ParticipantRole) (session.ParticipantRole, error) {
	switch role {
	case "", session.ParticipantRoleDelegated:
		return session.ParticipantRoleDelegated, nil
	case session.ParticipantRoleSidecar:
		return session.ParticipantRoleSidecar, nil
	default:
		return "", fmt.Errorf("agent-sdk/runtime: unsupported subagent participant role %q", role)
	}
}

func (tm *taskRuntime) authorizeSubagentControl(task *subagentTask, principal session.ActorKind, action string) error {
	switch principal {
	case session.ActorKindTool:
		if isSideSubagentTask(task) {
			return fmt.Errorf("agent-sdk/runtime: tool principal cannot %s sidecar subagent %q", strings.TrimSpace(action), task.handle)
		}
	case session.ActorKindUser:
		if !isSideSubagentTask(task) {
			return fmt.Errorf("agent-sdk/runtime: user principal cannot %s delegated subagent %q", strings.TrimSpace(action), task.handle)
		}
	case session.ActorKindController, session.ActorKindSystem:
		return nil
	default:
		return fmt.Errorf("agent-sdk/runtime: unsupported control principal %q", principal)
	}
	return nil
}

func (r *Runtime) StartSubagent(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
) (taskapi.Snapshot, error) {
	return r.StartSubagentWithOptions(ctx, ref, agent, prompt, source, StartSubagentOptions{})
}

func (r *Runtime) StartSubagentWithOptions(
	ctx context.Context,
	ref session.SessionRef,
	agent string,
	prompt string,
	source string,
	opts StartSubagentOptions,
) (taskapi.Snapshot, error) {
	if r == nil || r.sessions == nil || r.tasks == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: runtime is unavailable")
	}
	if r.subagents == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: subagent runner is unavailable")
	}
	ref = session.NormalizeSessionRef(ref)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	activeSession, err = r.ensureSessionController(ctx, activeSession)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	agent, err = resolveSpawnAgent(activeSession, agent)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if strings.TrimSpace(prompt) == "" {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: subagent prompt is required")
	}
	approvalMode := strings.TrimSpace(opts.ApprovalMode)
	if approvalMode == "" {
		if state, stateErr := r.sessions.SnapshotState(ctx, ref); stateErr == nil {
			approvalMode = string(r.currentApprovalMode(state))
		}
	}
	contextTransfer, _, err := r.buildSideSubagentPromptContext(ctx, activeSession, ref, strings.TrimSpace(agent), 0)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	snapshot, err := r.tasks.StartSubagent(ctx, activeSession, ref, r.subagents, taskapi.SubagentStartRequest{
		SpawnID:      strings.TrimSpace(opts.SpawnID),
		Agent:        strings.TrimSpace(agent),
		Prompt:       strings.TrimSpace(prompt),
		Context:      contextTransfer,
		Role:         session.ParticipantRoleSidecar,
		Source:       firstNonEmpty(strings.TrimSpace(source), "user"),
		Mode:         strings.TrimSpace(r.defaultPolicyMode),
		ApprovalMode: approvalMode,
		Approval:     newSubagentApprovalRequester(r, r.defaultPolicyMode, opts.ApprovalRequester, activeSession, ref),
	})
	if err != nil || !snapshot.Running {
		return snapshot, err
	}
	return r.tasks.Wait(ctx, ref, taskapi.ControlRequest{
		TaskID:    snapshot.Ref.TaskID,
		Yield:     2 * time.Second,
		Principal: session.ActorKindController,
		Source:    "runtime",
	})
}

func (r *Runtime) WaitSubagentTask(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	yield time.Duration,
) (taskapi.Snapshot, error) {
	if r == nil || r.tasks == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: runtime is unavailable")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: subagent task id is required")
	}
	return r.tasks.Wait(ctx, session.NormalizeSessionRef(ref), taskapi.ControlRequest{
		TaskID:    taskID,
		Yield:     yield,
		Principal: session.ActorKindUser,
		Source:    "user",
	})
}

func (tm *taskRuntime) waitSubagent(ctx context.Context, task *subagentTask, yield time.Duration) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	task.mu.Lock()
	cancelPhase := subagentCancelPhase(taskStringValue(task.metadata["cancel_phase"]))
	runner := task.runner
	running := task.running
	hasUnreadFinalResponses := task.hasUnreadFinalResponsesLocked()
	anchor := delegation.CloneAnchor(task.anchor)
	turnSeq := task.turnSeq
	task.mu.Unlock()
	if cancelPhase != subagentCancelPhaseNone && cancelPhase != subagentCancelPhaseCompleted {
		return tm.observePendingSubagentCancel(ctx, task, turnSeq, int(yield/time.Millisecond))
	}
	if hasUnreadFinalResponses {
		snapshot := task.snapshot()
		if snapshot.Metadata == nil {
			snapshot.Metadata = map[string]any{}
		}
		snapshot.Metadata[subagentUnreadFinalResponsesMetaKey] = true
		return snapshot, nil
	}
	if runner == nil || !running {
		return task.snapshot(), nil
	}
	result, err := runner.Wait(ctx, anchor, int(yield/time.Millisecond))
	if err != nil {
		// Cancelling a Task observation cancels only the observer. In
		// particular, wait-any cancels losing waits after one target finishes;
		// that must never interrupt the child itself.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return task.snapshot(), err
		}
		return tm.interruptObservedSubagent(ctx, task, turnSeq, err)
	}
	return tm.applyObservedSubagentResult(ctx, task, turnSeq, result, true)
}

// observeSubagent samples the attached child without advancing cancellation or
// converting a transport observation error into a terminal child state. A
// successful sample may still promote newly observed canonical result state.
func (tm *taskRuntime) observeSubagent(ctx context.Context, task *subagentTask) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	task.mu.Lock()
	cancelPhase := subagentCancelPhase(taskStringValue(task.metadata["cancel_phase"]))
	runner := task.runner
	running := task.running
	anchor := delegation.CloneAnchor(task.anchor)
	turnSeq := task.turnSeq
	task.mu.Unlock()
	if cancelPhase != subagentCancelPhaseNone && cancelPhase != subagentCancelPhaseCompleted {
		return task.snapshot(), nil
	}
	if runner == nil || !running {
		return task.snapshot(), nil
	}
	result, err := runner.Wait(ctx, anchor, 0)
	if err != nil {
		return task.snapshot(), err
	}
	return tm.applyObservedSubagentResult(ctx, task, turnSeq, result, false)
}

// observePendingSubagentCancel lets Task wait help reconcile a previously
// claimed cancellation without turning an observation into a competing
// lifecycle operation. If another owner is already applying the saga, the
// current snapshot is a successful observation and callers can wait again.
func (tm *taskRuntime) observePendingSubagentCancel(
	ctx context.Context,
	task *subagentTask,
	expectedTurnSeq int64,
	yieldMS int,
) (taskapi.Snapshot, error) {
	release, claimed := tm.tryClaimSubagentOperation(task.sessionRef, task.ref.TaskID)
	if !claimed {
		return task.snapshot(), nil
	}
	defer release()
	canonical, err := tm.lookupSubagentCanonical(ctx, task.sessionRef, task.ref.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	canonical.mu.Lock()
	turnSeq := canonical.turnSeq
	phase := subagentCancelPhase(taskStringValue(canonical.metadata["cancel_phase"]))
	snapshot := canonical.snapshotLocked()
	canonical.mu.Unlock()
	if turnSeq != expectedTurnSeq || phase == subagentCancelPhaseNone || phase == subagentCancelPhaseCompleted {
		return snapshot, nil
	}
	return tm.advanceSubagentCancel(ctx, canonical, phase, yieldMS)
}

// interruptObservedSubagent converts a failed wait into the existing bounded
// recovery state only while the sampled Turn is still current. A newer Turn or
// an in-flight lifecycle owner makes the failed sample stale and prevents it
// from interrupting current work.
func (tm *taskRuntime) interruptObservedSubagent(
	ctx context.Context,
	task *subagentTask,
	expectedTurnSeq int64,
	observationErr error,
) (taskapi.Snapshot, error) {
	release, claimed := tm.tryClaimSubagentOperation(task.sessionRef, task.ref.TaskID)
	if !claimed {
		return task.snapshot(), observationErr
	}
	defer release()
	canonical, err := tm.lookupSubagentCanonical(ctx, task.sessionRef, task.ref.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	canonical.mu.Lock()
	currentTurnSeq := canonical.turnSeq
	running := canonical.running
	snapshot := canonical.snapshotLocked()
	canonical.mu.Unlock()
	if currentTurnSeq != expectedTurnSeq || !running {
		return snapshot, nil
	}
	return tm.interruptSubagentTask(ctx, canonical, "subagent session interrupted during recovery")
}

// applyObservedSubagentResult records a canonical producer result or a
// successful explicit Task observation. Either path may durably promote
// terminal Task state and a completed sidecar's canonical final message. A
// read-only running sample is returned without mutating or persisting the Task.
func (tm *taskRuntime) applyObservedSubagentResult(
	ctx context.Context,
	task *subagentTask,
	expectedTurnSeq int64,
	result delegation.Result,
	persistRunning bool,
) (taskapi.Snapshot, error) {
	release, claimed := tm.tryClaimSubagentOperation(task.sessionRef, task.ref.TaskID)
	if !claimed {
		return task.snapshot(), nil
	}
	defer release()
	canonical, err := tm.lookupSubagentCanonical(ctx, task.sessionRef, task.ref.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	task = canonical
	task.mu.Lock()
	if task.turnSeq != expectedTurnSeq {
		snapshot := task.snapshotLocked()
		task.mu.Unlock()
		return snapshot, nil
	}
	if task.running && !persistRunning &&
		(result.Running || result.State == delegation.StateRunning) {
		snapshot := task.snapshotLocked()
		if preview := result.OutputPreview; taskOutputHasNonBlankLine(preview) {
			snapshot.Result["output_preview"] = preview
		}
		task.mu.Unlock()
		return snapshot, nil
	}
	// A Task-stream observer may have applied a terminal result while this
	// runner sample was in flight. Terminal state is monotonic: persist the
	// current result instead of reopening it with an older running snapshot.
	if task.running {
		task.seedStreamFromResult(result)
		task.applyResult(result)
	}
	snapshot := task.snapshotLocked()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return taskapi.Snapshot{}, err
	}
	if !snapshot.Running && snapshot.State != taskapi.StateCompleted {
		_ = tm.updateSubagentParticipant(ctx, task, "updated")
	}
	return snapshot, nil
}

func (tm *taskRuntime) cancelSubagent(ctx context.Context, task *subagentTask) (taskapi.Snapshot, error) {
	return tm.cancelSubagentSaga(ctx, task)
}

func (tm *taskRuntime) lookupSubagent(ctx context.Context, ref session.SessionRef, taskID string) (*subagentTask, error) {
	lookupID := strings.TrimSpace(taskID)
	tm.mu.RLock()
	task, ok := tm.subagents[lookupID]
	tm.mu.RUnlock()
	if ok && task != nil {
		if strings.TrimSpace(task.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
			return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
		}
		return task, nil
	}
	if tm.store == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err := tm.store.Get(ctx, lookupID)
	if err != nil || entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	if strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) || entry.Kind != taskapi.KindSubagent {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err = tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	if err != nil {
		return nil, err
	}
	rehydrated := tm.rehydrateSubagentTask(entry)
	tm.mu.Lock()
	if current := tm.subagents[rehydrated.ref.TaskID]; current != nil {
		tm.mu.Unlock()
		if strings.TrimSpace(current.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
			return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
		}
		return current, nil
	}
	tm.subagents[rehydrated.ref.TaskID] = rehydrated
	tm.rememberTaskHandleLocked(rehydrated.sessionRef.SessionID, rehydrated.handle)
	tm.mu.Unlock()
	return rehydrated, nil
}

// lookupSubagentCanonical reloads and publishes one canonical task while its
// session-scoped operation claim is held by the caller. Durable state wins over
// an older registry pointer; a live pointer at the same or newer revision keeps
// its process-local runner.
func (tm *taskRuntime) lookupSubagentCanonical(ctx context.Context, ref session.SessionRef, taskID string) (*subagentTask, error) {
	ref = session.NormalizeSessionRef(ref)
	taskID = strings.TrimSpace(taskID)
	tm.mu.RLock()
	current := tm.subagents[taskID]
	tm.mu.RUnlock()
	if tm.store == nil {
		if current != nil && strings.TrimSpace(current.sessionRef.SessionID) == strings.TrimSpace(ref.SessionID) {
			return current, nil
		}
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err := tm.store.Get(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/runtime: reload subagent task %q: %w", taskID, err)
	}
	if entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	if entry.Kind != taskapi.KindSubagent || strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err = tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	if err != nil {
		return nil, err
	}
	if current != nil && strings.TrimSpace(current.sessionRef.SessionID) == strings.TrimSpace(ref.SessionID) {
		current.mu.Lock()
		currentRevision := current.revision
		current.mu.Unlock()
		if currentRevision >= entry.Revision {
			return current, nil
		}
	}
	fresh := tm.rehydrateSubagentTask(entry)
	tm.mu.Lock()
	installed := tm.subagents[taskID]
	if installed != nil {
		installed.mu.Lock()
		installedRevision := installed.revision
		installed.mu.Unlock()
		if installedRevision >= entry.Revision {
			tm.mu.Unlock()
			return installed, nil
		}
	}
	tm.subagents[taskID] = fresh
	tm.rememberTaskHandleLocked(fresh.sessionRef.SessionID, fresh.handle)
	tm.mu.Unlock()
	return fresh, nil
}

func (tm *taskRuntime) invalidateSubagentTask(ref session.SessionRef, taskID string, throughRevision uint64) {
	if tm == nil {
		return
	}
	taskID = strings.TrimSpace(taskID)
	ref = session.NormalizeSessionRef(ref)
	tm.mu.Lock()
	current := tm.subagents[taskID]
	if current == nil || strings.TrimSpace(current.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
		tm.mu.Unlock()
		return
	}
	current.mu.Lock()
	stale := current.revision <= throughRevision
	current.mu.Unlock()
	if stale {
		delete(tm.subagents, taskID)
	}
	tm.mu.Unlock()
}

func (tm *taskRuntime) hasActiveSubagentTask(entry *taskapi.Entry) bool {
	if tm == nil || entry == nil {
		return false
	}
	taskID := strings.TrimSpace(entry.TaskID)
	sessionID := strings.TrimSpace(entry.Session.SessionID)
	if taskID == "" || sessionID == "" {
		return false
	}
	tm.mu.RLock()
	task := tm.subagents[taskID]
	tm.mu.RUnlock()
	if task == nil || strings.TrimSpace(task.sessionRef.SessionID) != sessionID {
		return false
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	return task.running
}

func interruptedSubagentEntry(entry *taskapi.Entry, reason string) *taskapi.Entry {
	next := taskapi.CloneEntry(entry)
	if next == nil {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "subagent interrupted during resume"
	}
	next.Running = false
	next.State = taskapi.StateInterrupted
	normalizeSubagentEntryResult(next, reason)
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["state"] = string(taskapi.StateInterrupted)
	next.Metadata["interrupted_reason"] = reason
	return next
}

func (tm *taskRuntime) interruptSubagentTask(ctx context.Context, task *subagentTask, reason string) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	task.mu.Lock()
	task.applyInterruptedLocked(reason)
	snapshot := task.snapshotLocked()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	_ = tm.updateSubagentParticipant(ctx, task, "updated")
	return snapshot, nil
}

func (tm *taskRuntime) rehydrateSubagentTask(entry *taskapi.Entry) *subagentTask {
	if entry == nil {
		return nil
	}
	target := taskSpecTarget(entry.Spec, "target")
	if target.Selector == "" {
		agent := taskSpecString(entry.Spec, "agent")
		target = delegation.Target{
			Selector: agent,
			Placement: delegation.Placement{
				Kind:  delegation.PlacementAgent,
				Agent: firstNonEmpty(taskSpecString(entry.Spec, "target_agent"), agent),
			},
		}
	}
	target = delegation.NormalizeTarget(target)
	result := session.CloneState(entry.Result)
	if taskTurnSeqFromSpec(entry.Spec) <= 1 {
		if stored, ok := entry.Spec["spawn_result"].(map[string]any); ok {
			if result == nil {
				result = map[string]any{}
			}
			for key, value := range session.CloneState(stored) {
				if _, exists := result[key]; !exists {
					result[key] = value
				}
			}
		}
	}
	// Durable Task records may predate FailureDiagnostic. Rehydrate lifecycle
	// state but never trust a legacy free-form Result["error"] as a
	// Runtime-produced diagnostic.
	normalizeSubagentResultForState(&result, entry.State, entry.FailureDiagnostic)
	task := &subagentTask{
		ref: taskapi.Ref{
			TaskID:     strings.TrimSpace(entry.TaskID),
			SessionID:  taskSpecString(entry.Spec, "session_id"),
			TerminalID: firstNonEmpty(taskSpecString(entry.Spec, "terminal_id"), subagentTerminalID(entry.TaskID)),
		},
		sessionRef: session.NormalizeSessionRef(entry.Session),
		anchor: delegation.Anchor{
			TaskID:    strings.TrimSpace(entry.TaskID),
			SessionID: taskSpecString(entry.Spec, "session_id"),
			AgentID:   taskSpecString(entry.Spec, "agent_id"),
		},
		runner:          tm.runtime.subagents,
		agent:           target.Selector,
		target:          target,
		handle:          firstNonEmpty(entry.Handle, taskSpecString(entry.Spec, "handle"), taskStringValue(entry.Metadata["handle"])),
		title:           strings.TrimSpace(entry.Title),
		prompt:          taskSpecString(entry.Spec, "prompt"),
		mode:            taskSpecString(entry.Spec, "mode"),
		approvalMode:    taskSpecString(entry.Spec, "approval_mode"),
		createdAt:       entry.CreatedAt,
		revision:        entry.Revision,
		lease:           taskapi.CloneLease(entry.Lease),
		state:           entry.State,
		running:         entry.Running,
		turnSeq:         taskTurnSeqFromSpec(entry.Spec),
		result:          result,
		metadata:        session.CloneState(entry.Metadata),
		completionReady: true,
	}
	if cursor, ok := taskInt64Value(entry.Metadata[subagentStreamEventCursorMeta]); ok && cursor >= 0 {
		task.streamEventBase = cursor
	}
	if cursor, ok := taskInt64Value(entry.Metadata[subagentStreamOutputCursorMeta]); ok && cursor >= 0 {
		task.streamOutputCursor = cursor
	}
	if cursor, ok := taskInt64Value(entry.Metadata[subagentFinalResponseCursorMeta]); ok && cursor >= 0 {
		task.finalResponseCursor = cursor
	}
	task.activityID = strings.TrimSpace(taskStringValue(entry.Metadata[subagentActivityIDMeta]))
	if generation, ok := taskInt64Value(entry.Metadata[subagentActivityGenerationMeta]); ok && generation > 0 {
		task.activityGeneration = generation
	}
	if cursor, ok := taskInt64Value(entry.Metadata[subagentActivityCursorMeta]); ok && cursor >= 0 {
		task.activityCursor = uint64(cursor)
		task.activityDurableCursor = uint64(cursor)
	}
	if task.state == taskapi.StateCompleted {
		if final := firstNonBlankTaskOutput(taskRawStringValue(task.result["final_message"]), taskRawStringValue(task.result["result"])); taskOutputHasNonBlankLine(final) {
			task.latestFinalText = final
			task.latestFinalTurnSeq = max(task.turnSeq, 1)
			task.latestFinalOrder = task.streamEventBase
			task.latestFinalAt = entry.UpdatedAt
			task.latestFinalActivityID = task.activityID
			if task.latestFinalAt.IsZero() {
				task.latestFinalAt = entry.CreatedAt
			}
			task.semanticRetention.protectLatestFinal(
				subagentTurnID(task.ref.TaskID, task.latestFinalTurnSeq), task.latestFinalOrder,
			)
		}
	}
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	if _, ok := task.metadata["context_unsupported"]; !ok && taskSpecBool(entry.Spec, "context_unsupported") {
		task.metadata["context_unsupported"] = true
	}
	if _, ok := task.metadata["include_context"]; !ok && taskSpecBool(entry.Spec, "include_context") {
		task.metadata["include_context"] = true
	}
	if _, ok := task.metadata["parent_call"]; !ok {
		task.metadata["parent_call"] = taskSpecString(entry.Spec, "parent_call")
	}
	if strings.TrimSpace(taskStringValue(task.metadata["parent_call"])) != "" {
		task.metadata["parent_tool"] = firstNonEmpty(taskStringValue(task.metadata["parent_tool"]), spawn.ToolName)
	}
	if task.turnSeq <= 0 {
		task.turnSeq = taskTurnSeqFromSpec(entry.Metadata)
	}
	if task.turnSeq <= 0 {
		task.turnSeq = 1
	}
	if task.runner == nil && task.running {
		task.applyInterruptedLocked("subagent session requires reconnect")
	}
	return task
}

func (t *subagentTask) applyResult(result delegation.Result) {
	if t == nil {
		return
	}
	result = delegation.CloneResult(result)
	t.state = taskStateFromDelegation(result.State)
	t.running = result.State == delegation.StateRunning
	if t.state == taskapi.StateCompleted && taskOutputHasNonBlankLine(result.Result) {
		t.retainCompletedFinalLocked(result.Result)
	}
	if t.result == nil {
		t.result = map[string]any{}
	}
	if t.metadata == nil {
		t.metadata = map[string]any{}
	}
	t.metadata["task_id"] = t.ref.TaskID
	t.metadata["task_kind"] = string(taskapi.KindSubagent)
	t.metadata["agent"] = t.agent
	t.metadata["agent_id"] = t.anchor.AgentID
	t.metadata["handle"] = t.handle
	t.metadata["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.metadata["prompt"] = t.prompt
	t.metadata["session_id"] = t.anchor.SessionID
	t.metadata["terminal_id"] = t.ref.TerminalID
	t.metadata["state"] = string(t.state)
	if taskOutputHasNonBlankLine(result.OutputPreview) {
		t.result["output_preview"] = result.OutputPreview
	} else if t.result != nil {
		delete(t.result, "output_preview")
	}
	if taskOutputHasNonBlankLine(result.Result) {
		t.result["result"] = result.Result
		if !t.running {
			t.result["final_message"] = result.Result
		}
	} else if !t.running {
		delete(t.result, "result")
		delete(t.result, "final_message")
	} else if t.result != nil {
		delete(t.result, "result")
		delete(t.result, "final_message")
	}
	t.result["handle"] = t.handle
	t.result["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.result["agent"] = t.agent
	t.result["state"] = string(t.state)
	normalizeSubagentResultForState(&t.result, t.state, result.Error)
	t.notifyStreamChangeLocked()
}

func (t *subagentTask) isRunning() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

func (t *subagentTask) applyInterruptedLocked(reason string) {
	if t == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "subagent interrupted during resume"
	}
	t.running = false
	t.state = taskapi.StateInterrupted
	normalizeSubagentResultForState(&t.result, taskapi.StateInterrupted, reason)
	if t.metadata == nil {
		t.metadata = map[string]any{}
	}
	t.result["handle"] = t.handle
	t.result["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.result["agent"] = t.agent
	t.metadata["state"] = string(taskapi.StateInterrupted)
	t.metadata["interrupted_reason"] = reason
	t.metadata["task_id"] = t.ref.TaskID
	t.metadata["task_kind"] = string(taskapi.KindSubagent)
	t.metadata["agent"] = t.agent
	t.metadata["agent_id"] = t.anchor.AgentID
	t.metadata["handle"] = t.handle
	t.metadata["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.metadata["prompt"] = t.prompt
	t.metadata["session_id"] = t.anchor.SessionID
	t.metadata["terminal_id"] = t.ref.TerminalID
	t.notifyStreamChangeLocked()
}

// normalizeSubagentResultForState is the sole writer for mutually exclusive
// subagent result and failure fields. diagnostic is trusted only when supplied
// directly by a typed Runtime producer; callers rehydrating legacy map data
// pass an empty diagnostic and receive the fixed state fallback.
func normalizeSubagentResultForState(result *map[string]any, state taskapi.State, diagnostic string) {
	if result == nil {
		return
	}
	if *result == nil {
		*result = map[string]any{}
	}
	(*result)["state"] = string(state)
	if diagnostic, failureState := subagentFailureDiagnostic(state, diagnostic); failureState {
		delete(*result, "result")
		delete(*result, "final_message")
		delete(*result, "output_preview")
		(*result)["error"] = diagnostic
		return
	}
	delete(*result, "error")
	switch state {
	case taskapi.StateCancelled, taskapi.StateTerminated:
		delete(*result, "result")
		delete(*result, "final_message")
		delete(*result, "output_preview")
	}
}

// normalizeSubagentEntryResult is the durable counterpart to
// normalizeSubagentResultForState. FailureDiagnostic is the typed trust
// boundary; callers pass only diagnostics received directly from a typed
// Runtime producer.
func normalizeSubagentEntryResult(entry *taskapi.Entry, diagnostic string) {
	if entry == nil {
		return
	}
	diagnostic, failureState := subagentFailureDiagnostic(entry.State, diagnostic)
	if failureState {
		entry.FailureDiagnostic = diagnostic
	} else {
		entry.FailureDiagnostic = ""
	}
	normalizeSubagentResultForState(&entry.Result, entry.State, diagnostic)
}

func subagentFailureDiagnostic(state taskapi.State, diagnostic string) (string, bool) {
	diagnostic = strings.TrimSpace(diagnostic)
	switch state {
	case taskapi.StateFailed:
		return firstNonEmpty(diagnostic, "subagent failed"), true
	case taskapi.StateInterrupted:
		return firstNonEmpty(diagnostic, "subagent interrupted"), true
	case taskapi.StateUnknownOutcome:
		return firstNonEmpty(diagnostic, "subagent outcome could not be confirmed"), true
	default:
		return "", false
	}
}

func (t *subagentTask) snapshot() taskapi.Snapshot {
	if t == nil {
		return taskapi.Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked()
}

func (t *subagentTask) snapshotLocked() taskapi.Snapshot {
	if t == nil {
		return taskapi.Snapshot{}
	}
	result := session.CloneState(t.result)
	metadata := session.CloneState(t.metadata)
	if result == nil {
		result = map[string]any{}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	turnID := subagentTurnID(t.ref.TaskID, t.turnSeq)
	result["turn_id"] = turnID
	result["turn_seq"] = t.turnSeq
	metadata["turn_id"] = turnID
	metadata["turn_seq"] = t.turnSeq
	return taskapi.CloneSnapshot(taskapi.Snapshot{
		Ref:            t.ref,
		Handle:         t.handle,
		Revision:       t.revision,
		Kind:           taskapi.KindSubagent,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  false,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      time.Now(),
		Lease:          taskapi.CloneLease(t.lease),
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		EventCursor:    t.streamEventBase + int64(len(t.streamFrames)),
		Result:         result,
		Metadata:       metadata,
	})
}

func (t *subagentTask) entrySnapshot(now time.Time) *taskapi.Entry {
	if t == nil {
		return nil
	}
	metadata := session.CloneState(t.metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[subagentStreamEventCursorMeta] = t.streamEventBase + int64(len(t.streamFrames))
	metadata[subagentStreamOutputCursorMeta] = t.streamOutputCursor
	metadata[subagentFinalResponseCursorMeta] = t.finalResponseCursor
	entry := &taskapi.Entry{
		TaskID:         t.ref.TaskID,
		Handle:         t.handle,
		Revision:       t.revision,
		Kind:           taskapi.KindSubagent,
		Session:        t.sessionRef,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  false,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      now,
		Lease:          taskapi.CloneLease(t.lease),
		Spec: map[string]any{
			"target":               delegation.NormalizeTarget(t.target),
			"prompt":               t.prompt,
			"include_context":      taskSpecBool(t.metadata, "include_context"),
			"context_unsupported":  taskSpecBool(t.metadata, "context_unsupported"),
			"mode":                 t.mode,
			"approval_mode":        t.approvalMode,
			"spawn_identity":       taskStringValue(t.metadata["spawn_identity"]),
			"spawn_request_digest": taskStringValue(t.metadata["spawn_request_digest"]),
			"spawn_phase":          taskStringValue(t.metadata["spawn_status"]),
			"cancel_phase":         taskStringValue(t.metadata["cancel_phase"]),
			"session_id":           t.anchor.SessionID,
			"agent_id":             t.anchor.AgentID,
			"handle":               t.handle,
			"terminal_id":          t.ref.TerminalID,
			"turn_seq":             t.turnSeq,
			"turn_id":              subagentTurnID(t.ref.TaskID, t.turnSeq),
			"participant_role":     string(subagentParticipantRole(t)),
			"spawn_result":         taskapi.SanitizeResultForPersistence(t.result, taskapi.ResultPersistenceCanonical),
		},
		Result:   subagentTaskEntryResult(t.result, t.running),
		Metadata: metadata,
	}
	normalizeSubagentEntryResult(entry, taskRawStringValue(t.result["error"]))
	return entry
}

func subagentTaskEntryResult(result map[string]any, running bool) map[string]any {
	mode := taskapi.ResultPersistenceCanonical
	if !running {
		mode = taskapi.ResultPersistenceDeferred
	}
	return taskapi.SanitizeResultForPersistence(result, mode)
}

func taskSpecTarget(values map[string]any, key string) delegation.Target {
	if len(values) == 0 {
		return delegation.Target{}
	}
	switch raw := values[key].(type) {
	case delegation.Target:
		return delegation.NormalizeTarget(raw)
	case *delegation.Target:
		if raw != nil {
			return delegation.NormalizeTarget(*raw)
		}
		return delegation.Target{}
	}
	encoded, err := json.Marshal(values[key])
	if err != nil {
		return delegation.Target{}
	}
	var target delegation.Target
	if err := json.Unmarshal(encoded, &target); err != nil {
		return delegation.Target{}
	}
	return delegation.NormalizeTarget(target)
}

func subagentTurnID(taskID string, seq int64) string {
	taskID = strings.TrimSpace(taskID)
	if seq <= 0 {
		seq = 1
	}
	if taskID == "" {
		return fmt.Sprintf("turn-%d", seq)
	}
	return fmt.Sprintf("%s:%d", taskID, seq)
}

func taskTurnSeqFromSpec(values map[string]any) int64 {
	if len(values) == 0 {
		return 0
	}
	value, ok := intArg(values, "turn_seq")
	if !ok {
		return 0
	}
	return int64(value)
}
