package kernel

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// lostRunRecoverer is a Control-local optional Runtime capability. It may
// quiesce only a process-local producer that has already lost durable lease
// authority; a healthy active run remains a normal single-flight conflict.
type lostRunRecoverer interface {
	RecoverLostRun(context.Context, session.SessionRef) (bool, error)
}

func (g *Gateway) recoverLostActiveRun(ctx context.Context, ref session.SessionRef) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ref = session.NormalizeSessionRef(ref)
	for {
		g.mu.Lock()
		if g.quiescing {
			g.mu.Unlock()
			return hostClosingError()
		}
		_, active := g.active[ref.SessionID]
		changed := g.activeChanged
		g.mu.Unlock()
		if !active {
			return nil
		}
		recoverer, ok := g.runtime.(lostRunRecoverer)
		if !ok {
			return activeRunConflictError()
		}
		recovered, err := recoverer.RecoverLostRun(ctx, ref)
		if err != nil {
			return fmt.Errorf("gateway: recover lost active run: %w", err)
		}
		if !recovered {
			return activeRunConflictError()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func activeRunConflictError() *Error {
	return &Error{
		Kind:        KindConflict,
		Code:        CodeActiveRunConflict,
		UserVisible: true,
		Message:     "gateway: session already has an active run",
	}
}
