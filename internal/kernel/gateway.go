package kernel

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

type Config struct {
	Sessions         session.Service
	Runtime          agent.Runtime
	Resolver         TurnResolver
	RequestPolicy    RequestPolicy
	ApprovalApprover approval.Approver
	ApprovalReviewer ApprovalReviewer
	Clock            func() time.Time
}

type Gateway struct {
	sessions         session.Service
	runtime          agent.Runtime
	control          agent.SessionControlPlane
	resolver         TurnResolver
	request          RequestPolicy
	approvalApprover approval.Approver
	approvalReviewer ApprovalReviewer
	clock            func() time.Time

	mu       sync.Mutex
	active   map[string]*turnHandle
	bindings map[string]sessionBinding
	nextID   atomic.Uint64
}

type sessionBinding struct {
	current         session.SessionRef
	surface         string
	actorKind       string
	actorID         string
	owner           string
	boundAt         time.Time
	updatedAt       time.Time
	expiresAt       time.Time
	lastHandleID    string
	lastRunID       string
	lastTurnID      string
	lastEventCursor string
}

func New(cfg Config) (*Gateway, error) {
	if cfg.Sessions == nil {
		return nil, fmt.Errorf("gateway: sessions service is required")
	}
	if cfg.Runtime == nil {
		return nil, fmt.Errorf("gateway: runtime is required")
	}
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("gateway: turn resolver is required")
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.RequestPolicy == nil {
		cfg.RequestPolicy = defaultRequestPolicy{}
	}
	if cfg.ApprovalApprover == nil {
		if cfg.ApprovalReviewer != nil {
			cfg.ApprovalApprover = approval.ReviewerAdapter{Reviewer: cfg.ApprovalReviewer}
		} else {
			cfg.ApprovalApprover = denyingApprovalApprover{}
		}
	}
	if cfg.ApprovalReviewer == nil {
		cfg.ApprovalReviewer = approval.ApproverAdapter{Approver: cfg.ApprovalApprover}
	}
	return &Gateway{
		sessions:         cfg.Sessions,
		runtime:          cfg.Runtime,
		control:          resolveControlPlane(cfg.Runtime),
		resolver:         cfg.Resolver,
		request:          cfg.RequestPolicy,
		approvalApprover: cfg.ApprovalApprover,
		approvalReviewer: cfg.ApprovalReviewer,
		clock:            cfg.Clock,
		active:           map[string]*turnHandle{},
		bindings:         map[string]sessionBinding{},
	}, nil
}

func resolveControlPlane(runtime agent.Runtime) agent.SessionControlPlane {
	if control, ok := runtime.(agent.SessionControlPlane); ok {
		return control
	}
	return nil
}

func (g *Gateway) Streams() stream.Service {
	if g == nil || g.runtime == nil {
		return nil
	}
	provider, ok := g.runtime.(agent.StreamProvider)
	if !ok {
		return nil
	}
	return provider.Streams()
}

// Resolver returns the underlying *AssemblyResolver if the gateway's
// TurnResolver is one. Returns nil otherwise.
func (g *Gateway) Resolver() *AssemblyResolver {
	if g == nil {
		return nil
	}
	r, _ := g.resolver.(*AssemblyResolver)
	return r
}

// ApprovalReviewer returns the reviewer configured for automatic approval
// decisions so non-gateway surfaces can reuse the same policy bridge.
func (g *Gateway) ApprovalReviewer() ApprovalReviewer {
	if g == nil {
		return nil
	}
	return g.approvalReviewer
}

func (g *Gateway) StartSession(ctx context.Context, req StartSessionRequest) (session.Session, error) {
	activeSession, err := g.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            req.AppName,
		UserID:             req.UserID,
		Workspace:          req.Workspace,
		PreferredSessionID: req.PreferredSessionID,
		Title:              req.Title,
		Metadata:           cloneMap(req.Metadata),
	})
	if err != nil {
		return session.Session{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, activeSession.SessionRef, req.Binding)
	return activeSession, nil
}

func (g *Gateway) BindSession(ctx context.Context, req BindSessionRequest) error {
	ref, err := g.sessionTarget(req.SessionRef, "")
	if err != nil {
		return err
	}
	if _, err := g.sessions.Session(ctx, ref); err != nil {
		return wrapSessionError(err)
	}
	g.bind(req.BindingKey, ref, req.Binding)
	return nil
}

func (g *Gateway) ForkSession(ctx context.Context, req ForkSessionRequest) (session.Session, error) {
	if strings.TrimSpace(req.SourceSessionRef.SessionID) == "" {
		return session.Session{}, &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: source session ref is required",
		}
	}
	source, err := g.sessions.Session(ctx, req.SourceSessionRef)
	if err != nil {
		return session.Session{}, wrapSessionError(err)
	}
	metadata := cloneMap(source.Metadata)
	for key, value := range req.Metadata {
		metadata[key] = value
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["forked_from_session_id"] = source.SessionID
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = source.Title
	}
	started, err := g.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            source.AppName,
		UserID:             source.UserID,
		Workspace:          session.WorkspaceRef{Key: source.WorkspaceKey, CWD: source.CWD},
		PreferredSessionID: req.PreferredSessionID,
		Title:              title,
		Metadata:           metadata,
	})
	if err != nil {
		return session.Session{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, started.SessionRef, req.Binding)
	return started, nil
}

