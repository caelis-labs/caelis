package runtime

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

type streamService struct {
	tasks *taskRuntime
}

func newStreamService(tasks *taskRuntime) *streamService {
	return &streamService{tasks: tasks}
}

func (s *streamService) Read(ctx context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	ref := stream.NormalizeRef(req.Ref)
	cursor := stream.CloneCursor(req.Cursor)
	read, _, err := s.resolveReader(ctx, ref)
	if err != nil {
		return stream.Snapshot{}, err
	}
	return read(ctx, cursor)
}

type resolvedStreamReader func(context.Context, stream.Cursor) (stream.Snapshot, error)
type resolvedStreamAwaiter func(context.Context, stream.Cursor, bool) (stream.Snapshot, error)

// resolveReader pins one stream operation to the task instance it resolved.
// A completed task may leave the live registry before its canonical result is
// promoted from the deferred durable entry; re-resolving during that interval
// would close an active subscription against an empty reconstructed snapshot.
func (s *streamService) resolveReader(ctx context.Context, ref stream.Ref) (resolvedStreamReader, resolvedStreamAwaiter, error) {
	if err := stream.ValidateRef(ref); err != nil {
		return nil, nil, err
	}
	task, err := s.resolveTask(ctx, ref)
	if err == nil {
		return func(readCtx context.Context, cursor stream.Cursor) (stream.Snapshot, error) {
				return s.readCommand(readCtx, task, cursor)
			}, func(awaitCtx context.Context, cursor stream.Cursor, followContinue bool) (stream.Snapshot, error) {
				return s.awaitCommand(awaitCtx, task, cursor, followContinue)
			}, nil
	}
	subagent, subagentErr := s.resolveSubagent(ctx, ref)
	if subagentErr != nil {
		return nil, nil, subagentErr
	}
	return func(readCtx context.Context, cursor stream.Cursor) (stream.Snapshot, error) {
			return s.readSubagent(readCtx, subagent, cursor)
		}, func(awaitCtx context.Context, cursor stream.Cursor, followContinue bool) (stream.Snapshot, error) {
			return s.awaitSubagent(awaitCtx, subagent, cursor, followContinue)
		}, nil
}

