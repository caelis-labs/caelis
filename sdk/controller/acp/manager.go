package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdkacpclient "github.com/OnslaughtSnail/caelis/acp/client"
	sdkcontroller "github.com/OnslaughtSnail/caelis/sdk/controller"
	"github.com/OnslaughtSnail/caelis/sdk/internal/acputil"
	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdksubagentacp "github.com/OnslaughtSnail/caelis/sdk/subagent/acp"
)

type Config struct {
	Registry   *sdksubagentacp.Registry
	ClientInfo *sdkacpclient.Implementation
	Clock      func() time.Time
}

type Manager struct {
	registry   *sdksubagentacp.Registry
	clientInfo *sdkacpclient.Implementation
	clock      func() time.Time

	counter atomic.Uint64

	mu           sync.RWMutex
	controllers  map[string]*controllerRun
	participants map[string]*participantRun
}

type controllerRun struct {
	parentSessionID       string
	agent                 string
	client                *sdkacpclient.Client
	remoteSessionID       string
	binding               sdksession.ControllerBinding
	contextPrelude        string
	contextPreludePending bool

	mu                sync.Mutex
	turnID            string
	turnSession       sdksession.Session
	turnStream        bool
	turnMode          string
	approvalRequester sdkcontroller.ApprovalRequester
	handle            *turnHandle
	events            []*sdksession.Event
	updatedAt         time.Time
}

type participantRun struct {
	id              string
	parentSessionID string
	agent           string
	client          *sdkacpclient.Client
	remoteSessionID string
	binding         sdksession.ParticipantBinding

	mu        sync.Mutex
	turnID    string
	handle    *turnHandle
	events    []*sdksession.Event
	updatedAt time.Time
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("sdk/controller/acp: registry is required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Manager{
		registry:     cfg.Registry,
		clientInfo:   cfg.ClientInfo,
		clock:        clock,
		controllers:  map[string]*controllerRun{},
		participants: map[string]*participantRun{},
	}, nil
}

func (m *Manager) Activate(ctx context.Context, req sdkcontroller.HandoffRequest) (sdksession.ControllerBinding, error) {
	req = sdkcontroller.NormalizeHandoffRequest(req)
	parentSessionID := strings.TrimSpace(req.Session.SessionID)
	if parentSessionID == "" {
		return sdksession.ControllerBinding{}, fmt.Errorf("sdk/controller/acp: parent session id is required")
	}
	cfg, err := m.registry.Resolve(req.Agent)
	if err != nil {
		return sdksession.ControllerBinding{}, err
	}

	run := &controllerRun{
		parentSessionID:       parentSessionID,
		agent:                 strings.TrimSpace(cfg.Name),
		binding:               controllerBinding(cfg.Name, req.Source, m.nextID("controller"), m.clock()),
		contextPrelude:        strings.TrimSpace(req.ContextPrelude),
		contextPreludePending: strings.TrimSpace(req.ContextPrelude) != "",
		updatedAt:             m.clock(),
	}
	client, remoteSessionID, err := m.startClient(ctx, req.Session.CWD, cfg,
		func(env sdkacpclient.UpdateEnvelope) {
			run.handleUpdate(m.clock, env)
		},
		func(ctx context.Context, in sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
			return run.permissionHandler(ctx, in)
		},
	)
	if err != nil {
		return sdksession.ControllerBinding{}, err
	}
	run.client = client
	run.remoteSessionID = remoteSessionID
	run.binding.RemoteSessionID = remoteSessionID
	run.binding.ContextSyncSeq = req.ContextSyncSeq

	m.mu.Lock()
	if old := m.controllers[parentSessionID]; old != nil && old.client != nil {
		_ = old.client.Close(ctx)
	}
	m.controllers[parentSessionID] = run
	m.mu.Unlock()
	return sdksession.CloneControllerBinding(run.binding), nil
}

func (m *Manager) Deactivate(ctx context.Context, ref sdksession.SessionRef) error {
	ref = sdksession.NormalizeSessionRef(ref)
	if ref.SessionID == "" {
		return nil
	}
	m.mu.Lock()
	run := m.controllers[ref.SessionID]
	delete(m.controllers, ref.SessionID)
	m.mu.Unlock()
	if run != nil && run.client != nil {
		_ = run.client.Close(context.WithoutCancel(ctx))
	}
	return nil
}

