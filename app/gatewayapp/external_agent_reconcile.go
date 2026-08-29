package gatewayapp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
)

const committedExternalAgentObserveTimeout = 10 * time.Second

type disconnectedACPFallback struct {
	profile   modelprofile.ModelProfile
	effort    string
	placement sdkplacement.Placement
}

func (s *controlCommandBackend) reconcileCommittedExternalAgents(ctx context.Context) error {
	if s == nil || s.composition == nil || s.composition.authorities.store == nil {
		return errors.New("gatewayapp: external Agent configuration is unavailable")
	}
	reconcileCtx, cancel := context.WithTimeout(
		context.WithoutCancel(contextOrBackground(ctx)),
		committedExternalAgentObserveTimeout,
	)
	defer cancel()
	_, err := s.composition.authorities.store.LoadContext(reconcileCtx)
	return err
}

// reconcileDisconnectedACPAgent makes the committed Host removal immediately
// authoritative for durable Sessions and every activated execution snapshot.
// It does not start dormant Runtimes merely to repair their stale bindings.
func (s *controlCommandBackend) reconcileDisconnectedACPAgent(
	ctx context.Context,
	agentID string,
	removedProfileIDs []string,
) error {
	if s == nil || s.composition == nil || s.composition.authorities.store == nil || s.composition.sessions == nil {
		return errors.New("gatewayapp: external Agent reconciliation is unavailable")
	}
	agentID = strings.TrimSpace(agentID)
	removed := make(map[string]struct{}, len(removedProfileIDs))
	for _, profileID := range removedProfileIDs {
		if profileID = modelprofile.NormalizeID(profileID); profileID != "" {
			removed[profileID] = struct{}{}
		}
	}

	// The configuration removal is already committed and cannot be rolled back.
	// Finish the local durable/live revocation even if the initiating client
	// disconnects; otherwise a timed-out scan could leave an ACP controller that
	// can no longer be selected or recovered.
	reconcileCtx := context.WithoutCancel(contextOrBackground(ctx))

	runtimes := s.runtimeRegistry()
	if runtimes != nil {
		var unlock func()
		var err error
		reconcileCtx, unlock, err = runtimes.lockActivation(reconcileCtx)
		if err != nil {
			return fmt.Errorf("gatewayapp: serialize external Agent reconciliation: %w", err)
		}
		defer unlock()
	}

	doc, err := s.composition.authorities.store.LoadContext(reconcileCtx)
	if err != nil {
		return fmt.Errorf("gatewayapp: read canonical external Agent configuration: %w", err)
	}
	nextPlacement := newPlacementSnapshot(doc)
	if err := controlplacement.ValidateSnapshot(nextPlacement.placement); err != nil {
		return fmt.Errorf("gatewayapp: validate canonical placement after ACP disconnect: %w", err)
	}
	fallback, err := resolveDisconnectedACPFallback(nextPlacement, doc.ModelProfiles)
	if err != nil {
		return err
	}

	// Publish the revoked catalog before detaching bindings so concurrent reads
	// cannot select the removed Agent for new work during reconciliation.
	var reconcileErr error
	if err := s.composition.applyDisconnectedACPAgent(doc, agentID); err != nil {
		reconcileErr = errors.Join(reconcileErr, fmt.Errorf("host Runtime: %w", err))
	}
	if runtimes != nil {
		for _, runtime := range runtimes.snapshot() {
			if runtime == nil || runtime.instance == nil {
				continue
			}
			if err := runtime.instance.applyDisconnectedACPAgent(doc, agentID); err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("session %q Runtime: %w", runtime.sessionID, err))
			}
		}
	}

	cursor := ""
	for {
		listed, err := s.composition.sessions.ListSessions(reconcileCtx, session.ListSessionsRequest{
			AppName: s.composition.authorities.appName,
			UserID:  s.composition.authorities.userID,
			Cursor:  cursor,
			Limit:   100,
		})
		if err != nil {
			return errors.Join(reconcileErr, fmt.Errorf("gatewayapp: list Sessions for external Agent reconciliation: %w", err))
		}
		for _, summary := range listed.Sessions {
			if err := s.reconcileDisconnectedACPAgentSession(reconcileCtx, runtimes, summary.SessionRef, agentID, removed, fallback, doc.ConfigurationRevision); err != nil {
				reconcileErr = errors.Join(reconcileErr, fmt.Errorf("Session %q: %w", summary.SessionID, err))
			}
		}
		cursor = strings.TrimSpace(listed.NextCursor)
		if cursor == "" {
			break
		}
	}

	observed, err := s.composition.authorities.store.LoadContext(reconcileCtx)
	if err != nil {
		return errors.Join(reconcileErr, fmt.Errorf("gatewayapp: verify external Agent reconciliation: %w", err))
	}
	if observed.ConfigurationRevision != doc.ConfigurationRevision {
		return errors.Join(reconcileErr, errors.New("gatewayapp: external Agent configuration changed during Session reconciliation"))
	}
	return reconcileErr
}

