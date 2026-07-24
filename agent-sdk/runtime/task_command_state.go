package runtime

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func (t *commandTask) snapshotLocked(status sandbox.SessionStatus) taskapi.Snapshot {
	return taskapi.CloneSnapshot(taskapi.Snapshot{
		Ref:            t.ref,
		Handle:         t.handle,
		Revision:       t.revision,
		Kind:           taskapi.KindCommand,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  status.SupportsInput,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      status.UpdatedAt,
		Lease:          taskapi.CloneLease(t.lease),
		StdoutCursor:   t.outputState.backend.stdout,
		StderrCursor:   t.outputState.backend.stderr,
		Result:         session.CloneState(t.result),
		Metadata:       session.CloneState(t.metadata),
		Terminal:       status.Terminal,
	})
}

func (tm *taskRuntime) rehydrateCommandTask(entry *taskapi.Entry) (*commandTask, error) {
	if entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task entry is required")
	}
	seededOutput, seededFromResult := rehydratedCommandOutput(entry)
	checkpoint := parseCommandOutputCheckpoint(entry)
	restoredCheckpoint := checkpoint
	if !restoredCheckpoint.resumable() {
		restoredCheckpoint.backend = commandBackendCursor{}
		restoredCheckpoint.available = false
	}
	task := &commandTask{
		ref: taskapi.Ref{
			TaskID:     strings.TrimSpace(entry.TaskID),
			SessionID:  strings.TrimSpace(entry.Terminal.SessionID),
			TerminalID: strings.TrimSpace(entry.Terminal.TerminalID),
		},
		handle:        firstNonEmpty(entry.Handle, taskSpecString(entry.Spec, "handle"), taskStringValue(entry.Metadata["handle"]), strings.TrimSpace(entry.TaskID)),
		sessionRef:    session.NormalizeSessionRef(entry.Session),
		command:       taskSpecString(entry.Spec, "command"),
		workdir:       taskSpecString(entry.Spec, "workdir"),
		parentCall:    taskSpecString(entry.Spec, "parent_call"),
		requestDigest: taskSpecString(entry.Spec, "command_request_digest"),
		title:         strings.TrimSpace(entry.Title),
		createdAt:     entry.CreatedAt,
		revision:      entry.Revision,
		lease:         taskapi.CloneLease(entry.Lease),
		state:         entry.State,
		running:       entry.Running,
		output:        seededOutput,
		outputState: commandOutputState{
			backend:    restoredCheckpoint.backend,
			checkpoint: restoredCheckpoint,
			resume:     restoredCheckpoint,
			callback:   seededFromResult || !entry.Running,
		},
		result:   session.CloneState(entry.Result),
		metadata: session.CloneState(entry.Metadata),
	}
	if eventCursor, ok := taskInt64Value(entry.Metadata[commandStreamEventCursorMeta]); ok && eventCursor >= 0 {
		if eventCursor == math.MaxInt64 {
			return nil, fmt.Errorf("agent-sdk/runtime: command stream event cursor is exhausted")
		}
		task.streamEventBase = eventCursor
	}
	if task.parentCall == "" {
		task.parentCall = taskStringValue(entry.Metadata["parent_call"])
	}
	if task.requestDigest == "" {
		task.requestDigest = taskStringValue(entry.Metadata["command_request_digest"])
	}
	if restoredCheckpoint.resumable() {
		task.outputState.frontier.model = restoredCheckpoint.model
	}
	if restoredCheckpoint.resumable() && task.output == "" {
		task.outputState.frontier.base = restoredCheckpoint.output
	}
	if !entry.Running && stream.IsTerminalState(string(entry.State)) {
		replayCursor, ok := taskInt64Value(entry.Metadata[commandStreamOutputCursorMeta])
		if ok && replayCursor >= 0 {
			// Canonical Result text is the terminal model/final-text view, not a
			// byte-for-byte durable copy of the live interleaved stream. Keep
			// the absolute stream cursor but expose the unavailable byte range
			// as truncated instead of mapping different text onto old cursors.
			task.output = ""
			task.outputState.frontier.base = replayCursor
			task.outputState.frontier.model = replayCursor
		} else {
			replayCursor = int64(len([]byte(task.output)))
			task.outputState.frontier.base = 0
			task.outputState.frontier.model = replayCursor
		}
	}
	phase := taskStringValue(entry.Metadata["command_phase"])
	if phase == commandPhaseIntent {
		return task, nil
	}
	if strings.TrimSpace(entry.Terminal.SessionID) == "" && (phase == commandPhaseEffectClaimed || phase == commandPhaseUnknown) {
		// A process restart erased the only live observation handle. The external
		// effect may have happened, but there is no longer an asynchronous producer
		// that can prove progress. Expose an explicit terminal unknown outcome.
		task.state = taskapi.StateUnknownOutcome
		task.running = false
		if task.metadata == nil {
			task.metadata = map[string]any{}
		}
		task.metadata["state"] = string(taskapi.StateUnknownOutcome)
		task.metadata["running"] = false
		task.result = map[string]any{
			"state": string(taskapi.StateUnknownOutcome),
			"error": "command effect outcome is unavailable after process restart",
		}
		return task, nil
	}
	if !entry.Running {
		task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
		return task, nil
	}
	backend := entry.Terminal.Backend
	if backend == "" {
		backend = sandbox.BackendHost
	}
	tm.mu.RLock()
	runtime := tm.backends[backend]
	tm.mu.RUnlock()
	if runtime == nil {
		if commandUnknownWhileRunningPhase(phase) {
			return task, nil
		}
		interruptRehydratedCommandTask(task, entry)
		return task, nil
	}
	var (
		session sandbox.Session
		err     error
	)
	if opener, ok := runtime.(sandboxSessionRefOpener); ok && opener != nil {
		session, err = opener.OpenSessionRef(sandbox.SessionRef{
			Backend:   backend,
			SessionID: strings.TrimSpace(entry.Terminal.SessionID),
		})
	} else {
		session, err = runtime.OpenSession(strings.TrimSpace(entry.Terminal.SessionID))
	}
	if err != nil {
		if commandUnknownWhileRunningPhase(phase) {
			return task, nil
		}
		interruptRehydratedCommandTask(task, entry)
		return task, nil
	}
	task.session = session
	return task, nil
}

