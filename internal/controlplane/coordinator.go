package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// CoordinatorConfig wires product-owned controller handoff coordination to
// neutral SDK endpoint and participant mechanisms.
type CoordinatorConfig struct {
	Sessions              session.Service
	Controllers           controller.Backend
	Context               controller.ContextRouter
	Clock                 func() time.Time
	IDGenerator           func() string
	LifecycleInterceptors []agent.LifecycleInterceptor
	TraceSink             agent.TraceSink
}

// Coordinator owns controller activation/deactivation and the atomic durable
// binding/event commit. Participant execution is delegated to the SDK's
// neutral participant mechanism.
type Coordinator struct {
	sessions    session.Service
	controllers controller.Backend
	context     controller.ContextRouter
	clock       func() time.Time
	idGenerator func() string
	nextID      atomic.Uint64
	lifecycle   agent.LifecycleOptions
}

// NewCoordinator constructs one Control-owned session coordinator.
func NewCoordinator(cfg CoordinatorConfig) (*Coordinator, error) {
	if cfg.Sessions == nil {
		return nil, fmt.Errorf("controlplane: sessions service is required")
	}
	if cfg.Context == nil {
		return nil, fmt.Errorf("controlplane: context router is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Coordinator{
		sessions:    cfg.Sessions,
		controllers: cfg.Controllers,
		context:     cfg.Context,
		clock:       cfg.Clock,
		idGenerator: cfg.IDGenerator,
		lifecycle: agent.LifecycleOptions{
			Interceptors: append([]agent.LifecycleInterceptor(nil), cfg.LifecycleInterceptors...),
			TraceSink:    cfg.TraceSink,
			Clock:        cfg.Clock,
		},
	}, nil
}

// ReattachController restores one persisted ACP controller process and
// refreshes its durable binding before Runtime retries the interrupted turn.
func (c *Coordinator) ReattachController(ctx context.Context, req controller.RecoveryRequest) (session.Session, error) {
	if c == nil || c.controllers == nil {
		return session.Session{}, fmt.Errorf("controlplane: ACP controller backend is not configured")
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession := session.CloneSession(req.Session)
	if activeSession.SessionID == "" {
		var err error
		activeSession, err = c.sessions.Session(ctx, ref)
		if err != nil {
			return session.Session{}, err
		}
	}
	binding := session.CloneControllerBinding(activeSession.Controller)
	if binding.Kind != session.ControllerKindACP {
		return session.Session{}, fmt.Errorf("controlplane: session %q is not ACP-controlled", ref.SessionID)
	}
	agentName := firstNonEmpty(binding.AgentName, binding.ControllerID, binding.Label)
	if agentName == "" {
		return session.Session{}, fmt.Errorf("controlplane: persisted ACP controller has no agent identity")
	}
	route, err := c.context.ControllerContext(ctx, controller.ControllerContextRequest{
		SessionRef:    ref,
		Session:       activeSession,
		Controller:    binding,
		SinceSeq:      binding.ContextSyncSeq,
		ExcludeTurnID: strings.TrimSpace(req.ExcludeTurnID),
	})
	if err != nil {
		return session.Session{}, err
	}
	attached, err := c.controllers.Activate(ctx, controller.HandoffRequest{
		SessionRef:     ref,
		Session:        activeSession,
		Agent:          agentName,
		Source:         "controller_rehydrate",
		Reason:         "controller process rehydrate",
		ContextPrelude: strings.TrimSpace(route.Prelude),
		ContextSyncSeq: route.SyncSeq,
	})
	if err != nil {
		return session.Session{}, err
	}
	updated, err := c.sessions.BindController(ctx, session.BindControllerRequest{SessionRef: ref, Binding: attached})
	if err != nil {
		cleanupErr := c.controllers.Deactivate(context.WithoutCancel(ctx), ref)
		return session.Session{}, errors.Join(err, cleanupErr)
	}
	return updated, nil
}

// HandoffController authorizes no transition by itself; it executes a handoff
// already selected by the calling user or Control policy.
func (c *Coordinator) HandoffController(ctx context.Context, req agent.HandoffControllerRequest) (session.Session, error) {
	var result session.Session
	event := agent.LifecycleEvent{
		Operation:  agent.LifecycleHandoff,
		SessionRef: session.NormalizeSessionRef(req.SessionRef),
		Name:       strings.TrimSpace(req.Agent),
	}
	err := agent.ExecuteLifecycle(ctx, event, c.lifecycle, func(callCtx context.Context) error {
		var handoffErr error
		result, handoffErr = c.handoffController(callCtx, req)
		return handoffErr
	})
	return result, err
}

func (c *Coordinator) handoffController(ctx context.Context, req agent.HandoffControllerRequest) (session.Session, error) {
	if c == nil || c.sessions == nil {
		return session.Session{}, fmt.Errorf("controlplane: coordinator is unavailable")
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	activeSession, err := c.sessions.Session(ctx, ref)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err = c.ensureSessionController(ctx, activeSession)
	if err != nil {
		return session.Session{}, err
	}
	from := session.CloneControllerBinding(activeSession.Controller)
	kind := req.Kind
	if kind == "" {
		kind = session.ControllerKindKernel
	}
	var to session.ControllerBinding
	switch kind {
	case session.ControllerKindACP:
		if c.controllers == nil {
			return session.Session{}, fmt.Errorf("controlplane: ACP controller backend is not configured")
		}
		sinceSeq := 0
		if from.Kind == session.ControllerKindACP && sameControllerAgent(from, req.Agent) {
			sinceSeq = from.ContextSyncSeq
		}
		route, routeErr := c.context.ControllerContext(ctx, controller.ControllerContextRequest{
			SessionRef: ref,
			Session:    activeSession,
			Controller: from,
			SinceSeq:   sinceSeq,
		})
		if routeErr != nil {
			return session.Session{}, routeErr
		}
		to, err = c.controllers.Activate(ctx, controller.HandoffRequest{
			SessionRef:     ref,
			Session:        activeSession,
			Agent:          strings.TrimSpace(req.Agent),
			Source:         strings.TrimSpace(req.Source),
			Reason:         strings.TrimSpace(req.Reason),
			ContextPrelude: strings.TrimSpace(route.Prelude),
			ContextSyncSeq: route.SyncSeq,
		})
		if err != nil {
			return session.Session{}, err
		}
	default:
		if c.controllers != nil && from.Kind == session.ControllerKindACP {
			if err := c.controllers.Deactivate(ctx, ref); err != nil {
				return session.Session{}, err
			}
		}
		to = c.kernelControllerBinding(firstNonEmpty(strings.TrimSpace(req.Source), "handoff"))
	}

	handoffs, ok := c.sessions.(session.ControllerHandoffService)
	if !ok {
		return session.Session{}, fmt.Errorf("controlplane: session service must support atomic controller handoff")
	}
	expected := activeSession.Revision
	activeSession, _, err = handoffs.BindControllerWithEvent(ctx, session.BindControllerWithEventRequest{
		SessionRef:       ref,
		ExpectedRevision: &expected,
		Binding:          to,
		Event:            handoffEvent(from, to, strings.TrimSpace(req.Reason), c.clock()),
	})
	if err != nil && c.controllers != nil {
		cleanupErr := error(nil)
		if kind == session.ControllerKindACP {
			cleanupErr = c.controllers.Deactivate(context.WithoutCancel(ctx), ref)
		}
		if from.Kind == session.ControllerKindACP {
			rollbackErr := c.reactivatePreviousController(context.WithoutCancel(ctx), ref, activeSession, from)
			cleanupErr = errors.Join(cleanupErr, rollbackErr)
		}
		return session.Session{}, errors.Join(err, cleanupErr)
	}
	return activeSession, err
}

func (c *Coordinator) reactivatePreviousController(ctx context.Context, ref session.SessionRef, activeSession session.Session, from session.ControllerBinding) error {
	agentName := firstNonEmpty(from.AgentName, from.ControllerID, from.Label)
	if agentName == "" {
		return fmt.Errorf("controlplane: cannot roll back ACP controller without an agent identity")
	}
	route, err := c.context.ControllerContext(ctx, controller.ControllerContextRequest{
		SessionRef: ref,
		Session:    activeSession,
		Controller: from,
		SinceSeq:   from.ContextSyncSeq,
	})
	if err != nil {
		return err
	}
	_, err = c.controllers.Activate(ctx, controller.HandoffRequest{
		SessionRef:     ref,
		Session:        activeSession,
		Agent:          agentName,
		Source:         "handoff_rollback",
		Reason:         "durable handoff commit failed",
		ContextPrelude: strings.TrimSpace(route.Prelude),
		ContextSyncSeq: route.SyncSeq,
	})
	return err
}

func (c *Coordinator) ensureSessionController(ctx context.Context, activeSession session.Session) (session.Session, error) {
	if activeSession.Controller.Kind != "" {
		return session.CloneSession(activeSession), nil
	}
	return c.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding:    c.kernelControllerBinding("control"),
	})
}

func (c *Coordinator) kernelControllerBinding(source string) session.ControllerBinding {
	epochID := ""
	if c.idGenerator != nil {
		epochID = strings.TrimSpace(c.idGenerator())
	}
	if epochID == "" {
		epochID = fmt.Sprintf("control-kernel-%d", c.nextID.Add(1))
	}
	return session.ControllerBinding{
		Kind:         session.ControllerKindKernel,
		ControllerID: "sdk-kernel",
		AgentName:    "local",
		Label:        "SDK Kernel",
		EpochID:      epochID,
		AttachedAt:   c.clock(),
		Source:       firstNonEmpty(strings.TrimSpace(source), "control"),
	}
}

func sameControllerAgent(binding session.ControllerBinding, agentName string) bool {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return false
	}
	for _, candidate := range []string{binding.AgentName, binding.Label, binding.ControllerID} {
		if strings.EqualFold(strings.TrimSpace(candidate), agentName) {
			return true
		}
	}
	return false
}