func resolveDisconnectedACPFallback(
	snapshot *placementSnapshot,
	profiles modelprofile.Configuration,
) (disconnectedACPFallback, error) {
	profiles = modelprofile.NormalizeConfiguration(profiles)
	profile, ok := modelprofile.Lookup(profiles, profiles.DefaultProfileID)
	if !ok {
		return disconnectedACPFallback{}, nil
	}
	fallback := disconnectedACPFallback{profile: profile, effort: profiles.DefaultEffort}
	if profile.Kind() != modelprofile.BackendACP {
		return fallback, nil
	}
	if snapshot == nil {
		return disconnectedACPFallback{}, errors.New("gatewayapp: ACP fallback placement is unavailable")
	}
	placed, err := controlplacement.ResolveProfile(snapshot.placement, profile.ID, fallback.effort)
	if err != nil {
		return disconnectedACPFallback{}, fmt.Errorf("gatewayapp: resolve ACP fallback profile %q: %w", profile.ID, err)
	}
	fallback.placement = placed
	return fallback, nil
}

func (s *controlCommandBackend) reconcileDisconnectedACPAgentSession(
	ctx context.Context,
	runtimes *sessionRuntimeRegistry,
	ref session.SessionRef,
	agentID string,
	removed map[string]struct{},
	fallback disconnectedACPFallback,
	configurationRevision uint64,
) error {
	active, err := s.composition.sessions.Session(ctx, ref)
	if err != nil {
		return err
	}
	closed, err := appserver.IsSessionClosed(ctx, s.composition.sessions, active.SessionRef)
	if err != nil {
		return err
	}
	if closed {
		return nil
	}
	if !controllerUsesDisconnectedACP(active.Controller, agentID, removed) &&
		!sessionHasDisconnectedACPParticipant(active, agentID, removed) {
		return nil
	}

	var runtime *sessionRuntime
	var releaseRuntime func()
	if runtimes != nil {
		runtime, releaseRuntime, err = runtimes.acquireLoadedRuntime(active.SessionID)
		if err != nil {
			return err
		}
		if releaseRuntime != nil {
			defer releaseRuntime()
		}
	}
	if runtime != nil && runtime.instance != nil && runtime.instance.currentGateway() != nil {
		gateway := runtime.instance.currentGateway()
		unblockTurns, blockErr := gateway.BlockSessionTurnAdmission(active.SessionRef)
		if blockErr != nil {
			return blockErr
		}
		defer unblockTurns()
		if err := interruptDisconnectedACPActiveTurn(ctx, gateway, active, agentID, removed); err != nil {
			return err
		}
	}

	for attempt := 0; attempt < sessionModelRecoveryMaxAttempts; attempt++ {
		active, err = s.composition.sessions.Session(ctx, active.SessionRef)
		if err != nil {
			return err
		}
		changed := false
		for _, binding := range active.Participants {
			if !participantUsesDisconnectedACP(binding, agentID, removed) {
				continue
			}
			if runtime != nil && runtime.instance != nil && runtime.instance.currentGateway() != nil {
				active, err = runtime.instance.currentGateway().DetachParticipant(ctx, kernel.DetachParticipantRequest{
					SessionRef:    active.SessionRef,
					ParticipantID: binding.ID,
					Source:        "external_agent_disconnect",
				})
			} else {
				active, err = s.removeDormantDisconnectedParticipant(ctx, active, binding, configurationRevision)
			}
			if err != nil {
				if errors.Is(err, session.ErrRevisionConflict) {
					break
				}
				return err
			}
			changed = true
		}
		if err != nil && errors.Is(err, session.ErrRevisionConflict) {
			continue
		}
		if controllerUsesDisconnectedACP(active.Controller, agentID, removed) {
			if runtime != nil && runtime.instance != nil && runtime.instance.currentGateway() != nil {
				active, err = handoffLoadedDisconnectedController(ctx, runtime.instance, active, fallback)
			} else {
				active, err = s.rebindDormantDisconnectedController(ctx, active, fallback, configurationRevision)
			}
			if err != nil {
				if errors.Is(err, session.ErrRevisionConflict) {
					continue
				}
				return err
			}
			changed = true
		}
		if !changed || (!controllerUsesDisconnectedACP(active.Controller, agentID, removed) &&
			!sessionHasDisconnectedACPParticipant(active, agentID, removed)) {
			return nil
		}
	}
	return fmt.Errorf("external Agent binding reconciliation did not converge: %w", session.ErrRevisionConflict)
}

