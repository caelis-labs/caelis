package gatewaydriver

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/kernel"
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/eventbridge"
)

type appServiceGateway struct {
	services appservices.Services
	mu       sync.Mutex
	active   map[string]appServiceActiveTurn
}

type appServiceActiveTurn interface {
	kernel.TurnHandle
	state() kernel.ActiveTurnState
}

func newAppServiceGateway(svc appservices.Services) *appServiceGateway {
	return &appServiceGateway{
		services: svc,
		active:   map[string]appServiceActiveTurn{},
	}
}

func (g *appServiceGateway) BeginTurn(ctx context.Context, req kernel.BeginTurnRequest) (kernel.BeginTurnResult, error) {
	ref := coreRefFromPort(req.SessionRef)
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return kernel.BeginTurnResult{}, err
	}
	controller := controllerFromCoreSnapshot(snapshot)
	snapshot.Session.Controller = controller
	turn, err := g.services.Turns().Begin(ctx, appservices.BeginTurnRequest{
		SessionRef:   ref,
		Input:        req.Input,
		ContentParts: coremodel.CloneContentParts(req.ContentParts),
		Model:        req.ModelHint,
		Surface:      req.Surface,
		Meta:         maps.Clone(req.Metadata),
	})
	if err != nil {
		return kernel.BeginTurnResult{}, err
	}
	handle := newAppServiceTurnHandle(g.services, turn)
	g.register(handle)
	go g.forward(handle)
	return kernel.BeginTurnResult{
		Session: portSessionFromCore(snapshot.Session),
		Handle:  handle,
	}, nil
}

func (g *appServiceGateway) SubmitActiveTurn(ctx context.Context, req kernel.SubmitActiveTurnRequest) error {
	handle := g.activeForSession(req.SessionRef.SessionID)
	if handle == nil {
		return &kernel.Error{
			Kind:    kernel.KindConflict,
			Code:    kernel.CodeNoActiveRun,
			Message: "no active core turn for session",
		}
	}
	if handle.state().Kind != kernel.ActiveTurnKindKernel {
		return &kernel.Error{
			Kind:    kernel.KindConflict,
			Code:    kernel.CodeNoActiveRun,
			Message: "no active core turn for session",
		}
	}
	return handle.Submit(ctx, kernel.SubmitRequest{
		Kind:         req.Kind,
		Text:         req.Text,
		ContentParts: coremodel.CloneContentParts(req.ContentParts),
		Metadata:     maps.Clone(req.Metadata),
		Approval:     req.Approval,
	})
}

func coreSubmissionFromKernelSubmit(req kernel.SubmitRequest) coreruntime.Submission {
	out := coreruntime.Submission{
		Kind:         coreruntime.SubmissionConversation,
		Text:         req.Text,
		ContentParts: coremodel.CloneContentParts(req.ContentParts),
		Meta:         maps.Clone(req.Metadata),
	}
	if req.Kind == kernel.SubmissionKindApproval && req.Approval != nil {
		out.Kind = coreruntime.SubmissionApproval
		out.Approval = &coreruntime.ApprovalDecision{
			Outcome:  strings.TrimSpace(req.Approval.Outcome),
			OptionID: strings.TrimSpace(req.Approval.OptionID),
			Approved: req.Approval.Approved,
			Reason:   strings.TrimSpace(req.Approval.Reason),
		}
	}
	return out
}

func (g *appServiceGateway) Interrupt(ctx context.Context, req kernel.InterruptRequest) error {
	return g.services.Turns().Interrupt(ctx, coreRefFromPort(req.SessionRef))
}

func (g *appServiceGateway) ResumeSession(ctx context.Context, req kernel.ResumeSessionRequest) (portsession.LoadedSession, error) {
	snapshot, err := g.services.Sessions().Load(ctx, coresession.Ref{
		AppName:      strings.TrimSpace(req.AppName),
		UserID:       strings.TrimSpace(req.UserID),
		SessionID:    strings.TrimSpace(req.SessionID),
		WorkspaceKey: strings.TrimSpace(req.Workspace.Key),
	})
	if err != nil {
		return portsession.LoadedSession{}, err
	}
	return loadedSessionFromCore(snapshot), nil
}

