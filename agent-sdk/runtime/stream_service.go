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
	read, err := s.resolveReader(ctx, ref)
	if err != nil {
		return stream.Snapshot{}, err
	}
	return read(ctx, cursor)
}

type resolvedStreamReader func(context.Context, stream.Cursor) (stream.Snapshot, error)

// resolveReader pins one stream operation to the task instance it resolved.
// A completed task may leave the live registry before its canonical result is
// promoted from the deferred durable entry; re-resolving during that interval
// would close an active subscription against an empty reconstructed snapshot.
func (s *streamService) resolveReader(ctx context.Context, ref stream.Ref) (resolvedStreamReader, error) {
	if err := stream.ValidateRef(ref); err != nil {
		return nil, err
	}
	task, err := s.resolveTask(ctx, ref)
	if err == nil {
		return func(readCtx context.Context, cursor stream.Cursor) (stream.Snapshot, error) {
			return s.readCommand(readCtx, task, cursor)
		}, nil
	}
	subagent, subagentErr := s.resolveSubagent(ctx, ref)
	if subagentErr != nil {
		return nil, subagentErr
	}
	return func(readCtx context.Context, cursor stream.Cursor) (stream.Snapshot, error) {
		return s.readSubagent(readCtx, subagent, cursor)
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
	task.mu.Lock()
	task.ensureCommandOutputSeedLocked()
	stdoutCursor := task.stdoutCursor
	stderrCursor := task.stderrCursor
	useReadOutput := commandSession != nil && !task.outputCallback
	task.mu.Unlock()
	if useReadOutput {
		stdout, stderr, nextStdout, nextStderr, err := commandSession.ReadOutput(ctx, stdoutCursor, stderrCursor)
		if err != nil {
			return stream.Snapshot{}, err
		}
		task.mu.Lock()
		task.stdoutCursor = nextStdout
		task.stderrCursor = nextStderr
		task.appendOutputLocked(terminalDeltaText(string(stdout), string(stderr)))
		task.mu.Unlock()
	}
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
		task.reconcileFinalOutputLocked(terminalOutputText(task.output, result.Stdout, result.Stderr))
		outputCursor = task.outputCursorLocked()
	}
	task.ensureCommandTerminalFrameLocked(state, status, hasExitStatus)
	truncatedBefore := int64(0)
	if cursor.Output < task.outputBase {
		truncatedBefore = task.outputBase
	}
	eventsTruncatedBefore := int64(0)
	if cursor.Events < task.streamEventBase {
		eventsTruncatedBefore = task.streamEventBase
	}
	eventCursor := task.streamEventBase
	if count := len(task.streamFrames); count > 0 {
		eventCursor = task.streamFrames[count-1].Cursor.Events
	}
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
		if retained.Cursor.Events <= cursor.Events {
			continue
		}
		frame := stream.CloneFrame(retained)
		frame.Ref = snap.Ref
		frame.TruncatedBefore = truncatedBefore
		frame.EventsTruncatedBefore = eventsTruncatedBefore
		if frame.Text != "" {
			end := frame.Cursor.Output
			start := end - int64(len([]byte(frame.Text)))
			delivered := max(cursor.Output, task.outputBase)
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
		read, err := s.resolveReader(ctx, ref)
		if err != nil {
			yield(nil, err)
			return
		}
		poll := req.PollInterval
		if poll <= 0 {
			poll = 100 * time.Millisecond
		}
		for {
			snap, err := read(ctx, cursor)
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
			timer := time.NewTimer(poll)
			select {
			case <-ctx.Done():
				timer.Stop()
				yield(nil, ctx.Err())
				return
			case <-timer.C:
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
	read, err := s.resolveReader(ctx, ref)
	if err != nil {
		return stream.Snapshot{}, err
	}
	poll := 100 * time.Millisecond
	for {
		snap, err := read(ctx, stream.Cursor{})
		if err != nil {
			return stream.Snapshot{}, err
		}
		if !snap.Running {
			return snap, nil
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return stream.Snapshot{}, ctx.Err()
		case <-timer.C:
		}
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
