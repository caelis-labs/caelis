package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/sandbox"
)

// TaskState represents the lifecycle state of an async task.
type TaskState string

const (
	TaskStateRunning   TaskState = "running"
	TaskStateCompleted TaskState = "completed"
	TaskStateFailed    TaskState = "failed"
	TaskStateCancelled TaskState = "cancelled"
	TaskStateWaitingIn TaskState = "waiting_input"
)

// TaskSnapshot is the current state of a task for tool results.
type TaskSnapshot struct {
	SessionRef     string
	SandboxSession sandbox.SessionRef
	TaskID         string
	State          TaskState
	Output         string
	ExitCode       int
	Error          string
	Started        time.Time
	Ended          time.Time
}

// taskEntry tracks a running task.
type taskEntry struct {
	id       string
	state    TaskState
	stdout   []byte
	stderr   []byte
	exitCode int
	err      error
	started  time.Time
	ended    time.Time
	cancel   context.CancelFunc
	session  sandbox.Session
	sandbox  sandbox.SessionRef
	done     chan struct{} // closed when task completes
	mu       sync.Mutex
}

// TaskManager manages async command tasks for the runner.
type TaskManager struct {
	mu       sync.Mutex
	tasks    map[string]*taskEntry
	order    []string // insertion order for GC
	counter  int
	backend  sandbox.Backend
	maxTasks int // max completed tasks to retain
	store    TaskStore
	scope    string
	writer   taskWriter
}

type taskWriter interface {
	WriteTask(context.Context, string, string) (TaskSnapshot, error)
}

// NewTaskManager creates a new task manager.
func NewTaskManager(backend sandbox.Backend) *TaskManager {
	return &TaskManager{
		tasks:    make(map[string]*taskEntry),
		backend:  backend,
		maxTasks: 100,
	}
}

// NewTaskManagerWithStore creates a task manager backed by a shared task store.
func NewTaskManagerWithStore(backend sandbox.Backend, store TaskStore, scope string) *TaskManager {
	tm := NewTaskManager(backend)
	tm.store = store
	tm.scope = scope
	return tm
}

func (tm *TaskManager) SetWriter(writer taskWriter) {
	tm.writer = writer
}

// StartCommand launches an async command and returns a task ID.
func (tm *TaskManager) StartCommand(ctx context.Context, req sandbox.CommandRequest) (string, error) {
	if tm.backend == nil {
		return "", fmt.Errorf("no sandbox backend available")
	}
	if async, ok := tm.backend.(sandbox.AsyncBackend); ok {
		return tm.startSession(ctx, async, req)
	}

	tm.mu.Lock()
	tm.counter++
	taskID := fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), tm.counter)
	entry := &taskEntry{
		id:      taskID,
		state:   TaskStateRunning,
		started: time.Now(),
		done:    make(chan struct{}),
	}
	tm.tasks[taskID] = entry
	tm.order = append(tm.order, taskID)
	tm.mu.Unlock()
	if err := tm.saveEntry(ctx, entry); err != nil {
		return "", err
	}

	// Run command in background.
	runCtx, cancel := context.WithCancel(ctx)
	entry.cancel = cancel

	go func() {
		result, err := tm.backend.Run(runCtx, req)
		entry.mu.Lock()
		defer entry.mu.Unlock()
		// Don't overwrite state if already cancelled.
		if entry.state == TaskStateCancelled {
			close(entry.done)
			return
		}
		entry.finishLocked(result, err)
		_ = tm.saveSnapshot(context.Background(), entry.snapshotLocked(tm.scope))
		close(entry.done)
	}()

	// GC old completed tasks.
	tm.gc()

	return taskID, nil
}

func (tm *TaskManager) startSession(ctx context.Context, backend sandbox.AsyncBackend, req sandbox.CommandRequest) (string, error) {
	session, err := backend.Start(ctx, req)
	if err != nil {
		return "", err
	}
	if session == nil {
		return "", fmt.Errorf("sandbox backend returned nil session")
	}

	tm.mu.Lock()
	tm.counter++
	taskID := fmt.Sprintf("task-%d-%d", time.Now().UnixNano(), tm.counter)
	entry := &taskEntry{
		id:      taskID,
		state:   TaskStateRunning,
		started: time.Now(),
		session: session,
		sandbox: session.Ref(),
		done:    make(chan struct{}),
	}
	if entry.sandbox.Backend == "" && tm.backend != nil {
		entry.sandbox.Backend = tm.backend.Name()
	}
	tm.tasks[taskID] = entry
	tm.order = append(tm.order, taskID)
	tm.mu.Unlock()
	if err := tm.saveEntry(ctx, entry); err != nil {
		_ = session.Terminate(ctx)
		return "", err
	}

	go tm.finishSession(context.Background(), entry, session)

	tm.gc()
	return taskID, nil
}