func (g *appServiceGateway) ListSessions(ctx context.Context, req kernel.ListSessionsRequest) (portsession.SessionList, error) {
	workspaceKey := strings.TrimSpace(req.WorkspaceKey)
	page, err := g.services.Sessions().List(ctx, appservices.ListSessionsRequest{
		Workspace: coresession.Workspace{
			Key: workspaceKey,
		},
		AllWorkspaces: workspaceKey == "",
		After:         coresession.Cursor(req.Cursor),
		Limit:         req.Limit,
	})
	if err != nil {
		return portsession.SessionList{}, err
	}
	return g.sessionListFromCore(ctx, page), nil
}

func (g *appServiceGateway) ReplayEvents(ctx context.Context, req kernel.ReplayEventsRequest) (kernel.ReplayEventsResult, error) {
	events, err := g.services.Events().Replay(ctx, appservices.EventReplayRequest{
		SessionRef:       coreRefFromPort(req.SessionRef),
		After:            coresession.Cursor(req.Cursor),
		Limit:            req.Limit,
		IncludeTransient: req.IncludeTransient,
	})
	if err != nil {
		return kernel.ReplayEventsResult{}, err
	}
	out := make([]kernel.EventEnvelope, 0)
	for env := range events {
		if converted, ok := eventbridge.KernelEnvelopeFromAppEvent(env); ok {
			out = append(out, converted)
		}
	}
	return kernel.ReplayEventsResult{Events: out}, nil
}

func (g *appServiceGateway) ControlPlaneState(ctx context.Context, req kernel.ControlPlaneStateRequest) (kernel.ControlPlaneState, error) {
	snapshot, err := g.services.Sessions().Load(ctx, coreRefFromPort(req.SessionRef))
	if err != nil {
		return kernel.ControlPlaneState{}, err
	}
	return controlPlaneStateFromCore(snapshot, g.ActiveTurns()), nil
}

func (g *appServiceGateway) HandoffController(ctx context.Context, req kernel.HandoffControllerRequest) (portsession.Session, error) {
	ref := coreRefFromPort(req.SessionRef)
	target, err := handoffTargetFromRequest(req)
	if err != nil {
		return portsession.Session{}, err
	}
	if _, err := g.services.Controllers().Handoff(ctx, appservices.ControllerHandoffRequest{
		SessionRef: ref,
		Target:     target,
		Source:     req.Source,
		Reason:     req.Reason,
	}); err != nil {
		return portsession.Session{}, err
	}
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return portsession.Session{}, err
	}
	snapshot.Session.Controller = controllerFromCoreSnapshot(snapshot)
	return portSessionFromCore(snapshot.Session), nil
}

func handoffTargetFromRequest(req kernel.HandoffControllerRequest) (string, error) {
	switch req.Kind {
	case "", portsession.ControllerKindKernel:
		return "local", nil
	case portsession.ControllerKindACP:
		target := strings.TrimSpace(req.Agent)
		if target == "" {
			return "", fmt.Errorf("core app-service TUI gateway: ACP controller agent is required")
		}
		return target, nil
	default:
		return "", fmt.Errorf("core app-service TUI gateway: unsupported controller kind %q", req.Kind)
	}
}

func (g *appServiceGateway) AttachParticipant(ctx context.Context, req kernel.AttachParticipantRequest) (portsession.Session, error) {
	ref := coreRefFromPort(req.SessionRef)
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return portsession.Session{}, err
	}
	participant, err := g.participantForAttach(ctx, snapshot, req)
	if err != nil {
		return portsession.Session{}, err
	}
	event := participantLifecycleEvent(participant, "attached", req.Source)
	if _, err := g.services.Engine().RecordEvents(ctx, snapshot.Session.Ref, []coresession.Event{event}); err != nil {
		return portsession.Session{}, err
	}
	snapshot.Events = append(snapshot.Events, event)
	snapshot.Session.Participants = participantsFromCoreSnapshot(snapshot)
	return portSessionFromCore(snapshot.Session), nil
}