func interruptDisconnectedACPActiveTurn(
	ctx context.Context,
	gateway *kernel.Gateway,
	active session.Session,
	agentID string,
	removed map[string]struct{},
) error {
	if gateway == nil {
		return nil
	}
	turn, ok := gateway.ActiveTurn(active.SessionID)
	if !ok {
		return nil
	}
	affected := controllerUsesDisconnectedACP(active.Controller, agentID, removed)
	if !affected && turn.Kind == kernel.ActiveTurnKindParticipant {
		for _, binding := range active.Participants {
			if strings.TrimSpace(binding.ID) == strings.TrimSpace(turn.ParticipantID) &&
				participantUsesDisconnectedACP(binding, agentID, removed) {
				affected = true
				break
			}
		}
	}
	if !affected {
		return nil
	}
	if err := gateway.CancelActiveTurnAndWait(ctx, turn); err != nil {
		return fmt.Errorf("cancel active Turn before ACP disconnect reconciliation: %w", err)
	}
	return nil
}

func handoffLoadedDisconnectedController(
	ctx context.Context,
	instance *sessionRuntimeInstance,
	active session.Session,
	fallback disconnectedACPFallback,
) (session.Session, error) {
	if instance == nil || instance.currentGateway() == nil {
		return session.Session{}, errors.New("gatewayapp: loaded Session Runtime is unavailable")
	}
	var finishPin func(bool)
	var err error
	switch fallback.profile.Kind() {
	case modelprofile.BackendACP:
		finishPin, err = instance.beginPinnedACPSelection(ctx, fallback.placement)
		if err != nil {
			return session.Session{}, err
		}
	case modelprofile.BackendProvider:
		if fallback.profile.Backend.Provider == nil || instance.lookup == nil {
			return session.Session{}, errors.New("gatewayapp: provider fallback model is unavailable")
		}
		catalog := instance.lookup
		if instance.activation != nil && instance.activation.modelCatalog != nil {
			catalog = instance.activation.modelCatalog
		}
		configured, ok := catalog.Config(fallback.profile.Backend.Provider.ModelConfigID)
		if !ok {
			return session.Session{}, fmt.Errorf(
				"gatewayapp: provider fallback model %q is unavailable",
				fallback.profile.Backend.Provider.ModelConfigID,
			)
		}
		configured.ReasoningEffort = fallback.effort
		finishPin, err = instance.beginPinnedModelSelection(ctx, configured)
		if err != nil {
			return session.Session{}, err
		}
	}
	req := kernel.HandoffControllerRequest{
		SessionRef:              active.SessionRef,
		ExpectedRevision:        &active.Revision,
		ExpectedControllerEpoch: active.Controller.EpochID,
		Kind:                    session.ControllerKindKernel,
		Source:                  "external_agent_disconnect",
		Reason:                  "active ACP model profile was disconnected",
		StateUpdate:             disconnectedACPStateUpdate(fallback),
	}
	if fallback.profile.Kind() == modelprofile.BackendACP {
		req.Kind = session.ControllerKindACP
		req.Agent = fallback.placement.Agent
		req.Placement = fallback.placement
	}
	updated, err := instance.currentGateway().HandoffController(ctx, req)
	if finishPin != nil {
		finishPin(err == nil || session.IsCommitted(err))
	}
	return updated, err
}

