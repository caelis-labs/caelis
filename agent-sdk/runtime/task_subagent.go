package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

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
	taskID, err := randomTaskID()
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = strings.TrimSpace(tm.runtime.defaultPolicyMode)
	}
	childPrompt := subagentPromptWithContext(req.ContextPrelude, req.Prompt)
	anchor, result, err := runner.Spawn(ctx, subagent.SpawnContext{
		SessionRef:        session.NormalizeSessionRef(ref),
		Session:           session.CloneSession(activeSession),
		CWD:               strings.TrimSpace(activeSession.CWD),
		TaskID:            taskID,
		ParentCallID:      strings.TrimSpace(req.ParentCall),
		Mode:              mode,
		ApprovalMode:      strings.TrimSpace(req.ApprovalMode),
		ApprovalRequester: req.Approval,
		Streams:           tm,
	}, delegation.Request{
		Agent:  strings.TrimSpace(req.Agent),
		Prompt: childPrompt,
	})
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	anchor.TaskID = taskID
	now := tm.runtime.now()
	task := &subagentTask{
		ref: taskapi.Ref{
			TaskID:     taskID,
			SessionID:  strings.TrimSpace(anchor.SessionID),
			TerminalID: subagentTerminalID(taskID),
		},
		sessionRef: session.NormalizeSessionRef(ref),
		anchor:     delegation.CloneAnchor(anchor),
		runner:     runner,
		agent:      strings.TrimSpace(anchor.Agent),
		handle:     tm.reserveSubagentHandle(activeSession, ref, anchor.Agent),
		title:      spawn.ToolName + " " + strings.TrimSpace(anchor.Agent),
		prompt:     strings.TrimSpace(req.Prompt),
		createdAt:  now,
		state:      taskStateFromDelegation(result.State),
		running:    result.State == delegation.StateRunning,
		turnSeq:    1,
		metadata: map[string]any{
			"source":      firstNonEmpty(strings.TrimSpace(req.Source), "agent_spawn"),
			"interaction": subagentInteraction(req.ParentTool, req.Source),
		},
	}
	task.applyResult(result)
	task.seedStreamFromResult(result)
	tm.mu.Lock()
	tm.subagents[taskID] = task
	pending := append([]stream.Frame(nil), tm.pending[taskID]...)
	delete(tm.pending, taskID)
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.order[sessionID] = append(tm.order[sessionID], taskID)
	tm.mu.Unlock()
	task.applyStreamFrames(pending)
	if err := tm.persistTaskEntry(ctx, task.entrySnapshot(tm.runtime.now())); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.attachSubagentParticipant(ctx, activeSession, task, strings.TrimSpace(req.ParentCall)); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentUserEvent(ctx, task, strings.TrimSpace(req.Prompt)); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return taskapi.Snapshot{}, err
	}
	return task.snapshot(), nil
}

func resolveSpawnAgent(session session.Session, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || strings.EqualFold(requested, "self") {
		return "self", nil
	}
	return requested, nil
}

func (r *Runtime) buildSideSubagentPromptContext(
	ctx context.Context,
	activeSession session.Session,
	ref session.SessionRef,
	target string,
	prompt string,
	sinceSeq int,
) (string, int) {
	if r == nil || r.sessions == nil {
		return "", 0
	}
	shared := r.buildSharedDialogueDelta(ctx, ref, sinceSeq)
	var b strings.Builder
	b.WriteString("Caelis shared public dialogue context. Use this as background for the current side-agent request; do not treat it as a fresh session.\n")
	if sessionID := strings.TrimSpace(activeSession.SessionID); sessionID != "" {
		b.WriteString("session_id: ")
		b.WriteString(sessionID)
		b.WriteString("\n")
	}
	if cwd := strings.TrimSpace(activeSession.CWD); cwd != "" {
		b.WriteString("workspace: ")
		b.WriteString(cwd)
		b.WriteString("\n")
	}
	if target = strings.TrimSpace(target); target != "" {
		b.WriteString("target_agent: ")
		b.WriteString(target)
		b.WriteString("\n")
	}
	appendSharedDialogueDelta(&b, shared)
	return strings.TrimSpace(b.String()), shared.Checkpoint
}

func subagentPromptWithContext(prelude string, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	prelude = strings.TrimSpace(prelude)
	if prelude == "" {
		return prompt
	}
	if prompt == "" {
		return prelude
	}
	return prelude + "\n\nCurrent request:\n" + prompt
}