func (g *appServiceGateway) PromptParticipant(ctx context.Context, req kernel.PromptParticipantRequest) (kernel.BeginTurnResult, error) {
	ref := coreRefFromPort(req.SessionRef)
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return kernel.BeginTurnResult{}, err
	}
	participants := participantsFromCoreSnapshot(snapshot)
	participant, ok := findCoreParticipant(participants, req.ParticipantID)
	if !ok {
		return kernel.BeginTurnResult{}, fmt.Errorf("core app-service TUI gateway: participant %q is not attached", strings.TrimSpace(req.ParticipantID))
	}
	snapshot.Session.Participants = participants
	handle := newAppServiceAgentTurnHandle(g.services, snapshot.Session.Ref, participant, req.Input, coremodel.CloneContentParts(req.ContentParts), req.Source)
	g.register(handle)
	go func() {
		defer g.unregister(handle)
		handle.run(ctx)
	}()
	return kernel.BeginTurnResult{
		Session: portSessionFromCore(snapshot.Session),
		Handle:  handle,
	}, nil
}

func (g *appServiceGateway) DetachParticipant(ctx context.Context, req kernel.DetachParticipantRequest) (portsession.Session, error) {
	ref := coreRefFromPort(req.SessionRef)
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return portsession.Session{}, err
	}
	participants := participantsFromCoreSnapshot(snapshot)
	participant, ok := findCoreParticipant(participants, req.ParticipantID)
	if !ok {
		snapshot.Session.Participants = participants
		return portSessionFromCore(snapshot.Session), nil
	}
	event := participantLifecycleEvent(participant, "detached", req.Source)
	if _, err := g.services.Engine().RecordEvents(ctx, snapshot.Session.Ref, []coresession.Event{event}); err != nil {
		return portsession.Session{}, err
	}
	snapshot.Events = append(snapshot.Events, event)
	snapshot.Session.Participants = participantsFromCoreSnapshot(snapshot)
	return portSessionFromCore(snapshot.Session), nil
}

func (g *appServiceGateway) ActiveTurns() []kernel.ActiveTurnState {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]kernel.ActiveTurnState, 0, len(g.active))
	for _, handle := range g.active {
		out = append(out, handle.state())
	}
	return out
}

func (g *appServiceGateway) register(handle appServiceActiveTurn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active[handle.SessionRef().SessionID] = handle
}

func (g *appServiceGateway) unregister(handle appServiceActiveTurn) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if current := g.active[handle.SessionRef().SessionID]; current == handle {
		delete(g.active, handle.SessionRef().SessionID)
	}
}

func (g *appServiceGateway) activeForSession(sessionID string) appServiceActiveTurn {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active[strings.TrimSpace(sessionID)]
}

func (g *appServiceGateway) forward(handle *appServiceTurnHandle) {
	defer g.unregister(handle)
	handle.forward()
}

type appServiceTurnHandle struct {
	services  appservices.Services
	turn      coreruntime.Turn
	events    chan kernel.EventEnvelope
	appEvents chan appviewmodel.SessionEventEnvelope
	done      chan struct{}
	mu        sync.Mutex
	history   []kernel.EventEnvelope
}

func newAppServiceTurnHandle(services appservices.Services, turn coreruntime.Turn) *appServiceTurnHandle {
	return &appServiceTurnHandle{
		services:  services,
		turn:      turn,
		events:    make(chan kernel.EventEnvelope, 32),
		appEvents: make(chan appviewmodel.SessionEventEnvelope, 32),
		done:      make(chan struct{}),
	}
}

func (h *appServiceTurnHandle) HandleID() string {
	return h.turn.ID()
}

func (h *appServiceTurnHandle) RunID() string {
	return h.turn.RunID()
}

func (h *appServiceTurnHandle) TurnID() string {
	return h.turn.ID()
}

func (h *appServiceTurnHandle) SessionRef() portsession.SessionRef {
	return portRefFromCore(h.turn.SessionRef())
}