func (m *Manager) RunTurn(ctx context.Context, req sdkcontroller.TurnRequest) (sdkcontroller.TurnResult, error) {
	req = sdkcontroller.NormalizeTurnRequest(req)
	sessionID := strings.TrimSpace(req.SessionRef.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.Session.SessionID)
	}
	m.mu.RLock()
	run := m.controllers[sessionID]
	m.mu.RUnlock()
	if run == nil {
		return sdkcontroller.TurnResult{}, fmt.Errorf("sdk/controller/acp: no active ACP controller for session %q", sessionID)
	}

	prompt := buildPromptParts(req.Input, req.ContentParts)
	prompt = run.consumeContextPrelude(prompt)
	turnCtx, cancel := context.WithCancel(ctx)
	handle := newTurnHandle(cancel)
	run.beginTurn(req, handle)
	if len(prompt) == 0 {
		run.finishTurn()
		handle.finish()
		return sdkcontroller.TurnResult{Handle: handle, UpdatedAt: m.clock()}, nil
	}
	go func() {
		defer handle.finish()
		if _, err := run.client.PromptParts(turnCtx, run.remoteSessionID, prompt, nil); err != nil {
			run.finishTurn()
			handle.publishError(err)
			return
		}
		buffered, stream := run.finishTurn()
		if !stream {
			for _, event := range buffered {
				handle.publishEvent(event)
			}
		}
	}()
	return sdkcontroller.TurnResult{Handle: handle, UpdatedAt: m.clock()}, nil
}

func (m *Manager) Attach(ctx context.Context, req sdkcontroller.AttachRequest) (sdksession.ParticipantBinding, error) {
	req = sdkcontroller.NormalizeAttachRequest(req)
	if strings.TrimSpace(req.Session.SessionID) == "" {
		return sdksession.ParticipantBinding{}, fmt.Errorf("sdk/controller/acp: session id is required")
	}
	cfg, err := m.registry.Resolve(req.Agent)
	if err != nil {
		return sdksession.ParticipantBinding{}, err
	}
	run, err := m.startParticipant(ctx, req.Session, cfg, req)
	if err != nil {
		return sdksession.ParticipantBinding{}, err
	}
	return sdksession.CloneParticipantBinding(run.binding), nil
}

func (m *Manager) PromptParticipant(ctx context.Context, req sdkcontroller.ParticipantPromptRequest) (sdkcontroller.TurnResult, error) {
	req = sdkcontroller.NormalizeParticipantPromptRequest(req)
	if req.ParticipantID == "" {
		return sdkcontroller.TurnResult{}, fmt.Errorf("sdk/controller/acp: participant id is required")
	}
	m.mu.RLock()
	run := m.participants[req.ParticipantID]
	m.mu.RUnlock()
	if run == nil {
		return sdkcontroller.TurnResult{}, fmt.Errorf("sdk/controller/acp: participant %q not found", req.ParticipantID)
	}
	prompt := buildPromptParts(req.Input, req.ContentParts)
	if len(prompt) == 0 {
		return sdkcontroller.TurnResult{}, fmt.Errorf("sdk/controller/acp: participant prompt is required")
	}
	turnCtx, cancel := context.WithCancel(ctx)
	handle := newTurnHandle(cancel)
	run.beginPrompt(req, handle)
	go func() {
		defer handle.finish()
		if _, err := run.client.PromptParts(turnCtx, run.remoteSessionID, prompt, nil); err != nil {
			run.finishPrompt()
			handle.publishError(err)
			return
		}
		buffered := run.finishPrompt()
		for _, event := range buffered {
			handle.publishEvent(event)
		}
	}()
	return sdkcontroller.TurnResult{Handle: handle, UpdatedAt: m.clock()}, nil
}

func (m *Manager) Detach(ctx context.Context, req sdkcontroller.DetachRequest) error {
	req = sdkcontroller.NormalizeDetachRequest(req)
	if req.ParticipantID == "" {
		return nil
	}
	m.mu.Lock()
	run := m.participants[req.ParticipantID]
	delete(m.participants, req.ParticipantID)
	m.mu.Unlock()
	if run != nil && run.client != nil {
		_ = run.client.Close(context.WithoutCancel(ctx))
	}
	return nil
}

