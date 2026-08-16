package gatewayapp

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const sessionModelRecoveryMaxAttempts = 3

// prepareControlClientReconnect reconciles only an explicit reconnect. Plain
// InspectSession remains read-only, while reconnect returns the repaired
// revision that the following work-bearing command will use for CAS.
func (s *Stack) prepareControlClientReconnect(ctx context.Context, ref session.SessionRef) error {
	if s == nil || s.sessionRuntimes == nil || s.Sessions == nil || s.store == nil {
		return nil
	}
	buildCtx, unlock, err := s.sessionRuntimes.lockActivation(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if _, loaded := s.sessionRuntimes.loaded(ref.SessionID); loaded || s.sessionRuntimes.isReleasing(ref.SessionID) {
		return nil
	}
	active, err := s.Sessions.Session(buildCtx, ref)
	if err != nil {
		return err
	}
	closed, err := appserver.IsSessionClosed(buildCtx, s.Sessions, active.SessionRef)
	if err != nil {
		return err
	}
	if closed {
		return nil
	}
	_, err = s.repairMissingSessionModelSelection(buildCtx, active)
	return err
}

// repairMissingSessionModelSelection replaces one stale durable model
// reference from the current App store. It never scans other Sessions and a
// revision conflict always yields to the concurrent user mutation.
func (s *Stack) repairMissingSessionModelSelection(
	ctx context.Context,
	active session.Session,
) (session.Session, error) {
	if s == nil || s.Sessions == nil || s.store == nil {
		return active, nil
	}
	var conflictErr error
	for attempt := 0; attempt < sessionModelRecoveryMaxAttempts; attempt++ {
		if active.Controller.Kind == session.ControllerKindACP {
			return active, nil
		}
		state, err := s.Sessions.SnapshotState(ctx, active.SessionRef)
		if err != nil {
			return active, err
		}
		currentModel := strings.TrimSpace(kernel.CurrentModelAlias(state))
		if currentModel == "" {
			return active, nil
		}
		if pinned, ok := s.sessionModelPins.config(active.SessionID); ok && strings.EqualFold(pinned.ID, currentModel) {
			return active, nil
		}

		doc, err := s.store.LoadContext(ctx)
		if err != nil {
			return active, err
		}
		contextWindow := 0
		if s.lookup != nil {
			s.lookup.mu.RLock()
			contextWindow = s.lookup.contextWindow
			s.lookup.mu.RUnlock()
		}
		catalog, err := newModelLookupFromDocument(doc, contextWindow)
		if err != nil {
			return active, err
		}
		if catalog.HasAlias(currentModel) {
			return active, nil
		}

		fallbackID := strings.TrimSpace(catalog.DefaultID())
		if fallbackID == "" {
			if choices := catalog.ListModelChoices(); len(choices) > 0 {
				fallbackID = strings.TrimSpace(choices[0].ID)
			}
		}
		fallback, hasFallback := catalog.Config(fallbackID)
		currentEffort := modelcatalog.NormalizeReasoningEffort(kernel.CurrentReasoningEffort(state))
		expectedRevision := active.Revision
		updated, err := s.Sessions.UpdateState(ctx, session.UpdateStateRequest{
			SessionRef:       active.SessionRef,
			ExpectedRevision: &expectedRevision,
			MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
			Update: func(current map[string]any) (map[string]any, error) {
				next := session.CloneState(current)
				if next == nil {
					next = map[string]any{}
				}
				if !hasFallback {
					delete(next, kernel.StateCurrentModelAlias)
					delete(next, kernel.StateCurrentReasoningEffort)
					return next, nil
				}
				next[kernel.StateCurrentModelAlias] = fallback.ID
				if currentEffort == "" || !modelConfigSupportsReasoningEffort(fallback, currentEffort) {
					delete(next, kernel.StateCurrentReasoningEffort)
				} else {
					next[kernel.StateCurrentReasoningEffort] = currentEffort
				}
				return next, nil
			},
		})
		if err == nil {
			return updated, nil
		}
		if !errors.Is(err, session.ErrRevisionConflict) {
			return active, err
		}
		conflictErr = err
		active, err = s.Sessions.Session(ctx, active.SessionRef)
		if err != nil {
			return active, err
		}
	}
	return active, conflictErr
}