func subagentInteraction(parentTool string, source string) string {
	if strings.EqualFold(strings.TrimSpace(parentTool), "slash") || isSlashSubagentSource(source) {
		return "side"
	}
	return "delegated"
}

func isSlashSubagentSource(source string) bool {
	source = strings.ToLower(strings.TrimSpace(source))
	return source == "slash" || source == "slash_agent" || strings.HasPrefix(source, "slash_")
}

func isSideSubagentTask(task *subagentTask) bool {
	if task == nil {
		return false
	}
	if strings.EqualFold(taskStringValue(task.metadata["interaction"]), "side") {
		return true
	}
	return isSlashSubagentSource(taskStringValue(task.metadata["source"]))
}

func subagentParticipantRole(task *subagentTask) session.ParticipantRole {
	if isSideSubagentTask(task) {
		return session.ParticipantRoleSidecar
	}
	return session.ParticipantRoleDelegated
}

func (tm *taskRuntime) authorizeSubagentControl(task *subagentTask, source string, action string) error {
	source = strings.ToLower(strings.TrimSpace(source))
	switch source {
	case "agent_tool":
		if isSideSubagentTask(task) {
			return fmt.Errorf("agent-sdk/runtime: TASK %s cannot control user-created side subagent %q", strings.TrimSpace(action), task.handle)
		}
	case "user_side_agent":
		if !isSideSubagentTask(task) {
			return fmt.Errorf("agent-sdk/runtime: @handle can only target side subagents created with /<agent>")
		}
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
	contextPrelude, _ := r.buildSideSubagentPromptContext(ctx, activeSession, ref, strings.TrimSpace(agent), strings.TrimSpace(prompt), 0)
	snapshot, err := r.tasks.StartSubagent(ctx, activeSession, ref, r.subagents, taskapi.SubagentStartRequest{
		Agent:          strings.TrimSpace(agent),
		Prompt:         strings.TrimSpace(prompt),
		ContextPrelude: contextPrelude,
		ParentTool:     "slash",
		Source:         firstNonEmpty(strings.TrimSpace(source), "slash_agent"),
		Mode:           strings.TrimSpace(r.defaultPolicyMode),
		ApprovalMode:   approvalMode,
		Approval:       newSubagentApprovalRequester(opts.ApprovalRequester, activeSession, ref),
	})
	if err != nil || !snapshot.Running {
		return snapshot, err
	}
	return r.tasks.Wait(ctx, ref, taskapi.ControlRequest{
		TaskID: snapshot.Ref.TaskID,
		Yield:  2 * time.Second,
		Source: "ui_side_agent",
	})
}

func (r *Runtime) ContinueSubagentByHandle(
	ctx context.Context,
	ref session.SessionRef,
	handle string,
	prompt string,
	yield time.Duration,
) (taskapi.Snapshot, error) {
	if r == nil || r.sessions == nil || r.tasks == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: runtime is unavailable")
	}
	ref = session.NormalizeSessionRef(ref)
	activeSession, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	taskID, binding, ok := subagentTaskIDForHandle(activeSession, handle)
	if !ok {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: subagent handle %q not found", strings.TrimSpace(handle))
	}
	contextPrelude, _ := r.buildSideSubagentPromptContext(ctx, activeSession, ref, strings.TrimSpace(handle), strings.TrimSpace(prompt), binding.ContextSyncSeq)
	return r.tasks.Write(ctx, ref, taskapi.ControlRequest{
		TaskID:         taskID,
		Input:          strings.TrimSpace(prompt),
		Yield:          yield,
		Source:         "user_side_agent",
		ContextPrelude: contextPrelude,
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
		TaskID: taskID,
		Yield:  yield,
		Source: "ui_side_agent",
	})
}

func subagentTaskIDForHandle(activeSession session.Session, handle string) (string, session.ParticipantBinding, bool) {
	handle = normalizeSubagentHandle(handle)
	if handle == "" {
		return "", session.ParticipantBinding{}, false
	}
	for _, participant := range activeSession.Participants {
		if participant.Kind != session.ParticipantKindSubagent || participant.Role != session.ParticipantRoleSidecar {
			continue
		}
		if normalizeSubagentHandle(participant.Label) != handle {
			continue
		}
		taskID := strings.TrimSpace(participant.DelegationID)
		return taskID, session.CloneParticipantBinding(participant), taskID != ""
	}
	return "", session.ParticipantBinding{}, false
}

