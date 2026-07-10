package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

const subagentSagaRecoveryTimeout = 5 * time.Second

func (tm *taskRuntime) StartSubagent(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	runner subagent.Runner,
	req taskapi.SubagentStartRequest,
) (taskapi.Snapshot, error) {
	if runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: subagent runner is required")
	}
	taskID, err := subagentSpawnTaskID(ref, req.SpawnID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = strings.TrimSpace(tm.runtime.defaultPolicyMode)
	}
	spawnID := firstNonEmpty(strings.TrimSpace(req.SpawnID), taskID)
	role, err := normalizeSubagentParticipantRole(req.Role)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	handle := tm.reserveSubagentHandle(activeSession, ref, req.Agent)
	intent, existing, done, shouldSpawn, err := tm.beginSubagentSpawn(ctx, ref, taskID, spawnID, req.Agent, req.Prompt, handle, runner)
	if done || err != nil {
		return existing, err
	}
	var task *subagentTask
	if shouldSpawn {
		childPrompt := subagentPromptWithContext(req.ContextPrelude, req.Prompt)
		anchor, result, err := runner.Spawn(ctx, subagent.SpawnContext{
			SessionRef: session.NormalizeSessionRef(ref), Session: session.CloneSession(activeSession), CWD: strings.TrimSpace(activeSession.CWD),
			TaskID: taskID, ParentCallID: strings.TrimSpace(req.ParentCall), Mode: mode, ApprovalMode: strings.TrimSpace(req.ApprovalMode),
			ApprovalRequester: req.Approval, Streams: tm,
		}, delegation.Request{Agent: strings.TrimSpace(req.Agent), Prompt: childPrompt})
		if err != nil {
			intent.State = taskapi.StateUnknownOutcome
			setSpawnEntryStatus(intent, spawnStatusUnknownOutcome, err.Error())
			_ = tm.persistSpawnEntry(context.WithoutCancel(ctx), intent)
			return taskapi.Snapshot{}, err
		}
		anchor.TaskID = taskID
		now := tm.runtime.now()
		task = &subagentTask{
			ref:        taskapi.Ref{TaskID: taskID, SessionID: strings.TrimSpace(anchor.SessionID), TerminalID: subagentTerminalID(taskID)},
			sessionRef: session.NormalizeSessionRef(ref), anchor: delegation.CloneAnchor(anchor), runner: runner,
			agent: strings.TrimSpace(anchor.Agent), handle: handle, title: spawn.ToolName + " " + strings.TrimSpace(anchor.Agent),
			prompt: strings.TrimSpace(req.Prompt), createdAt: now, revision: intent.Revision,
			state: taskStateFromDelegation(result.State), running: result.State == delegation.StateRunning, turnSeq: 1,
			metadata: map[string]any{
				"source": firstNonEmpty(strings.TrimSpace(req.Source), "agent_spawn"), "participant_role": string(role),
				"spawn_status": spawnStatusSpawned, "spawn_identity": spawnID,
			},
		}
		task.applyResult(result)
		task.seedStreamFromResult(result)
		spawnedEntry := task.entrySnapshot(tm.runtime.now())
		if err := tm.persistSpawnEntry(ctx, spawnedEntry); err != nil {
			return taskapi.Snapshot{}, tm.compensateSubagentSpawn(ctx, task, err)
		}
		task.revision = spawnedEntry.Revision
	} else {
		task = tm.rehydrateSubagentTask(intent)
		task.runner = runner
	}
	tm.mu.Lock()
	tm.subagents[taskID] = task
	pending := append([]stream.Frame(nil), tm.pending[taskID]...)
	delete(tm.pending, taskID)
	tm.order[strings.TrimSpace(ref.SessionID)] = append(tm.order[strings.TrimSpace(ref.SessionID)], taskID)
	tm.mu.Unlock()
	task.applyStreamFrames(pending)
	return tm.advanceSubagentSpawn(ctx, activeSession, task, strings.TrimSpace(req.ParentCall), strings.TrimSpace(req.Prompt))
}