func (g *Gateway) LoadSession(ctx context.Context, req LoadSessionRequest) (session.LoadedSession, error) {
	loaded, err := g.sessions.LoadSession(ctx, session.LoadSessionRequest{
		SessionRef:       req.SessionRef,
		Limit:            req.Limit,
		IncludeTransient: req.IncludeTransient,
	})
	if err != nil {
		return session.LoadedSession{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, loaded.Session.SessionRef, req.Binding)
	return loaded, nil
}

func (g *Gateway) ResumeSession(ctx context.Context, req ResumeSessionRequest) (session.LoadedSession, error) {
	list, err := g.ListSessions(ctx, ListSessionsRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.Workspace.Key,
		Limit:        200,
	})
	if err != nil {
		return session.LoadedSession{}, err
	}
	target, err := g.resolveResumeTarget(req, list.Sessions)
	if err != nil {
		return session.LoadedSession{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}
	return g.LoadSession(ctx, LoadSessionRequest{
		SessionRef:       target.SessionRef,
		Limit:            limit,
		IncludeTransient: req.IncludeTransient,
		BindingKey:       req.BindingKey,
		Binding:          req.Binding,
	})
}

func (g *Gateway) ListSessions(ctx context.Context, req ListSessionsRequest) (session.SessionList, error) {
	list, err := g.sessions.ListSessions(ctx, session.ListSessionsRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.WorkspaceKey,
		Cursor:       req.Cursor,
		Limit:        req.Limit,
	})
	if err != nil {
		return session.SessionList{}, wrapSessionError(err)
	}
	return list, nil
}

func (g *Gateway) Interrupt(ctx context.Context, req InterruptRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ref, err := g.interruptTarget(req)
	if err != nil {
		return err
	}
	g.mu.Lock()
	handle, ok := g.active[ref.SessionID]
	g.mu.Unlock()
	if !ok || handle == nil {
		return &Error{
			Kind:        KindConflict,
			Code:        CodeNoActiveRun,
			UserVisible: true,
			Message:     "gateway: session has no active run",
		}
	}
	if !handle.Cancel().Cancelled() {
		return &Error{
			Kind:        KindConflict,
			Code:        CodeNoActiveRun,
			UserVisible: true,
			Message:     "gateway: session has no active run",
		}
	}
	return nil
}

