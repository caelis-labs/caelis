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
	"github.com/OnslaughtSnail/caelis/ports/agent"
	portsapproval "github.com/OnslaughtSnail/caelis/ports/approval"
	portsmodel "github.com/OnslaughtSnail/caelis/ports/model"
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
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

func (g *appServiceGateway) Streams() stream.Service {
	return nil
}

func (g *appServiceGateway) BeginTurn(ctx context.Context, req kernel.BeginTurnRequest) (kernel.BeginTurnResult, error) {
	ref := coreRefFromPort(req.SessionRef)
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return kernel.BeginTurnResult{}, err
	}
	controller := controllerFromCoreSnapshot(snapshot)
	snapshot.Session.Controller = controller
	if controller.Kind == coresession.ControllerACP {
		handle := newAppServiceControllerTurnHandle(g.services, snapshot.Session.Ref, controller, req.Input, coreContentPartsFromPort(req.ContentParts), req.Surface)
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
	turn, err := g.services.Turns().Begin(ctx, appservices.BeginTurnRequest{
		SessionRef:   ref,
		Input:        req.Input,
		ContentParts: coreContentPartsFromPort(req.ContentParts),
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
		ContentParts: append([]portsmodel.ContentPart(nil), req.ContentParts...),
		Metadata:     maps.Clone(req.Metadata),
		Approval:     req.Approval,
	})
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
		if converted, ok := kernelEnvelopeFromAppEvent(env); ok {
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
	snapshot, err := g.services.Sessions().Load(ctx, ref)
	if err != nil {
		return portsession.Session{}, err
	}
	controller, err := g.controllerForHandoff(ctx, req)
	if err != nil {
		return portsession.Session{}, err
	}
	event := controllerHandoffEvent(controller, req.Source, req.Reason)
	if _, err := g.services.Engine().RecordEvents(ctx, snapshot.Session.Ref, []coresession.Event{event}); err != nil {
		return portsession.Session{}, err
	}
	snapshot.Events = append(snapshot.Events, event)
	snapshot.Session.Controller = controllerFromCoreSnapshot(snapshot)
	return portSessionFromCore(snapshot.Session), nil
}

func (g *appServiceGateway) controllerForHandoff(ctx context.Context, req kernel.HandoffControllerRequest) (coresession.ControllerBinding, error) {
	switch req.Kind {
	case "", portsession.ControllerKindKernel:
		return coresession.ControllerBinding{
			Kind:       coresession.ControllerBuiltin,
			ID:         "builtin",
			AgentName:  "local",
			Label:      "local kernel",
			EpochID:    controllerEpochID(),
			AttachedAt: time.Now().UTC(),
			Source:     firstNonEmpty(req.Source, "tui_agent_handoff"),
		}, nil
	case portsession.ControllerKindACP:
		descriptor, ok, err := g.resolveAgentDescriptor(ctx, req.Agent)
		if err != nil {
			return coresession.ControllerBinding{}, err
		}
		if !ok {
			return coresession.ControllerBinding{}, fmt.Errorf("core app-service TUI gateway: agent %q is not configured", strings.TrimSpace(req.Agent))
		}
		id := firstNonEmpty(descriptor.ID, descriptor.Name, descriptor.Command)
		return coresession.ControllerBinding{
			Kind:       coresession.ControllerACP,
			ID:         id,
			AgentName:  id,
			Label:      firstNonEmpty(descriptor.Name, id),
			EpochID:    controllerEpochID(),
			AttachedAt: time.Now().UTC(),
			Source:     firstNonEmpty(req.Source, "tui_agent_handoff"),
		}, nil
	default:
		return coresession.ControllerBinding{}, fmt.Errorf("core app-service TUI gateway: unsupported controller kind %q", req.Kind)
	}
}

func controllerHandoffEvent(controller coresession.ControllerBinding, source string, reason string) coresession.Event {
	return coresession.Event{
		Type:       coresession.EventHandoff,
		Visibility: coresession.VisibilityCanonical,
		Time:       time.Now().UTC(),
		Actor:      coresession.ActorRef{Kind: coresession.ActorSystem, ID: "caelis", Name: "caelis"},
		Scope: &coresession.EventScope{
			Source:     firstNonEmpty(source, "tui_agent_handoff"),
			Controller: controller,
		},
		Meta: map[string]any{
			"reason": strings.TrimSpace(reason),
		},
	}
}

func controllerEpochID() string {
	return fmt.Sprintf("controller-%d", time.Now().UTC().UnixNano())
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
	handle := newAppServiceAgentTurnHandle(g.services, snapshot.Session.Ref, participant, req.Input, coreContentPartsFromPort(req.ContentParts), req.Source)
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
	services appservices.Services
	turn     coreruntime.Turn
	events   chan kernel.EventEnvelope
	done     chan struct{}
	mu       sync.Mutex
	history  []kernel.EventEnvelope
}

func newAppServiceTurnHandle(services appservices.Services, turn coreruntime.Turn) *appServiceTurnHandle {
	return &appServiceTurnHandle{
		services: services,
		turn:     turn,
		events:   make(chan kernel.EventEnvelope, 32),
		done:     make(chan struct{}),
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
	submission := coreruntime.Submission{
		Kind:         coreruntime.SubmissionConversation,
		Text:         req.Text,
		ContentParts: coreContentPartsFromPort(req.ContentParts),
		Meta:         maps.Clone(req.Metadata),
	}
	if req.Kind == kernel.SubmissionKindApproval && req.Approval != nil {
		_, err := h.services.Approvals().Submit(ctx, h.turn, appservices.ApprovalDecisionRequest{
			Outcome:  strings.TrimSpace(req.Approval.Outcome),
			OptionID: strings.TrimSpace(req.Approval.OptionID),
			Approved: req.Approval.Approved,
			Reason:   strings.TrimSpace(req.Approval.Reason),
		})
		return err
	}
	return h.turn.Submit(ctx, submission)
}

func (h *appServiceTurnHandle) Cancel() agent.CancelResult {
	result := h.turn.Cancel()
	status := agent.CancelStatusAlreadyCancelled
	if result.Status == coreruntime.CancelCancelled {
		status = agent.CancelStatusCancelled
	}
	return agent.CancelResult{Status: status, Err: result.Err}
}

func (h *appServiceTurnHandle) Close() error {
	return h.turn.Close()
}

func (h *appServiceTurnHandle) forward() {
	defer close(h.events)
	defer close(h.done)
	stream, err := h.services.Events().SubscribeTurn(context.Background(), h.turn)
	if err != nil {
		h.events <- kernel.EventEnvelope{Err: &kernel.Error{Kind: kernel.KindInternal, Code: kernel.CodeInternal, Message: err.Error()}}
		return
	}
	for env := range stream {
		converted, ok := kernelEnvelopeFromAppEvent(env)
		if !ok {
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
		h.events <- converted
	}
}

func kernelEnvelopeFromAppEvent(env appviewmodel.SessionEventEnvelope) (kernel.EventEnvelope, bool) {
	if strings.TrimSpace(env.Error) != "" {
		return kernel.EventEnvelope{
			Err: &kernel.Error{Kind: kernel.KindInternal, Code: kernel.CodeInternal, Message: env.Error},
		}, true
	}
	if env.Canonical == nil {
		return kernel.EventEnvelope{}, false
	}
	return kernelEnvelopeFromCore(coreruntime.EventEnvelope{
		Cursor: coresession.Cursor(strings.TrimSpace(env.Cursor)),
		Event:  coresession.CloneEvent(*env.Canonical),
	})
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

func kernelEnvelopeFromCore(env coreruntime.EventEnvelope) (kernel.EventEnvelope, bool) {
	if strings.TrimSpace(env.Err) != "" {
		return kernel.EventEnvelope{
			Err: &kernel.Error{Kind: kernel.KindInternal, Code: kernel.CodeInternal, Message: env.Err},
		}, true
	}
	if env.Event.Type == "" {
		return kernel.EventEnvelope{}, false
	}
	event := kernelEventFromCore(env.Event)
	cursor := strings.TrimSpace(string(env.Cursor))
	if cursor == "" {
		cursor = strings.TrimSpace(env.Event.ID)
	}
	return kernel.EventEnvelope{Cursor: cursor, Event: event}, true
}

func kernelEventFromCore(event coresession.Event) kernel.Event {
	ref := portsession.SessionRef{SessionID: strings.TrimSpace(event.SessionID)}
	scope := coreEventScope(event)
	participantID := coreEventParticipantID(event)
	actor := coreEventActor(event)
	out := kernel.Event{
		Kind:       kernelEventKind(event.Type),
		TurnID:     coreEventTurnID(event),
		OccurredAt: event.Time,
		SessionRef: ref,
		Origin:     coreEventOrigin(event, scope, participantID, actor),
		Meta:       coreEventMeta(event),
	}
	if out.Meta == nil {
		out.Meta = map[string]any{}
	}
	text := coresession.EventText(event)
	switch event.Type {
	case coresession.EventUser:
		out.Narrative = &kernel.NarrativePayload{Role: kernel.NarrativeRoleUser, Actor: actor, Text: text, Final: true, Scope: scope, ParticipantID: participantID}
	case coresession.EventAssistant:
		out.Narrative = &kernel.NarrativePayload{Role: kernel.NarrativeRoleAssistant, Actor: actor, Text: text, Final: true, Scope: scope, ParticipantID: participantID}
	case coresession.EventSystem:
		out.Narrative = &kernel.NarrativePayload{Role: kernel.NarrativeRoleSystem, Actor: actor, Text: text, Final: true, Scope: scope, ParticipantID: participantID}
	case coresession.EventNotice:
		out.Narrative = &kernel.NarrativePayload{Role: kernel.NarrativeRoleNotice, Actor: actor, Text: text, Final: true, Scope: scope, ParticipantID: participantID}
	case coresession.EventToolCall:
		out.ToolCall = coreToolCallPayload(event)
	case coresession.EventToolResult:
		out.ToolResult = coreToolResultPayload(event)
	case coresession.EventApproval:
		out.ApprovalPayload = coreApprovalPayload(event)
	case coresession.EventPlan:
		out.Plan = corePlanPayload(event)
	case coresession.EventLifecycle:
		out.Lifecycle = coreLifecyclePayload(event)
	case coresession.EventParticipant:
		out.Participant = coreParticipantPayload(event)
	}
	return out
}

func coreEventScope(event coresession.Event) kernel.EventScope {
	if event.Scope == nil {
		return kernel.EventScopeMain
	}
	participant := event.Scope.Participant
	if strings.TrimSpace(participant.ID) == "" {
		return kernel.EventScopeMain
	}
	if participant.Kind == coresession.ParticipantSubagent {
		return kernel.EventScopeSubagent
	}
	return kernel.EventScopeParticipant
}

func coreEventParticipantID(event coresession.Event) string {
	if event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.Participant.ID)
}

func coreEventActor(event coresession.Event) string {
	if event.Scope != nil && strings.TrimSpace(event.Scope.Participant.ID) != "" {
		participant := event.Scope.Participant
		return firstNonEmpty(participant.Label, participant.AgentName, participant.ID)
	}
	return firstNonEmpty(event.Actor.Name, event.Actor.ID, string(event.Actor.Kind))
}

func coreEventOrigin(event coresession.Event, scope kernel.EventScope, participantID string, actor string) *kernel.EventOrigin {
	if scope == kernel.EventScopeMain && strings.TrimSpace(participantID) == "" {
		return nil
	}
	origin := &kernel.EventOrigin{
		Scope:         scope,
		ScopeID:       strings.TrimSpace(participantID),
		Source:        "core",
		Actor:         strings.TrimSpace(actor),
		ParticipantID: strings.TrimSpace(participantID),
	}
	if event.Scope != nil {
		origin.Source = firstNonEmpty(event.Scope.Source, origin.Source)
		participant := event.Scope.Participant
		origin.ParticipantKind = strings.TrimSpace(string(participant.Kind))
		origin.ParticipantSessionID = strings.TrimSpace(participant.SessionID)
	}
	return origin
}

func kernelEventKind(kind coresession.EventType) kernel.EventKind {
	switch kind {
	case coresession.EventUser:
		return kernel.EventKindUserMessage
	case coresession.EventAssistant:
		return kernel.EventKindAssistantMessage
	case coresession.EventSystem:
		return kernel.EventKindSystemMessage
	case coresession.EventToolCall:
		return kernel.EventKindToolCall
	case coresession.EventToolResult:
		return kernel.EventKindToolResult
	case coresession.EventApproval:
		return kernel.EventKindApprovalRequested
	case coresession.EventPlan:
		return kernel.EventKindPlanUpdate
	case coresession.EventCompact:
		return kernel.EventKindCompact
	case coresession.EventLifecycle:
		return kernel.EventKindLifecycle
	case coresession.EventParticipant:
		return kernel.EventKindParticipant
	case coresession.EventHandoff:
		return kernel.EventKindHandoff
	case coresession.EventNotice:
		return kernel.EventKindNotice
	default:
		return kernel.EventKindNotice
	}
}

func coreEventTurnID(event coresession.Event) string {
	if event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.TurnID)
}

func coreToolCallPayload(event coresession.Event) *kernel.ToolCallPayload {
	if event.Tool == nil {
		return nil
	}
	scope := coreEventScope(event)
	return &kernel.ToolCallPayload{
		CallID:        strings.TrimSpace(event.Tool.ID),
		ToolName:      strings.TrimSpace(event.Tool.Name),
		ToolKind:      strings.TrimSpace(event.Tool.Kind),
		ToolTitle:     strings.TrimSpace(event.Tool.Title),
		RawInput:      maps.Clone(event.Tool.Input),
		Status:        coreToolStatus(event.Tool.Status),
		Actor:         coreEventActor(event),
		Scope:         scope,
		ParticipantID: coreEventParticipantID(event),
	}
}

func coreToolResultPayload(event coresession.Event) *kernel.ToolResultPayload {
	if event.Tool == nil {
		return nil
	}
	scope := coreEventScope(event)
	return &kernel.ToolResultPayload{
		CallID:        strings.TrimSpace(event.Tool.ID),
		ToolName:      strings.TrimSpace(event.Tool.Name),
		ToolKind:      strings.TrimSpace(event.Tool.Kind),
		ToolTitle:     strings.TrimSpace(event.Tool.Title),
		RawInput:      maps.Clone(event.Tool.Input),
		RawOutput:     maps.Clone(event.Tool.Output),
		Status:        coreToolStatus(event.Tool.Status),
		Error:         event.Tool.Status == coresession.ToolFailed,
		Actor:         coreEventActor(event),
		Scope:         scope,
		ParticipantID: coreEventParticipantID(event),
	}
}

func coreEventMeta(event coresession.Event) map[string]any {
	meta := maps.Clone(event.Meta)
	if event.Tool != nil {
		meta = mergeCoreMeta(meta, event.Tool.Meta)
	}
	return meta
}

func mergeCoreMeta(base map[string]any, extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return maps.Clone(base)
	}
	out := maps.Clone(base)
	if out == nil {
		out = map[string]any{}
	}
	for key, value := range extra {
		if value == nil {
			continue
		}
		if existing, ok := out[key]; ok {
			existingMap, existingOK := existing.(map[string]any)
			valueMap, valueOK := value.(map[string]any)
			if existingOK && valueOK {
				out[key] = mergeCoreMeta(existingMap, valueMap)
			}
			continue
		}
		out[key] = cloneCoreMetaValue(value)
	}
	return out
}

func cloneCoreMetaValue(value any) any {
	mapped, ok := value.(map[string]any)
	if !ok {
		return value
	}
	return mergeCoreMeta(nil, mapped)
}

func coreToolStatus(status coresession.ToolStatus) kernel.ToolStatus {
	switch status {
	case coresession.ToolStarted:
		return kernel.ToolStatusStarted
	case coresession.ToolRunning:
		return kernel.ToolStatusRunning
	case coresession.ToolWaitingApproval:
		return kernel.ToolStatusWaitingApproval
	case coresession.ToolCompleted:
		return kernel.ToolStatusCompleted
	case coresession.ToolFailed:
		return kernel.ToolStatusFailed
	case coresession.ToolCancelled:
		return kernel.ToolStatusCancelled
	default:
		return kernel.ToolStatusRunning
	}
}

func coreApprovalPayload(event coresession.Event) *portsapproval.Payload {
	if event.Approval == nil {
		return nil
	}
	payload := &portsapproval.Payload{
		Reason:  strings.TrimSpace(event.Approval.Reason),
		Status:  portsapproval.Status(event.Approval.Status),
		Options: coreApprovalOptions(event.Approval.Options),
	}
	if tool := event.Approval.Tool; tool != nil {
		payload.ToolCallID = strings.TrimSpace(tool.ID)
		payload.ToolName = strings.TrimSpace(tool.Name)
		payload.RawInput = maps.Clone(tool.Input)
	} else if event.Tool != nil {
		payload.ToolCallID = strings.TrimSpace(event.Tool.ID)
		payload.ToolName = strings.TrimSpace(event.Tool.Name)
		payload.RawInput = maps.Clone(event.Tool.Input)
	}
	return payload
}

func coreApprovalOptions(in []coresession.ApprovalOption) []portsapproval.Option {
	if len(in) == 0 {
		return nil
	}
	out := make([]portsapproval.Option, 0, len(in))
	for _, option := range in {
		out = append(out, portsapproval.Option{
			ID:   strings.TrimSpace(option.ID),
			Name: strings.TrimSpace(option.Name),
			Kind: strings.TrimSpace(option.Kind),
		})
	}
	return out
}

func corePlanPayload(event coresession.Event) *kernel.PlanPayload {
	if len(event.Plan) == 0 {
		return nil
	}
	out := &kernel.PlanPayload{Entries: make([]kernel.PlanEntryPayload, 0, len(event.Plan))}
	for _, entry := range event.Plan {
		out.Entries = append(out.Entries, kernel.PlanEntryPayload{
			Content: strings.TrimSpace(entry.Content),
			Status:  strings.TrimSpace(entry.Status),
		})
	}
	return out
}

func coreLifecyclePayload(event coresession.Event) *kernel.LifecyclePayload {
	if event.Lifecycle == nil {
		return nil
	}
	return &kernel.LifecyclePayload{
		Status:        kernel.LifecycleStatus(event.Lifecycle.Status),
		Reason:        strings.TrimSpace(event.Lifecycle.Reason),
		Actor:         coreEventActor(event),
		Scope:         coreEventScope(event),
		ParticipantID: coreEventParticipantID(event),
	}
}

func coreParticipantPayload(event coresession.Event) *kernel.ParticipantPayload {
	if event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return nil
	}
	participant := event.Scope.Participant
	return &kernel.ParticipantPayload{
		ParticipantID:   strings.TrimSpace(participant.ID),
		ParticipantKind: strings.TrimSpace(string(participant.Kind)),
		Role:            strings.TrimSpace(string(participant.Role)),
		Label:           firstNonEmpty(participant.Label, participant.AgentName, participant.ID),
		Action:          coreParticipantAction(event),
		SessionID:       strings.TrimSpace(participant.SessionID),
		ParentTurnID:    strings.TrimSpace(participant.ParentTurnID),
		DelegationID:    strings.TrimSpace(participant.DelegationID),
		Scope:           coreEventScope(event),
	}
}

func coreParticipantAction(event coresession.Event) kernel.ParticipantAction {
	action := strings.ToLower(strings.TrimSpace(coreEventMetaString(event.Meta, "action")))
	switch action {
	case "attached":
		return kernel.ParticipantActionAttached
	case "detached":
		return kernel.ParticipantActionDetached
	default:
		return ""
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

func coreContentPartsFromPort(parts []portsmodel.ContentPart) []coremodel.ContentPart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]coremodel.ContentPart, 0, len(parts))
	for _, part := range parts {
		out = append(out, coremodel.ContentPart{
			Type:     coremodel.ContentPartType(part.Type),
			Text:     part.Text,
			MimeType: part.MimeType,
			Data:     part.Data,
			FileName: part.FileName,
		})
	}
	return out
}