func (tm *taskRuntime) waitSubagent(ctx context.Context, task *subagentTask, yield time.Duration) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	if task.runner == nil {
		task.mu.Lock()
		snapshot := task.snapshot()
		task.mu.Unlock()
		return snapshot, nil
	}
	if !task.isRunning() {
		task.mu.Lock()
		snapshot := task.snapshot()
		task.mu.Unlock()
		return snapshot, nil
	}
	result, err := task.runner.Wait(ctx, delegation.CloneAnchor(task.anchor), int(yield/time.Millisecond))
	if err != nil {
		if task.isRunning() {
			return tm.interruptSubagentTask(ctx, task, "subagent session interrupted during recovery: "+strings.TrimSpace(err.Error()))
		}
		return taskapi.Snapshot{}, err
	}
	task.mu.Lock()
	task.applyResult(result)
	snapshot := task.snapshot()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return taskapi.Snapshot{}, err
	}
	if shouldDropInactiveSubagentTask(snapshot) {
		tm.mu.Lock()
		delete(tm.subagents, task.ref.TaskID)
		tm.mu.Unlock()
		_ = tm.updateSubagentParticipant(ctx, task, "updated")
	}
	return snapshot, nil
}

func (tm *taskRuntime) continueSubagent(ctx context.Context, task *subagentTask, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	prompt := strings.TrimSpace(req.Input)
	if prompt == "" {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: TASK write for SPAWN task %q requires a follow-up prompt", task.ref.TaskID)
	}
	task.mu.Lock()
	state := task.state
	running := task.running
	task.mu.Unlock()
	if running || state != taskapi.StateCompleted {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: SPAWN task %q is %s; use TASK wait until completed before TASK write", task.ref.TaskID, state)
	}
	if task.runner == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: SPAWN task %q cannot continue because its child session runner is unavailable", task.ref.TaskID)
	}
	if err := tm.authorizeSubagentControl(task, req.Source, "write"); err != nil {
		return taskapi.Snapshot{}, err
	}
	task.mu.Lock()
	previousStdout := task.stdout
	previousStderr := task.stderr
	previousStdoutCursor := task.stdoutCursor
	previousStderrCursor := task.stderrCursor
	previousStreamFrames := append([]stream.Frame(nil), task.streamFrames...)
	previousTurnSeq := task.turnSeq
	task.turnSeq++
	if task.turnSeq <= 0 {
		task.turnSeq = 1
	}
	task.stdout = ""
	task.stderr = ""
	task.stdoutCursor = 0
	task.stderrCursor = 0
	task.streamFrames = nil
	if task.metadata != nil {
		delete(task.metadata, "final_event_persisted")
	}
	task.mu.Unlock()
	childPrompt := subagentPromptWithContext(req.ContextPrelude, prompt)
	result, err := task.runner.Continue(ctx, delegation.CloneAnchor(task.anchor), delegation.ContinueRequest{
		Agent:       task.agent,
		Prompt:      childPrompt,
		YieldTimeMS: int(req.Yield / time.Millisecond),
	})
	if err != nil {
		task.mu.Lock()
		if task.stdout == "" && task.stderr == "" {
			task.stdout = previousStdout
			task.stderr = previousStderr
			task.stdoutCursor = previousStdoutCursor
			task.stderrCursor = previousStderrCursor
			task.streamFrames = previousStreamFrames
			task.turnSeq = previousTurnSeq
		}
		task.mu.Unlock()
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentUserEvent(ctx, task, prompt); err != nil {
		return taskapi.Snapshot{}, err
	}
	task.mu.Lock()
	task.prompt = prompt
	task.applyResult(result)
	task.seedStreamFromResult(result)
	snapshot := task.snapshot()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	if err := tm.appendSideSubagentFinalEvent(ctx, task); err != nil {
		return taskapi.Snapshot{}, err
	}
	if shouldDropInactiveSubagentTask(snapshot) {
		tm.mu.Lock()
		delete(tm.subagents, task.ref.TaskID)
		tm.mu.Unlock()
	}
	_ = tm.updateSubagentParticipant(ctx, task, "updated")
	return snapshot, nil
}