func (m *Manager) startParticipant(
	ctx context.Context,
	session sdksession.Session,
	cfg sdksubagentacp.AgentConfig,
	req sdkcontroller.AttachRequest,
) (*participantRun, error) {
	var run *participantRun
	client, remoteSessionID, err := m.startClient(ctx, session.CWD, cfg, func(env sdkacpclient.UpdateEnvelope) {
		if run != nil {
			run.handleUpdate(m.clock, env)
		}
	},
		m.permissionHandler(sdksession.CloneSession(session), strings.TrimSpace(cfg.Name), "", nil),
	)
	if err != nil {
		return nil, err
	}
	id := m.nextID(firstNonEmpty(req.Agent, "participant"))
	role := req.Role
	if role == "" {
		role = sdksession.ParticipantRoleSidecar
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = strings.TrimSpace(req.Agent)
	}
	run = &participantRun{
		id:              id,
		parentSessionID: strings.TrimSpace(session.SessionID),
		agent:           strings.TrimSpace(req.Agent),
		client:          client,
		remoteSessionID: remoteSessionID,
		binding: sdksession.ParticipantBinding{
			ID:            id,
			Kind:          sdksession.ParticipantKindACP,
			Role:          role,
			AgentName:     strings.TrimSpace(req.Agent),
			Label:         label,
			SessionID:     remoteSessionID,
			Source:        firstNonEmpty(req.Source, "user_attach"),
			AttachedAt:    m.clock(),
			ControllerRef: strings.TrimSpace(session.Controller.EpochID),
		},
	}
	m.mu.Lock()
	m.participants[id] = run
	m.mu.Unlock()
	return run, nil
}

func (m *Manager) startClient(
	ctx context.Context,
	cwd string,
	cfg sdksubagentacp.AgentConfig,
	onUpdate func(sdkacpclient.UpdateEnvelope),
	onPermission func(context.Context, sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error),
) (*sdkacpclient.Client, string, error) {
	client, err := sdkacpclient.Start(ctx, sdkacpclient.Config{
		Command:    cfg.Command,
		Args:       append([]string(nil), cfg.Args...),
		Env:        maps.Clone(cfg.Env),
		WorkDir:    pickWorkDir(cfg.WorkDir, cwd),
		ClientInfo: m.clientInfo,
		OnUpdate:   onUpdate,
		OnPermissionRequest: func(ctx context.Context, req sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
			return onPermission(ctx, req)
		},
	})
	if err != nil {
		return nil, "", err
	}
	if _, err := client.Initialize(ctx); err != nil {
		_ = client.Close(ctx)
		return nil, "", err
	}
	resp, err := client.NewSession(ctx, strings.TrimSpace(cwd), nil)
	if err != nil {
		_ = client.Close(ctx)
		return nil, "", err
	}
	return client, strings.TrimSpace(resp.SessionID), nil
}

func (m *Manager) permissionHandler(
	session sdksession.Session,
	agent string,
	mode string,
	requester sdkcontroller.ApprovalRequester,
) func(context.Context, sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
	return func(ctx context.Context, req sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
		trimmedAgent := strings.TrimSpace(agent)
		if auto, ok := acputil.AutoApproveAllOnce(mode, trimmedAgent, req); ok {
			return auto, nil
		}
		if requester != nil {
			resp, err := requester.RequestControllerApproval(ctx, translateApprovalRequest(session, trimmedAgent, mode, req))
			if err != nil {
				return sdkacpclient.RequestPermissionResponse{}, err
			}
			if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
				return selected, nil
			}
		}
		return acputil.RejectOnce(), nil
	}
}

