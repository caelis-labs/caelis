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
	TaskID   string
	State    TaskState
	Output   string
	ExitCode int
	Error    string
	Started  time.Time
	Ended    time.Time
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
}

// NewTaskManager creates a new task manager.
func NewTaskManager(backend sandbox.Backend) *TaskManager {
	return &TaskManager{
		tasks:    make(map[string]*taskEntry),
		backend:  backend,
		maxTasks: 100,
	}
}

// StartCommand launches an async command and returns a task ID.
func (tm *TaskManager) StartCommand(ctx context.Context, req sandbox.CommandRequest) (string, error) {
	if tm.backend == nil {
		return "", fmt.Errorf("no sandbox backend available")
	}

	tm.mu.Lock()
	tm.counter++
	taskID := fmt.Sprintf("task-%d", tm.counter)
	entry := &taskEntry{
		id:      taskID,
		state:   TaskStateRunning,
		started: time.Now(),
		done:    make(chan struct{}),
	}
	tm.tasks[taskID] = entry
	tm.order = append(tm.order, taskID)
	tm.mu.Unlock()

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
		entry.ended = time.Now()
		entry.stdout = result.Stdout
		entry.stderr = result.Stderr
		entry.exitCode = result.ExitCode
		if err != nil {
			entry.state = TaskStateFailed
			entry.err = err
		} else if result.ExitCode != 0 {
			entry.state = TaskStateFailed
			entry.err = fmt.Errorf("exit code %d", result.ExitCode)
		} else {
			entry.state = TaskStateCompleted
		}
		close(entry.done)
	}()

	// GC old completed tasks.
	tm.gc()

	return taskID, nil
}

// Wait blocks until a task completes and returns its snapshot.
func (tm *TaskManager) Wait(ctx context.Context, taskID string) (TaskSnapshot, error) {
	entry := tm.getTask(taskID)
	if entry == nil {
		return TaskSnapshot{}, fmt.Errorf("task not found: %s", taskID)
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
		return fmt.Errorf("task not found: %s", taskID)
	}
	// TODO: implement stdin write for async sessions
	return fmt.Errorf("task write not yet implemented")
}

// Cancel stops a running task.
func (tm *TaskManager) Cancel(ctx context.Context, taskID string) error {
	entry := tm.getTask(taskID)
	if entry == nil {
		return fmt.Errorf("task not found: %s", taskID)
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.cancel != nil {
		entry.cancel()
	}
	entry.state = TaskStateCancelled
	entry.ended = time.Now()
	// Don't close done here — the background goroutine will close it
	// after it detects the cancellation.
	return nil
}

// Snapshot returns the current state of a task.
func (tm *TaskManager) Snapshot(taskID string) (TaskSnapshot, error) {
	entry := tm.getTask(taskID)
	if entry == nil {
		return TaskSnapshot{}, fmt.Errorf("task not found: %s", taskID)
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()

	snap := TaskSnapshot{
		TaskID:   entry.id,
		State:    entry.state,
		Output:   string(entry.stdout),
		ExitCode: entry.exitCode,
		Started:  entry.started,
		Ended:    entry.ended,
	}
	if entry.err != nil {
		snap.Error = entry.err.Error()
	}
	if len(entry.stderr) > 0 {
		snap.Error = string(entry.stderr)
	}
	return snap, nil
}

func (tm *TaskManager) getTask(id string) *taskEntry {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.tasks[id]
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
