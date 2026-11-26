package gatewayapp

import (
	"context"
	"log/slog"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// The Kernel serializes callbacks for one Turn. The recorder consumes only
// committed result positions and producer terminal facts, independently of
// whether the Session feed can deliver to a client.
type sessionWorkbenchObserver struct {
	kernel.TurnEventObserver
	store       *appserver.SessionWorkbenchStore
	ref         session.SessionRef
	diagnostics *slog.Logger
	lastResult  uint64
}

func (s *runtimeComposition) controlMainTurnObserver(ref session.SessionRef, operationID string) (kernel.TurnEventObserver, func()) {
	observer, release := s.controlTurnObserver(ref, operationID)
	if s.authorities.sessionWorkbench == nil {
		return observer, release
	}
	return &sessionWorkbenchObserver{
		TurnEventObserver: observer, store: s.authorities.sessionWorkbench, ref: ref, diagnostics: s.authorities.diagnostics,
	}, release
}

func (o *sessionWorkbenchObserver) ObserveTurnResult(ctx context.Context, seq uint64) {
	if seq <= o.lastResult {
		return
	}
	o.lastResult = seq
	write, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := o.store.RecordResult(write, o.ref.SessionID, o.ref.UserID, seq); err != nil && o.diagnostics != nil {
		o.diagnostics.Warn("Session workbench could not record result", "session_id", o.ref.SessionID, "error", err)
	}
}

func (o *sessionWorkbenchObserver) ObserveTurnEvent(ctx context.Context, env eventstream.Envelope) error {
	if eventstream.IsTurnTerminalLifecycle(env) && env.Scope == eventstream.ScopeMain {
		write, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		err := o.store.RecordTurn(write, o.ref.SessionID, o.ref.UserID, o.lastResult, env.Lifecycle.State == eventstream.LifecycleStateFailed)
		cancel()
		seq := uint64(0)
		if err == nil {
			seq = o.lastResult
		} else if o.diagnostics != nil {
			o.diagnostics.Warn("Session workbench could not record terminal outcome", "session_id", o.ref.SessionID, "error", err)
		}
		eventstream.SetSessionResultSequence(&env, seq)
	}
	if o.TurnEventObserver != nil {
		return o.TurnEventObserver.ObserveTurnEvent(ctx, env)
	}
	return nil
}