func (r *controllerRun) permissionHandler(ctx context.Context, req sdkacpclient.RequestPermissionRequest) (sdkacpclient.RequestPermissionResponse, error) {
	if r == nil {
		return acputil.RejectOnce(), nil
	}
	r.mu.Lock()
	session := sdksession.CloneSession(r.turnSession)
	mode := strings.TrimSpace(r.turnMode)
	requester := r.approvalRequester
	agent := strings.TrimSpace(r.agent)
	r.mu.Unlock()
	if auto, ok := acputil.AutoApproveAllOnce(mode, agent, req); ok {
		return auto, nil
	}
	if requester != nil {
		resp, err := requester.RequestControllerApproval(ctx, translateApprovalRequest(session, agent, mode, req))
		if err != nil {
			return sdkacpclient.RequestPermissionResponse{}, err
		}
		if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
			return selected, nil
		}
	}
	return acputil.RejectOnce(), nil
}

func translateApprovalRequest(
	session sdksession.Session,
	agent string,
	mode string,
	req sdkacpclient.RequestPermissionRequest,
) sdkcontroller.ApprovalRequest {
	options := make([]sdkcontroller.ApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, sdkcontroller.ApprovalOption{
			ID:   strings.TrimSpace(item.OptionID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	return sdkcontroller.ApprovalRequest{
		SessionRef: sdksession.NormalizeSessionRef(session.SessionRef),
		Session:    sdksession.CloneSession(session),
		Agent:      strings.TrimSpace(agent),
		Mode:       strings.TrimSpace(mode),
		ToolCall: sdkcontroller.ApprovalToolCall{
			ID:     strings.TrimSpace(req.ToolCall.ToolCallID),
			Name:   acputil.ToolCallName(req.ToolCall),
			Kind:   derefString(req.ToolCall.Kind),
			Title:  derefString(req.ToolCall.Title),
			Status: derefString(req.ToolCall.Status),
		},
		Options: options,
	}
}

func controllerBinding(agent string, source string, epochID string, now time.Time) sdksession.ControllerBinding {
	return sdksession.ControllerBinding{
		Kind:         sdksession.ControllerKindACP,
		ControllerID: strings.TrimSpace(agent),
		AgentName:    strings.TrimSpace(agent),
		Label:        strings.TrimSpace(agent),
		EpochID:      strings.TrimSpace(epochID),
		AttachedAt:   now,
		Source:       firstNonEmpty(source, "handoff"),
	}
}

func (r *controllerRun) beginTurn(req sdkcontroller.TurnRequest, handle *turnHandle) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnID = strings.TrimSpace(req.TurnID)
	r.turnSession = sdksession.CloneSession(req.Session)
	r.turnStream = req.Stream
	r.turnMode = strings.TrimSpace(req.Mode)
	r.approvalRequester = req.ApprovalRequester
	r.handle = handle
	r.events = nil
}

func (r *controllerRun) consumeContextPrelude(prompt []json.RawMessage) []json.RawMessage {
	if r == nil {
		return prompt
	}
	r.mu.Lock()
	prelude := strings.TrimSpace(r.contextPrelude)
	pending := r.contextPreludePending
	if pending {
		r.contextPreludePending = false
	}
	r.mu.Unlock()
	if !pending || prelude == "" {
		return prompt
	}
	raw, _ := json.Marshal(sdkacpclient.TextContent{
		Type: "text",
		Text: prelude,
	})
	out := make([]json.RawMessage, 0, len(prompt)+1)
	out = append(out, raw)
	out = append(out, prompt...)
	return out
}

func (r *controllerRun) finishTurn() ([]*sdksession.Event, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buffered := make([]*sdksession.Event, 0, len(r.events))
	for _, event := range r.events {
		buffered = append(buffered, sdksession.CloneEvent(event))
	}
	stream := r.turnStream
	r.turnID = ""
	r.turnSession = sdksession.Session{}
	r.turnStream = false
	r.turnMode = ""
	r.approvalRequester = nil
	r.handle = nil
	r.events = nil
	return buffered, stream
}

func (r *controllerRun) handleUpdate(clock func() time.Time, env sdkacpclient.UpdateEnvelope) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.turnID == "" {
		r.mu.Unlock()
		return
	}
	turnID := r.turnID
	stream := r.turnStream
	handle := r.handle
	event := normalizeACPUpdateEvent(clock, r.binding, r.remoteSessionID, turnID, env.Update)
	if event == nil {
		r.mu.Unlock()
		return
	}
	r.updatedAt = clock()
	r.events = append(r.events, sdksession.CloneEvent(event))
	r.mu.Unlock()
	if stream && handle != nil {
		handle.publishEvent(event)
	}
}

func (r *participantRun) beginPrompt(req sdkcontroller.ParticipantPromptRequest, handle *turnHandle) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnID = firstNonEmpty(strings.TrimSpace(req.ParticipantID), r.id)
	r.handle = handle
	r.events = nil
}