func (g *Gateway) HandoffController(ctx context.Context, req HandoffControllerRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err := g.control.HandoffController(ctx, agent.HandoffControllerRequest{
		SessionRef: ref,
		Kind:       req.Kind,
		Agent:      strings.TrimSpace(req.Agent),
		Source:     strings.TrimSpace(req.Source),
		Reason:     strings.TrimSpace(req.Reason),
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) AttachParticipant(ctx context.Context, req AttachParticipantRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err := g.control.AttachACPParticipant(ctx, agent.AttachACPParticipantRequest{
		SessionRef: ref,
		Agent:      strings.TrimSpace(req.Agent),
		Role:       req.Role,
		Source:     strings.TrimSpace(req.Source),
		Label:      strings.TrimSpace(req.Label),
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) DetachParticipant(ctx context.Context, req DetachParticipantRequest) (session.Session, error) {
	if g.control == nil {
		return session.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return session.Session{}, err
	}
	activeSession, err := g.control.DetachACPParticipant(ctx, agent.DetachACPParticipantRequest{
		SessionRef:    ref,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Source:        strings.TrimSpace(req.Source),
	})
	if err != nil {
		return session.Session{}, err
	}
	g.bind(req.BindingKey, activeSession.SessionRef, BindingDescriptor{})
	return activeSession, nil
}

func (g *Gateway) PromptParticipant(ctx context.Context, req PromptParticipantRequest) (BeginTurnResult, error) {
	if g.control == nil {
		return BeginTurnResult{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return BeginTurnResult{}, err
	}
	session, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return BeginTurnResult{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, session.SessionRef, BindingDescriptor{})
	runCtx, cancel := context.WithCancel(ctx)
	cancelFn := sync.OnceValue(func() bool {
		cancel()
		return true
	})
	g.mu.Lock()
	if _, ok := g.active[session.SessionID]; ok {
		g.mu.Unlock()
		return BeginTurnResult{}, &Error{
			Kind:        KindConflict,
			Code:        CodeActiveRunConflict,
			UserVisible: true,
			Message:     "gateway: session already has an active run",
		}
	}
	handle := newTurnHandle(turnHandleConfig{
		handleID:                g.allocateID("handle"),
		runID:                   g.allocateID("participant-run"),
		turnID:                  g.allocateID("participant-turn"),
		activeKind:              ActiveTurnKindParticipant,
		sessionRef:              session.SessionRef,
		createdAt:               g.clock(),
		allowPendingSubmissions: true,
		cancel: func() bool {
			return cancelFn()
		},
	})
	g.active[session.SessionID] = handle
	g.noteActiveHandleLocked(session.SessionID, handle)
	g.mu.Unlock()

	go g.runParticipantTurn(runCtx, session, req, handle)

	return BeginTurnResult{
		Session: session,
		Handle:  handle,
	}, nil
}

func (g *Gateway) ControlPlaneState(ctx context.Context, req ControlPlaneStateRequest) (ControlPlaneState, error) {
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return ControlPlaneState{}, err
	}
	activeSession, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return ControlPlaneState{}, wrapSessionError(err)
	}
	events, err := g.sessions.Events(ctx, session.EventsRequest{
		SessionRef: ref,
	})
	if err != nil {
		return ControlPlaneState{}, wrapSessionError(err)
	}
	runState, err := g.runtime.RunState(ctx, ref)
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return ControlPlaneState{}, err
	}
	return buildControlPlaneState(activeSession, runState, events), nil
}

func (g *Gateway) ReplayEvents(ctx context.Context, req ReplayEventsRequest) (ReplayEventsResult, error) {
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	activeSession, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return ReplayEventsResult{}, wrapSessionError(err)
	}
	events, err := g.sessions.Events(ctx, session.EventsRequest{
		SessionRef:       ref,
		Limit:            0,
		IncludeTransient: true,
	})
	if err != nil {
		return ReplayEventsResult{}, wrapSessionError(err)
	}
	if err := validateReplaySessionEvents(events); err != nil {
		return ReplayEventsResult{}, err
	}
	replayEvents := replayTranscriptEvents(events, req.IncludeTransient)
	controlEvents := replayControlPlaneEvents(events, req.IncludeTransient)
	runState, err := g.runtime.RunState(ctx, ref)
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return ReplayEventsResult{}, err
	}
	projected := projectSessionEvents(ref, replayEvents)
	projected, err = replayAfterCursor(projected, req.Cursor, req.Limit)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	out := ReplayEventsResult{
		SessionRef:    ref,
		Events:        projected,
		NextCursor:    lastCursor(projected),
		Durable:       true,
		HasLiveHandle: g.hasActiveHandle(ref.SessionID),
		ControlPlane:  buildControlPlaneState(activeSession, runState, controlEvents),
	}
	return out, nil
}

func (g *Gateway) LookupBinding(req BindingStateRequest) (BindingState, error) {
	if strings.TrimSpace(req.BindingKey) == "" {
		return BindingState{}, &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: binding key is required",
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	binding, ok := g.bindingLocked(strings.TrimSpace(req.BindingKey))
	if !ok {
		return BindingState{}, &Error{
			Kind:        KindNotFound,
			Code:        CodeBindingNotFound,
			UserVisible: true,
			Message:     "gateway: binding not found",
		}
	}
	state := BindingState{
		BindingKey:      strings.TrimSpace(req.BindingKey),
		SessionRef:      binding.current,
		Surface:         binding.surface,
		ActorKind:       binding.actorKind,
		ActorID:         binding.actorID,
		Owner:           binding.owner,
		BoundAt:         binding.boundAt,
		UpdatedAt:       binding.updatedAt,
		ExpiresAt:       binding.expiresAt,
		LastHandleID:    binding.lastHandleID,
		LastRunID:       binding.lastRunID,
		LastTurnID:      binding.lastTurnID,
		LastEventCursor: binding.lastEventCursor,
	}
	if active, ok := g.active[binding.current.SessionID]; ok && active != nil {
		state.HasActiveTurn = true
		state.LastHandleID = active.HandleID()
		state.LastRunID = active.RunID()
		state.LastTurnID = active.TurnID()
	}
	return state, nil
}

func (g *Gateway) BeginTurn(ctx context.Context, req BeginTurnRequest) (BeginTurnResult, error) {
	session, err := g.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return BeginTurnResult{}, wrapSessionError(err)
	}
	req.SessionRef = session.SessionRef
	runCtx, cancel := context.WithCancel(ctx)
	cancelFn := sync.OnceValue(func() bool {
		cancel()
		return true
	})
	g.mu.Lock()
	if _, ok := g.active[session.SessionID]; ok {
		g.mu.Unlock()
		return BeginTurnResult{}, &Error{
			Kind:        KindConflict,
			Code:        CodeActiveRunConflict,
			UserVisible: true,
			Message:     "gateway: session already has an active run",
		}
	}
	handle := newTurnHandle(turnHandleConfig{
		handleID:                g.allocateID("handle"),
		runID:                   g.allocateID("run"),
		turnID:                  g.allocateID("turn"),
		activeKind:              ActiveTurnKindKernel,
		sessionRef:              session.SessionRef,
		createdAt:               g.clock(),
		allowPendingSubmissions: true,
		cancel: func() bool {
			return cancelFn()
		},
	})
	g.active[session.SessionID] = handle
	g.mu.Unlock()

	resolved, err := g.resolveBeginTurn(ctx, session, req)
	if err != nil {
		cancelFn()
		handle.finish()
		g.releaseActive(session.SessionID, handle)
		return BeginTurnResult{}, err
	}
	resolved.RunRequest.Request = resolved.RunRequest.Request.WithDefaults(g.requestOptions(req))
	g.mu.Lock()
	if g.active[session.SessionID] == handle {
		g.noteActiveHandleLocked(session.SessionID, handle)
	}
	g.mu.Unlock()

	go g.runTurn(runCtx, session, req, resolved, handle)

	return BeginTurnResult{
		Session: session,
		Handle:  handle,
	}, nil
}