func (tm *TaskManager) finishSession(ctx context.Context, entry *taskEntry, session sandbox.Session) {
	result, err := session.Result(ctx)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.state == TaskStateCancelled {
		close(entry.done)
		return
	}
	entry.finishLocked(result, err)
	_ = tm.saveSnapshot(context.Background(), entry.snapshotLocked(tm.scope))
	close(entry.done)
}

// Wait blocks until a task completes and returns its snapshot.
func (tm *TaskManager) Wait(ctx context.Context, taskID string) (TaskSnapshot, error) {
	entry := tm.getTask(taskID)
	if entry == nil {
		return tm.waitStored(ctx, taskID)
	}

	select {
	case <-entry.done:
		// Task completed.
	case <-ctx.Done():
		return TaskSnapshot{}, ctx.Err()
	}

	return tm.Snapshot(taskID)
}

// Write sends input to a running task (stub — requires async session).
func (tm *TaskManager) Write(ctx context.Context, taskID string, input string) error {
	entry := tm.getTask(taskID)
	if entry == nil {
		if tm.store != nil {
			snap, ok, err := tm.store.LoadTask(ctx, taskID)
			if err != nil {
				return err
			}
			if ok {
				if hasSandboxSession(snap.SandboxSession) {
					session, err := tm.openSandboxSession(snap.SandboxSession)
					if err != nil {
						return err
					}
					return writeSession(ctx, session, input)
				}
				if tm.writer != nil {
					_, err := tm.writer.WriteTask(ctx, taskID, input)
					return err
				}
				return fmt.Errorf("task write not supported: %s", taskID)
			}
		}
		if tm.writer != nil {
			_, err := tm.writer.WriteTask(ctx, taskID, input)
			return err
		}
		return fmt.Errorf("task not found: %s", taskID)
	}
	entry.mu.Lock()
	session := entry.session
	state := entry.state
	entry.mu.Unlock()
	if isTerminalTaskState(state) {
		return fmt.Errorf("task is not running: %s", taskID)
	}
	if session == nil {
		return fmt.Errorf("task write not supported: %s", taskID)
	}
	return writeSession(ctx, session, input)
}