func (r *participantRun) finishPrompt() []*sdksession.Event {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buffered := make([]*sdksession.Event, 0, len(r.events))
	for _, event := range r.events {
		buffered = append(buffered, sdksession.CloneEvent(event))
	}
	r.turnID = ""
	r.handle = nil
	r.events = nil
	return buffered
}

func (r *participantRun) handleUpdate(clock func() time.Time, env sdkacpclient.UpdateEnvelope) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.turnID == "" {
		r.mu.Unlock()
		return
	}
	turnID := r.turnID
	event := normalizeACPUpdateEvent(clock, sdksession.ControllerBinding{
		Kind:         sdksession.ControllerKindACP,
		ControllerID: r.agent,
		Label:        r.binding.Label,
		EpochID:      r.binding.ControllerRef,
	}, r.remoteSessionID, turnID, env.Update)
	if event == nil {
		r.mu.Unlock()
		return
	}
	event.Actor = sdksession.ActorRef{Kind: sdksession.ActorKindParticipant, ID: r.id, Name: strings.TrimSpace(firstNonEmpty(r.binding.Label, r.agent, r.id))}
	if event.Scope == nil {
		event.Scope = &sdksession.EventScope{}
	}
	event.Scope.Source = "acp_participant"
	event.Scope.Controller = sdksession.ControllerRef{}
	event.Scope.Participant = sdksession.ParticipantRef{
		ID:           r.id,
		Kind:         r.binding.Kind,
		Role:         r.binding.Role,
		DelegationID: r.binding.DelegationID,
	}
	r.updatedAt = clock()
	r.events = append(r.events, sdksession.CloneEvent(event))
	r.mu.Unlock()
}

type turnHandle struct {
	cancelFn  context.CancelFunc
	eventsCh  chan turnHandleEvent
	closeOnce sync.Once
	mu        sync.Mutex
	cancelled bool
}

type turnHandleEvent struct {
	event *sdksession.Event
	err   error
}

func newTurnHandle(cancel context.CancelFunc) *turnHandle {
	return &turnHandle{
		cancelFn: cancel,
		eventsCh: make(chan turnHandleEvent, 64),
	}
}

