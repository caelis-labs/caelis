package runner

import (
	"context"
	"fmt"
	"sync"
)

// TaskStore persists task snapshots so TASK can observe tasks across
// invocations that use different TaskManager instances.
type TaskStore interface {
	SaveTask(context.Context, TaskSnapshot) error
	LoadTask(context.Context, string) (TaskSnapshot, bool, error)
	WaitTask(context.Context, string) (TaskSnapshot, error)
}

// TaskCancelStore optionally tracks in-process task cancellation hooks. The
// hooks are runtime-only and are never part of TaskSnapshot durability.
type TaskCancelStore interface {
	RegisterTaskCancel(context.Context, string, context.CancelFunc) error
	CancelTaskRun(context.Context, string) bool
	UnregisterTaskCancel(context.Context, string)
}

// MemoryTaskStore is an in-memory TaskStore suitable for SDK embedding and
// tests. Durable stores can implement TaskStore with file or database backing.
type MemoryTaskStore struct {
	mu      sync.Mutex
	tasks   map[string]TaskSnapshot
	waiters map[string][]chan struct{}
	cancels map[string]context.CancelFunc
}

func NewMemoryTaskStore() *MemoryTaskStore {
	return &MemoryTaskStore{
		tasks:   make(map[string]TaskSnapshot),
		waiters: make(map[string][]chan struct{}),
		cancels: make(map[string]context.CancelFunc),
	}
}

func (s *MemoryTaskStore) SaveTask(_ context.Context, snap TaskSnapshot) error {
	if s == nil {
		return nil
	}
	if snap.TaskID == "" {
		return fmt.Errorf("runner: task id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.tasks[snap.TaskID]; ok && existing.State == TaskStateCancelled && snap.State != TaskStateCancelled {
		return nil
	}
	s.tasks[snap.TaskID] = snap
	if isTerminalTaskState(snap.State) {
		delete(s.cancels, snap.TaskID)
		for _, waiter := range s.waiters[snap.TaskID] {
			close(waiter)
		}
		delete(s.waiters, snap.TaskID)
	}
	return nil
}

func (s *MemoryTaskStore) LoadTask(_ context.Context, taskID string) (TaskSnapshot, bool, error) {
	if s == nil {
		return TaskSnapshot{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	snap, ok := s.tasks[taskID]
	return snap, ok, nil
}

func (s *MemoryTaskStore) WaitTask(ctx context.Context, taskID string) (TaskSnapshot, error) {
	if s == nil {
		return TaskSnapshot{}, fmt.Errorf("task not found: %s", taskID)
	}
	for {
		s.mu.Lock()
		snap, ok := s.tasks[taskID]
		if !ok {
			s.mu.Unlock()
			return TaskSnapshot{}, fmt.Errorf("task not found: %s", taskID)
		}
		if isTerminalTaskState(snap.State) {
			s.mu.Unlock()
			return snap, nil
		}
		waiter := make(chan struct{})
		s.waiters[taskID] = append(s.waiters[taskID], waiter)
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return TaskSnapshot{}, ctx.Err()
		case <-waiter:
		}
	}
}

func (s *MemoryTaskStore) RegisterTaskCancel(_ context.Context, taskID string, cancel context.CancelFunc) error {
	if s == nil {
		return nil
	}
	if taskID == "" {
		return fmt.Errorf("runner: task id is required")
	}
	if cancel == nil {
		return fmt.Errorf("runner: task cancel func is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap, ok := s.tasks[taskID]; ok && isTerminalTaskState(snap.State) {
		return nil
	}
	s.cancels[taskID] = cancel
	return nil
}

func (s *MemoryTaskStore) CancelTaskRun(_ context.Context, taskID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	cancel := s.cancels[taskID]
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

func (s *MemoryTaskStore) UnregisterTaskCancel(_ context.Context, taskID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, taskID)
}

func isTerminalTaskState(state TaskState) bool {
	switch state {
	case TaskStateCompleted, TaskStateFailed, TaskStateCancelled:
		return true
	default:
		return false
	}
}