func (g *Gateway) resolveBeginTurn(ctx context.Context, activeSession session.Session, req BeginTurnRequest) (ResolvedTurn, error) {
	if activeSession.Controller.Kind == session.ControllerKindACP {
		if resolver, ok := g.resolver.(ControllerTurnResolver); ok && resolver != nil {
			return resolver.ResolveControllerTurn(ctx, req)
		}
		return ResolvedTurn{
			RunRequest: agent.RunRequest{
				SessionRef:   activeSession.SessionRef,
				Input:        req.Input,
				ContentParts: append([]model.ContentPart(nil), req.ContentParts...),
			},
		}, nil
	}
	return g.resolver.ResolveTurn(ctx, req)
}

func (g *Gateway) requestOptions(req BeginTurnRequest) agent.ModelRequestOptions {
	if g == nil || g.request == nil {
		return req.Request
	}
	return req.Request.WithDefaults(g.request.ResolveTurnRequest(req))
}

func (g *Gateway) allocateID(prefix string) string {
	id := g.nextID.Add(1)
	return fmt.Sprintf("%s-%d", prefix, id)
}

func (g *Gateway) runTurn(
	ctx context.Context,
	session session.Session,
	req BeginTurnRequest,
	resolved ResolvedTurn,
	handle *turnHandle,
) {
	defer handle.finish()
	defer g.releaseActive(session.SessionID, handle)

	runReq := resolved.RunRequest
	runReq.SessionRef = session.SessionRef
	if strings.TrimSpace(runReq.Input) == "" {
		runReq.Input = req.Input
	}
	if len(runReq.ContentParts) == 0 && len(req.ContentParts) > 0 {
		runReq.ContentParts = append([]model.ContentPart(nil), req.ContentParts...)
	}
	runReq.ApprovalRequester = approvalRequesterFunc(func(approvalCtx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		return g.resolveApprovalRequest(ctx, approvalCtx, handle, &req, runReq.AgentSpec.Model)
	})

	result, err := g.runtime.Run(ctx, runReq)
	if err != nil {
		handle.publish(EventEnvelope{
			Event: Event{
				Kind:       EventKindLifecycle,
				HandleID:   handle.handleID,
				RunID:      handle.runID,
				TurnID:     handle.turnID,
				SessionRef: handle.sessionRef,
			},
			Err: EventError(err),
		})
		return
	}
	if result.Handle == nil {
		return
	}
	handle.setRunner(result.Handle)
	defer result.Handle.Close()
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			handle.publish(EventEnvelope{
				Event: Event{
					Kind:       EventKindLifecycle,
					HandleID:   handle.handleID,
					RunID:      handle.runID,
					TurnID:     handle.turnID,
					SessionRef: handle.sessionRef,
				},
				Err: EventError(seqErr),
			})
			return
		}
		handle.publishSessionEvent(event)
		g.noteSessionCursor(session.SessionID, event.ID)
	}
}

