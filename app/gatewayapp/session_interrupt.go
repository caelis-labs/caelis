package gatewayapp

import (
	"context"
	"errors"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func (s *controlCommandBackend) interruptSession(ctx context.Context, req appserver.SessionInterruptRequest) (appserver.CommandResult, error) {
	active, err := s.interruptSessionTree(ctx, session.SessionRef{SessionID: req.SessionID}, &req.WriteBase, req.Reason, map[string]bool{})
	result := sessionCommandResult(active)
	if err != nil {
		return result, err
	}
	if feeds := s.composition.authorities.controlFeeds; feeds != nil {
		feed, feedErr := feeds.Session(active.SessionRef)
		if feedErr == nil {
			publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlFeedPublishTimeout)
			feedErr = feed.Prime(publishCtx)
			cancel()
		}
		if feedErr != nil {
			result.Detail = controlFeedCatchUpWarning
		}
	}
	return result, nil
}

// interruptSessionTree follows only SDK delegation endpoints whose local child
// Session confirms both its parent and Task identity. A remote ACP Session ID,
// shared workspace, or unrelated loaded Runtime grants no subtree ownership.
func (s *controlCommandBackend) interruptSessionTree(ctx context.Context, ref session.SessionRef, expected *appserver.WriteBase, reason string, visited map[string]bool) (session.Session, error) {
	ref = session.NormalizeSessionRef(ref)
	visited[ref.SessionID] = true
	composition := s.composition
	var unlockActivation func()
	if registry := s.runtimeRegistry(); registry != nil {
		// A dormant Session must not activate concurrently with reconciliation of
		// its orphaned Tasks. Loaded execution owners are retained independently.
		_, unlock, err := registry.lockActivation(ctx)
		if err != nil {
			return session.Session{}, classifyControlPreDispatchError(err)
		}
		unlockActivation = unlock
		defer func() {
			if unlockActivation != nil {
				unlockActivation()
			}
		}()
		loaded, release, err := registry.acquireLoadedRuntime(ref.SessionID, runtimeUseCommand)
		if isSessionRuntimeReleasingError(err) {
			if err = registry.waitSessionRelease(ctx, ref.SessionID); err != nil {
				return session.Session{}, classifyControlPreDispatchError(err)
			}
		} else if err != nil {
			return session.Session{}, classifyControlPreDispatchError(err)
		}
		if loaded != nil {
			defer release()
			composition = &loaded.instance.runtimeComposition
			unlockActivation()
			unlockActivation = nil
		} else {
			// Reconcile durable lifecycle through the SDK without assembling a
			// provider, plugins, sandbox, ACP connection, or resident Runtime.
			engine, err := runtime.New(runtime.Config{Sessions: s.composition.sessions, TaskStore: s.composition.authorities.taskStore, AgentFactory: chat.Factory{}})
			if err != nil {
				return session.Session{}, classifyControlPreDispatchError(err)
			}
			composition = &runtimeComposition{sessions: s.composition.sessions, engine: engine, authorities: s.composition.authorities}
		}
	}
	gw := composition.currentGateway()
	if gw != nil {
		unblock, err := gw.BlockSessionTurnAdmission(ref)
		if err != nil {
			return session.Session{}, classifyControlPreDispatchError(err)
		}
		defer unblock()
	}
	var active session.Session
	var err error
	if expected != nil {
		active, err = composition.checkControlCommandCAS(ctx, *expected)
	} else {
		active, err = composition.sessions.Session(ctx, ref)
	}
	if err != nil {
		return active, classifyControlBackendError(err)
	}
	if gw != nil {
		if turn, ok := gw.ActiveTurn(ref.SessionID); ok {
			err = gw.Interrupt(ctx, kernel.InterruptRequest{SessionRef: ref, HandleID: turn.HandleID, RunID: turn.RunID, TurnID: turn.TurnID, Kind: turn.Kind, ParticipantID: turn.ParticipantID, Reason: reason})
			var gatewayErr *kernel.Error
			if err != nil && (!kernel.As(err, &gatewayErr) || gatewayErr.Code != kernel.CodeNoActiveRun) {
				return active, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
			}
		}
	}
	var endpoints []agent.ChildEndpointRef
	var errs []error
	if composition.engine != nil {
		endpoints, err = composition.engine.CancelSessionSubagents(ctx, ref)
		errs = append(errs, err)
	}
	if mail := s.composition.authorities.collaboration; mail != nil {
		errs = append(errs, mail.InterruptSession(ctx, ref.SessionID))
	}
	if unlockActivation != nil {
		unlockActivation()
		unlockActivation = nil
	}
	for _, endpoint := range endpoints {
		if endpoint.SessionID == "" || endpoint.EndpointKey == "" || visited[endpoint.SessionID] {
			continue
		}
		child, err := composition.sessions.Session(ctx, session.SessionRef{SessionID: endpoint.SessionID})
		if errors.Is(err, session.ErrSessionNotFound) {
			continue // External ACP endpoint, with no local managed Session.
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !sessionvisibility.IsSpawnedSubagentSession(child) ||
			hostedChildMetadataString(child.Metadata, sessionvisibility.MetadataSystemManagedParent) != ref.SessionID ||
			hostedChildMetadataString(child.Metadata, sessionvisibility.MetadataSystemManagedTask) != endpoint.EndpointKey {
			continue
		}
		_, err = s.interruptSessionTree(ctx, child.SessionRef, nil, reason, visited)
		errs = append(errs, err)
	}
	latest, loadErr := composition.sessions.Session(ctx, ref)
	if loadErr == nil {
		active = latest
	}
	errs = append(errs, loadErr)
	if err := errors.Join(errs...); err != nil {
		return active, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	return active, nil
}