func (s *streamService) readCommand(ctx context.Context, task *commandTask, cursor stream.Cursor) (stream.Snapshot, error) {
	commandSession := task.session
	hasExitStatus := commandSession != nil
	var (
		status sandbox.SessionStatus
		err    error
	)
	if commandSession != nil {
		status, err = commandSession.Status(ctx)
	} else {
		status, err = commandStatusWithoutSession(task)
	}
	if err != nil {
		return stream.Snapshot{}, err
	}
	task.outputReadMu.Lock()
	task.mu.Lock()
	task.ensureCommandOutputSeedLocked()
	stdoutCursor := task.outputState.backend.stdout
	stderrCursor := task.outputState.backend.stderr
	useReadOutput := commandSession != nil && !task.outputState.callback
	task.mu.Unlock()
	if useReadOutput {
		stdout, stderr, nextStdout, nextStderr, err := commandSession.ReadOutput(ctx, stdoutCursor, stderrCursor)
		if err != nil {
			task.outputReadMu.Unlock()
			return stream.Snapshot{}, err
		}
		task.mu.Lock()
		err = task.ingestRecoveredOutputLocked(
			stdout,
			stderr,
			stdoutCursor,
			stderrCursor,
			nextStdout,
			nextStderr,
			!status.Running,
		)
		task.mu.Unlock()
		if err != nil {
			task.outputReadMu.Unlock()
			return stream.Snapshot{}, err
		}
	}
	task.outputReadMu.Unlock()
	var result sandbox.CommandResult
	var resultErr error
	if commandSession != nil && !status.Running {
		result, resultErr = commandSession.Result(ctx)
	}
	task.mu.Lock()
	state := task.state
	if !stream.IsTerminalState(string(state)) {
		state = stateFromStatus(status)
	}
	task.state = state
	task.running = status.Running && !stream.IsTerminalState(string(state))
	outputCursor := task.outputCursorLocked()
	finalText := ""
	if !status.Running {
		finalText = terminalFinalText(task.output, result.Stdout, result.Stderr, resultErr)
		if !task.outputState.exact {
			task.reconcileFinalOutputLocked(terminalOutputText(task.output, result.Stdout, result.Stderr))
		}
		outputCursor = task.outputCursorLocked()
	}
	task.ensureCommandTerminalFrameLocked(state, status, hasExitStatus)
	truncatedBefore := int64(0)
	if cursor.Output < task.outputState.frontier.base {
		truncatedBefore = task.outputState.frontier.base
	}
	eventsTruncatedBefore := int64(0)
	if cursor.Events < task.streamEventBase {
		eventsTruncatedBefore = task.streamEventBase
	}
	eventCursor := task.streamEventBase
	if count := len(task.streamFrames); count > 0 {
		eventCursor = task.streamFrames[count-1].Cursor.Events
	}
	eventCursor = max(eventCursor, cursor.Events)
	running := status.Running && !stream.IsTerminalState(string(state))
	snap := stream.Snapshot{
		Ref: stream.Ref{
			SessionID:  strings.TrimSpace(task.sessionRef.SessionID),
			TaskID:     strings.TrimSpace(task.ref.TaskID),
			TerminalID: strings.TrimSpace(status.Terminal.TerminalID),
		},
		Cursor: stream.Cursor{
			Output: outputCursor,
			Events: eventCursor,
		},
		TruncatedBefore:       truncatedBefore,
		EventsTruncatedBefore: eventsTruncatedBefore,
		Running:               running,
		State:                 string(state),
		SupportsInput:         status.SupportsInput && !stream.IsTerminalState(string(state)),
		TerminalFramed:        task.streamTerminalFramed,
		StartedAt:             status.StartedAt,
		UpdatedAt:             status.UpdatedAt,
	}
	if !status.Running && stream.IsTerminalState(string(state)) {
		snap.FinalText = finalText
		if hasExitStatus {
			exitCode := status.ExitCode
			snap.ExitCode = &exitCode
		}
	}
	for _, retained := range task.streamFrames {
		eventAdvanced := retained.Cursor.Events > cursor.Events
		outputAdvanced := retained.Text != "" && retained.Cursor.Output > cursor.Output
		if !eventAdvanced && !outputAdvanced {
			continue
		}
		frame := stream.CloneFrame(retained)
		frame.Ref = snap.Ref
		// Retained frames are immutable shared state. Project the caller's newer
		// event cursor only onto this delivery clone so every returned frame
		// remains a valid non-regressing public resume point.
		frame.Cursor.Events = max(frame.Cursor.Events, cursor.Events)
		frame.TruncatedBefore = truncatedBefore
		frame.EventsTruncatedBefore = eventsTruncatedBefore
		if frame.Text != "" {
			end := frame.Cursor.Output
			start := end - int64(len([]byte(frame.Text)))
			delivered := max(cursor.Output, task.outputState.frontier.base)
			switch {
			case delivered >= end:
				frame.Text = ""
			case delivered > start:
				frame.Text = sliceStringFromByteCursor(frame.Text, delivered-start)
			}
		}
		if frame.Text != "" || frame.Event != nil || frame.Closed {
			snap.Frames = append(snap.Frames, frame)
		}
	}
	task.mu.Unlock()
	return stream.CloneSnapshot(snap), nil
}

// await returns once output/events advance beyond the requested cursor or the
// Task reaches a terminal state. It is level-triggered and non-consuming.
func (s *streamService) await(ctx context.Context, req stream.ReadRequest) (stream.Snapshot, error) {
	ref := stream.NormalizeRef(req.Ref)
	cursor := stream.CloneCursor(req.Cursor)
	_, await, err := s.resolveReader(ctx, ref)
	if err != nil {
		return stream.Snapshot{}, err
	}
	return await(ctx, cursor, false)
}