func commandOutputCheckpointState(metadata map[string]any) (available bool, coherent bool, recoveryGap bool) {
	if metadata == nil {
		return false, false, false
	}
	coherent, _ = metadata["output_checkpoint_coherent"].(bool)
	recoveryGap, _ = metadata["output_recovery_gap"].(bool)
	if recoveryGap {
		coherent = false
	}
	available, _ = metadata["output_checkpoint_available"].(bool)
	// Pre-field entries with a coherent checkpoint remain resumable.
	available = available || coherent || recoveryGap
	return available, coherent, recoveryGap
}

// parseCommandOutputCheckpoint is the only durable decoder for command output
// recovery state. It keeps the backend, presentation, and model markers in one
// value so rehydrate and live adopt cannot apply different validity rules.
func parseCommandOutputCheckpoint(entry *taskapi.Entry) commandOutputCheckpoint {
	if entry == nil {
		return commandOutputCheckpoint{}
	}
	available, coherent, recoveryGap := commandOutputCheckpointState(entry.Metadata)
	output, outputKnown := taskInt64Value(entry.Metadata["output_cursor"])
	modelCursor, modelKnown := taskInt64Value(entry.Metadata["model_output_cursor"])
	if !outputKnown && !modelKnown && entry.StdoutCursor == 0 && entry.StderrCursor == 0 {
		output, modelCursor = 0, 0
		outputKnown, modelKnown = true, true
	}
	return commandOutputCheckpoint{
		backend: commandBackendCursor{
			stdout: max(entry.StdoutCursor, 0),
			stderr: max(entry.StderrCursor, 0),
		},
		output:    output,
		model:     modelCursor,
		available: available && outputKnown && modelKnown,
		coherent:  coherent && !recoveryGap,
		gap:       recoveryGap,
	}
}

func (state *commandOutputState) applyDurableCheckpoint(checkpoint commandOutputCheckpoint) {
	if state == nil || !checkpoint.resumable() {
		return
	}
	state.backend.advance(checkpoint.backend)
	state.checkpoint.advance(checkpoint)
	state.resume.advance(checkpoint)
}

func interruptRehydratedCommandTask(task *commandTask, entry *taskapi.Entry) {
	if task == nil || entry == nil {
		return
	}
	task.session = completedTaskSession{entry: taskapi.CloneEntry(entry)}
	task.running = false
	task.state = taskapi.StateInterrupted
	if task.result == nil {
		task.result = map[string]any{}
	}
	task.result["state"] = string(taskapi.StateInterrupted)
	task.result["error"] = "task interrupted during resume"
	task.result["result"] = "task interrupted during resume"
}

func rehydratedCommandOutput(entry *taskapi.Entry) (string, bool) {
	if entry == nil || entry.Result == nil {
		return "", false
	}
	text := taskRawStringValue(entry.Result["result"])
	if text == "" {
		return "", false
	}
	if strings.TrimSpace(text) == noOutputPlaceholder && entry.StdoutCursor == 0 && entry.StderrCursor == 0 {
		return "", true
	}
	return text, true
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}