func (tm *taskRuntime) cancelSubagent(ctx context.Context, task *subagentTask) (taskapi.Snapshot, error) {
	if task == nil {
		return taskapi.Snapshot{}, fmt.Errorf("agent-sdk/runtime: task is required")
	}
	if task.runner == nil {
		task.mu.Lock()
		task.state = taskapi.StateCancelled
		task.running = false
		snapshot := task.snapshot()
		entry := task.entrySnapshot(tm.runtime.now())
		task.mu.Unlock()
		if err := tm.persistTaskEntry(ctx, entry); err != nil {
			return taskapi.Snapshot{}, err
		}
		return snapshot, nil
	}
	if err := task.runner.Cancel(ctx, delegation.CloneAnchor(task.anchor)); err != nil {
		return taskapi.Snapshot{}, err
	}
	result, err := task.runner.Wait(ctx, delegation.CloneAnchor(task.anchor), 10)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	task.mu.Lock()
	task.applyResult(result)
	task.state = taskapi.StateCancelled
	task.running = false
	snapshot := task.snapshot()
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return taskapi.Snapshot{}, err
	}
	tm.mu.Lock()
	delete(tm.subagents, task.ref.TaskID)
	tm.mu.Unlock()
	_ = tm.updateSubagentParticipant(ctx, task, "detached")
	return snapshot, nil
}

func (tm *taskRuntime) lookupSubagent(ctx context.Context, ref session.SessionRef, taskID string) (*subagentTask, error) {
	lookupID := strings.TrimSpace(taskID)
	tm.mu.RLock()
	task, ok := tm.subagents[lookupID]
	if !ok {
		handle := normalizeSubagentHandle(lookupID)
		var matches []*subagentTask
		for _, candidate := range tm.subagents {
			if candidate == nil {
				continue
			}
			if strings.TrimSpace(candidate.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
				continue
			}
			if normalizeSubagentHandle(candidate.handle) == handle || normalizeSubagentHandle(taskStringValue(candidate.metadata["handle"])) == handle {
				matches = append(matches, candidate)
			}
		}
		if len(matches) == 1 {
			task = matches[0]
			ok = true
		} else if len(matches) > 1 {
			tm.mu.RUnlock()
			return nil, fmt.Errorf("agent-sdk/runtime: subagent handle %q is ambiguous; use the task id", lookupID)
		}
	}
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
		entry, err = tm.lookupStoredSubagentByHandle(ctx, ref, lookupID)
	}
	if err != nil || entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	if strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) || entry.Kind != taskapi.KindSubagent {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry = tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	rehydrated := tm.rehydrateSubagentTask(entry)
	tm.mu.Lock()
	tm.subagents[rehydrated.ref.TaskID] = rehydrated
	tm.rememberSubagentHandleLocked(rehydrated.sessionRef.SessionID, rehydrated.handle)
	tm.mu.Unlock()
	return rehydrated, nil
}

func (tm *taskRuntime) lookupStoredSubagentByHandle(ctx context.Context, ref session.SessionRef, handle string) (*taskapi.Entry, error) {
	if tm == nil || tm.store == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", handle)
	}
	handle = normalizeSubagentHandle(handle)
	if handle == "" {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", handle)
	}
	entry, err := tm.store.GetSessionTaskByHandle(ctx, ref, taskapi.KindSubagent, handle)
	if err != nil {
		return nil, err
	}
	return taskapi.CloneEntry(entry), nil
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
	if next.Result == nil {
		next.Result = map[string]any{}
	}
	next.Result["state"] = string(taskapi.StateInterrupted)
	next.Result["error"] = reason
	next.Result["result"] = reason
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
	snapshot := task.snapshot()
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
	agent := taskSpecString(entry.Spec, "agent")
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
			Agent:     agent,
			AgentID:   taskSpecString(entry.Spec, "agent_id"),
		},
		runner:    tm.runtime.subagents,
		agent:     agent,
		handle:    firstNonEmpty(taskSpecString(entry.Spec, "handle"), taskStringValue(entry.Metadata["handle"])),
		title:     strings.TrimSpace(entry.Title),
		prompt:    taskSpecString(entry.Spec, "prompt"),
		createdAt: entry.CreatedAt,
		revision:  entry.Revision,
		lease:     taskapi.CloneLease(entry.Lease),
		state:     entry.State,
		running:   entry.Running,
		turnSeq:   taskTurnSeqFromSpec(entry.Spec),
		result:    session.CloneState(entry.Result),
		metadata:  session.CloneState(entry.Metadata),
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

