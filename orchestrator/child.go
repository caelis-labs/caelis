package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/session"
)

// DelegationState represents the lifecycle state of a delegated child.
type DelegationState string

const (
	DelegationRunning         DelegationState = "running"
	DelegationCompleted       DelegationState = "completed"
	DelegationFailed          DelegationState = "failed"
	DelegationCancelled       DelegationState = "cancelled"
	DelegationWaitingApproval DelegationState = "waiting_approval"
)

// Anchor identifies a delegated child instance. It is system-controlled
// and not fully exposed to the LLM (only TaskID appears in tool output).
type Anchor struct {
	// TaskID is the unique identifier for this delegation.
	TaskID string

	// ChildSessionRef is the session reference of the child.
	ChildSessionRef session.Ref

	// RemoteACPSessionID is the remote ACP session ID (empty for loopback).
	RemoteACPSessionID string

	// AgentName is the name of the child agent.
	AgentName string

	// AgentID is a unique instance identifier for the child agent.
	AgentID string

	// ParentCallID is the tool call ID that triggered the spawn.
	ParentCallID string

	// ParentRunID is the invocation/run ID of the parent.
	ParentRunID string
}

// ChildHandle tracks a delegated child session.
type ChildHandle struct {
	mu sync.Mutex

	anchor Anchor
	state  DelegationState
	output string // final output text

	cancel   context.CancelFunc
	done     chan struct{}
	waiters  []chan struct{}
	createdAt time.Time
	updatedAt time.Time
}

// newChildHandle creates a new child handle in running state.
func newChildHandle(anchor Anchor, cancel context.CancelFunc) *ChildHandle {
	now := time.Now()
	return &ChildHandle{
		anchor:    anchor,
		state:     DelegationRunning,
		cancel:    cancel,
		done:      make(chan struct{}),
		createdAt: now,
		updatedAt: now,
	}
}

// Anchor returns the child's anchor.
func (h *ChildHandle) Anchor() Anchor {
	return h.anchor
}

// State returns the current delegation state.
func (h *ChildHandle) State() DelegationState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

// Output returns the final output text (empty if still running).
func (h *ChildHandle) Output() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.output
}

// Done returns a channel that is closed when the child completes.
func (h *ChildHandle) Done() <-chan struct{} {
	return h.done
}

// markCompleted transitions the handle to completed state.
func (h *ChildHandle) markCompleted(output string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != DelegationRunning {
		return
	}
	h.state = DelegationCompleted
	h.output = output
	h.updatedAt = time.Now()
	close(h.done)
	for _, w := range h.waiters {
		close(w)
	}
	h.waiters = nil
}

// markFailed transitions the handle to failed state.
func (h *ChildHandle) markFailed(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != DelegationRunning {
		return
	}
	h.state = DelegationFailed
	if err != nil {
		h.output = "error: " + err.Error()
	}
	h.updatedAt = time.Now()
	close(h.done)
	for _, w := range h.waiters {
		close(w)
	}
	h.waiters = nil
}

// markCancelled transitions the handle to cancelled state.
func (h *ChildHandle) markCancelled() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != DelegationRunning {
		return
	}
	h.state = DelegationCancelled
	h.output = "cancelled"
	h.updatedAt = time.Now()
	close(h.done)
	for _, w := range h.waiters {
		close(w)
	}
	h.waiters = nil
}

// Cancel requests cancellation of the child.
func (h *ChildHandle) Cancel() {
	h.mu.Lock()
	cancel := h.cancel
	state := h.state
	h.mu.Unlock()

	if state == DelegationRunning && cancel != nil {
		cancel()
	}
}

// waitDone blocks until the child completes or the context is cancelled.
func (h *ChildHandle) waitDone(ctx context.Context) {
	select {
	case <-h.done:
	case <-ctx.Done():
	}
}

// waitFor blocks until the child completes, the yield deadline passes,
// or the context is cancelled. Returns true if the child completed.
func (h *ChildHandle) waitFor(ctx context.Context, yieldMS int) bool {
	if yieldMS <= 0 {
		h.waitDone(ctx)
		return h.State() != DelegationRunning
	}

	timer := time.NewTimer(time.Duration(yieldMS) * time.Millisecond)
	defer timer.Stop()

	select {
	case <-h.done:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}