func (t *commandTask) entrySnapshot(now time.Time) *taskapi.Entry {
	if t == nil {
		return nil
	}
	var terminal sandbox.TerminalRef
	if t.session != nil {
		terminal = t.session.Terminal()
	}
	metadataSource := session.CloneState(t.metadata)
	if metadataSource == nil {
		metadataSource = map[string]any{}
	}
	metadataSource["output_checkpoint_available"] = t.outputState.resume.available
	metadataSource["output_checkpoint_coherent"] = t.outputState.resume.coherent
	metadataSource["output_recovery_gap"] = t.outputState.resume.gap
	replayCursor := t.outputCursorLocked()
	if observedCursor, ok := taskInt64Value(metadataSource["output_cursor"]); ok {
		replayCursor = max(replayCursor, observedCursor)
	}
	metadataSource[commandStreamOutputCursorMeta] = replayCursor
	metadataSource[commandStreamEventCursorMeta] = t.commandStreamEventCursorLocked()
	if t.outputState.resume.available && (t.outputState.resume.coherent || t.outputState.resume.gap) {
		metadataSource["output_cursor"] = t.outputState.resume.output
		metadataSource["model_output_cursor"] = t.outputState.resume.model
	}
	metadata := commandTaskEntryMetadata(metadataSource, t.running)
	metadata["parent_call"] = t.parentCall
	metadata["command_request_digest"] = t.requestDigest
	stdoutCursor, stderrCursor := t.outputState.resume.backend.stdout, t.outputState.resume.backend.stderr
	if !t.outputState.resume.available || (!t.outputState.resume.coherent && !t.outputState.resume.gap) {
		stdoutCursor = 0
		stderrCursor = 0
	}
	return &taskapi.Entry{
		TaskID:         t.ref.TaskID,
		Handle:         t.handle,
		Revision:       t.revision,
		Kind:           taskapi.KindCommand,
		Session:        t.sessionRef,
		Title:          t.title,
		State:          t.state,
		Running:        t.running,
		SupportsInput:  true,
		SupportsCancel: true,
		CreatedAt:      t.createdAt,
		UpdatedAt:      now,
		Lease:          taskapi.CloneLease(t.lease),
		StdoutCursor:   stdoutCursor,
		StderrCursor:   stderrCursor,
		Spec: map[string]any{
			"handle":                 t.handle,
			"command":                t.command,
			"workdir":                t.workdir,
			"session_id":             t.ref.SessionID,
			"parent_call":            t.parentCall,
			"command_request_digest": t.requestDigest,
		},
		Result:   commandTaskEntryResult(t.result, t.running),
		Metadata: metadata,
		Terminal: terminal,
	}
}

func (t *commandTask) commandOutcomeUnattached() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	phase := taskStringValue(t.metadata["command_phase"])
	return strings.TrimSpace(t.ref.SessionID) == "" && (phase == commandPhaseEffectClaimed || phase == commandPhaseUnknown)
}

func (t *commandTask) snapshotWithoutSession(now time.Time) taskapi.Snapshot {
	if t == nil {
		return taskapi.Snapshot{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.snapshotLocked(sandbox.SessionStatus{Running: t.running, SupportsInput: false, UpdatedAt: now})
}

func commandTaskEntryResult(result map[string]any, running bool) map[string]any {
	mode := taskapi.ResultPersistenceCanonical
	if !running {
		mode = taskapi.ResultPersistenceDeferred
	}
	return taskapi.SanitizeResultForPersistence(result, mode)
}

func commandTaskEntryMetadata(metadata map[string]any, running bool) map[string]any {
	out := session.CloneState(metadata)
	delete(out, "output_start_cursor")
	delete(out, "output_delta")
	if running {
		available, coherent, recoveryGap := commandOutputCheckpointState(out)
		if !available || (!coherent && !recoveryGap) {
			delete(out, "output_cursor")
			delete(out, "model_output_cursor")
		}
		return out
	}
	delete(out, "output_cursor")
	delete(out, "model_output_cursor")
	return out
}

func commandObservationSnapshot(snapshot taskapi.Snapshot, start int64, delta string) taskapi.Snapshot {
	snapshot.Metadata = session.CloneState(snapshot.Metadata)
	if snapshot.Metadata == nil {
		snapshot.Metadata = map[string]any{}
	}
	snapshot.Metadata["output_start_cursor"] = max(start, 0)
	if delta != "" {
		snapshot.Metadata["output_delta"] = delta
	}
	return snapshot
}