func (s *controlCommandBackend) rebindDormantDisconnectedController(
	ctx context.Context,
	active session.Session,
	fallback disconnectedACPFallback,
	configurationRevision uint64,
) (session.Session, error) {
	handoffs, ok := s.composition.sessions.(session.ControllerHandoffService)
	if !ok {
		return session.Session{}, errors.New("gatewayapp: Session store does not support atomic controller reconciliation")
	}
	next := disconnectedACPFallbackController(fallback, time.Now())
	protocol := session.NewHandoffProtocol(session.ProtocolHandoff{Phase: "activation"})
	event := &session.Event{
		IdempotencyKey: fmt.Sprintf("external-agent-disconnect-controller-%d-%s", configurationRevision, active.SessionID),
		Type:           session.EventTypeHandoff,
		Visibility:     session.VisibilityCanonical,
		Time:           time.Now(),
		Actor:          session.ActorRef{Kind: session.ActorKindSystem, Name: "control"},
		Text:           "model fallback to " + firstNonEmpty(next.Label, next.ControllerID),
		Protocol:       &protocol,
		Scope: &session.EventScope{
			Source: "external_agent_disconnect",
			Controller: session.ControllerRef{
				Kind: next.Kind, ID: next.ControllerID, EpochID: next.EpochID,
			},
		},
		Meta: map[string]any{
			"reason":          "active ACP model profile was disconnected",
			"from_profile_id": active.Controller.Placement.ProfileID,
			"to_profile_id":   next.Placement.ProfileID,
		},
	}
	expected := active.Revision
	updated, _, err := handoffs.BindControllerWithEvent(ctx, session.BindControllerWithEventRequest{
		SessionRef:       active.SessionRef,
		ExpectedRevision: &expected,
		MutationGuard:    session.ControlMutationGuard(session.ControlMutationPurposeConfiguration),
		TransactionID:    event.IdempotencyKey,
		MutationDigest:   "external-agent-disconnect-controller-state-v1",
		Binding:          next,
		Event:            event,
		UpdateState:      disconnectedACPStateUpdate(fallback),
	})
	return updated, err
}

func (s *controlCommandBackend) removeDormantDisconnectedParticipant(
	ctx context.Context,
	active session.Session,
	binding session.ParticipantBinding,
	configurationRevision uint64,
) (session.Session, error) {
	lifecycle, ok := s.composition.sessions.(session.ParticipantLifecycleService)
	if !ok {
		return session.Session{}, errors.New("gatewayapp: Session store does not support atomic participant reconciliation")
	}
	protocol := session.NewParticipantProtocol(session.ProtocolParticipant{Action: "detached"})
	event := &session.Event{
		IdempotencyKey: fmt.Sprintf("external-agent-disconnect-participant-%d-%s-%s", configurationRevision, active.SessionID, binding.ID),
		Type:           session.EventTypeParticipant,
		Visibility:     session.VisibilityCanonical,
		Time:           time.Now(),
		Actor:          session.ActorRef{Kind: session.ActorKindSystem, Name: "control"},
		Text:           "detached participant " + firstNonEmpty(binding.Label, binding.ID),
		Protocol:       &protocol,
		Scope: &session.EventScope{
			Source: "external_agent_disconnect",
			Controller: session.ControllerRef{
				Kind: active.Controller.Kind, ID: active.Controller.ControllerID, EpochID: active.Controller.EpochID,
			},
			Participant: session.ParticipantRef{
				ID: binding.ID, Kind: binding.Kind, Role: binding.Role, DelegationID: binding.DelegationID,
			},
		},
		Meta: map[string]any{
			"reason":     "external Agent was disconnected",
			"agent":      binding.AgentName,
			"profile_id": binding.Placement.ProfileID,
		},
	}
	expected := active.Revision
	delegationID := binding.DelegationID
	updated, _, err := lifecycle.RemoveParticipantWithEvent(ctx, session.RemoveParticipantWithEventRequest{
		SessionRef:           active.SessionRef,
		ExpectedRevision:     &expected,
		MutationGuard:        session.ControlMutationGuard(session.ControlMutationPurposeParticipant),
		ParticipantID:        binding.ID,
		ExpectedDelegationID: &delegationID,
		Event:                event,
	})
	return updated, err
}

func disconnectedACPFallbackController(fallback disconnectedACPFallback, now time.Time) session.ControllerBinding {
	if fallback.profile.Kind() == modelprofile.BackendACP {
		return dormantACPControllerBinding(fallback.placement, "external_agent_disconnect", now)
	}
	return session.ControllerBinding{
		Kind:         session.ControllerKindKernel,
		ControllerID: "sdk-kernel",
		AgentName:    "local",
		Label:        "SDK Kernel",
		EpochID:      "control-kernel-" + strings.ToLower(rand.Text()),
		AttachedAt:   now,
		Source:       "external_agent_disconnect",
	}
}