func (g *Gateway) runParticipantTurn(
	ctx context.Context,
	session session.Session,
	req PromptParticipantRequest,
	handle *turnHandle,
) {
	defer handle.finish()
	defer g.releaseActive(session.SessionID, handle)

	runReq := agent.PromptACPParticipantRequest{
		SessionRef:    session.SessionRef,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Input:         strings.TrimSpace(req.Input),
		ContentParts:  append([]model.ContentPart(nil), req.ContentParts...),
		Source:        strings.TrimSpace(req.Source),
		Stream:        true,
	}
	runReq.ApprovalRequester = approvalRequesterFunc(func(approvalCtx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		return g.resolveApprovalRequest(ctx, approvalCtx, handle, &req, nil)
	})

	result, err := g.control.PromptACPParticipant(ctx, runReq)
	if err != nil {
		handle.publish(EventEnvelope{
			Event: Event{
				Kind:       EventKindLifecycle,
				HandleID:   handle.handleID,
				RunID:      handle.runID,
				TurnID:     handle.turnID,
				SessionRef: handle.sessionRef,
			},
			Err: EventError(err),
		})
		return
	}
	if result.Handle == nil {
		return
	}
	handle.setRunner(result.Handle)
	defer result.Handle.Close()
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			handle.publish(EventEnvelope{
				Event: Event{
					Kind:       EventKindLifecycle,
					HandleID:   handle.handleID,
					RunID:      handle.runID,
					TurnID:     handle.turnID,
					SessionRef: handle.sessionRef,
				},
				Err: EventError(seqErr),
			})
			return
		}
		handle.publishSessionEvent(event)
		g.noteSessionCursor(session.SessionID, event.ID)
	}
}

func (g *Gateway) releaseActive(sessionID string, handle *turnHandle) {
	g.mu.Lock()
	defer g.mu.Unlock()
	current, ok := g.active[sessionID]
	if !ok || current != handle {
		return
	}
	delete(g.active, sessionID)
}

func (g *Gateway) CurrentSession(bindingKey string) (session.SessionRef, bool) {
	if g == nil || strings.TrimSpace(bindingKey) == "" {
		return session.SessionRef{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	binding, ok := g.bindingLocked(strings.TrimSpace(bindingKey))
	if !ok || strings.TrimSpace(binding.current.SessionID) == "" {
		return session.SessionRef{}, false
	}
	return binding.current, true
}

func (g *Gateway) ClearBinding(bindingKey string) {
	if g == nil || strings.TrimSpace(bindingKey) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.bindings, strings.TrimSpace(bindingKey))
}

func (g *Gateway) bind(bindingKey string, ref session.SessionRef, desc BindingDescriptor) {
	if g == nil || strings.TrimSpace(bindingKey) == "" || strings.TrimSpace(ref.SessionID) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	key := strings.TrimSpace(bindingKey)
	now := g.clock()
	current, ok := g.bindingLocked(key)
	if !ok || current.current.SessionID != ref.SessionID {
		current = sessionBinding{
			current: ref,
			boundAt: now,
		}
	}
	current.current = ref
	current.updatedAt = now
	current.surface = firstNonEmpty(strings.TrimSpace(desc.Surface), current.surface, key)
	current.actorKind = firstNonEmpty(strings.TrimSpace(desc.ActorKind), current.actorKind)
	current.actorID = firstNonEmpty(strings.TrimSpace(desc.ActorID), current.actorID)
	current.owner = firstNonEmpty(strings.TrimSpace(desc.Owner), current.owner)
	if !desc.ExpiresAt.IsZero() {
		current.expiresAt = desc.ExpiresAt
	}
	g.bindings[key] = current
}

func (g *Gateway) resolveResumeTarget(req ResumeSessionRequest, sessions []session.SessionSummary) (session.SessionSummary, error) {
	target := strings.TrimSpace(req.SessionID)
	if target != "" {
		return resolveSessionSummary(sessions, target)
	}
	exclude := strings.TrimSpace(req.ExcludeSessionID)
	if exclude == "" {
		if current, ok := g.CurrentSession(req.BindingKey); ok {
			exclude = current.SessionID
		}
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.SessionID) == "" || session.SessionID == exclude {
			continue
		}
		return session, nil
	}
	return session.SessionSummary{}, &Error{
		Kind:        KindNotFound,
		Code:        CodeNoResumableSession,
		UserVisible: true,
		Message:     "gateway: no resumable session found",
	}
}

func resolveSessionSummary(sessions []session.SessionSummary, target string) (session.SessionSummary, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return session.SessionSummary{}, &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: session id is required",
		}
	}
	var exact *session.SessionSummary
	var prefixMatches []session.SessionSummary
	for _, session := range sessions {
		id := strings.TrimSpace(session.SessionID)
		if id == "" {
			continue
		}
		if id == target {
			matched := session
			exact = &matched
			break
		}
		if strings.HasPrefix(id, target) {
			prefixMatches = append(prefixMatches, session)
		}
	}
	if exact != nil {
		return *exact, nil
	}
	switch len(prefixMatches) {
	case 1:
		return prefixMatches[0], nil
	case 0:
		return session.SessionSummary{}, &Error{
			Kind:        KindNotFound,
			Code:        CodeSessionNotFound,
			UserVisible: true,
			Message:     "gateway: session not found",
		}
	default:
		return session.SessionSummary{}, &Error{
			Kind:        KindConflict,
			Code:        CodeSessionAmbiguous,
			UserVisible: true,
			Message:     "gateway: session id is ambiguous",
		}
	}
}

func (g *Gateway) interruptTarget(req InterruptRequest) (session.SessionRef, error) {
	return g.sessionTarget(req.SessionRef, req.BindingKey)
}