func (h *appServiceTurnHandle) CreatedAt() time.Time {
	return h.turn.StartedAt()
}

func (h *appServiceTurnHandle) Events() <-chan kernel.EventEnvelope {
	return h.events
}

func (h *appServiceTurnHandle) SessionEvents() <-chan appviewmodel.SessionEventEnvelope {
	return h.appEvents
}

func (h *appServiceTurnHandle) EventsAfter(cursor string) ([]kernel.EventEnvelope, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cursor = strings.TrimSpace(cursor)
	start := 0
	if cursor != "" {
		for i, env := range h.history {
			if strings.TrimSpace(env.Cursor) == cursor {
				start = i + 1
				break
			}
		}
	}
	out := append([]kernel.EventEnvelope(nil), h.history[start:]...)
	next := cursor
	if len(out) > 0 {
		next = out[len(out)-1].Cursor
	}
	return out, next, nil
}

func (h *appServiceTurnHandle) Submit(ctx context.Context, req kernel.SubmitRequest) error {
	return h.SubmitCore(ctx, coreSubmissionFromKernelSubmit(req))
}

func (h *appServiceTurnHandle) SubmitCore(ctx context.Context, submission coreruntime.Submission) error {
	submission = cloneCoreSubmission(submission)
	if submission.Kind == coreruntime.SubmissionApproval && submission.Approval != nil {
		_, err := h.services.Approvals().Submit(ctx, h.turn, appservices.ApprovalDecisionRequest{
			Outcome:  strings.TrimSpace(submission.Approval.Outcome),
			OptionID: strings.TrimSpace(submission.Approval.OptionID),
			Approved: submission.Approval.Approved,
			Reason:   strings.TrimSpace(submission.Approval.Reason),
		})
		return err
	}
	return h.turn.Submit(ctx, submission)
}

func cloneCoreSubmission(in coreruntime.Submission) coreruntime.Submission {
	out := in
	out.ContentParts = coremodel.CloneContentParts(in.ContentParts)
	out.Meta = maps.Clone(in.Meta)
	if in.Approval != nil {
		approval := *in.Approval
		out.Approval = &approval
	}
	return out
}

func (h *appServiceTurnHandle) Cancel() kernel.CancelResult {
	result := h.turn.Cancel()
	status := kernel.CancelStatusAlreadyCancelled
	if result.Status == coreruntime.CancelCancelled {
		status = kernel.CancelStatusCancelled
	}
	return kernel.CancelResult{Status: status, Err: result.Err}
}

func (h *appServiceTurnHandle) Close() error {
	return h.turn.Close()
}

func (h *appServiceTurnHandle) forward() {
	defer close(h.events)
	defer close(h.appEvents)
	defer close(h.done)
	stream, err := h.services.Events().SubscribeTurn(context.Background(), h.turn)
	if err != nil {
		h.publishAppEvent(appviewmodel.EventEnvelopeFromError(err.Error()))
		h.publishKernelEvent(kernel.EventEnvelope{Err: &kernel.Error{Kind: kernel.KindInternal, Code: kernel.CodeInternal, Message: err.Error()}})
		return
	}
	for env := range stream {
		appEnv := appviewmodel.CloneSessionEventEnvelope(env)
		converted, ok := eventbridge.KernelEnvelopeFromAppEvent(env)
		if !ok {
			h.publishAppEvent(appEnv)
			continue
		}
		converted.Event.HandleID = h.HandleID()
		converted.Event.RunID = h.RunID()
		if converted.Event.TurnID == "" {
			converted.Event.TurnID = h.TurnID()
		}
		h.mu.Lock()
		h.history = append(h.history, converted)
		h.mu.Unlock()
		h.publishAppEvent(appEnv)
		h.publishKernelEvent(converted)
	}
}

func (h *appServiceTurnHandle) publishAppEvent(env appviewmodel.SessionEventEnvelope) {
	h.appEvents <- appviewmodel.CloneSessionEventEnvelope(env)
}

func (h *appServiceTurnHandle) publishKernelEvent(env kernel.EventEnvelope) {
	select {
	case h.events <- env:
	default:
	}
}