const (
	spawnStatusPrepared            = "prepared"
	spawnStatusSpawning            = "spawning"
	spawnStatusSpawned             = "spawned"
	spawnStatusParticipantAttached = "participant_attached"
	spawnStatusCanonicalCommitting = "canonical_committing"
	spawnStatusCanonicalCommitted  = "canonical_committed"
	spawnStatusCommitted           = "committed"
	spawnStatusCompensated         = "compensated"
	spawnStatusUnknownOutcome      = "unknown_outcome"
)

func (tm *taskRuntime) beginSubagentSpawn(
	ctx context.Context,
	ref session.SessionRef,
	taskID string,
	spawnID string,
	agent string,
	prompt string,
	handle string,
	runner subagent.Runner,
) (*taskapi.Entry, taskapi.Snapshot, bool, bool, error) {
	if tm == nil || tm.store == nil {
		return nil, taskapi.Snapshot{}, false, false, errors.New("agent-sdk/runtime: durable task store is required before subagent spawn")
	}
	if _, ok := tm.store.(taskapi.CASStore); !ok {
		return nil, taskapi.Snapshot{}, false, false, errors.New("agent-sdk/runtime: subagent spawn requires task.CASStore")
	}
	if existing, err := tm.store.Get(ctx, taskID); err == nil && existing != nil {
		if taskSpecString(existing.Spec, "spawn_identity") != strings.TrimSpace(spawnID) ||
			taskSpecString(existing.Spec, "agent") != strings.TrimSpace(agent) ||
			taskSpecString(existing.Spec, "prompt") != strings.TrimSpace(prompt) {
			return nil, taskapi.Snapshot{}, false, false, fmt.Errorf("agent-sdk/runtime: spawn identity %q conflicts with durable intent", spawnID)
		}
		status := taskStringValue(existing.Metadata["spawn_status"])
		switch status {
		case spawnStatusCommitted:
			task := tm.rehydrateSubagentTask(existing)
			tm.rememberRehydratedSubagent(task)
			return existing, task.snapshot(), true, false, nil
		case spawnStatusPrepared:
			claimed := taskapi.CloneEntry(existing)
			setSpawnEntryStatus(claimed, spawnStatusSpawning, "")
			if err := tm.persistSpawnEntry(ctx, claimed); err != nil {
				var conflict *taskapi.RevisionConflictError
				if errors.As(err, &conflict) {
					return existing, snapshotFromTaskEntry(existing), true, false, fmt.Errorf("agent-sdk/runtime: subagent spawn %q was claimed concurrently: %w", spawnID, err)
				}
				return nil, taskapi.Snapshot{}, false, false, err
			}
			return claimed, taskapi.Snapshot{}, false, true, nil
		case spawnStatusSpawned, spawnStatusParticipantAttached, spawnStatusCanonicalCommitting, spawnStatusCanonicalCommitted:
			task := tm.rehydrateSubagentTask(existing)
			task.runner = runner
			tm.rememberRehydratedSubagent(task)
			return existing, task.snapshot(), false, false, nil
		case spawnStatusSpawning:
			return existing, snapshotFromTaskEntry(existing), true, false, fmt.Errorf("agent-sdk/runtime: subagent spawn %q crossed the external effect boundary; refusing blind respawn", spawnID)
		case spawnStatusUnknownOutcome:
			return existing, snapshotFromTaskEntry(existing), true, false, fmt.Errorf("agent-sdk/runtime: subagent spawn %q has unknown outcome; refusing blind respawn", spawnID)
		case spawnStatusCompensated:
			return existing, snapshotFromTaskEntry(existing), true, false, fmt.Errorf("agent-sdk/runtime: subagent spawn %q was compensated", spawnID)
		default:
			return existing, snapshotFromTaskEntry(existing), true, false, fmt.Errorf("agent-sdk/runtime: subagent spawn %q has invalid durable status %q", spawnID, status)
		}
	}

	now := tm.runtime.now()
	entry := &taskapi.Entry{
		TaskID: taskID, Kind: taskapi.KindSubagent, Session: session.NormalizeSessionRef(ref),
		Title: "SPAWN " + strings.TrimSpace(agent), State: taskapi.StatePrepared, CreatedAt: now, UpdatedAt: now,
		SupportsCancel: true,
		Spec: map[string]any{
			"spawn_identity": strings.TrimSpace(spawnID), "agent": strings.TrimSpace(agent), "prompt": strings.TrimSpace(prompt),
			"handle": strings.TrimSpace(handle), "terminal_id": subagentTerminalID(taskID), "turn_seq": int64(1),
		},
		Metadata: map[string]any{"spawn_status": spawnStatusPrepared, "spawn_identity": strings.TrimSpace(spawnID)},
	}
	if err := tm.persistSpawnEntry(ctx, entry); err != nil {
		return nil, taskapi.Snapshot{}, false, false, err
	}
	setSpawnEntryStatus(entry, spawnStatusSpawning, "")
	if err := tm.persistSpawnEntry(ctx, entry); err != nil {
		return nil, taskapi.Snapshot{}, false, false, err
	}
	return entry, taskapi.Snapshot{}, false, true, nil
}