func (tm *taskRuntime) attachSubagentParticipant(ctx context.Context, activeSession session.Session, task *subagentTask, parentCall string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil {
		return nil
	}
	handle := strings.TrimSpace(task.handle)
	if handle == "" {
		handle = tm.reserveSubagentHandle(activeSession, task.sessionRef, task.agent)
		task.handle = handle
	}
	mention := "@" + strings.TrimPrefix(handle, "@")
	role := subagentParticipantRole(task)
	_, err := tm.runtime.sessions.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: task.sessionRef,
		Binding: session.ParticipantBinding{
			ID:            strings.TrimSpace(task.anchor.AgentID),
			Kind:          session.ParticipantKindSubagent,
			Role:          role,
			AgentName:     strings.TrimSpace(task.agent),
			Label:         mention,
			SessionID:     strings.TrimSpace(task.anchor.SessionID),
			Source:        firstNonEmpty(strings.TrimSpace(taskStringValue(task.metadata["source"])), "agent_spawn"),
			ParentTurnID:  strings.TrimSpace(parentCall),
			DelegationID:  strings.TrimSpace(task.ref.TaskID),
			AttachedAt:    tm.runtime.now(),
			ControllerRef: strings.TrimSpace(activeSession.Controller.EpochID),
		},
	})
	if err != nil {
		return err
	}
	_, err = tm.runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: task.sessionRef,
		Event: &session.Event{
			Type:       session.EventTypeParticipant,
			Visibility: session.VisibilityUIOnly,
			Time:       tm.runtime.now(),
			Actor: session.ActorRef{
				Kind: session.ActorKindSystem,
				ID:   "spawn",
				Name: "spawn",
			},
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodParticipantUpdate,
				Update: &session.ProtocolUpdate{SessionUpdate: "attached"},
			},
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:           strings.TrimSpace(task.anchor.AgentID),
					Kind:         session.ParticipantKindSubagent,
					Role:         role,
					DelegationID: strings.TrimSpace(task.ref.TaskID),
				},
			},
			Meta: map[string]any{
				"task_id":    task.ref.TaskID,
				"agent":      task.agent,
				"agent_id":   task.anchor.AgentID,
				"handle":     handle,
				"mention":    mention,
				"session_id": task.anchor.SessionID,
				"state":      string(task.state),
			},
		},
	})
	return err
}

func (tm *taskRuntime) updateSubagentParticipant(ctx context.Context, task *subagentTask, action string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil {
		return nil
	}
	role := subagentParticipantRole(task)
	_, err := tm.runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: task.sessionRef,
		Event: &session.Event{
			Type:       session.EventTypeParticipant,
			Visibility: session.VisibilityUIOnly,
			Time:       tm.runtime.now(),
			Actor: session.ActorRef{
				Kind: session.ActorKindSystem,
				ID:   "spawn",
				Name: "spawn",
			},
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodParticipantUpdate,
				Update: &session.ProtocolUpdate{SessionUpdate: strings.TrimSpace(action)},
			},
			Scope: &session.EventScope{
				Participant: session.ParticipantRef{
					ID:           strings.TrimSpace(task.anchor.AgentID),
					Kind:         session.ParticipantKindSubagent,
					Role:         role,
					DelegationID: strings.TrimSpace(task.ref.TaskID),
				},
			},
			Meta: map[string]any{
				"task_id":        task.ref.TaskID,
				"agent":          task.agent,
				"agent_id":       task.anchor.AgentID,
				"handle":         task.handle,
				"mention":        "@" + strings.TrimPrefix(task.handle, "@"),
				"session_id":     task.anchor.SessionID,
				"state":          string(task.state),
				"output_preview": taskRawStringValue(task.result["output_preview"]),
			},
		},
	})
	return err
}