func (h *appServiceTurnHandle) state() kernel.ActiveTurnState {
	return kernel.ActiveTurnState{
		SessionRef: h.SessionRef(),
		Kind:       kernel.ActiveTurnKindKernel,
		HandleID:   h.HandleID(),
		RunID:      h.RunID(),
		TurnID:     h.TurnID(),
		StartedAt:  h.CreatedAt(),
	}
}

func unsupportedControlPlaneError(action string) error {
	return &kernel.Error{
		Kind:    kernel.KindUnsupported,
		Code:    kernel.CodeControlPlaneUnsupported,
		Message: "core app-service TUI gateway does not support " + action,
	}
}

func loadedSessionFromCore(snapshot coresession.Snapshot) portsession.LoadedSession {
	snapshot.Session.Controller = controllerFromCoreSnapshot(snapshot)
	snapshot.Session.Participants = participantsFromCoreSnapshot(snapshot)
	return portsession.LoadedSession{
		Session: portSessionFromCore(snapshot.Session),
		Events:  portEventsFromCore(snapshot.Events),
		State:   maps.Clone(snapshot.State),
	}
}

func portEventsFromCore(events []coresession.Event) []*portsession.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]*portsession.Event, 0, len(events))
	for _, event := range events {
		next := portEventFromCore(event)
		out = append(out, &next)
	}
	return out
}

func portEventFromCore(event coresession.Event) portsession.Event {
	return portsession.Event{
		ID:        strings.TrimSpace(event.ID),
		SessionID: strings.TrimSpace(event.SessionID),
		Type:      portsession.EventType(event.Type),
		Time:      event.Time,
		Meta:      maps.Clone(event.Meta),
	}
}

func (g *appServiceGateway) sessionListFromCore(_ context.Context, page coresession.SessionPage) portsession.SessionList {
	out := portsession.SessionList{
		Sessions:   make([]portsession.SessionSummary, 0, len(page.Sessions)),
		NextCursor: string(page.NextCursor),
	}
	for _, item := range page.Sessions {
		out.Sessions = append(out.Sessions, portsession.SessionSummary{
			SessionRef: portRefFromCore(item.Session.Ref),
			CWD:        strings.TrimSpace(item.Session.Workspace.CWD),
			Title:      strings.TrimSpace(item.Session.Title),
			UpdatedAt:  item.Session.UpdatedAt,
		})
	}
	return out
}

func controlPlaneStateFromCore(snapshot coresession.Snapshot, active []kernel.ActiveTurnState) kernel.ControlPlaneState {
	ref := portRefFromCore(snapshot.Session.Ref)
	participants := participantsFromCoreSnapshot(snapshot)
	controller := controllerFromCoreSnapshot(snapshot)
	return kernel.ControlPlaneState{
		SessionRef:    ref,
		Controller:    controllerStateFromCore(controller),
		Participants:  participantStatesFromCore(participants),
		HasActiveTurn: activeTurnForSession(active, ref.SessionID),
	}
}

func controllerFromCoreSnapshot(snapshot coresession.Snapshot) coresession.ControllerBinding {
	controller := normalizeCoreController(snapshot.Session.Controller)
	for _, event := range snapshot.Events {
		if event.Scope == nil {
			continue
		}
		switch event.Type {
		case coresession.EventHandoff:
			next := normalizeCoreController(event.Scope.Controller)
			if next.Kind == "" && strings.TrimSpace(next.ID) == "" {
				continue
			}
			controller = next
		default:
			next := normalizeCoreController(event.Scope.Controller)
			if !sameCoreController(controller, next) {
				continue
			}
			controller = mergeCoreController(controller, next)
		}
	}
	return controller
}