func (g *Gateway) sessionTarget(ref session.SessionRef, bindingKey string) (session.SessionRef, error) {
	if strings.TrimSpace(ref.SessionID) != "" {
		return ref, nil
	}
	if current, ok := g.CurrentSession(bindingKey); ok {
		return current, nil
	}
	if strings.TrimSpace(bindingKey) != "" {
		return session.SessionRef{}, &Error{
			Kind:        KindNotFound,
			Code:        CodeBindingNotFound,
			UserVisible: true,
			Message:     "gateway: binding not found",
		}
	}
	return session.SessionRef{}, &Error{
		Kind:        KindValidation,
		Code:        CodeInvalidRequest,
		UserVisible: true,
		Message:     "gateway: session ref or binding key is required",
	}
}

func (g *Gateway) hasActiveHandle(sessionID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	handle, ok := g.active[strings.TrimSpace(sessionID)]
	return ok && handle != nil
}

func (g *Gateway) noteActiveHandleLocked(sessionID string, handle *turnHandle) {
	if handle == nil {
		return
	}
	for key, binding := range g.bindings {
		if binding.current.SessionID != sessionID {
			continue
		}
		binding.lastHandleID = handle.HandleID()
		binding.lastRunID = handle.RunID()
		binding.lastTurnID = handle.TurnID()
		g.bindings[key] = binding
	}
}

func (g *Gateway) noteSessionCursor(sessionID string, cursor string) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(cursor) == "" {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, binding := range g.bindings {
		if binding.current.SessionID != sessionID {
			continue
		}
		binding.lastEventCursor = strings.TrimSpace(cursor)
		g.bindings[key] = binding
	}
}

func (g *Gateway) bindingLocked(bindingKey string) (sessionBinding, bool) {
	binding, ok := g.bindings[strings.TrimSpace(bindingKey)]
	if !ok {
		return sessionBinding{}, false
	}
	if !binding.expiresAt.IsZero() && !binding.expiresAt.After(g.clock()) {
		delete(g.bindings, strings.TrimSpace(bindingKey))
		return sessionBinding{}, false
	}
	return binding, true
}

func wrapSessionError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, session.ErrSessionNotFound):
		return &Error{
			Kind:        KindNotFound,
			Code:        CodeSessionNotFound,
			UserVisible: true,
			Message:     "gateway: session not found",
			Cause:       err,
		}
	case errors.Is(err, session.ErrAmbiguousSession):
		return &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: session workspace is ambiguous",
			Cause:       err,
		}
	case errors.Is(err, session.ErrInvalidSession):
		return &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: invalid session request",
			Cause:       err,
		}
	default:
		return err
	}
}

type approvalRequesterFunc func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)

func (f approvalRequesterFunc) RequestApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return f(ctx, req)
}