func (s *streamService) awaitCommand(ctx context.Context, task *commandTask, cursor stream.Cursor, followContinue bool) (stream.Snapshot, error) {
	if task == nil || task.session == nil {
		return stream.Snapshot{}, fmt.Errorf("agent-sdk/runtime: command task has no observable sandbox session")
	}
	type backendObservation struct {
		observation sandbox.OutputObservation
		err         error
	}
	backendResults := make(chan backendObservation, 1)
	backendDone := make(chan struct{})
	backendCtx, cancelBackend := context.WithCancel(ctx)
	task.mu.Lock()
	backendCursor := task.outputState.backend.outputCursor()
	commandSession := task.session
	task.mu.Unlock()
	go func() {
		defer close(backendDone)
		defer close(backendResults)
		for {
			observation, err := commandSession.AwaitOutput(backendCtx, backendCursor)
			if err == nil && observation.Status.Running &&
				observation.Cursor.Stdout <= backendCursor.Stdout &&
				observation.Cursor.Stderr <= backendCursor.Stderr {
				err = fmt.Errorf("agent-sdk/runtime: command output observer returned without output or terminal progress")
			}
			result := backendObservation{observation: observation, err: err}
			select {
			case backendResults <- result:
			case <-backendCtx.Done():
				return
			}
			if err != nil || !observation.Status.Running {
				return
			}
			next := observation.Cursor
			if next.Stdout <= backendCursor.Stdout && next.Stderr <= backendCursor.Stderr {
				return
			}
			backendCursor.Stdout = max(backendCursor.Stdout, next.Stdout)
			backendCursor.Stderr = max(backendCursor.Stderr, next.Stderr)
		}
	}()
	defer func() {
		cancelBackend()
		<-backendDone
	}()

	for {
		snap, err := s.readCommand(ctx, task, cursor)
		if err != nil {
			return stream.Snapshot{}, err
		}
		if streamSnapshotReady(snap, cursor, followContinue) {
			return snap, nil
		}
		task.mu.Lock()
		wait, ready := task.commandStreamChangeWaiterLocked(cursor)
		if ready {
			task.mu.Unlock()
			continue
		}
		task.mu.Unlock()
		// Command streams have two independent level-triggered sources:
		// callback frames/state transitions notify wait, while AwaitOutput also
		// observes a zero-output process exit. One backend waiter is retained for
		// the complete await/subscribe call and re-arms itself after progress;
		// stream wakes no longer allocate and cancel a new goroutine.
		select {
		case <-ctx.Done():
			return stream.Snapshot{}, ctx.Err()
		case <-wait:
		case result, ok := <-backendResults:
			if !ok {
				return stream.Snapshot{}, fmt.Errorf("agent-sdk/runtime: command output observer stopped before the stream advanced")
			}
			if result.err != nil {
				return stream.Snapshot{}, result.err
			}
		}
	}
}

func (s *streamService) awaitSubagent(ctx context.Context, task *subagentTask, cursor stream.Cursor, followContinue bool) (stream.Snapshot, error) {
	for {
		snap, err := s.readSubagent(ctx, task, cursor)
		if err != nil {
			return stream.Snapshot{}, err
		}
		if streamSnapshotReady(snap, cursor, followContinue) {
			return snap, nil
		}
		task.mu.Lock()
		wait, ready := task.streamChangeWaiterLocked(cursor, followContinue)
		task.mu.Unlock()
		if ready {
			continue
		}
		select {
		case <-ctx.Done():
			return stream.Snapshot{}, ctx.Err()
		case <-wait:
		}
	}
}

func streamSnapshotReady(snap stream.Snapshot, cursor stream.Cursor, followContinue bool) bool {
	if snap.Cursor.Output > cursor.Output || snap.Cursor.Events > cursor.Events {
		return true
	}
	if snap.TruncatedBefore > cursor.Output || snap.EventsTruncatedBefore > cursor.Events {
		return true
	}
	if snap.Running || !stream.IsTerminalState(snap.State) {
		return false
	}
	return !followContinue || !snap.SupportsInput
}

