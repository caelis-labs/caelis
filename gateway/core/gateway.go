package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

type Config struct {
	Sessions      sdksession.Service
	Runtime       sdkruntime.Runtime
	Resolver      TurnResolver
	RequestPolicy RequestPolicy
	Clock         func() time.Time
}

type Gateway struct {
	sessions sdksession.Service
	runtime  sdkruntime.Runtime
	control  sdkruntime.SessionControlPlane
	resolver TurnResolver
	request  RequestPolicy
	clock    func() time.Time

	mu       sync.Mutex
	active   map[string]*turnHandle
	bindings map[string]sessionBinding
	nextID   atomic.Uint64
}

type sessionBinding struct {
	current         sdksession.SessionRef
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
	return &Gateway{
		sessions: cfg.Sessions,
		runtime:  cfg.Runtime,
		control:  resolveControlPlane(cfg.Runtime),
		resolver: cfg.Resolver,
		request:  cfg.RequestPolicy,
		clock:    cfg.Clock,
		active:   map[string]*turnHandle{},
		bindings: map[string]sessionBinding{},
	}, nil
}

func resolveControlPlane(runtime sdkruntime.Runtime) sdkruntime.SessionControlPlane {
	if control, ok := runtime.(sdkruntime.SessionControlPlane); ok {
		return control
	}
	return nil
}