func (g *Gateway) resolveApprovalRequest(
	turnCtx context.Context,
	approvalCtx context.Context,
	handle *turnHandle,
	req *agent.ApprovalRequest,
	reviewModel model.LLM,
) (agent.ApprovalResponse, error) {
	if g == nil || handle == nil || req == nil {
		return agent.ApprovalResponse{}, nil
	}
	mode, modeErr := g.currentApprovalMode(turnCtx, req.SessionRef)
	if modeErr != nil {
		mode = ApprovalModeManual
	}
	if NormalizeApprovalMode(req.Mode) == ApprovalModeManual {
		mode = ApprovalModeManual
	}
	if mode == ApprovalModeManual {
		wait := handle.publishApproval(req)
		select {
		case decision := <-wait:
			return agent.ApprovalResponse{
				Outcome:    decision.Outcome,
				OptionID:   decision.OptionID,
				Approved:   decision.Approved,
				Reason:     decision.Reason,
				ReviewText: decision.ReviewText,
			}, nil
		case <-approvalCtx.Done():
			return agent.ApprovalResponse{}, approvalCtx.Err()
		case <-turnCtx.Done():
			return agent.ApprovalResponse{}, turnCtx.Err()
		}
	}

	payload := canonicalApprovalPayload(req)
	if payload == nil {
		payload = &ApprovalPayload{
			ToolName: strings.TrimSpace(firstNonEmpty(req.Tool.Name, req.Call.Name)),
			Status:   ApprovalStatusPending,
		}
	}
	if mode == ApprovalModeManual {
		result, err := turnManualApprover{
			turnCtx: turnCtx,
			handle:  handle,
		}.Decide(approvalCtx, ApprovalReviewRequest{
			SessionRef:     req.SessionRef,
			RunID:          req.RunID,
			TurnID:         req.TurnID,
			Mode:           mode,
			Approval:       cloneApprovalPayload(payload),
			RuntimeRequest: *req,
		})
		return approval.RuntimeResponseFromReview(payload, result), err
	}

	reviewID := handle.nextApprovalReviewID()
	payload.ReviewID = reviewID
	payload.ReviewStatus = ApprovalReviewStatusInProgress
	payload.DecisionSource = string(ApprovalModeAutoReview)
	handle.publishApprovalReviewPayload(req, payload)

	approver := g.approvalApprover
	if approver == nil {
		if g.approvalReviewer != nil {
			approver = approval.ReviewerAdapter{Reviewer: g.approvalReviewer}
		} else {
			approver = denyingApprovalApprover{}
		}
	}
	if reviewModel == nil {
		reviewModel, _ = g.approvalReviewModel(turnCtx, req.SessionRef)
	}
	result, err := approver.Decide(approvalCtx, ApprovalReviewRequest{
		SessionRef:     req.SessionRef,
		RunID:          req.RunID,
		TurnID:         req.TurnID,
		Mode:           mode,
		ReviewID:       reviewID,
		Model:          reviewModel,
		Approval:       cloneApprovalPayload(payload),
		RuntimeRequest: *req,
	})
	if err != nil {
		rationale := "automatic approval review failed: " + err.Error()
		result = ApprovalReviewResult{
			Approved:       false,
			Outcome:        string(ApprovalStatusRejected),
			Risk:           "unknown",
			Authorization:  "unknown",
			Rationale:      rationale,
			DisplayText:    FormatApprovalReviewText(false, "unknown", "unknown", rationale),
			DecisionSource: string(ApprovalModeAutoReview),
		}
	}
	response := approval.RuntimeResponseFromReview(payload, result)
	result.OptionID = response.OptionID
	result.Outcome = response.Outcome
	if strings.TrimSpace(result.DisplayText) == "" {
		result.DisplayText = FormatApprovalReviewText(result.Approved, result.Risk, result.Authorization, result.Rationale)
	}
	if strings.TrimSpace(result.DecisionSource) == "" {
		result.DecisionSource = string(ApprovalModeAutoReview)
	}

	terminal := cloneApprovalPayload(payload)
	terminal.Status = ApprovalStatusRejected
	if result.Approved {
		terminal.Status = ApprovalStatusApproved
	}
	terminal.ReviewStatus = approvalReviewTerminalStatus(result)
	terminal.ReviewText = strings.TrimSpace(result.DisplayText)
	terminal.Risk = strings.TrimSpace(result.Risk)
	terminal.Authorization = strings.TrimSpace(result.Authorization)
	terminal.DecisionSource = strings.TrimSpace(result.DecisionSource)
	handle.publishApprovalReviewPayloadWithUsage(req, terminal, result.Usage)
	_ = g.persistApprovalReviewUsage(context.WithoutCancel(turnCtx), req, result.Usage, terminal.DecisionSource)

	if handle.recordApprovalReviewDecision(result.Approved) {
		return agent.ApprovalResponse{}, fmt.Errorf("automatic approval review rejected too many approval requests for this turn")
	}
	response.ReviewText = strings.TrimSpace(result.DisplayText)
	return response, nil
}

func (g *Gateway) persistApprovalReviewUsage(ctx context.Context, req *agent.ApprovalRequest, usage *UsageSnapshot, source string) error {
	if g == nil || g.sessions == nil || req == nil || usage == nil || usageSnapshotEmpty(*usage) {
		return nil
	}
	source = firstNonEmpty(strings.TrimSpace(source), string(ApprovalModeAutoReview))
	usageCopy := *usage
	return g.sessions.UpdateState(ctx, req.SessionRef, func(state map[string]any) (map[string]any, error) {
		next := session.CloneState(state)
		if next == nil {
			next = map[string]any{}
		}
		accounting := anyMapValue(next[StateUsageAccounting])
		if accounting == nil {
			accounting = map[string]any{}
		}
		total := UsageSnapshot{}
		if existing := UsageSnapshotFromMap(anyMapValue(accounting["auto_review"])); existing != nil {
			total = *existing
		}
		addUsageSnapshot(&total, usageCopy)
		accounting["auto_review"] = usageSnapshotMeta(total)
		accounting["auto_review_source"] = source
		next[StateUsageAccounting] = accounting
		return next, nil
	})
}

func usageSnapshotMeta(usage UsageSnapshot) map[string]any {
	return map[string]any{
		"prompt_tokens":       usage.PromptTokens,
		"cached_input_tokens": usage.CachedInputTokens,
		"completion_tokens":   usage.CompletionTokens,
		"reasoning_tokens":    usage.ReasoningTokens,
		"total_tokens":        usage.TotalTokens,
	}
}

func usageSnapshotEmpty(usage UsageSnapshot) bool {
	return usage.PromptTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.CompletionTokens == 0 &&
		usage.ReasoningTokens == 0 &&
		usage.TotalTokens == 0
}

