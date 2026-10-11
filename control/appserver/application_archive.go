package appserver

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/application"
)

// Keep the admitted archive intent's serialized shape stable for existing IDs.
type applicationArchiveRequest struct {
	Action  string
	Request CloseSessionRequest
}

// CloseSession uses the canonical lifecycle for the exact scoped object.
// Application sessions retain Archive's durable receipt and binding update;
// shared Workers close through the native Session command owner.
func (s *ApplicationService) CloseSession(ctx context.Context, p Principal, req CloseSessionRequest) (CommandResult, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return CommandResult{}, err
	}
	if _, err := s.config.Store.Worker(ctx, scope, req.SessionID); err == nil {
		return s.config.Sessions.CloseSession(ctx, p, req)
	} else if !errors.Is(err, application.ErrNotFound) {
		return CommandResult{}, err
	}
	if bound, err := s.config.Store.GetBinding(ctx, scope, req.SessionID); err != nil {
		return CommandResult{}, err
	} else if bound.Child != nil {
		return s.config.Sessions.CloseSession(ctx, p, req)
	}
	return s.Archive(ctx, p, req)
}

// Archive closes canonical Session admission without deleting history/resources.
// Observation detach and application connection revocation never call Archive.
func (s *ApplicationService) Archive(ctx context.Context, p Principal, req CloseSessionRequest) (CommandResult, error) {
	scope, err := ApplicationScope(p)
	if err != nil {
		return CommandResult{}, err
	}
	if _, err = s.config.Store.GetBinding(ctx, scope, req.SessionID); err != nil {
		return CommandResult{}, err
	}
	// Persist the exact native receipt before touching the binding. A failed
	// archive write must not replace a proven close with an immutable unknown.
	out, err := s.execute(ctx, p, req.OperationID, applicationArchiveRequest{"archive", req}, func() (CommandResult, error) {
		return s.config.Sessions.CloseSession(ctx, p, req)
	})
	if err != nil {
		return out, err
	}
	if err = s.finishArchive(ctx, scope, req, out); err != nil {
		out.Outcome = OutcomeUnknown
		return out, err
	}
	return out, nil
}

// finishArchive only consumes an exact, committed native close receipt. Neither
// a closed Session nor an intent alone proves that this operation succeeded.
// It is safe to repeat after restart or race with the original completion.
func (s *ApplicationService) finishArchive(ctx context.Context, scope application.Scope, req CloseSessionRequest, result CommandResult) error {
	if result.Outcome != OutcomeCommitted {
		return nil
	}
	if result.OperationID != req.OperationID || result.SessionID != req.SessionID {
		return application.ErrConflict
	}
	completion, cancel := operationCompletionContext(ctx)
	defer cancel()
	if err := s.config.Store.ArchiveBinding(completion, scope, req.SessionID); err != nil {
		return errorcode.Wrap(errorcode.UnknownOutcome, "application: archive completion failed; query the original operation", err)
	}
	return nil
}