func sameCoreController(active coresession.ControllerBinding, next coresession.ControllerBinding) bool {
	active = normalizeCoreController(active)
	next = normalizeCoreController(next)
	if active.Kind == "" || next.Kind == "" || active.Kind != next.Kind {
		return false
	}
	if active.Kind != coresession.ControllerACP {
		return false
	}
	if active.EpochID != "" && next.EpochID != "" && active.EpochID != next.EpochID {
		return false
	}
	activeID := strings.ToLower(firstNonEmpty(active.ID, active.AgentName, active.Label))
	nextID := strings.ToLower(firstNonEmpty(next.ID, next.AgentName, next.Label))
	return activeID != "" && activeID == nextID
}

func mergeCoreController(existing coresession.ControllerBinding, next coresession.ControllerBinding) coresession.ControllerBinding {
	existing = normalizeCoreController(existing)
	next = normalizeCoreController(next)
	existing.Kind = firstCoreControllerKind(existing.Kind, next.Kind)
	existing.ID = firstNonEmpty(existing.ID, next.ID)
	existing.AgentName = firstNonEmpty(existing.AgentName, next.AgentName)
	existing.Label = firstNonEmpty(existing.Label, next.Label)
	existing.EpochID = firstNonEmpty(existing.EpochID, next.EpochID)
	existing.RemoteSessionID = firstNonEmpty(next.RemoteSessionID, existing.RemoteSessionID)
	if next.ContextSyncSeq != 0 {
		existing.ContextSyncSeq = next.ContextSyncSeq
	}
	if !next.AttachedAt.IsZero() {
		existing.AttachedAt = next.AttachedAt
	}
	existing.Source = firstNonEmpty(next.Source, existing.Source)
	return existing
}

func firstCoreControllerKind(values ...coresession.ControllerKind) coresession.ControllerKind {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeCoreController(in coresession.ControllerBinding) coresession.ControllerBinding {
	in.ID = strings.TrimSpace(in.ID)
	in.AgentName = strings.TrimSpace(in.AgentName)
	in.Label = strings.TrimSpace(in.Label)
	in.EpochID = strings.TrimSpace(in.EpochID)
	in.RemoteSessionID = strings.TrimSpace(in.RemoteSessionID)
	in.Source = strings.TrimSpace(in.Source)
	return in
}

func controllerStateFromCore(in coresession.ControllerBinding) kernel.ControllerState {
	kind := portsession.ControllerKind(in.Kind)
	if in.Kind == coresession.ControllerBuiltin {
		kind = portsession.ControllerKindKernel
	}
	return kernel.ControllerState{
		Kind:            kind,
		ControllerID:    strings.TrimSpace(in.ID),
		AgentName:       strings.TrimSpace(in.AgentName),
		Label:           strings.TrimSpace(in.Label),
		EpochID:         strings.TrimSpace(in.EpochID),
		RemoteSessionID: strings.TrimSpace(in.RemoteSessionID),
		ContextSyncSeq:  in.ContextSyncSeq,
		AttachedAt:      in.AttachedAt,
		Source:          strings.TrimSpace(in.Source),
	}
}

func participantStatesFromCore(in []coresession.ParticipantBinding) []kernel.ParticipantState {
	if len(in) == 0 {
		return nil
	}
	out := make([]kernel.ParticipantState, 0, len(in))
	for _, participant := range in {
		out = append(out, kernel.ParticipantState{
			ID:             strings.TrimSpace(participant.ID),
			Kind:           portsession.ParticipantKind(participant.Kind),
			Role:           portsession.ParticipantRole(participant.Role),
			AgentName:      strings.TrimSpace(participant.AgentName),
			Label:          strings.TrimSpace(participant.Label),
			SessionID:      strings.TrimSpace(participant.SessionID),
			Source:         strings.TrimSpace(participant.Source),
			ParentTurnID:   strings.TrimSpace(participant.ParentTurnID),
			DelegationID:   strings.TrimSpace(participant.DelegationID),
			ContextSyncSeq: participant.ContextSyncSeq,
			AttachedAt:     participant.AttachedAt,
			ControllerRef:  strings.TrimSpace(participant.ControllerRef),
		})
	}
	return out
}

func activeTurnForSession(active []kernel.ActiveTurnState, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	for _, item := range active {
		if strings.TrimSpace(item.SessionRef.SessionID) == sessionID {
			return true
		}
	}
	return false
}