func disconnectedACPStateUpdate(fallback disconnectedACPFallback) session.AppendStateUpdate {
	return func(_ []*session.Event, current map[string]any) (map[string]any, error) {
		next := session.CloneState(current)
		if next == nil {
			next = map[string]any{}
		}
		delete(next, stateControllerConfigEpoch)
		delete(next, stateControllerConfigModel)
		delete(next, stateControllerConfigReasoning)
		delete(next, stateControllerConfigMode)
		delete(next, stateControllerConfigOperationID)
		if fallback.profile.Kind() == modelprofile.BackendProvider && fallback.profile.Backend.Provider != nil {
			next[kernel.StateCurrentModelAlias] = fallback.profile.Backend.Provider.ModelConfigID
			if fallback.effort == "" {
				delete(next, kernel.StateCurrentReasoningEffort)
			} else {
				next[kernel.StateCurrentReasoningEffort] = fallback.effort
			}
			return next, nil
		}
		delete(next, kernel.StateCurrentModelAlias)
		delete(next, kernel.StateCurrentReasoningEffort)
		return next, nil
	}
}

func controllerUsesDisconnectedACP(binding session.ControllerBinding, agentID string, removed map[string]struct{}) bool {
	if binding.Kind != session.ControllerKindACP {
		return false
	}
	if _, ok := removed[modelprofile.NormalizeID(binding.Placement.ProfileID)]; ok {
		return true
	}
	return sameACPAgent(agentID, binding.AgentName, binding.ControllerID, binding.Placement.Agent)
}

func sessionHasDisconnectedACPParticipant(active session.Session, agentID string, removed map[string]struct{}) bool {
	return slices.ContainsFunc(active.Participants, func(binding session.ParticipantBinding) bool {
		return participantUsesDisconnectedACP(binding, agentID, removed)
	})
}

func participantUsesDisconnectedACP(binding session.ParticipantBinding, agentID string, removed map[string]struct{}) bool {
	if binding.Kind != session.ParticipantKindACP && binding.Placement.Kind != sdkplacement.KindAgent {
		return false
	}
	if _, ok := removed[modelprofile.NormalizeID(binding.Placement.ProfileID)]; ok {
		return true
	}
	return sameACPAgent(agentID, binding.AgentName, binding.Placement.Agent)
}

func sameACPAgent(agentID string, candidates ...string) bool {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return false
	}
	return slices.ContainsFunc(candidates, func(candidate string) bool {
		return strings.EqualFold(strings.TrimSpace(candidate), agentID)
	})
}

func (s *runtimeComposition) applyDisconnectedACPAgent(doc AppConfig, agentID string) error {
	if s == nil {
		return nil
	}
	s.acpSelectionMu.Lock()
	defer s.acpSelectionMu.Unlock()
	nextPlacement := newPlacementSnapshot(doc)
	if err := controlplacement.ValidateSnapshot(nextPlacement.placement); err != nil {
		return err
	}
	profiles := modelprofile.NormalizeConfiguration(doc.ModelProfiles)
	profile, _ := modelprofile.Lookup(profiles, profiles.DefaultProfileID)
	s.mu.Lock()
	nextBaseAgents := removeAssemblyACPAgent(s.activeRuntime.BaseAssembly.Agents, agentID)
	nextAgents := removeAssemblyACPAgent(s.activeRuntime.Assembly.Agents, agentID)
	if s.acpControlPlane != nil {
		// The shared registry is the execution authority for both main ACP
		// controllers and participants. Revoke it before publishing the
		// presentation and placement snapshots.
		if err := s.acpControlPlane.UpdateAgents(nextAgents); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	s.activeRuntime.BaseAssembly.Agents = nextBaseAgents
	s.activeRuntime.Assembly.Agents = nextAgents
	if current := modelprofile.NormalizeID(s.activeRuntime.ModelProfileID); current != "" {
		if _, ok := modelprofile.Lookup(profiles, current); !ok {
			s.activeRuntime.ModelProfileID = profile.ID
			s.activeRuntime.ModelProfileEffort = profiles.DefaultEffort
			s.activeRuntime.Model = ModelConfig{}
			if profile.Kind() == modelprofile.BackendProvider && profile.Backend.Provider != nil && s.lookup != nil {
				s.activeRuntime.Model, _ = s.lookup.Config(profile.Backend.Provider.ModelConfigID)
				s.activeRuntime.Model.ReasoningEffort = profiles.DefaultEffort
			}
		}
	}
	s.mu.Unlock()

	s.placementCacheMu.Lock()
	s.placementCache = nextPlacement
	s.placementCacheGeneration++
	s.placementCacheMu.Unlock()

	if s.process != nil {
		s.setRuntimeDefaultProfile(profiles)
	}
	return nil
}

func removeAssemblyACPAgent(agents []assembly.AgentConfig, agentID string) []assembly.AgentConfig {
	filtered := make([]assembly.AgentConfig, 0, len(agents))
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), strings.TrimSpace(agentID)) {
			continue
		}
		filtered = append(filtered, agent)
	}
	return filtered
}