func handoffEvent(from session.ControllerBinding, to session.ControllerBinding, reason string, now time.Time) *session.Event {
	meta := map[string]any{
		"from": map[string]any{
			"kind": from.Kind, "id": strings.TrimSpace(from.ControllerID), "agent": strings.TrimSpace(from.AgentName),
			"remote_session_id": strings.TrimSpace(from.RemoteSessionID), "context_sync_seq": from.ContextSyncSeq,
		},
		"to": map[string]any{
			"kind": to.Kind, "id": strings.TrimSpace(to.ControllerID), "agent": strings.TrimSpace(to.AgentName),
			"remote_session_id": strings.TrimSpace(to.RemoteSessionID), "context_sync_seq": to.ContextSyncSeq,
		},
	}
	if reason = strings.TrimSpace(reason); reason != "" {
		meta["reason"] = reason
	}
	return &session.Event{
		Type:       session.EventTypeHandoff,
		Visibility: session.VisibilityCanonical,
		Time:       now,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "control"},
		Text:       "handoff to " + firstNonEmpty(to.Label, to.ControllerID),
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodControllerHandoff,
			Update: &session.ProtocolUpdate{SessionUpdate: "activation"},
		},
		Scope: &session.EventScope{
			Source: "handoff",
			Controller: session.ControllerRef{
				Kind: to.Kind, ID: strings.TrimSpace(to.ControllerID), EpochID: strings.TrimSpace(to.EpochID),
			},
		},
		Meta: meta,
	}
}
