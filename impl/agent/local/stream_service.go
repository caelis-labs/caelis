package local

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/ports/task"
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
	task, err := s.resolveTask(ctx, ref)
	if err == nil {
		return s.readCommand(ctx, task, cursor)
	}
	subagent, subagentErr := s.resolveSubagent(ctx, ref)
	if subagentErr != nil {
		return stream.Snapshot{}, err
	}
	return s.readSubagent(ctx, subagent, cursor)
}

func (s *streamService) readCommand(ctx context.Context, task *commandTask, cursor stream.Cursor) (stream.Snapshot, error) {
	status, err := task.session.Status(ctx)
	if err != nil {
		return stream.Snapshot{}, err
	}
	task.mu.Lock()
	stdoutCursor := task.stdoutCursor
	stderrCursor := task.stderrCursor
	useReadOutput := !task.outputCallback
	task.mu.Unlock()
	if useReadOutput {
		stdout, stderr, nextStdout, nextStderr, err := task.session.ReadOutput(ctx, stdoutCursor, stderrCursor)
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
	if !status.Running {
		result, resultErr = task.session.Result(ctx)
	}
	task.mu.Lock()
	state := stateFromStatus(status)
	task.state = state
	task.running = status.Running
	outputCursor := task.outputCursorLocked()
	finalText := ""
	if !status.Running {
		finalText = terminalFinalText(task.output, result.Stdout, result.Stderr, resultErr)
		outputCursor = int64(len([]byte(finalText)))
	}
	snap := stream.Snapshot{
		Ref: stream.Ref{
			SessionID:  strings.TrimSpace(task.sessionRef.SessionID),
			TaskID:     strings.TrimSpace(task.ref.TaskID),
			TerminalID: strings.TrimSpace(status.Terminal.TerminalID),
		},
		Cursor: stream.Cursor{
			Output: outputCursor,
		},
		Running:       status.Running,
		State:         string(state),
		SupportsInput: status.SupportsInput,
		StartedAt:     status.StartedAt,
		UpdatedAt:     status.UpdatedAt,
	}
	if !status.Running {
		exitCode := status.ExitCode
		snap.ExitCode = &exitCode
		snap.FinalText = finalText
	}
	if delta := task.outputFromCursorLocked(cursor.Output); delta != "" {
		snap.Frames = append(snap.Frames, stream.Frame{
			Ref:       snap.Ref,
			Text:      delta,
			Cursor:    snap.Cursor,
			Running:   status.Running,
			UpdatedAt: status.UpdatedAt,
		})
	}
	task.mu.Unlock()
	return stream.CloneSnapshot(snap), nil
}

func (s *streamService) readSubagent(ctx context.Context, sub *subagentTask, cursor stream.Cursor) (stream.Snapshot, error) {
	if sub == nil {
		return stream.Snapshot{}, fmt.Errorf("impl/agent/local: subagent task is required")
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
	output := subagentStreamOutput(sub.stdout, sub.stderr)
	delta := sliceStringFromByteCursor(output, cursor.Output)
	nextOutput := int64(len([]byte(output)))
	nextEvents := int64(len(sub.streamFrames))
	state := sub.state
	if state == "" {
		if sub.running {
			state = task.StateRunning
		} else {
			state = task.StateCompleted
		}
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
		Running:       sub.running,
		State:         string(state),
		SupportsInput: false,
		StartedAt:     sub.createdAt,
		UpdatedAt:     time.Now(),
	}
	if !sub.running {
		snap.FinalText = firstNonEmpty(
			taskStringValue(sub.result["final_message"]),
			taskStringValue(sub.result["result"]),
			strings.TrimSpace(output),
			taskStringValue(sub.result["error"]),
			"(no output)",
		)
	}
	deliveredTextFrame := false
	if start := cursor.Events; start < nextEvents {
		if start < 0 {
			start = 0
		}
		for _, frame := range sub.streamFrames[start:] {
			cloned := stream.CloneFrame(frame)
			terminalID := strings.TrimSpace(cloned.Ref.TerminalID)
			cloned.Ref = snap.Ref
			if terminalID != "" {
				cloned.Ref.TerminalID = terminalID
			}
			cloned.Cursor = snap.Cursor
			if cloned.UpdatedAt.IsZero() {
				cloned.UpdatedAt = snap.UpdatedAt
			}
			if cloned.Text != "" {
				deliveredTextFrame = true
			}
			snap.Frames = append(snap.Frames, cloned)
		}
	}
	if delta != "" && !deliveredTextFrame {
		snap.Frames = append(snap.Frames, stream.Frame{
			Ref:       snap.Ref,
			Text:      delta,
			Cursor:    snap.Cursor,
			Running:   sub.running,
			UpdatedAt: snap.UpdatedAt,
		})
	}
	return stream.CloneSnapshot(snap), nil
}

func (s *streamService) Subscribe(ctx context.Context, req stream.SubscribeRequest) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		ref := stream.NormalizeRef(req.Ref)
		cursor := stream.CloneCursor(req.Cursor)
		poll := req.PollInterval
		if poll <= 0 {
			poll = 100 * time.Millisecond
		}
		closedSent := false
		for {
			snap, err := s.Read(ctx, stream.ReadRequest{Ref: ref, Cursor: cursor})
			if err != nil {
				yield(nil, err)
				return
			}
			cursor = snap.Cursor
			for _, frame := range snap.Frames {
				cloned := stream.CloneFrame(frame)
				if !yield(&cloned, nil) {
					return
				}
			}
			if !snap.Running {
				if !closedSent {
					closeText := ""
					if snap.ExitCode == nil {
						closeText = snap.FinalText
					}
					frame := stream.Frame{
						Ref:       snap.Ref,
						Text:      closeText,
						Cursor:    snap.Cursor,
						Running:   false,
						Closed:    true,
						State:     streamClosedState(snap),
						UpdatedAt: snap.UpdatedAt,
					}
					if !yield(&frame, nil) {
						return
					}
				}
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

func streamClosedState(snap stream.Snapshot) string {
	switch strings.ToLower(strings.TrimSpace(snap.State)) {
	case "completed":
		return "completed"
	case "failed":
		return "failed"
	case "interrupted":
		return "interrupted"
	case "cancelled", "canceled":
		return "cancelled"
	}
	if snap.ExitCode != nil && *snap.ExitCode != 0 {
		if *snap.ExitCode < 0 {
			return "cancelled"
		}
		return "failed"
	}
	return "completed"
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
	poll := 100 * time.Millisecond
	for {
		snap, err := s.Read(ctx, stream.ReadRequest{Ref: ref})
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
		return nil, fmt.Errorf("impl/agent/local: terminal service is unavailable")
	}
	if ref.SessionID == "" {
		return nil, fmt.Errorf("impl/agent/local: session_id is required")
	}
	sessionRef := session.SessionRef{SessionID: ref.SessionID}
	if ref.TaskID != "" {
		return s.tasks.lookupCommand(ctx, sessionRef, ref.TaskID)
	}
	if ref.TerminalID == "" {
		return nil, fmt.Errorf("impl/agent/local: task_id or terminal_id is required")
	}
	s.tasks.mu.RLock()
	for _, task := range s.tasks.tasks {
		if task == nil {
			continue
		}
		if strings.TrimSpace(task.sessionRef.SessionID) != ref.SessionID {
			continue
		}
		if strings.TrimSpace(task.ref.TerminalID) == ref.TerminalID {
			s.tasks.mu.RUnlock()
			return task, nil
		}
	}
	s.tasks.mu.RUnlock()
	if s.tasks.store == nil {
		return nil, fmt.Errorf("impl/agent/local: terminal %q not found", ref.TerminalID)
	}
	entries, err := s.tasks.store.ListSession(ctx, sessionRef)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry == nil || strings.TrimSpace(entry.Terminal.TerminalID) != ref.TerminalID {
			continue
		}
		hydrated, err := s.tasks.store.Get(ctx, strings.TrimSpace(entry.TaskID))
		if err != nil {
			return nil, err
		}
		return s.tasks.rehydrateCommandTask(hydrated)
	}
	return nil, fmt.Errorf("impl/agent/local: terminal %q not found", ref.TerminalID)
}

func (s *streamService) resolveSubagent(ctx context.Context, ref stream.Ref) (*subagentTask, error) {
	if s == nil || s.tasks == nil {
		return nil, fmt.Errorf("impl/agent/local: terminal service is unavailable")
	}
	if ref.SessionID == "" {
		return nil, fmt.Errorf("impl/agent/local: session_id is required")
	}
	sessionRef := session.SessionRef{SessionID: ref.SessionID}
	if ref.TaskID != "" {
		return s.tasks.lookupSubagent(ctx, sessionRef, ref.TaskID)
	}
	if ref.TerminalID == "" {
		return nil, fmt.Errorf("impl/agent/local: task_id or terminal_id is required")
	}
	s.tasks.mu.RLock()
	for _, task := range s.tasks.subagents {
		if task == nil {
			continue
		}
		if strings.TrimSpace(task.sessionRef.SessionID) != ref.SessionID {
			continue
		}
		if strings.TrimSpace(task.ref.TerminalID) == ref.TerminalID {
			s.tasks.mu.RUnlock()
			return task, nil
		}
	}
	s.tasks.mu.RUnlock()
	if s.tasks.store == nil {
		return nil, fmt.Errorf("impl/agent/local: terminal %q not found", ref.TerminalID)
	}
	entries, err := s.tasks.store.ListSession(ctx, sessionRef)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry == nil || entry.Kind != "subagent" {
			continue
		}
		if firstNonEmpty(taskSpecString(entry.Spec, "terminal_id"), subagentTerminalID(entry.TaskID)) != ref.TerminalID {
			continue
		}
		hydrated, err := s.tasks.store.Get(ctx, strings.TrimSpace(entry.TaskID))
		if err != nil {
			return nil, err
		}
		return s.tasks.rehydrateSubagentTask(hydrated), nil
	}
	return nil, fmt.Errorf("impl/agent/local: terminal %q not found", ref.TerminalID)
}

func sliceStringFromByteCursor(text string, cursor int64) string {
	if cursor < 0 {
		cursor = 0
	}
	raw := []byte(text)
	if cursor >= int64(len(raw)) {
		return ""
	}
	return string(raw[cursor:])
}