func commandStatusWithoutSession(task *commandTask) (sandbox.SessionStatus, error) {
	if task == nil {
		return sandbox.SessionStatus{}, fmt.Errorf("agent-sdk/runtime: command task is required")
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.running || !stream.IsTerminalState(string(task.state)) {
		return sandbox.SessionStatus{}, fmt.Errorf("agent-sdk/runtime: command task %q has no observable sandbox session", task.ref.TaskID)
	}
	return sandbox.SessionStatus{
		Terminal: sandbox.TerminalRef{
			SessionID:  strings.TrimSpace(task.ref.SessionID),
			TerminalID: strings.TrimSpace(task.ref.TerminalID),
		},
		Running:   false,
		StartedAt: task.createdAt,
		UpdatedAt: task.createdAt,
	}, nil
}

func (s *streamService) readSubagent(ctx context.Context, sub *subagentTask, cursor stream.Cursor) (stream.Snapshot, error) {
	if sub == nil {
		return stream.Snapshot{}, fmt.Errorf("agent-sdk/runtime: subagent task is required")
	}
	if sub.runner != nil {
		result, err := sub.runner.Wait(ctx, sub.anchor, 0)
		if err == nil {
			sub.mu.Lock()
			sub.applyResult(result)
			sub.seedStreamFromResult(result)
			sub.mu.Unlock()
		} else if ctx.Err() != nil {
			return stream.Snapshot{}, ctx.Err()
		} else if sub.isRunning() && s != nil && s.tasks != nil {
			if _, interruptErr := s.tasks.interruptSubagentTask(ctx, sub, "subagent session interrupted during recovery: "+strings.TrimSpace(err.Error())); interruptErr != nil {
				return stream.Snapshot{}, interruptErr
			}
		}
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	sub.ensureTerminalStreamFrameLocked()
	output := subagentStreamOutput(sub.stdout, sub.stderr)
	if buffered := int64(len([]byte(output))); sub.streamOutputCursor < buffered {
		sub.streamOutputCursor = buffered
	}
	nextOutput := sub.streamOutputCursor
	nextEvents := sub.streamEventBase + int64(len(sub.streamFrames))
	state := sub.state
	if state == "" {
		if sub.running {
			state = task.StateRunning
		} else {
			state = task.StateUnknownOutcome
		}
	}
	eventsTruncatedBefore := int64(0)
	if cursor.Events < sub.streamEventBase {
		eventsTruncatedBefore = sub.streamEventBase
	}
	snap := stream.Snapshot{
		Ref: stream.Ref{
			SessionID:  strings.TrimSpace(sub.sessionRef.SessionID),
			TaskID:     strings.TrimSpace(sub.ref.TaskID),
			TerminalID: strings.TrimSpace(sub.ref.TerminalID),
		},
		Cursor: stream.Cursor{
			Output: nextOutput,
			Events: nextEvents,
		},
		EventsTruncatedBefore: eventsTruncatedBefore,
		Running:               sub.running,
		State:                 string(state),
		SupportsInput:         !sub.running && state == task.StateCompleted,
		TerminalFramed:        !sub.running && stream.IsTerminalState(string(state)),
		StartedAt:             sub.createdAt,
		UpdatedAt:             time.Now(),
	}
	if !sub.running {
		snap.FinalText = firstNonBlankTaskOutput(
			taskRawStringValue(sub.result["final_message"]),
			taskRawStringValue(sub.result["result"]),
			output,
			taskRawStringValue(sub.result["error"]),
			"(no output)",
		)
	}
	if start := cursor.Events; start < nextEvents {
		if start < sub.streamEventBase {
			start = sub.streamEventBase
		}
		for _, frame := range sub.streamFrames[start-sub.streamEventBase:] {
			cloned := stream.CloneFrame(frame)
			terminalID := strings.TrimSpace(cloned.Ref.TerminalID)
			cloned.Ref = snap.Ref
			if terminalID != "" {
				cloned.Ref.TerminalID = terminalID
			}
			cloned.EventsTruncatedBefore = max(cloned.EventsTruncatedBefore, eventsTruncatedBefore)
			if cloned.UpdatedAt.IsZero() {
				cloned.UpdatedAt = snap.UpdatedAt
			}
			snap.Frames = append(snap.Frames, cloned)
		}
	}
	// Older in-process producers may populate only the aggregate text buffer.
	// Keep that fallback local to a task with no structured frames; normal
	// producers publish cursor-owned frames through PublishStream.
	if len(sub.streamFrames) == 0 && output != "" {
		turnBase := nextOutput - int64(len([]byte(output)))
		if cursor.Output >= turnBase {
			if delta := sliceStringFromByteCursor(output, cursor.Output-turnBase); delta != "" {
				snap.Frames = append(snap.Frames, stream.Frame{
					Ref:       snap.Ref,
					Text:      delta,
					Cursor:    snap.Cursor,
					Running:   sub.running,
					UpdatedAt: snap.UpdatedAt,
				})
			}
		}
	}
	return stream.CloneSnapshot(snap), nil
}

func (s *streamService) Subscribe(ctx context.Context, req stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		ref := stream.NormalizeRef(req.Ref)
		cursor := stream.CloneCursor(req.Cursor)
		_, await, err := s.resolveReader(ctx, ref)
		if err != nil {
			yield(nil, err)
			return
		}
		for {
			snap, err := await(ctx, cursor, req.FollowContinues)
			if err != nil {
				yield(nil, err)
				return
			}
			for _, frame := range stream.FramesForSnapshot(snap) {
				cloned := stream.CloneFrame(frame)
				if !yield(&cloned, nil) {
					return
				}
			}
			// The cursor represents the whole source snapshot. Advance it only
			// after every frame from that snapshot was accepted by the consumer.
			cursor = stream.CloneCursor(snap.Cursor)
			followContinue := req.FollowContinues && snap.SupportsInput
			if !snap.Running && !followContinue {
				return
			}
		}
	}
}

func subagentStreamOutput(stdout string, stderr string) string {
	switch {
	case stdout != "" && stderr != "":
		return stdout + stderr
	case stdout != "":
		return stdout
	case stderr != "":
		return stderr
	default:
		return ""
	}
}

func (s *streamService) Wait(ctx context.Context, ref stream.Ref) (stream.Snapshot, error) {
	ref = stream.NormalizeRef(ref)
	_, await, err := s.resolveReader(ctx, ref)
	if err != nil {
		return stream.Snapshot{}, err
	}
	cursor := stream.Cursor{}
	for {
		snap, err := await(ctx, cursor, false)
		if err != nil {
			return stream.Snapshot{}, err
		}
		if !snap.Running {
			return snap, nil
		}
		cursor = stream.CloneCursor(snap.Cursor)
	}
}

func (s *streamService) Kill(ctx context.Context, ref stream.Ref) error {
	task, err := s.resolveTask(ctx, stream.NormalizeRef(ref))
	if err != nil {
		return err
	}
	return task.session.Terminate(ctx)
}

func (s *streamService) Release(ctx context.Context, ref stream.Ref) error {
	task, err := s.resolveTask(ctx, stream.NormalizeRef(ref))
	if err != nil {
		return err
	}
	status, err := task.session.Status(ctx)
	if err != nil {
		return err
	}
	if status.Running {
		return task.session.Terminate(ctx)
	}
	return nil
}

func (s *streamService) resolveTask(ctx context.Context, ref stream.Ref) (*commandTask, error) {
	if s == nil || s.tasks == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: terminal service is unavailable")
	}
	if err := stream.ValidateRef(ref); err != nil {
		return nil, err
	}
	sessionRef := session.SessionRef{SessionID: ref.SessionID}
	return s.tasks.lookupCommand(ctx, sessionRef, ref.TaskID)
}

func (s *streamService) resolveSubagent(ctx context.Context, ref stream.Ref) (*subagentTask, error) {
	if s == nil || s.tasks == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: terminal service is unavailable")
	}
	if err := stream.ValidateRef(ref); err != nil {
		return nil, err
	}
	sessionRef := session.SessionRef{SessionID: ref.SessionID}
	return s.tasks.lookupSubagent(ctx, sessionRef, ref.TaskID)
}

func sliceStringFromByteCursor(text string, cursor int64) string {
	if cursor < 0 {
		cursor = 0
	}
	raw := []byte(text)
	if cursor >= int64(len(raw)) {
		return ""
	}
	for cursor < int64(len(raw)) && !utf8.RuneStart(raw[cursor]) {
		cursor++
	}
	return string(raw[cursor:])
}