func addUsageSnapshot(total *UsageSnapshot, usage UsageSnapshot) {
	if total == nil {
		return
	}
	total.PromptTokens += usage.PromptTokens
	total.CachedInputTokens += usage.CachedInputTokens
	total.CompletionTokens += usage.CompletionTokens
	total.ReasoningTokens += usage.ReasoningTokens
	total.TotalTokens += usage.TotalTokens
}

func approvalOptionIDForDecision(options []ApprovalOption, approved bool) string {
	wantKind := "reject_once"
	wantID := "reject_once"
	if approved {
		wantKind = "allow_once"
		wantID = "allow_once"
	}
	for _, option := range options {
		kind := strings.ToLower(strings.TrimSpace(option.Kind))
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		if kind == wantKind {
			return id
		}
	}
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, wantID) {
			return id
		}
	}
	return ""
}

type approvalModelResolver = approval.ModelResolver

func (g *Gateway) approvalReviewModel(ctx context.Context, ref session.SessionRef) (model.LLM, error) {
	if g == nil || g.resolver == nil {
		return nil, fmt.Errorf("gateway: approval review model resolver is unavailable")
	}
	resolver, ok := g.resolver.(approvalModelResolver)
	if !ok || resolver == nil {
		return nil, fmt.Errorf("gateway: approval review model resolver is unsupported")
	}
	return resolver.ResolveApprovalModel(ctx, ref)
}

func (g *Gateway) currentApprovalMode(ctx context.Context, ref session.SessionRef) (ApprovalMode, error) {
	if g == nil || g.sessions == nil {
		return ApprovalModeManual, fmt.Errorf("gateway: sessions service unavailable")
	}
	state, err := g.sessions.SnapshotState(ctx, ref)
	if err != nil {
		return ApprovalModeManual, wrapSessionError(err)
	}
	return CurrentApprovalMode(state), nil
}

func (g *Gateway) ActiveCounts() (int, int) {
	if g == nil {
		return 0, 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.active), len(g.bindings)
}

func (g *Gateway) ActiveTurns() []ActiveTurnState {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]ActiveTurnState, 0, len(g.active))
	for sessionID, handle := range g.active {
		if handle == nil {
			continue
		}
		ref := handle.SessionRef()
		if strings.TrimSpace(ref.SessionID) == "" {
			ref.SessionID = strings.TrimSpace(sessionID)
		}
		out = append(out, ActiveTurnState{
			SessionRef: ref,
			Kind:       handle.ActiveKind(),
			HandleID:   handle.HandleID(),
			RunID:      handle.RunID(),
			TurnID:     handle.TurnID(),
			StartedAt:  handle.CreatedAt(),
		})
	}
	return out
}

func (g *Gateway) ActiveTurn(sessionID string) (ActiveTurnState, bool) {
	if g == nil {
		return ActiveTurnState{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ActiveTurnState{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	handle := g.active[sessionID]
	if handle == nil {
		return ActiveTurnState{}, false
	}
	ref := handle.SessionRef()
	if strings.TrimSpace(ref.SessionID) == "" {
		ref.SessionID = sessionID
	}
	return ActiveTurnState{
		SessionRef: ref,
		Kind:       handle.ActiveKind(),
		HandleID:   handle.HandleID(),
		RunID:      handle.RunID(),
		TurnID:     handle.TurnID(),
		StartedAt:  handle.CreatedAt(),
	}, true
}

func (g *Gateway) SubmitActiveTurn(ctx context.Context, req SubmitActiveTurnRequest) error {
	if g == nil {
		return &Error{
			Kind:        KindInternal,
			Code:        CodeInternal,
			UserVisible: true,
			Message:     "gateway: gateway is not configured",
		}
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: session id is required for active turn submission",
		}
	}
	g.mu.Lock()
	handle := g.active[sessionID]
	g.mu.Unlock()
	if handle == nil {
		return &Error{
			Kind:        KindConflict,
			Code:        CodeNoActiveRun,
			UserVisible: true,
			Message:     "gateway: no active run is available for this session",
		}
	}
	return handle.Submit(ctx, SubmitRequest{
		Kind:     req.Kind,
		Text:     req.Text,
		Metadata: cloneMap(req.Metadata),
		Approval: req.Approval,
	})
}

func (g *Gateway) CancelActiveTurns() {
	if g == nil {
		return
	}
	g.mu.Lock()
	handles := make([]*turnHandle, 0, len(g.active))
	for _, handle := range g.active {
		if handle != nil {
			handles = append(handles, handle)
		}
	}
	g.mu.Unlock()
	for _, handle := range handles {
		handle.Cancel()
	}
}

type defaultRequestPolicy struct{}

func (defaultRequestPolicy) ResolveTurnRequest(req BeginTurnRequest) agent.ModelRequestOptions {
	stream := ClassifySurface(req.Surface) != SurfaceClassBatch
	return agent.ModelRequestOptions{Stream: boolPtr(stream)}
}

func boolPtr(v bool) *bool { return &v }