func (h *turnHandle) Events() iter.Seq2[*sdksession.Event, error] {
	return func(yield func(*sdksession.Event, error) bool) {
		for item := range h.eventsCh {
			if !yield(sdksession.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (h *turnHandle) Cancel() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		return false
	}
	h.cancelled = true
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return true
}

func (h *turnHandle) Close() error { return nil }

func (h *turnHandle) publishEvent(event *sdksession.Event) {
	if h == nil || event == nil {
		return
	}
	h.publish(turnHandleEvent{event: sdksession.CloneEvent(event)})
}

func (h *turnHandle) publishError(err error) {
	if h == nil || err == nil {
		return
	}
	h.publish(turnHandleEvent{err: err})
}

func (h *turnHandle) publish(item turnHandleEvent) {
	if h == nil {
		return
	}
	select {
	case h.eventsCh <- item:
	default:
		h.eventsCh <- item
	}
}

func (h *turnHandle) finish() {
	if h == nil {
		return
	}
	h.closeOnce.Do(func() {
		close(h.eventsCh)
	})
}

func normalizeACPUpdateEvent(
	clock func() time.Time,
	binding sdksession.ControllerBinding,
	remoteSessionID string,
	turnID string,
	update sdkacpclient.Update,
) *sdksession.Event {
	controller := sdksession.ControllerRef{
		Kind:    sdksession.ControllerKindACP,
		ID:      strings.TrimSpace(binding.ControllerID),
		EpochID: strings.TrimSpace(binding.EpochID),
	}
	scope := &sdksession.EventScope{
		TurnID:     strings.TrimSpace(turnID),
		Source:     "acp",
		Controller: controller,
		ACP: sdksession.ACPRef{
			SessionID: strings.TrimSpace(remoteSessionID),
		},
	}
	now := time.Now
	if clock != nil {
		now = clock
	}
	switch typed := update.(type) {
	case sdkacpclient.ContentChunk:
		text := contentChunkText(typed)
		if text == "" {
			return nil
		}
		event := &sdksession.Event{
			Visibility: sdksession.VisibilityCanonical,
			Time:       now(),
			Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       text,
			Message:    ptrMessage(messageForContentChunk(typed, text)),
			Protocol:   &sdksession.EventProtocol{UpdateType: typed.SessionUpdate},
		}
		switch strings.TrimSpace(typed.SessionUpdate) {
		case sdkacpclient.UpdateUserMessage:
			event.Type = sdksession.EventTypeUser
			event.Actor = sdksession.ActorRef{Kind: sdksession.ActorKindUser, Name: "user"}
		default:
			event.Type = sdksession.EventTypeAssistant
		}
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		return event
	case sdkacpclient.ToolCall:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		return &sdksession.Event{
			Type:       sdksession.EventTypeToolCall,
			Visibility: sdksession.VisibilityCanonical,
			Time:       now(),
			Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       firstNonEmpty(strings.TrimSpace(typed.Title), strings.TrimSpace(typed.Kind), "tool call"),
			Protocol: &sdksession.EventProtocol{
				UpdateType: typed.SessionUpdate,
				ToolCall: &sdksession.ProtocolToolCall{
					ID:       strings.TrimSpace(typed.ToolCallID),
					Name:     acpToolDisplayName(typed.Kind, typed.Title),
					Kind:     strings.TrimSpace(typed.Kind),
					Title:    strings.TrimSpace(typed.Title),
					Status:   strings.TrimSpace(typed.Status),
					RawInput: acpToolRawInput(typed.Kind, typed.Title, typed.RawInput),
				},
			},
		}
	case sdkacpclient.ToolCallUpdate:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		return &sdksession.Event{
			Type:       toolEventTypeFromStatus(derefString(typed.Status)),
			Visibility: sdksession.VisibilityCanonical,
			Time:       now(),
			Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       firstNonEmpty(strings.TrimSpace(derefString(typed.Title)), strings.TrimSpace(derefString(typed.Kind)), "tool update"),
			Protocol: &sdksession.EventProtocol{
				UpdateType: typed.SessionUpdate,
				ToolCall: &sdksession.ProtocolToolCall{
					ID:        strings.TrimSpace(typed.ToolCallID),
					Name:      acpToolDisplayName(derefString(typed.Kind), derefString(typed.Title)),
					Kind:      strings.TrimSpace(derefString(typed.Kind)),
					Title:     strings.TrimSpace(derefString(typed.Title)),
					Status:    strings.TrimSpace(derefString(typed.Status)),
					RawInput:  acpToolRawInput(derefString(typed.Kind), derefString(typed.Title), typed.RawInput),
					RawOutput: acpToolRawOutput(typed.RawOutput, typed.Content),
				},
			},
		}
	case sdkacpclient.PlanUpdate:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		return &sdksession.Event{
			Type:       sdksession.EventTypePlan,
			Visibility: sdksession.VisibilityCanonical,
			Time:       now(),
			Actor:      sdksession.ActorRef{Kind: sdksession.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       "plan updated",
			Protocol: &sdksession.EventProtocol{
				UpdateType: typed.SessionUpdate,
				Plan:       &sdksession.ProtocolPlan{Entries: planEntries(typed.Entries)},
			},
		}
	}
	return nil
}

func contentChunkText(chunk sdkacpclient.ContentChunk) string {
	var text sdkacpclient.TextChunk
	if err := json.Unmarshal(chunk.Content, &text); err == nil {
		if text.Text != "" {
			return text.Text
		}
		return textFromRawContent(chunk.Content)
	}
	return textFromRawContent(chunk.Content)
}

func textFromRawContent(raw json.RawMessage) string {
	var content any
	if err := json.Unmarshal(raw, &content); err != nil {
		return strings.TrimSpace(string(raw))
	}
	return textFromContentValue(content)
}

func textFromContentValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		var out strings.Builder
		for _, item := range typed {
			out.WriteString(textFromContentValue(item))
		}
		return out.String()
	case map[string]any:
		for _, key := range []string{"text", "content", "detailedContent"} {
			if nested, ok := typed[key]; ok {
				if text := textFromContentValue(nested); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func acpToolDisplayName(kind string, title string) string {
	if title = strings.TrimSpace(title); title != "" {
		return title
	}
	return strings.TrimSpace(kind)
}

func acpToolRawInput(kind string, title string, raw any) map[string]any {
	out := acpRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpToolRawOutput(raw any, content []sdkacpclient.ToolCallContent) map[string]any {
	out := acpRawMap(raw)
	if out == nil {
		out = map[string]any{}
	}
	if text := strings.TrimSpace(acpToolContentText(content)); text != "" {
		if _, exists := out["text"]; !exists {
			out["text"] = text
		}
	}
	for _, item := range content {
		if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
			out["terminal_id"] = terminalID
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpRawMap(raw any) map[string]any {
	switch typed := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return maps.Clone(typed)
	default:
		if text := strings.TrimSpace(textFromContentValue(typed)); text != "" {
			return map[string]any{"text": text}
		}
		if text := strings.TrimSpace(fmt.Sprint(typed)); text != "" && text != "<nil>" {
			return map[string]any{"text": text}
		}
		return nil
	}
}

func acpToolContentText(content []sdkacpclient.ToolCallContent) string {
	if len(content) == 0 {
		return ""
	}
	parts := make([]string, 0, len(content))
	for _, item := range content {
		if text := strings.TrimSpace(textFromContentValue(item.Content)); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func messageForContentChunk(chunk sdkacpclient.ContentChunk, text string) sdkmodel.Message {
	role := sdkmodel.RoleAssistant
	if strings.TrimSpace(chunk.SessionUpdate) == sdkacpclient.UpdateUserMessage {
		role = sdkmodel.RoleUser
	}
	return sdkmodel.NewTextMessage(role, text)
}

func planEntries(in []sdkacpclient.PlanEntry) []sdksession.ProtocolPlanEntry {
	out := make([]sdksession.ProtocolPlanEntry, 0, len(in))
	for _, item := range in {
		out = append(out, sdksession.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: "",
		})
	}
	return out
}

func toolEventTypeFromStatus(status string) sdksession.EventType {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
		return sdksession.EventTypeToolResult
	default:
		return sdksession.EventTypeToolCall
	}
}

func buildPromptParts(input string, parts []sdkmodel.ContentPart) []json.RawMessage {
	if len(parts) == 0 {
		input = strings.TrimSpace(input)
		if input == "" {
			return nil
		}
		raw, _ := json.Marshal(sdkacpclient.TextContent{
			Type: "text",
			Text: input,
		})
		return []json.RawMessage{raw}
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case sdkmodel.ContentPartImage:
			raw, _ := json.Marshal(sdkacpclient.ImageContent{
				Type:     "image",
				MimeType: strings.TrimSpace(part.MimeType),
				Data:     strings.TrimSpace(part.Data),
				Name:     strings.TrimSpace(part.FileName),
			})
			out = append(out, raw)
		default:
			text := strings.TrimSpace(part.Text)
			if text == "" {
				continue
			}
			raw, _ := json.Marshal(sdkacpclient.TextContent{
				Type: "text",
				Text: text,
			})
			out = append(out, raw)
		}
	}
	if len(out) == 0 && strings.TrimSpace(input) != "" {
		raw, _ := json.Marshal(sdkacpclient.TextContent{
			Type: "text",
			Text: strings.TrimSpace(input),
		})
		out = append(out, raw)
	}
	return out
}

func ptrMessage(msg sdkmodel.Message) *sdkmodel.Message {
	return &msg
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func pickWorkDir(preferred string, fallback string) string {
	if strings.TrimSpace(preferred) != "" {
		return strings.TrimSpace(preferred)
	}
	return strings.TrimSpace(fallback)
}

func derefString(in *string) string {
	if in == nil {
		return ""
	}
	return strings.TrimSpace(*in)
}

func (m *Manager) nextID(prefix string) string {
	n := m.counter.Add(1)
	return fmt.Sprintf("%s-%d", prefix, n)
}
