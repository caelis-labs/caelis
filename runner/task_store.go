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

// MemoryTaskStore is an in-memory TaskStore suitable for SDK embedding and
// tests. Durable stores can implement TaskStore with file or database backing.
type MemoryTaskStore struct {
	mu      sync.Mutex
	tasks   map[string]TaskSnapshot
	waiters map[string][]chan struct{}
}

func NewMemoryTaskStore() *MemoryTaskStore {
	return &MemoryTaskStore{
		tasks:   make(map[string]TaskSnapshot),
		waiters: make(map[string][]chan struct{}),
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

func isTerminalTaskState(state TaskState) bool {
	switch state {
	case TaskStateCompleted, TaskStateFailed, TaskStateCancelled:
		return true
	default:
		return false
	}
}
