package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// PrepareIdleShutdown atomically closes Session Runtime admission only when no
// Session turn or command dispatch owns work. HTTP mutation dispatch is fenced by the caller.
// Subscriptions, detached Tasks and child execution references do not pin a Host.
// On rejection the Host remains fully usable; on success Quiesce must follow.
func (s *Stack) PrepareIdleShutdown(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.sessionRuntimes == nil {
		return errorcode.New(errorcode.Unavailable, "idle shutdown requires the managed Runtime registry")
	}
	r := s.sessionRuntimes
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	if r.building != 0 {
		return idleShutdownBusy()
	}
	for _, runtime := range r.sessions {
		if runtime.inUse != 0 || runtime.releasing {
			return idleShutdownBusy()
		}
		if runtime.instance != nil && runtime.instance.currentGateway().SessionRunning(runtime.sessionID) {
			return idleShutdownBusy()
		}
	}
	r.closed = true
	for _, runtime := range r.sessions {
		r.cancelIdleReleaseLocked(runtime)
	}
	// Prevent ordinary Control commands from entering the default composition
	// while the listener hands off to its existing quiesce/drain path.
	s.composition.closing.Store(true)
	return nil
}

func idleShutdownBusy() error {
	return errorcode.New(errorcode.Conflict, "Host is busy; retry replacement after work completes")
}