func (tm *taskRuntime) rememberRehydratedSubagent(task *subagentTask) {
	if tm == nil || task == nil {
		return
	}
	tm.mu.Lock()
	tm.subagents[task.ref.TaskID] = task
	tm.rememberSubagentHandleLocked(task.sessionRef.SessionID, task.handle)
	tm.mu.Unlock()
}

func setSpawnEntryStatus(entry *taskapi.Entry, status string, reason string) {
	if entry == nil {
		return
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	entry.Metadata["spawn_status"] = strings.TrimSpace(status)
	if strings.TrimSpace(reason) != "" {
		entry.Metadata["spawn_reason"] = strings.TrimSpace(reason)
	}
}

func snapshotFromTaskEntry(entry *taskapi.Entry) taskapi.Snapshot {
	if entry == nil {
		return taskapi.Snapshot{}
	}
	return taskapi.Snapshot{
		Ref:      taskapi.Ref{TaskID: entry.TaskID, SessionID: taskSpecString(entry.Spec, "session_id"), TerminalID: taskSpecString(entry.Spec, "terminal_id")},
		Revision: entry.Revision, Kind: entry.Kind, Title: entry.Title, State: entry.State, Running: entry.Running,
		SupportsInput: entry.SupportsInput, SupportsCancel: entry.SupportsCancel, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
		Result: session.CloneState(entry.Result), Metadata: session.CloneState(entry.Metadata),
	}
}

func (tm *taskRuntime) markSubagentSpawnStatus(ctx context.Context, task *subagentTask, status string, reason string) error {
	if task == nil {
		return nil
	}
	task.mu.Lock()
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata["spawn_status"] = status
	if reason != "" {
		task.metadata["spawn_reason"] = strings.TrimSpace(reason)
	}
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	err := tm.persistSpawnEntry(ctx, entry)
	if err == nil {
		task.mu.Lock()
		task.revision = entry.Revision
		task.mu.Unlock()
	}
	return err
}

func (tm *taskRuntime) advanceSubagentSpawn(
	ctx context.Context,
	activeSession session.Session,
	task *subagentTask,
	parentCall string,
	prompt string,
) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, errors.New("agent-sdk/runtime: subagent spawn task is required")
	}
	status := taskStringValue(task.metadata["spawn_status"])
	if status == spawnStatusSpawned {
		if err := tm.ensureSubagentParticipantAttached(ctx, activeSession, task, parentCall); err != nil {
			return taskapi.Snapshot{}, tm.compensateSubagentSpawn(ctx, task, err)
		}
		if err := tm.markSubagentSpawnStatus(ctx, task, spawnStatusParticipantAttached, ""); err != nil {
			return taskapi.Snapshot{}, tm.compensateSubagentSpawn(ctx, task, err)
		}
		status = spawnStatusParticipantAttached
	}
	if status == spawnStatusParticipantAttached {
		if err := tm.markSubagentSpawnStatus(ctx, task, spawnStatusCanonicalCommitting, ""); err != nil {
			return taskapi.Snapshot{}, tm.compensateSubagentSpawn(ctx, task, err)
		}
		status = spawnStatusCanonicalCommitting
	}
	if status == spawnStatusCanonicalCommitting {
		if err := tm.appendSideSubagentUserEvent(ctx, task, strings.TrimSpace(prompt)); err != nil {
			return taskapi.Snapshot{}, err
		}
		if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
			return taskapi.Snapshot{}, err
		}
		if err := tm.markSubagentSpawnStatus(context.WithoutCancel(ctx), task, spawnStatusCanonicalCommitted, ""); err != nil {
			return taskapi.Snapshot{}, err
		}
		status = spawnStatusCanonicalCommitted
	}
	if status == spawnStatusCanonicalCommitted {
		if err := tm.markSubagentSpawnStatus(context.WithoutCancel(ctx), task, spawnStatusCommitted, ""); err != nil {
			return taskapi.Snapshot{}, err
		}
		status = spawnStatusCommitted
	}
	if status != spawnStatusCommitted {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: cannot advance subagent spawn from phase %q", status)
	}
	return task.snapshot(), nil
}

