package gatewayapp

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const sessionModelRecoveryMaxAttempts = 3

// sessionModelRecovery owns dormant Session model-reference reconciliation.
// It depends only on the durable Session/config authorities and model catalog
// inputs required by the revision-guarded repair; Runtime activation and Host
// composition remain outside this component.
type sessionModelRecovery struct {
	store  *appConfigStore
	pins   *sessionModelPinRegistry
	lookup *modelLookup
}

func newSessionModelRecovery(
	store *appConfigStore,
	pins *sessionModelPinRegistry,
	lookup *modelLookup,
) *sessionModelRecovery {
	return &sessionModelRecovery{
		store:  store,
		pins:   pins,
		lookup: lookup,
	}
}

// prepareControlClientReconnect reconciles only an explicit reconnect. Plain
// InspectSession remains read-only, while reconnect returns the repaired
// revision that the following work-bearing command will use for CAS.
func (s *controlCommandBackend) prepareControlClientReconnect(ctx context.Context, ref session.SessionRef) error {
	runtimes := s.runtimeRegistry()
	if s == nil || runtimes == nil || s.modelRecovery == nil || s.composition == nil {
		return nil
	}
	buildCtx, unlock, err := runtimes.lockActivation(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if _, loaded := runtimes.loaded(ref.SessionID); loaded || runtimes.isReleasing(ref.SessionID) {
		return nil
	}
	sessions := s.composition.sessions
	if sessions == nil {
		return nil
	}
	active, err := sessions.Session(buildCtx, ref)
	if err != nil {
		return err
	}
	closed, err := appserver.IsSessionClosed(buildCtx, sessions, active.SessionRef)
	if err != nil {
		return err
	}
	if closed {
		return nil
	}
	_, err = s.modelRecovery.repairMissingSessionModelSelection(buildCtx, sessions, active)
	return err
}

// repairMissingSessionModelSelection replaces one stale durable model
// reference from the current App store. It never scans other Sessions and a
// revision conflict always yields to the concurrent user mutation.
func (r *sessionModelRecovery) repairMissingSessionModelSelection(
	ctx context.Context,
	sessions session.Service,
	active session.Session,
) (session.Session, error) {
	if r == nil || sessions == nil || r.store == nil {
		return active, nil
	}
	var conflictErr error
	for attempt := 0; attempt < sessionModelRecoveryMaxAttempts; attempt++ {
		if active.Controller.Kind == session.ControllerKindACP {
			return active, nil
		}
		state, err := sessions.SnapshotState(ctx, active.SessionRef)
		if err != nil {
			return active, err
		}
		currentModel := strings.TrimSpace(kernel.CurrentModelAlias(state))
		if currentModel == "" {
			return active, nil
		}
		doc, err := r.store.LoadContext(ctx)
		if err != nil {
			return active, err
		}
		contextWindow := 0
		if r.lookup != nil {
			r.lookup.mu.RLock()
			contextWindow = r.lookup.contextWindow
			r.lookup.mu.RUnlock()
		}
		catalog, err := newModelLookupFromDocument(doc, contextWindow)
		if err != nil {
			return active, err
		}
		if r.pins != nil {
			r.pins.syncConfiguredModels(catalog.Snapshot().Configs)
		}
		currentConfig, currentConfigured := catalog.Config(currentModel)
		currentEffort := modelcatalog.NormalizeReasoningEffort(kernel.CurrentReasoningEffort(state))
		currentFastMode, fastModePresent := kernel.CurrentModelFastMode(state)
		if currentConfigured && fastModePresent &&
			(currentEffort == "" || modelConfigSupportsReasoningEffort(currentConfig, currentEffort)) &&
			(!currentFastMode || modelconfig.SupportsSpeedMode(currentConfig, "fast")) {
			return active, nil
		}

		fallback, hasFallback := currentConfig, currentConfigured
		if !hasFallback {
			fallbackID := strings.TrimSpace(catalog.DefaultID())
			if fallbackID == "" {
				if choices := catalog.ListModelChoices(); len(choices) > 0 {
					fallbackID = strings.TrimSpace(choices[0].ID)
				}
			}
			fallback, hasFallback = catalog.Config(fallbackID)
		}
		expectedRevision := active.Revision
		updated, err := sessions.UpdateState(ctx, session.UpdateStateRequest{
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
					delete(next, kernel.StateCurrentModelFastMode)
					return next, nil
				}
				next[kernel.StateCurrentModelAlias] = fallback.ID
				next[kernel.StateCurrentModelFastMode] = false
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
		active, err = sessions.Session(ctx, active.SessionRef)
		if err != nil {
			return active, err
		}
	}
	return active, conflictErr
}
