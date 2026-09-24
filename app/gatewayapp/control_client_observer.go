package gatewayapp

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func (s *runtimeComposition) releaseControlTaskOutput(ctx context.Context, ref task.Ref) {
	if s == nil || s.authorities.taskOutputLifecycle == nil {
		return
	}
	if err := s.authorities.taskOutputLifecycle.ReleaseTask(ctx, ref); err != nil && s.authorities.diagnostics != nil {
		s.authorities.diagnostics.Warn("Control Task output trace release failed",
			"session_id", ref.SessionID, "task_id", ref.TaskID, "error", err)
	}
}

func (s *runtimeComposition) releaseControlSessionResources(ctx context.Context, ref session.SessionRef) {
	if s == nil {
		return
	}
	if s.authorities.taskOutputLifecycle != nil {
		if err := s.authorities.taskOutputLifecycle.ReleaseSession(ctx, ref); err != nil && s.authorities.diagnostics != nil {
			s.authorities.diagnostics.Warn("Control Session Task trace release failed", "session_id", ref.SessionID, "error", err)
		}
	}
	if s.authorities.controlFeedLifecycle != nil {
		if err := s.authorities.controlFeedLifecycle.CloseSession(ctx, ref); err != nil && s.authorities.diagnostics != nil {
			s.authorities.diagnostics.Warn("Control Session feed release failed", "session_id", ref.SessionID, "error", err)
		}
	}
	if s.authorities.collaboration != nil {
		if err := s.authorities.collaboration.CleanupClosedSession(ctx, ref.SessionID); err != nil && s.authorities.diagnostics != nil {
			s.authorities.diagnostics.Warn("Control Session collaboration cleanup failed", "session_id", ref.SessionID, "error", err)
		}
	}
}

// controlTurnObserver resolves the Control-owned Session spool ingress before
// a producer starts. A missing feed degrades live observation only; canonical
// Session state remains available for replay.
func (s *runtimeComposition) controlTurnObserver(ref session.SessionRef, operationID string) (kernel.TurnEventObserver, func()) {
	release := func() {}
	if s != nil && s.retainRuntimeWork != nil {
		release = s.retainRuntimeWork(ref)
	}
	if s == nil || s.authorities.controlFeeds == nil {
		return nil, release
	}
	feed, err := s.authorities.controlFeeds.Session(ref)
	if err != nil || feed == nil {
		return nil, release
	}
	return kernel.TurnEventObserverFunc(func(ctx context.Context, envelope eventstream.Envelope) error {
		if eventstream.IsTurnTerminalLifecycle(envelope) && (envelope.Lifecycle.State == "failed" || envelope.Lifecycle.State == "unknown") && s.authorities.diagnostics != nil {
			// The command may already have a successful admission receipt. Record
			// the later execution failure with the original operation identity.
			// Peer reasons and errors can contain private content; classify only.
			s.authorities.diagnostics.WarnContext(ctx, "Control Turn failed",
				"session_id", ref.SessionID, "turn_id", envelope.TurnID, "operation_id", operationID,
				"state", envelope.Lifecycle.State, "code", errorcode.CodeOf(envelope.Err))
		}
		// Session delivery is a lossy observation aid. Validation, canonical
		// replay, or spool I/O failure must not cancel the producing Turn; clients
		// recover from Session truth on their next fresh attachment.
		_ = feed.Publish(envelope)
		return nil
	}), release
}

func retainControlTurn(handle kernel.TurnHandle, release func()) {
	if release == nil {
		release = func() {}
	}
	if handle == nil {
		release()
		return
	}
	go func() {
		defer release()
		_ = handle.WaitCompletion(context.Background())
	}()
}