func (g *Gateway) Streams() sdkstream.Service {
	if g == nil || g.runtime == nil {
		return nil
	}
	provider, ok := g.runtime.(sdkruntime.StreamProvider)
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

func (g *Gateway) StartSession(ctx context.Context, req StartSessionRequest) (sdksession.Session, error) {
	session, err := g.sessions.StartSession(ctx, sdksession.StartSessionRequest{
		AppName:            req.AppName,
		UserID:             req.UserID,
		Workspace:          req.Workspace,
		PreferredSessionID: req.PreferredSessionID,
		Title:              req.Title,
		Metadata:           cloneMap(req.Metadata),
	})
	if err != nil {
		return sdksession.Session{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, session.SessionRef, req.Binding)
	return session, nil
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

func (g *Gateway) ForkSession(ctx context.Context, req ForkSessionRequest) (sdksession.Session, error) {
	if strings.TrimSpace(req.SourceSessionRef.SessionID) == "" {
		return sdksession.Session{}, &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: source session ref is required",
		}
	}
	source, err := g.sessions.Session(ctx, req.SourceSessionRef)
	if err != nil {
		return sdksession.Session{}, wrapSessionError(err)
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
	started, err := g.sessions.StartSession(ctx, sdksession.StartSessionRequest{
		AppName:            source.AppName,
		UserID:             source.UserID,
		Workspace:          sdksession.WorkspaceRef{Key: source.WorkspaceKey, CWD: source.CWD},
		PreferredSessionID: req.PreferredSessionID,
		Title:              title,
		Metadata:           metadata,
	})
	if err != nil {
		return sdksession.Session{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, started.SessionRef, req.Binding)
	return started, nil
}

func (g *Gateway) LoadSession(ctx context.Context, req LoadSessionRequest) (sdksession.LoadedSession, error) {
	loaded, err := g.sessions.LoadSession(ctx, sdksession.LoadSessionRequest{
		SessionRef:       req.SessionRef,
		Limit:            req.Limit,
		IncludeTransient: req.IncludeTransient,
	})
	if err != nil {
		return sdksession.LoadedSession{}, wrapSessionError(err)
	}
	g.bind(req.BindingKey, loaded.Session.SessionRef, req.Binding)
	return loaded, nil
}

func (g *Gateway) ResumeSession(ctx context.Context, req ResumeSessionRequest) (sdksession.LoadedSession, error) {
	list, err := g.ListSessions(ctx, ListSessionsRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.Workspace.Key,
		Limit:        200,
	})
	if err != nil {
		return sdksession.LoadedSession{}, err
	}
	target, err := g.resolveResumeTarget(req, list.Sessions)
	if err != nil {
		return sdksession.LoadedSession{}, err
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

func (g *Gateway) ListSessions(ctx context.Context, req ListSessionsRequest) (sdksession.SessionList, error) {
	list, err := g.sessions.ListSessions(ctx, sdksession.ListSessionsRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.WorkspaceKey,
		Cursor:       req.Cursor,
		Limit:        req.Limit,
	})
	if err != nil {
		return sdksession.SessionList{}, wrapSessionError(err)
	}
	return list, nil
}

func (g *Gateway) Interrupt(ctx context.Context, req InterruptRequest) error {
	_ = ctx
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
	if !handle.Cancel() {
		return &Error{
			Kind:        KindConflict,
			Code:        CodeNoActiveRun,
			UserVisible: true,
			Message:     "gateway: session has no active run",
		}
	}
	return nil
}

func (g *Gateway) HandoffController(ctx context.Context, req HandoffControllerRequest) (sdksession.Session, error) {
	if g.control == nil {
		return sdksession.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return sdksession.Session{}, err
	}
	session, err := g.control.HandoffController(ctx, sdkruntime.HandoffControllerRequest{
		SessionRef: ref,
		Kind:       req.Kind,
		Agent:      strings.TrimSpace(req.Agent),
		Source:     strings.TrimSpace(req.Source),
		Reason:     strings.TrimSpace(req.Reason),
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	g.bind(req.BindingKey, session.SessionRef, BindingDescriptor{})
	return session, nil
}

func (g *Gateway) AttachParticipant(ctx context.Context, req AttachParticipantRequest) (sdksession.Session, error) {
	if g.control == nil {
		return sdksession.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return sdksession.Session{}, err
	}
	session, err := g.control.AttachACPParticipant(ctx, sdkruntime.AttachACPParticipantRequest{
		SessionRef: ref,
		Agent:      strings.TrimSpace(req.Agent),
		Role:       req.Role,
		Source:     strings.TrimSpace(req.Source),
		Label:      strings.TrimSpace(req.Label),
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	g.bind(req.BindingKey, session.SessionRef, BindingDescriptor{})
	return session, nil
}

func (g *Gateway) DetachParticipant(ctx context.Context, req DetachParticipantRequest) (sdksession.Session, error) {
	if g.control == nil {
		return sdksession.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return sdksession.Session{}, err
	}
	session, err := g.control.DetachACPParticipant(ctx, sdkruntime.DetachACPParticipantRequest{
		SessionRef:    ref,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Source:        strings.TrimSpace(req.Source),
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	g.bind(req.BindingKey, session.SessionRef, BindingDescriptor{})
	return session, nil
}

func (g *Gateway) PromptParticipant(ctx context.Context, req PromptParticipantRequest) (sdksession.Session, error) {
	if g.control == nil {
		return sdksession.Session{}, &Error{
			Kind:        KindUnsupported,
			Code:        CodeControlPlaneUnsupported,
			UserVisible: true,
			Message:     "gateway: control plane is not available",
		}
	}
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return sdksession.Session{}, err
	}
	session, err := g.control.PromptACPParticipant(ctx, sdkruntime.PromptACPParticipantRequest{
		SessionRef:    ref,
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Input:         strings.TrimSpace(req.Input),
		ContentParts:  append([]sdkmodel.ContentPart(nil), req.ContentParts...),
		Source:        strings.TrimSpace(req.Source),
	})
	if err != nil {
		return sdksession.Session{}, err
	}
	g.bind(req.BindingKey, session.SessionRef, BindingDescriptor{})
	return session, nil
}

func (g *Gateway) ControlPlaneState(ctx context.Context, req ControlPlaneStateRequest) (ControlPlaneState, error) {
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return ControlPlaneState{}, err
	}
	session, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return ControlPlaneState{}, wrapSessionError(err)
	}
	events, err := g.sessions.Events(ctx, sdksession.EventsRequest{
		SessionRef: ref,
	})
	if err != nil {
		return ControlPlaneState{}, wrapSessionError(err)
	}
	runState, err := g.runtime.RunState(ctx, ref)
	if err != nil && !errors.Is(err, sdksession.ErrSessionNotFound) {
		return ControlPlaneState{}, err
	}
	return buildControlPlaneState(session, runState, events), nil
}

func (g *Gateway) ReplayEvents(ctx context.Context, req ReplayEventsRequest) (ReplayEventsResult, error) {
	ref, err := g.sessionTarget(req.SessionRef, req.BindingKey)
	if err != nil {
		return ReplayEventsResult{}, err
	}
	session, err := g.sessions.Session(ctx, ref)
	if err != nil {
		return ReplayEventsResult{}, wrapSessionError(err)
	}
	events, err := g.sessions.Events(ctx, sdksession.EventsRequest{
		SessionRef:       ref,
		Limit:            0,
		IncludeTransient: req.IncludeTransient,
	})
	if err != nil {
		return ReplayEventsResult{}, wrapSessionError(err)
	}
	runState, err := g.runtime.RunState(ctx, ref)
	if err != nil && !errors.Is(err, sdksession.ErrSessionNotFound) {
		return ReplayEventsResult{}, err
	}
	projected := projectSessionEvents(ref, events)
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
		ControlPlane:  buildControlPlaneState(session, runState, events),
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
	resolved, err := g.resolver.ResolveTurn(ctx, req)
	if err != nil {
		return BeginTurnResult{}, err
	}
	resolved.RunRequest.Request = resolved.RunRequest.Request.WithDefaults(g.requestOptions(req))
	session, err := g.sessions.Session(ctx, req.SessionRef)
	if err != nil {
		return BeginTurnResult{}, wrapSessionError(err)
	}
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
		handleID:   g.allocateID("handle"),
		runID:      g.allocateID("run"),
		turnID:     g.allocateID("turn"),
		sessionRef: session.SessionRef,
		createdAt:  g.clock(),
		cancel: func() bool {
			return cancelFn()
		},
	})
	g.active[session.SessionID] = handle
	g.noteActiveHandleLocked(session.SessionID, handle)
	g.mu.Unlock()

	go g.runTurn(runCtx, session, req, resolved, handle)

	return BeginTurnResult{
		Session: session,
		Handle:  handle,
	}, nil
}

func (g *Gateway) requestOptions(req BeginTurnRequest) sdkruntime.ModelRequestOptions {
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
	session sdksession.Session,
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
		runReq.ContentParts = append([]sdkmodel.ContentPart(nil), req.ContentParts...)
	}
	runReq.ApprovalRequester = approvalRequesterFunc(func(req sdkruntime.ApprovalRequest) (sdkruntime.ApprovalResponse, error) {
		wait := handle.publishApproval(&req)
		select {
		case decision := <-wait:
			return sdkruntime.ApprovalResponse{
				Outcome:  decision.Outcome,
				OptionID: decision.OptionID,
				Approved: decision.Approved,
			}, nil
		case <-ctx.Done():
			return sdkruntime.ApprovalResponse{}, ctx.Err()
		}
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

func (g *Gateway) releaseActive(sessionID string, handle *turnHandle) {
	g.mu.Lock()
	defer g.mu.Unlock()
	current, ok := g.active[sessionID]
	if !ok || current != handle {
		return
	}
	delete(g.active, sessionID)
}

func (g *Gateway) CurrentSession(bindingKey string) (sdksession.SessionRef, bool) {
	if g == nil || strings.TrimSpace(bindingKey) == "" {
		return sdksession.SessionRef{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	binding, ok := g.bindingLocked(strings.TrimSpace(bindingKey))
	if !ok || strings.TrimSpace(binding.current.SessionID) == "" {
		return sdksession.SessionRef{}, false
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

func (g *Gateway) bind(bindingKey string, ref sdksession.SessionRef, desc BindingDescriptor) {
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

func (g *Gateway) resolveResumeTarget(req ResumeSessionRequest, sessions []sdksession.SessionSummary) (sdksession.SessionSummary, error) {
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
	return sdksession.SessionSummary{}, &Error{
		Kind:        KindNotFound,
		Code:        CodeNoResumableSession,
		UserVisible: true,
		Message:     "gateway: no resumable session found",
	}
}

func resolveSessionSummary(sessions []sdksession.SessionSummary, target string) (sdksession.SessionSummary, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return sdksession.SessionSummary{}, &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: session id is required",
		}
	}
	var exact *sdksession.SessionSummary
	var prefixMatches []sdksession.SessionSummary
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
		return sdksession.SessionSummary{}, &Error{
			Kind:        KindNotFound,
			Code:        CodeSessionNotFound,
			UserVisible: true,
			Message:     "gateway: session not found",
		}
	default:
		return sdksession.SessionSummary{}, &Error{
			Kind:        KindConflict,
			Code:        CodeSessionAmbiguous,
			UserVisible: true,
			Message:     "gateway: session id is ambiguous",
		}
	}
}

func (g *Gateway) interruptTarget(req InterruptRequest) (sdksession.SessionRef, error) {
	return g.sessionTarget(req.SessionRef, req.BindingKey)
}

func (g *Gateway) sessionTarget(ref sdksession.SessionRef, bindingKey string) (sdksession.SessionRef, error) {
	if strings.TrimSpace(ref.SessionID) != "" {
		return ref, nil
	}
	if current, ok := g.CurrentSession(bindingKey); ok {
		return current, nil
	}
	if strings.TrimSpace(bindingKey) != "" {
		return sdksession.SessionRef{}, &Error{
			Kind:        KindNotFound,
			Code:        CodeBindingNotFound,
			UserVisible: true,
			Message:     "gateway: binding not found",
		}
	}
	return sdksession.SessionRef{}, &Error{
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
	case errors.Is(err, sdksession.ErrSessionNotFound):
		return &Error{
			Kind:        KindNotFound,
			Code:        CodeSessionNotFound,
			UserVisible: true,
			Message:     "gateway: session not found",
			Cause:       err,
		}
	case errors.Is(err, sdksession.ErrInvalidSession):
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

type approvalRequesterFunc func(sdkruntime.ApprovalRequest) (sdkruntime.ApprovalResponse, error)

func (f approvalRequesterFunc) RequestApproval(_ context.Context, req sdkruntime.ApprovalRequest) (sdkruntime.ApprovalResponse, error) {
	return f(req)
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
		HandleID:   handle.HandleID(),
		RunID:      handle.RunID(),
		TurnID:     handle.TurnID(),
		StartedAt:  handle.CreatedAt(),
	}, true
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

func (defaultRequestPolicy) ResolveTurnRequest(req BeginTurnRequest) sdkruntime.ModelRequestOptions {
	stream := ClassifySurface(req.Surface) != SurfaceClassBatch
	return sdkruntime.ModelRequestOptions{Stream: boolPtr(stream)}
}

func boolPtr(v bool) *bool { return &v }