// Cancel stops a running task.
func (tm *TaskManager) Cancel(ctx context.Context, taskID string) error {
	entry := tm.getTask(taskID)
	if entry == nil {
		if tm.store != nil {
			snap, ok, err := tm.store.LoadTask(ctx, taskID)
			if err != nil {
				return err
			}
			if ok && !isTerminalTaskState(snap.State) {
				if hasSandboxSession(snap.SandboxSession) {
					session, err := tm.openSandboxSession(snap.SandboxSession)
					if err != nil {
						return err
					}
					if err := session.Terminate(ctx); err != nil {
						return err
					}
				}
				snap.State = TaskStateCancelled
				snap.Ended = time.Now()
				return tm.store.SaveTask(ctx, snap)
			}
		}
		return fmt.Errorf("task not found: %s", taskID)
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.session != nil {
		if err := entry.session.Terminate(ctx); err != nil {
			return err
		}
	}
	if entry.cancel != nil {
		entry.cancel()
	}
	entry.state = TaskStateCancelled
	entry.ended = time.Now()
	_ = tm.saveSnapshot(ctx, entry.snapshotLocked(tm.scope))
	// Don't close done here — the background goroutine will close it
	// after it detects the cancellation.
	return nil
}

// Snapshot returns the current state of a task.
func (tm *TaskManager) Snapshot(taskID string) (TaskSnapshot, error) {
	entry := tm.getTask(taskID)
	if entry == nil {
		if tm.store != nil {
			snap, ok, err := tm.store.LoadTask(context.Background(), taskID)
			if err != nil {
				return TaskSnapshot{}, err
			}
			if ok {
				return snap, nil
			}
		}
		return TaskSnapshot{}, fmt.Errorf("task not found: %s", taskID)
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()

	return entry.snapshotLocked(tm.scope), nil
}

func (tm *TaskManager) getTask(id string) *taskEntry {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.tasks[id]
}

func (tm *TaskManager) waitStored(ctx context.Context, taskID string) (TaskSnapshot, error) {
	if tm.store == nil {
		return TaskSnapshot{}, fmt.Errorf("task not found: %s", taskID)
	}
	snap, ok, err := tm.store.LoadTask(ctx, taskID)
	if err != nil {
		return TaskSnapshot{}, err
	}
	if !ok {
		return TaskSnapshot{}, fmt.Errorf("task not found: %s", taskID)
	}
	if isTerminalTaskState(snap.State) {
		return snap, nil
	}
	if hasSandboxSession(snap.SandboxSession) {
		session, err := tm.openSandboxSession(snap.SandboxSession)
		if err != nil {
			return TaskSnapshot{}, err
		}
		return tm.waitSessionSnapshot(ctx, snap, session)
	}
	return tm.store.WaitTask(ctx, taskID)
}

func (tm *TaskManager) waitSessionSnapshot(ctx context.Context, snap TaskSnapshot, session sandbox.Session) (TaskSnapshot, error) {
	result, err := session.Result(ctx)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return TaskSnapshot{}, ctxErr
	}
	snap = finishSnapshot(snap, result, err)
	if err := tm.saveSnapshot(ctx, snap); err != nil {
		return TaskSnapshot{}, err
	}
	return snap, nil
}

func (tm *TaskManager) saveEntry(ctx context.Context, entry *taskEntry) error {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return tm.saveSnapshot(ctx, entry.snapshotLocked(tm.scope))
}

func (tm *TaskManager) saveSnapshot(ctx context.Context, snap TaskSnapshot) error {
	if tm.store == nil {
		return nil
	}
	return tm.store.SaveTask(ctx, snap)
}

func (e *taskEntry) snapshotLocked(scope string) TaskSnapshot {
	snap := TaskSnapshot{
		SessionRef:     scope,
		SandboxSession: e.sandbox,
		TaskID:         e.id,
		State:          e.state,
		Output:         string(e.stdout),
		ExitCode:       e.exitCode,
		Started:        e.started,
		Ended:          e.ended,
	}
	if e.err != nil {
		snap.Error = e.err.Error()
	}
	if len(e.stderr) > 0 {
		snap.Error = string(e.stderr)
	}
	return snap
}

func (e *taskEntry) finishLocked(result sandbox.CommandResult, err error) {
	e.ended = time.Now()
	e.stdout = result.Stdout
	e.stderr = result.Stderr
	e.exitCode = result.ExitCode
	if err != nil {
		e.state = TaskStateFailed
		e.err = err
	} else if result.ExitCode != 0 {
		e.state = TaskStateFailed
		e.err = fmt.Errorf("exit code %d", result.ExitCode)
	} else {
		e.state = TaskStateCompleted
		e.err = nil
	}
}

func finishSnapshot(snap TaskSnapshot, result sandbox.CommandResult, err error) TaskSnapshot {
	snap.Output = string(result.Stdout)
	snap.ExitCode = result.ExitCode
	snap.Ended = time.Now()
	if err != nil {
		snap.State = TaskStateFailed
		snap.Error = err.Error()
	} else if result.ExitCode != 0 {
		snap.State = TaskStateFailed
		snap.Error = fmt.Sprintf("exit code %d", result.ExitCode)
	} else {
		snap.State = TaskStateCompleted
		snap.Error = ""
	}
	if len(result.Stderr) > 0 {
		snap.Error = string(result.Stderr)
	}
	return snap
}

func hasSandboxSession(ref sandbox.SessionRef) bool {
	return ref.SessionID != ""
}

func (tm *TaskManager) openSandboxSession(ref sandbox.SessionRef) (sandbox.Session, error) {
	if ref.SessionID == "" {
		return nil, fmt.Errorf("sandbox session id is required")
	}
	async, ok := tm.backend.(sandbox.AsyncBackend)
	if !ok {
		return nil, fmt.Errorf("sandbox backend does not support async sessions")
	}
	session, err := async.OpenSessionRef(ref)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("sandbox backend returned nil session")
	}
	return session, nil
}

func writeSession(ctx context.Context, session sandbox.Session, input string) error {
	if session == nil {
		return fmt.Errorf("sandbox session is required")
	}
	status, err := session.Status(ctx)
	if err != nil {
		return err
	}
	if !status.SupportsInput {
		return fmt.Errorf("sandbox session does not support input")
	}
	return session.WriteInput(ctx, []byte(input))
}

// gc removes oldest completed tasks when count exceeds maxTasks.
func (tm *TaskManager) gc() {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	completed := 0
	for _, id := range tm.order {
		entry := tm.tasks[id]
		if entry == nil {
			continue
		}
		entry.mu.Lock()
		done := entry.state != TaskStateRunning
		entry.mu.Unlock()
		if done {
			completed++
		}
	}

	// Remove oldest completed tasks if over limit.
	excess := completed - tm.maxTasks
	if excess <= 0 {
		return
	}
	removed := 0
	newOrder := tm.order[:0]
	for _, id := range tm.order {
		entry := tm.tasks[id]
		if entry == nil {
			continue
		}
		entry.mu.Lock()
		done := entry.state != TaskStateRunning
		entry.mu.Unlock()
		if done && removed < excess {
			delete(tm.tasks, id)
			removed++
			continue
		}
		newOrder = append(newOrder, id)
	}
	tm.order = newOrder
}