func (tm *taskRuntime) appendSideSubagentUserEvent(ctx context.Context, task *subagentTask, prompt string) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil || !isSideSubagentTask(task) {
		return nil
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	role := subagentParticipantRole(task)
	message := model.NewTextMessage(model.RoleUser, prompt)
	_, err := tm.runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: task.sessionRef,
		Event: &session.Event{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Time:       tm.runtime.now(),
			Actor:      session.ActorRef{Kind: session.ActorKindUser, Name: "user"},
			Scope: &session.EventScope{
				TurnID: subagentTurnID(task.ref.TaskID, task.turnSeq),
				Source: firstNonEmpty(taskStringValue(task.metadata["source"]), "slash_agent"),
				Participant: session.ParticipantRef{
					ID:           strings.TrimSpace(task.anchor.AgentID),
					Kind:         session.ParticipantKindSubagent,
					Role:         role,
					DelegationID: strings.TrimSpace(task.ref.TaskID),
				},
			},
			Message: &message,
			Text:    prompt,
			Meta: map[string]any{
				"handle":  strings.TrimSpace(task.handle),
				"mention": "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
				"agent":   strings.TrimSpace(task.agent),
			},
		},
	})
	return err
}

func (tm *taskRuntime) appendSideSubagentFinalEvent(ctx context.Context, task *subagentTask) error {
	if tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || task == nil || !isSideSubagentTask(task) {
		return nil
	}
	task.mu.Lock()
	if task.running || task.state != taskapi.StateCompleted || strings.EqualFold(taskStringValue(task.metadata["final_event_persisted"]), "true") {
		task.mu.Unlock()
		return nil
	}
	text := firstNonBlankTaskOutput(taskRawStringValue(task.result["final_message"]), taskRawStringValue(task.result["result"]))
	if !taskOutputHasNonBlankLine(text) && subagentFramesContainAssistantText(task.streamFrames) {
		text = compactFinalOutput(task.stdout, task.stderr)
	}
	if !taskOutputHasNonBlankLine(text) {
		task.mu.Unlock()
		return nil
	}
	role := subagentParticipantRole(task)
	message := model.NewTextMessage(model.RoleAssistant, text)
	event := &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Time:       tm.runtime.now(),
		Actor: session.ActorRef{
			Kind: session.ActorKindParticipant,
			ID:   strings.TrimSpace(task.anchor.AgentID),
			Role: string(role),
			Name: "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
		},
		Scope: &session.EventScope{
			TurnID: subagentTurnID(task.ref.TaskID, task.turnSeq),
			Source: firstNonEmpty(taskStringValue(task.metadata["source"]), "slash_agent"),
			Participant: session.ParticipantRef{
				ID:           strings.TrimSpace(task.anchor.AgentID),
				Kind:         session.ParticipantKindSubagent,
				Role:         role,
				DelegationID: strings.TrimSpace(task.ref.TaskID),
			},
		},
		Message: &message,
		Text:    text,
		Meta: map[string]any{
			"handle":  strings.TrimSpace(task.handle),
			"mention": "@" + strings.TrimPrefix(strings.TrimSpace(task.handle), "@"),
			"agent":   strings.TrimSpace(task.agent),
		},
	}
	task.mu.Unlock()

	if _, err := tm.runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: task.sessionRef, Event: event}); err != nil {
		return err
	}
	if err := tm.runtime.updateParticipantContextCheckpoint(ctx, task.sessionRef, strings.TrimSpace(task.anchor.AgentID)); err != nil {
		return err
	}
	task.mu.Lock()
	if task.metadata == nil {
		task.metadata = map[string]any{}
	}
	task.metadata["final_event_persisted"] = "true"
	entry := task.entrySnapshot(tm.runtime.now())
	task.mu.Unlock()
	return tm.persistTaskEntry(ctx, entry)
}

func subagentTerminalID(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ""
	}
	return "subagent-" + taskID
}

func allocateSubagentHandle(activeSession session.Session, agent string) string {
	return agenthandle.Allocate(subagentHandlesFromSession(activeSession), agent)
}

