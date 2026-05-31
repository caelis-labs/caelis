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
)

type appServiceGateway struct {
	services appservices.Services
	mu       sync.Mutex
	active   map[string]appServiceActiveTurn
}

type appServiceActiveTurn interface {
	Turn
	CreatedAt() time.Time
	state() ActiveTurnState
}

func newAppServiceGateway(svc appservices.Services) *appServiceGateway {
	return &appServiceGateway{
		services: svc,
		active:   map[string]appServiceActiveTurn{},
	}
}

func (g *appServiceGateway) BeginCoreTurn(ctx context.Context, req BeginTurnRequest) (BeginTurnResult, error) {
	ref := coresession.NormalizeRef(req.SessionRef)
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return BeginTurnResult{}, err
	}
	controller := controllerFromCoreSnapshot(snapshot)
	snapshot.Session.Controller = controller
	turn, err := g.services.Turns().Begin(ctx, appservices.BeginTurnRequest{
		SessionRef:   ref,
		Input:        req.Input,
		ContentParts: coremodel.CloneContentParts(req.ContentParts),
		Model:        req.Model,
		Surface:      req.Surface,
		Meta:         maps.Clone(req.Meta),
	})
	if err != nil {
		return BeginTurnResult{}, err
	}
	handle := newAppServiceTurnHandle(g.services, turn)
	g.register(handle)
	go g.forward(handle)
	return BeginTurnResult{
		Session: snapshot.Session,
		Turn:    handle,
	}, nil
}

func (g *appServiceGateway) SubmitCoreActiveTurn(ctx context.Context, req SubmitActiveTurnRequest) error {
	handle := g.activeForSession(req.SessionRef.SessionID)
	if handle == nil {
		return errNoActiveRun
	}
	if handle.state().Kind != ActiveTurnKindKernel {
		return errNoActiveRun
	}
	return handle.Submit(ctx, cloneCoreSubmission(req.Submission))
}

func (g *appServiceGateway) InterruptCore(ctx context.Context, req InterruptRequest) error {
	return g.services.Turns().Interrupt(ctx, req.SessionRef)
}

func (g *appServiceGateway) CoreControlPlaneState(ctx context.Context, ref coresession.Ref) (ControlPlaneState, error) {
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return ControlPlaneState{}, err
	}
	return controlPlaneStateFromCore(snapshot, g.ActiveCoreTurns()), nil
}

func (g *appServiceGateway) PromptCoreParticipant(ctx context.Context, req PromptParticipantRequest) (BeginTurnResult, error) {
	ref := coresession.NormalizeRef(req.SessionRef)
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return BeginTurnResult{}, err
	}
	participants := participantsFromCoreSnapshot(snapshot)
	participant, ok := findCoreParticipant(participants, req.ParticipantID)
	if !ok {
		return BeginTurnResult{}, fmt.Errorf("core app-service TUI gateway: participant %q is not attached", strings.TrimSpace(req.ParticipantID))
	}
	snapshot.Session.Participants = participants
	handle := newAppServiceAgentTurnHandle(g.services, snapshot.Session.Ref, participant, req.Input, coremodel.CloneContentParts(req.ContentParts), req.Source)
	g.register(handle)
	go func() {
		defer g.unregister(handle)
		handle.run(ctx)
	}()
	return BeginTurnResult{
		Session: snapshot.Session,
		Turn:    handle,
	}, nil
}

func (g *appServiceGateway) ActiveCoreTurns() []ActiveTurnState {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]ActiveTurnState, 0, len(g.active))
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
	appEvents chan appviewmodel.SessionEventEnvelope
	done      chan struct{}
}

func newAppServiceTurnHandle(services appservices.Services, turn coreruntime.Turn) *appServiceTurnHandle {
	return &appServiceTurnHandle{
		services:  services,
		turn:      turn,
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

func (h *appServiceTurnHandle) SessionRef() coresession.Ref {
	return h.turn.SessionRef()
}

func (h *appServiceTurnHandle) CreatedAt() time.Time {
	return h.turn.StartedAt()
}

func (h *appServiceTurnHandle) SessionEvents() <-chan appviewmodel.SessionEventEnvelope {
	return h.appEvents
}

func (h *appServiceTurnHandle) Submit(ctx context.Context, submission coreruntime.Submission) error {
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

func (h *appServiceTurnHandle) Cancel() coreruntime.CancelResult {
	return h.turn.Cancel()
}

func (h *appServiceTurnHandle) Close() error {
	return h.turn.Close()
}

func (h *appServiceTurnHandle) forward() {
	defer close(h.appEvents)
	defer close(h.done)
	stream, err := h.services.Events().SubscribeTurn(context.Background(), h.turn)
	if err != nil {
		h.publishAppEvent(appviewmodel.EventEnvelopeFromError(err.Error()))
		return
	}
	for env := range stream {
		appEnv := appviewmodel.CloneSessionEventEnvelope(env)
		h.publishAppEvent(appEnv)
	}
}

func (h *appServiceTurnHandle) publishAppEvent(env appviewmodel.SessionEventEnvelope) {
	h.appEvents <- appviewmodel.CloneSessionEventEnvelope(env)
}

func (h *appServiceTurnHandle) state() ActiveTurnState {
	return ActiveTurnState{
		SessionRef: h.SessionRef(),
		Kind:       ActiveTurnKindKernel,
		HandleID:   h.HandleID(),
		RunID:      h.RunID(),
		TurnID:     h.TurnID(),
		StartedAt:  h.CreatedAt(),
	}
}

func controlPlaneStateFromCore(snapshot coresession.Snapshot, active []ActiveTurnState) ControlPlaneState {
	ref := coresession.NormalizeRef(snapshot.Session.Ref)
	participants := participantsFromCoreSnapshot(snapshot)
	controller := controllerFromCoreSnapshot(snapshot)
	return ControlPlaneState{
		SessionRef:    ref,
		Controller:    controller,
		Participants:  participants,
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

func activeTurnForSession(active []ActiveTurnState, sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	for _, item := range active {
		if strings.TrimSpace(item.SessionRef.SessionID) == sessionID {
			return true
		}
	}
	return false
}
