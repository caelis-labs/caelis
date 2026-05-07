package local

import (
	"context"
	"fmt"
	"iter"
	"maps"
	"strings"
	"time"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

type streamService struct {
	tasks *taskRuntime
}

func newStreamService(tasks *taskRuntime) *streamService {
	return &streamService{tasks: tasks}
}

func (s *streamService) Read(ctx context.Context, req sdkstream.ReadRequest) (sdkstream.Snapshot, error) {
	ref := sdkstream.NormalizeRef(req.Ref)
	cursor := sdkstream.CloneCursor(req.Cursor)
	task, err := s.resolveTask(ctx, ref)
	if err == nil {
		return s.readBash(ctx, task, cursor)
	}
	subagent, subagentErr := s.resolveSubagent(ctx, ref)
	if subagentErr != nil {
		return sdkstream.Snapshot{}, err
	}
	return s.readSubagent(ctx, subagent, cursor)
}

func (s *streamService) readBash(ctx context.Context, task *bashTask, cursor sdkstream.Cursor) (sdkstream.Snapshot, error) {
	status, err := task.session.Status(ctx)
	if err != nil {
		return sdkstream.Snapshot{}, err
	}
	stdout, stderr, nextStdout, nextStderr, err := task.session.ReadOutput(ctx, cursor.Stdout, cursor.Stderr)
	if err != nil {
		return sdkstream.Snapshot{}, err
	}
	snap := sdkstream.Snapshot{
		Ref: sdkstream.Ref{
			SessionID:  strings.TrimSpace(task.sessionRef.SessionID),
			TaskID:     strings.TrimSpace(task.ref.TaskID),
			TerminalID: strings.TrimSpace(status.Terminal.TerminalID),
		},
		Cursor: sdkstream.Cursor{
			Stdout: nextStdout,
			Stderr: nextStderr,
		},
		Running:       status.Running,
		SupportsInput: status.SupportsInput,
		StartedAt:     status.StartedAt,
		UpdatedAt:     status.UpdatedAt,
	}
	if !status.Running {
		exitCode := status.ExitCode
		snap.ExitCode = &exitCode
	}
	if len(stdout) > 0 {
		snap.Frames = append(snap.Frames, sdkstream.Frame{
			Ref:       snap.Ref,
			Stream:    "stdout",
			Text:      string(stdout),
			Cursor:    snap.Cursor,
			Running:   status.Running,
			UpdatedAt: status.UpdatedAt,
		})
	}
	if len(stderr) > 0 {
		snap.Frames = append(snap.Frames, sdkstream.Frame{
			Ref:       snap.Ref,
			Stream:    "stderr",
			Text:      string(stderr),
			Cursor:    snap.Cursor,
			Running:   status.Running,
			UpdatedAt: status.UpdatedAt,
		})
	}
	return sdkstream.CloneSnapshot(snap), nil
}

func (s *streamService) readSubagent(ctx context.Context, task *subagentTask, cursor sdkstream.Cursor) (sdkstream.Snapshot, error) {
	if task == nil {
		return sdkstream.Snapshot{}, fmt.Errorf("sdk/runtime/local: subagent task is required")
	}
	if task.runner != nil {
		result, err := task.runner.Wait(ctx, task.anchor, 0)
		if err == nil {
			task.mu.Lock()
			task.applyResult(result)
			task.seedStreamFromResult(result)
			task.mu.Unlock()
		} else if ctx.Err() != nil {
			return sdkstream.Snapshot{}, ctx.Err()
		} else if task.isRunning() && s != nil && s.tasks != nil {
			if _, interruptErr := s.tasks.interruptSubagentTask(ctx, task, "subagent session interrupted during recovery: "+strings.TrimSpace(err.Error())); interruptErr != nil {
				return sdkstream.Snapshot{}, interruptErr
			}
		}
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	stdout := sliceStringFromByteCursor(task.stdout, cursor.Stdout)
	stderr := sliceStringFromByteCursor(task.stderr, cursor.Stderr)
	nextStdout := int64(len([]byte(task.stdout)))
	nextStderr := int64(len([]byte(task.stderr)))
	nextEvents := int64(len(task.streamFrames))
	snap := sdkstream.Snapshot{
		Ref: sdkstream.Ref{
			SessionID:  strings.TrimSpace(task.sessionRef.SessionID),
			TaskID:     strings.TrimSpace(task.ref.TaskID),
			TerminalID: strings.TrimSpace(task.ref.TerminalID),
		},
		Cursor: sdkstream.Cursor{
			Stdout: nextStdout,
			Stderr: nextStderr,
			Events: nextEvents,
		},
		Running:       task.running,
		SupportsInput: false,
		StartedAt:     task.createdAt,
		UpdatedAt:     time.Now(),
	}
	if !task.running {
		code := 0
		if task.state != "completed" {
			code = 1
		}
		snap.ExitCode = &code
		snap.Result = maps.Clone(task.result)
	}
	deliveredStdoutFrame := false
	deliveredStderrFrame := false
	if start := cursor.Events; start < nextEvents {
		if start < 0 {
			start = 0
		}
		for _, frame := range task.streamFrames[start:] {
			cloned := sdkstream.CloneFrame(frame)
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
				switch strings.ToLower(strings.TrimSpace(cloned.Stream)) {
				case "stderr":
					deliveredStderrFrame = true
				default:
					deliveredStdoutFrame = true
				}
			}
			snap.Frames = append(snap.Frames, cloned)
		}
	}
	if stdout != "" && !deliveredStdoutFrame {
		snap.Frames = append(snap.Frames, sdkstream.Frame{
			Ref:       snap.Ref,
			Stream:    "stdout",
			Text:      stdout,
			Cursor:    snap.Cursor,
			Running:   task.running,
			UpdatedAt: snap.UpdatedAt,
		})
	}
	if stderr != "" && !deliveredStderrFrame {
		snap.Frames = append(snap.Frames, sdkstream.Frame{
			Ref:       snap.Ref,
			Stream:    "stderr",
			Text:      stderr,
			Cursor:    snap.Cursor,
			Running:   task.running,
			UpdatedAt: snap.UpdatedAt,
		})
	}
	return sdkstream.CloneSnapshot(snap), nil
}

func (s *streamService) Subscribe(ctx context.Context, req sdkstream.SubscribeRequest) iter.Seq2[*sdkstream.Frame, error] {
	return func(yield func(*sdkstream.Frame, error) bool) {
		ref := sdkstream.NormalizeRef(req.Ref)
		cursor := sdkstream.CloneCursor(req.Cursor)
		poll := req.PollInterval
		if poll <= 0 {
			poll = 100 * time.Millisecond
		}
		closedSent := false
		for {
			snap, err := s.Read(ctx, sdkstream.ReadRequest{Ref: ref, Cursor: cursor})
			if err != nil {
				yield(nil, err)
				return
			}
			cursor = snap.Cursor
			for _, frame := range snap.Frames {
				cloned := sdkstream.CloneFrame(frame)
				if !yield(&cloned, nil) {
					return
				}
			}
			if !snap.Running {
				if !closedSent {
					frame := sdkstream.Frame{
						Ref:       snap.Ref,
						Cursor:    snap.Cursor,
						Running:   false,
						Closed:    true,
						State:     streamClosedState(snap),
						Result:    maps.Clone(snap.Result),
						UpdatedAt: snap.UpdatedAt,
					}
					if snap.ExitCode != nil {
						code := *snap.ExitCode
						frame.ExitCode = &code
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

func streamClosedState(snap sdkstream.Snapshot) string {
	if state, _ := snap.Result["state"].(string); strings.TrimSpace(state) != "" {
		return strings.TrimSpace(state)
	}
	if snap.ExitCode != nil && *snap.ExitCode != 0 {
		return "failed"
	}
	return "completed"
}

func (s *streamService) Wait(ctx context.Context, ref sdkstream.Ref) (sdkstream.Snapshot, error) {
	ref = sdkstream.NormalizeRef(ref)
	poll := 100 * time.Millisecond
	for {
		snap, err := s.Read(ctx, sdkstream.ReadRequest{Ref: ref})
		if err != nil {
			return sdkstream.Snapshot{}, err
		}
		if !snap.Running {
			return snap, nil
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return sdkstream.Snapshot{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *streamService) Kill(ctx context.Context, ref sdkstream.Ref) error {
	task, err := s.resolveTask(ctx, sdkstream.NormalizeRef(ref))
	if err != nil {
		return err
	}
	return task.session.Terminate(ctx)
}

func (s *streamService) Release(ctx context.Context, ref sdkstream.Ref) error {
	task, err := s.resolveTask(ctx, sdkstream.NormalizeRef(ref))
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

func (s *streamService) resolveTask(ctx context.Context, ref sdkstream.Ref) (*bashTask, error) {
	if s == nil || s.tasks == nil {
		return nil, fmt.Errorf("sdk/runtime/local: terminal service is unavailable")
	}
	if ref.SessionID == "" {
		return nil, fmt.Errorf("sdk/runtime/local: session_id is required")
	}
	sessionRef := sdksession.SessionRef{SessionID: ref.SessionID}
	if ref.TaskID != "" {
		return s.tasks.lookupBash(ctx, sessionRef, ref.TaskID)
	}
	if ref.TerminalID == "" {
		return nil, fmt.Errorf("sdk/runtime/local: task_id or terminal_id is required")
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
		return nil, fmt.Errorf("sdk/runtime/local: terminal %q not found", ref.TerminalID)
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
		return s.tasks.rehydrateBashTask(hydrated)
	}
	return nil, fmt.Errorf("sdk/runtime/local: terminal %q not found", ref.TerminalID)
}

func (s *streamService) resolveSubagent(ctx context.Context, ref sdkstream.Ref) (*subagentTask, error) {
	if s == nil || s.tasks == nil {
		return nil, fmt.Errorf("sdk/runtime/local: terminal service is unavailable")
	}
	if ref.SessionID == "" {
		return nil, fmt.Errorf("sdk/runtime/local: session_id is required")
	}
	sessionRef := sdksession.SessionRef{SessionID: ref.SessionID}
	if ref.TaskID != "" {
		return s.tasks.lookupSubagent(ctx, sessionRef, ref.TaskID)
	}
	if ref.TerminalID == "" {
		return nil, fmt.Errorf("sdk/runtime/local: task_id or terminal_id is required")
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
		return nil, fmt.Errorf("sdk/runtime/local: terminal %q not found", ref.TerminalID)
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
	return nil, fmt.Errorf("sdk/runtime/local: terminal %q not found", ref.TerminalID)
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