func (tm *taskRuntime) reserveSubagentHandle(activeSession session.Session, ref session.SessionRef, agent string) string {
	used := subagentHandlesFromSession(activeSession)
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, task := range tm.subagents {
		if task == nil || strings.TrimSpace(task.sessionRef.SessionID) != sessionID {
			continue
		}
		for _, handle := range []string{task.handle, taskStringValue(task.metadata["handle"]), taskStringValue(task.result["handle"])} {
			if normalized := normalizeSubagentHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	if sessionID != "" {
		for handle := range tm.handles[sessionID] {
			if normalized := normalizeSubagentHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	handle := agenthandle.Allocate(used, agent)
	tm.rememberSubagentHandleLocked(sessionID, handle)
	return handle
}

func (tm *taskRuntime) rememberSubagentHandleLocked(sessionID string, handle string) {
	sessionID = strings.TrimSpace(sessionID)
	handle = normalizeSubagentHandle(handle)
	if sessionID == "" || handle == "" {
		return
	}
	if tm.handles == nil {
		tm.handles = map[string]map[string]struct{}{}
	}
	if tm.handles[sessionID] == nil {
		tm.handles[sessionID] = map[string]struct{}{}
	}
	tm.handles[sessionID][handle] = struct{}{}
}

func subagentHandlesFromSession(activeSession session.Session) map[string]struct{} {
	used := map[string]struct{}{}
	for _, participant := range activeSession.Participants {
		handle := normalizeSubagentHandle(participant.Label)
		if handle != "" {
			used[handle] = struct{}{}
		}
	}
	return used
}

func normalizeSubagentHandle(value string) string {
	return taskapi.NormalizeHandle(value)
}

func (t *subagentTask) applyResult(result delegation.Result) {
	if t == nil {
		return
	}
	t.state = taskStateFromDelegation(result.State)
	t.running = result.State == delegation.StateRunning
	if t.result == nil {
		t.result = map[string]any{}
	}
	if t.metadata == nil {
		t.metadata = map[string]any{}
	}
	t.metadata["task_id"] = t.handle
	t.metadata["internal_task_id"] = t.ref.TaskID
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
	t.result["task_id"] = t.handle
	t.result["handle"] = t.handle
	t.result["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.result["agent"] = t.agent
	t.result["state"] = string(t.state)
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
	if t.result == nil {
		t.result = map[string]any{}
	}
	if t.metadata == nil {
		t.metadata = map[string]any{}
	}
	t.result["state"] = string(taskapi.StateInterrupted)
	t.result["error"] = reason
	t.result["result"] = reason
	t.result["output_preview"] = reason
	t.result["task_id"] = t.handle
	t.result["handle"] = t.handle
	t.result["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.result["agent"] = t.agent
	t.metadata["state"] = string(taskapi.StateInterrupted)
	t.metadata["interrupted_reason"] = reason
	t.metadata["task_id"] = t.handle
	t.metadata["internal_task_id"] = t.ref.TaskID
	t.metadata["task_kind"] = string(taskapi.KindSubagent)
	t.metadata["agent"] = t.agent
	t.metadata["agent_id"] = t.anchor.AgentID
	t.metadata["handle"] = t.handle
	t.metadata["mention"] = "@" + strings.TrimPrefix(t.handle, "@")
	t.metadata["prompt"] = t.prompt
	t.metadata["session_id"] = t.anchor.SessionID
	t.metadata["terminal_id"] = t.ref.TerminalID
}

func (t *subagentTask) snapshot() taskapi.Snapshot {
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
		Revision:       t.revision,
		Kind:           taskapi.KindSubagent,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  !t.running && t.state == taskapi.StateCompleted,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      time.Now(),
		Lease:          taskapi.CloneLease(t.lease),
		StdoutCursor:   t.stdoutCursor,
		StderrCursor:   t.stderrCursor,
		EventCursor:    int64(len(t.streamFrames)),
		Result:         result,
		Metadata:       metadata,
	})
}

func (t *subagentTask) entrySnapshot(now time.Time) *taskapi.Entry {
	if t == nil {
		return nil
	}
	return &taskapi.Entry{
		TaskID:         t.ref.TaskID,
		Revision:       t.revision,
		Kind:           taskapi.KindSubagent,
		Session:        t.sessionRef,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  !t.running && t.state == taskapi.StateCompleted,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      now,
		Lease:          taskapi.CloneLease(t.lease),
		Spec: map[string]any{
			"agent":       t.agent,
			"prompt":      t.prompt,
			"session_id":  t.anchor.SessionID,
			"agent_id":    t.anchor.AgentID,
			"handle":      t.handle,
			"terminal_id": t.ref.TerminalID,
			"turn_seq":    t.turnSeq,
			"turn_id":     subagentTurnID(t.ref.TaskID, t.turnSeq),
		},
		Result:   subagentTaskEntryResult(t.result, t.running),
		Metadata: session.CloneState(t.metadata),
	}
}

func subagentTaskEntryResult(result map[string]any, running bool) map[string]any {
	mode := taskapi.ResultPersistenceCanonical
	if !running {
		mode = taskapi.ResultPersistenceDeferred
	}
	return taskapi.SanitizeResultForPersistence(result, mode)
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