func (tm *taskRuntime) ensureSubagentParticipantAttached(ctx context.Context, activeSession session.Session, task *subagentTask, parentCall string) error {
	current, err := tm.runtime.sessions.Session(ctx, task.sessionRef)
	if err != nil {
		return err
	}
	if binding, ok := participantBinding(current, task.anchor.AgentID); ok && strings.TrimSpace(binding.DelegationID) == strings.TrimSpace(task.ref.TaskID) {
		return nil
	}
	err = tm.attachSubagentParticipant(ctx, activeSession, task, strings.TrimSpace(parentCall))
	if err == nil || !session.IsCommitted(err) {
		return err
	}
	reloaded, loadErr := tm.runtime.sessions.Session(context.WithoutCancel(ctx), task.sessionRef)
	if loadErr != nil {
		return errors.Join(err, loadErr)
	}
	binding, ok := participantBinding(reloaded, task.anchor.AgentID)
	if !ok || strings.TrimSpace(binding.DelegationID) != strings.TrimSpace(task.ref.TaskID) {
		return err
	}
	return nil
}

func (tm *taskRuntime) compensateSubagentSpawn(ctx context.Context, task *subagentTask, cause error) error {
	if task == nil {
		return cause
	}
	cancelErr := task.runner.Cancel(context.WithoutCancel(ctx), delegation.CloneAnchor(task.anchor))
	status := spawnStatusCompensated
	state := taskapi.StateCancelled
	if cancelErr != nil {
		status = spawnStatusUnknownOutcome
		state = taskapi.StateUnknownOutcome
	}
	task.mu.Lock()
	task.running = false
	task.state = state
	if task.result == nil {
		task.result = map[string]any{}
	}
	task.result["state"] = string(state)
	task.result["error"] = strings.TrimSpace(cause.Error())
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata["spawn_status"] = status
	task.metadata["spawn_reason"] = strings.TrimSpace(cause.Error())
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	persistErr := tm.persistSpawnEntry(context.WithoutCancel(ctx), entry)
	detachErr := tm.detachSubagentParticipant(context.WithoutCancel(ctx), task)
	return errors.Join(cause, cancelErr, persistErr, detachErr)
}

func (tm *taskRuntime) detachSubagentParticipant(ctx context.Context, task *subagentTask) error {
	lifecycle, ok := tm.runtime.sessions.(session.ParticipantLifecycleService)
	if !ok {
		return errors.New("agent-sdk/runtime: participant lifecycle store does not support atomic compensation")
	}
	active, err := tm.runtime.sessions.Session(ctx, task.sessionRef)
	if err != nil {
		return err
	}
	binding, ok := participantBinding(active, task.anchor.AgentID)
	if !ok {
		return nil
	}
	event := participantLifecycleEvent(active, binding, "detached", tm.runtime.now())
	_, _, err = lifecycle.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
		SessionRef: task.sessionRef, ExpectedRevision: &active.Revision, MutationGuard: session.RuntimeMutationGuard(ctx), ParticipantID: binding.ID, Event: event,
	})
	return err
}

func (tm *taskRuntime) appendSubagentSagaEvent(ctx context.Context, ref session.SessionRef, event *session.Event) error {
	req := session.AppendEventRequest{SessionRef: ref, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event}
	_, err := tm.runtime.sessions.AppendEvent(ctx, req)
	if err == nil || !session.IsCommitted(err) {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), subagentSagaRecoveryTimeout)
	defer cancel()
	req.MutationGuard = session.RuntimeMutationGuard(recoveryCtx)
	_, retryErr := tm.runtime.sessions.AppendEvent(recoveryCtx, req)
	if retryErr != nil {
		return errors.Join(err, retryErr)
	}
	return nil
}
