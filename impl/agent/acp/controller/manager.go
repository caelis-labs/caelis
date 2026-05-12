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

	"github.com/OnslaughtSnail/caelis/impl/agent/acp/internal/acputil"
	"github.com/OnslaughtSnail/caelis/impl/agent/acp/subagent"
	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/client"
)

type Config struct {
	Registry   *acp.Registry
	ClientInfo *client.Implementation
	Clock      func() time.Time
}

type Manager struct {
	registry    *acp.Registry
	clientInfo  *client.Implementation
	clock       func() time.Time
	startClient clientStarter

	counter atomic.Uint64

	mu           sync.RWMutex
	controllers  map[string]*controllerRun
	participants map[string]*participantRun
}

type clientStarter func(
	ctx context.Context,
	cwd string,
	cfg acp.AgentConfig,
	resumeRemoteSessionID string,
	onUpdate func(client.UpdateEnvelope),
	onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
) (*client.Client, string, controllerClientState, error)

type controllerRun struct {
	parentSessionID       string
	agent                 string
	client                *client.Client
	remoteSessionID       string
	supportsClose         bool
	binding               session.ControllerBinding
	contextPrelude        string
	contextPreludePending bool

	mu                sync.Mutex
	commands          []controller.ControllerCommand
	configOptions     []controller.ControllerConfigOption
	models            *client.SessionModelState
	mode              string
	modeOptions       []controller.ControllerMode
	remoteTitle       string
	turnID            string
	turnSession       session.Session
	turnStream        bool
	turnMode          string
	approvalRequester controller.ApprovalRequester
	handle            *turnHandle
	events            []*session.Event
	updatedAt         time.Time
}

type controllerClientState struct {
	commands      []controller.ControllerCommand
	configOptions []controller.ControllerConfigOption
	models        *client.SessionModelState
	mode          string
	modeOptions   []controller.ControllerMode
	agentLabel    string
	supportsClose bool
}

type participantRun struct {
	id              string
	parentSessionID string
	agent           string
	client          *client.Client
	remoteSessionID string
	binding         session.ParticipantBinding

	mu                sync.Mutex
	turnID            string
	turnSession       session.Session
	turnStream        bool
	turnMode          string
	approvalRequester controller.ApprovalRequester
	handle            *turnHandle
	events            []*session.Event
	updatedAt         time.Time
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("impl/agent/acp/controller: registry is required")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	manager := &Manager{
		registry:     cfg.Registry,
		clientInfo:   cfg.ClientInfo,
		clock:        clock,
		controllers:  map[string]*controllerRun{},
		participants: map[string]*participantRun{},
	}
	manager.startClient = manager.startACPClient
	return manager, nil
}

func (m *Manager) Activate(ctx context.Context, req controller.HandoffRequest) (session.ControllerBinding, error) {
	req = controller.NormalizeHandoffRequest(req)
	parentSessionID := strings.TrimSpace(req.Session.SessionID)
	if parentSessionID == "" {
		return session.ControllerBinding{}, fmt.Errorf("impl/agent/acp/controller: parent session id is required")
	}
	cfg, err := m.registry.Resolve(req.Agent)
	if err != nil {
		return session.ControllerBinding{}, err
	}

	run := &controllerRun{
		parentSessionID:       parentSessionID,
		agent:                 strings.TrimSpace(cfg.Name),
		binding:               controllerBinding(cfg.Name, req.Source, m.nextID("controller"), m.clock()),
		contextPrelude:        strings.TrimSpace(req.ContextPrelude),
		contextPreludePending: strings.TrimSpace(req.ContextPrelude) != "",
		updatedAt:             m.clock(),
	}
	resumeRemoteSessionID := reusableControllerRemoteSessionID(req.Session, cfg.Name)
	client, remoteSessionID, state, err := m.startClient(ctx, req.Session.CWD, cfg, resumeRemoteSessionID,
		func(env client.UpdateEnvelope) {
			run.handleUpdate(m.clock, env)
		},
		func(ctx context.Context, in client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return run.permissionHandler(ctx, in)
		},
	)
	if err != nil {
		return session.ControllerBinding{}, err
	}
	run.applyStartupStateLocked(client, remoteSessionID, state, req.ContextSyncSeq)

	m.mu.Lock()
	if old := m.controllers[parentSessionID]; old != nil && old.client != nil {
		_ = old.client.Close(ctx)
	}
	m.controllers[parentSessionID] = run
	m.mu.Unlock()
	return session.CloneControllerBinding(run.binding), nil
}

func (r *controllerRun) applyStartupStateLocked(client *client.Client, remoteSessionID string, state controllerClientState, contextSyncSeq int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.client = client
	r.remoteSessionID = strings.TrimSpace(remoteSessionID)
	r.supportsClose = state.supportsClose
	r.commands = mergeControllerCommands(r.commands, state.commands)
	r.configOptions = fillControllerConfigOptions(r.configOptions, state.configOptions)
	r.models = cloneACPSessionModelState(state.models)
	if strings.TrimSpace(r.mode) == "" {
		r.mode = strings.TrimSpace(state.mode)
	}
	r.modeOptions = mergeControllerModes(r.modeOptions, state.modeOptions)
	r.binding.RemoteSessionID = strings.TrimSpace(remoteSessionID)
	r.binding.ContextSyncSeq = contextSyncSeq
	if label := strings.TrimSpace(state.agentLabel); label != "" {
		r.binding.Label = label
	}
}

func (r *controllerRun) closeRemoteSession(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	client := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	supportsClose := r.supportsClose
	r.mu.Unlock()
	if client == nil || remoteSessionID == "" || !supportsClose {
		return
	}
	_ = client.CloseSession(ctx, remoteSessionID)
}

func (m *Manager) Deactivate(ctx context.Context, ref session.SessionRef) error {
	ref = session.NormalizeSessionRef(ref)
	if ref.SessionID == "" {
		return nil
	}
	m.mu.Lock()
	run := m.controllers[ref.SessionID]
	delete(m.controllers, ref.SessionID)
	m.mu.Unlock()
	if run != nil && run.client != nil {
		run.closeRemoteSession(context.WithoutCancel(ctx))
		_ = run.client.Close(context.WithoutCancel(ctx))
	}
	return nil
}

func (m *Manager) RunTurn(ctx context.Context, req controller.TurnRequest) (controller.TurnResult, error) {
	req = controller.NormalizeTurnRequest(req)
	sessionID := strings.TrimSpace(req.SessionRef.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(req.Session.SessionID)
	}
	m.mu.RLock()
	run := m.controllers[sessionID]
	m.mu.RUnlock()
	if run == nil {
		return controller.TurnResult{}, fmt.Errorf("impl/agent/acp/controller: no active ACP controller for session %q", sessionID)
	}

	prompt := buildPromptParts(req.Input, req.ContentParts)
	prompt = run.consumeContextPrelude(prompt)
	prompt = prependACPContextPrelude(prompt, req.ContextPrelude)
	turnCtx, cancel := context.WithCancel(ctx)
	handle := newTurnHandle(cancel)
	run.beginTurn(req, handle)
	if len(prompt) == 0 {
		run.finishTurn()
		handle.finish()
		return controller.TurnResult{Handle: handle, UpdatedAt: m.clock()}, nil
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
	return controller.TurnResult{Handle: handle, UpdatedAt: m.clock()}, nil
}

func (m *Manager) ControllerStatus(_ context.Context, ref session.SessionRef) (controller.ControllerStatus, bool, error) {
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return controller.ControllerStatus{}, false, nil
	}
	m.mu.RLock()
	run := m.controllers[ref.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return controller.ControllerStatus{}, false, nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.controllerStatusLocked(ref), true, nil
}

func (m *Manager) SetControllerModel(ctx context.Context, req controller.SetControllerModelRequest) (controller.ControllerStatus, error) {
	req.SessionRef = session.NormalizeSessionRef(req.SessionRef)
	if strings.TrimSpace(req.SessionRef.SessionID) == "" {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: session id is required")
	}
	m.mu.RLock()
	run := m.controllers[req.SessionRef.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: no active ACP controller for session %q", req.SessionRef.SessionID)
	}
	return run.setControllerModel(ctx, req, m.clock)
}

func (m *Manager) SetControllerMode(ctx context.Context, req controller.SetControllerModeRequest) (controller.ControllerStatus, error) {
	req.SessionRef = session.NormalizeSessionRef(req.SessionRef)
	if strings.TrimSpace(req.SessionRef.SessionID) == "" {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: session id is required")
	}
	m.mu.RLock()
	run := m.controllers[req.SessionRef.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: no active ACP controller for session %q", req.SessionRef.SessionID)
	}
	return run.setControllerMode(ctx, req, m.clock)
}

func (m *Manager) Attach(ctx context.Context, req controller.AttachRequest) (session.ParticipantBinding, error) {
	req = controller.NormalizeAttachRequest(req)
	if strings.TrimSpace(req.Session.SessionID) == "" {
		return session.ParticipantBinding{}, fmt.Errorf("impl/agent/acp/controller: session id is required")
	}
	cfg, err := m.registry.Resolve(req.Agent)
	if err != nil {
		return session.ParticipantBinding{}, err
	}
	run, err := m.startParticipant(ctx, req.Session, cfg, req)
	if err != nil {
		return session.ParticipantBinding{}, err
	}
	return session.CloneParticipantBinding(run.binding), nil
}

func (m *Manager) PromptParticipant(ctx context.Context, req controller.ParticipantPromptRequest) (controller.TurnResult, error) {
	req = controller.NormalizeParticipantPromptRequest(req)
	if req.ParticipantID == "" {
		return controller.TurnResult{}, fmt.Errorf("impl/agent/acp/controller: participant id is required")
	}
	m.mu.RLock()
	run := m.participants[req.ParticipantID]
	m.mu.RUnlock()
	if run == nil {
		return controller.TurnResult{}, fmt.Errorf("impl/agent/acp/controller: participant %q not found", req.ParticipantID)
	}
	prompt := buildPromptParts(req.Input, req.ContentParts)
	prompt = prependACPContextPrelude(prompt, req.ContextPrelude)
	if len(prompt) == 0 {
		return controller.TurnResult{}, fmt.Errorf("impl/agent/acp/controller: participant prompt is required")
	}
	turnCtx, cancel := context.WithCancel(ctx)
	handle := newTurnHandle(cancel)
	run.beginPrompt(req, handle)
	go func() {
		defer handle.finish()
		if _, err := run.client.PromptParts(turnCtx, run.remoteSessionID, prompt, nil); err != nil {
			_, _ = run.finishPrompt()
			handle.publishError(err)
			return
		}
		buffered, stream := run.finishPrompt()
		if !stream {
			for _, event := range buffered {
				handle.publishEvent(event)
			}
		}
	}()
	return controller.TurnResult{Handle: handle, UpdatedAt: m.clock()}, nil
}

func (m *Manager) Detach(ctx context.Context, req controller.DetachRequest) error {
	req = controller.NormalizeDetachRequest(req)
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
	parentSession session.Session,
	cfg acp.AgentConfig,
	req controller.AttachRequest,
) (*participantRun, error) {
	var run *participantRun
	client, remoteSessionID, _, err := m.startClient(ctx, parentSession.CWD, cfg, "", func(env client.UpdateEnvelope) {
		if run != nil {
			run.handleUpdate(m.clock, env)
		}
	},
		func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			if run != nil {
				return run.permissionHandler(ctx, req)
			}
			return m.permissionHandler(session.CloneSession(parentSession), strings.TrimSpace(cfg.Name), "", nil)(ctx, req)
		},
	)
	if err != nil {
		return nil, err
	}
	id := m.nextID(firstNonEmpty(req.Agent, "participant"))
	role := req.Role
	if role == "" {
		role = session.ParticipantRoleSidecar
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = strings.TrimSpace(req.Agent)
	}
	run = &participantRun{
		id:              id,
		parentSessionID: strings.TrimSpace(parentSession.SessionID),
		agent:           strings.TrimSpace(req.Agent),
		client:          client,
		remoteSessionID: remoteSessionID,
		binding: session.ParticipantBinding{
			ID:            id,
			Kind:          session.ParticipantKindACP,
			Role:          role,
			AgentName:     strings.TrimSpace(req.Agent),
			Label:         label,
			SessionID:     remoteSessionID,
			Source:        firstNonEmpty(req.Source, "user_attach"),
			AttachedAt:    m.clock(),
			ControllerRef: strings.TrimSpace(parentSession.Controller.EpochID),
		},
	}
	m.mu.Lock()
	m.participants[id] = run
	m.mu.Unlock()
	return run, nil
}

func (m *Manager) startACPClient(
	ctx context.Context,
	cwd string,
	cfg acp.AgentConfig,
	resumeRemoteSessionID string,
	onUpdate func(client.UpdateEnvelope),
	onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
) (*client.Client, string, controllerClientState, error) {
	client, err := client.Start(ctx, client.Config{
		Command:    cfg.Command,
		Args:       append([]string(nil), cfg.Args...),
		Env:        maps.Clone(cfg.Env),
		WorkDir:    pickWorkDir(cfg.WorkDir, cwd),
		ClientInfo: m.clientInfo,
		OnUpdate:   onUpdate,
		OnPermissionRequest: func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return onPermission(ctx, req)
		},
	})
	if err != nil {
		return nil, "", controllerClientState{}, err
	}
	initResp, err := client.Initialize(ctx)
	if err != nil {
		_ = client.Close(ctx)
		return nil, "", controllerClientState{}, err
	}
	remoteSessionID := strings.TrimSpace(resumeRemoteSessionID)
	if remoteSessionID != "" && acpSessionCapability(initResp, "resume") {
		resp, err := client.ResumeSession(ctx, remoteSessionID, strings.TrimSpace(cwd), nil)
		if err != nil {
			_ = client.Close(ctx)
			return nil, "", controllerClientState{}, err
		}
		state := controllerClientState{
			configOptions: controllerConfigOptionsFromACP(resp.ConfigOptions),
			models:        cloneACPSessionModelState(resp.Models),
			mode:          currentModeID(resp.Modes),
			modeOptions:   controllerModesFromACP(resp.Modes),
			supportsClose: acpSessionCapability(initResp, "close"),
		}
		if initResp.AgentInfo != nil {
			state.agentLabel = strings.TrimSpace(firstNonEmpty(initResp.AgentInfo.Title, initResp.AgentInfo.Name, initResp.AgentInfo.Version))
		}
		return client, remoteSessionID, state, nil
	}
	resp, err := client.NewSession(ctx, strings.TrimSpace(cwd), nil)
	if err != nil {
		_ = client.Close(ctx)
		return nil, "", controllerClientState{}, err
	}
	state := controllerClientState{
		configOptions: controllerConfigOptionsFromACP(resp.ConfigOptions),
		models:        cloneACPSessionModelState(resp.Models),
		mode:          currentModeID(resp.Modes),
		modeOptions:   controllerModesFromACP(resp.Modes),
		supportsClose: acpSessionCapability(initResp, "close"),
	}
	if initResp.AgentInfo != nil {
		state.agentLabel = strings.TrimSpace(firstNonEmpty(initResp.AgentInfo.Title, initResp.AgentInfo.Name, initResp.AgentInfo.Version))
	}
	return client, strings.TrimSpace(resp.SessionID), state, nil
}

func reusableControllerRemoteSessionID(sess session.Session, agentName string) string {
	binding := session.CloneControllerBinding(sess.Controller)
	if binding.Kind != session.ControllerKindACP {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(binding.AgentName), strings.TrimSpace(agentName)) {
		return ""
	}
	return strings.TrimSpace(binding.RemoteSessionID)
}

func acpSessionCapability(resp client.InitializeResponse, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if resp.AgentCapabilities.SessionCapabilities == nil {
		return false
	}
	_, ok := resp.AgentCapabilities.SessionCapabilities[name]
	return ok
}

func (m *Manager) permissionHandler(
	session session.Session,
	agent string,
	mode string,
	requester controller.ApprovalRequester,
) func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
	return func(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
		trimmedAgent := strings.TrimSpace(agent)
		if auto, ok := acputil.AutoApproveAllOnce(mode, trimmedAgent, req); ok {
			return auto, nil
		}
		if requester != nil {
			resp, err := requester.RequestControllerApproval(ctx, translateApprovalRequest(session, trimmedAgent, mode, req))
			if err != nil {
				return client.RequestPermissionResponse{}, err
			}
			if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
				return selected, nil
			}
		}
		return acputil.RejectOnce(), nil
	}
}

func (r *controllerRun) permissionHandler(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
	if r == nil {
		return acputil.RejectOnce(), nil
	}
	r.mu.Lock()
	activeSession := session.CloneSession(r.turnSession)
	mode := strings.TrimSpace(r.turnMode)
	requester := r.approvalRequester
	agent := strings.TrimSpace(r.agent)
	r.mu.Unlock()
	if auto, ok := acputil.AutoApproveAllOnce(mode, agent, req); ok {
		return auto, nil
	}
	if requester != nil {
		resp, err := requester.RequestControllerApproval(ctx, translateApprovalRequest(activeSession, agent, mode, req))
		if err != nil {
			return client.RequestPermissionResponse{}, err
		}
		if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
			return selected, nil
		}
	}
	return acputil.RejectOnce(), nil
}

func translateApprovalRequest(
	turnSession session.Session,
	agent string,
	mode string,
	req client.RequestPermissionRequest,
) controller.ApprovalRequest {
	options := make([]controller.ApprovalOption, 0, len(req.Options))
	for _, item := range req.Options {
		options = append(options, controller.ApprovalOption{
			ID:   strings.TrimSpace(item.OptionID),
			Name: strings.TrimSpace(item.Name),
			Kind: strings.TrimSpace(item.Kind),
		})
	}
	return controller.ApprovalRequest{
		SessionRef: session.NormalizeSessionRef(turnSession.SessionRef),
		Session:    session.CloneSession(turnSession),
		Agent:      strings.TrimSpace(agent),
		Mode:       strings.TrimSpace(mode),
		ToolCall: controller.ApprovalToolCall{
			ID:       strings.TrimSpace(req.ToolCall.ToolCallID),
			Name:     acputil.ToolCallName(req.ToolCall),
			Kind:     derefString(req.ToolCall.Kind),
			Title:    derefString(req.ToolCall.Title),
			Status:   derefString(req.ToolCall.Status),
			RawInput: acpRawMap(req.ToolCall.RawInput),
		},
		Options: options,
	}
}

func controllerBinding(agent string, source string, epochID string, now time.Time) session.ControllerBinding {
	return session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: strings.TrimSpace(agent),
		AgentName:    strings.TrimSpace(agent),
		Label:        strings.TrimSpace(agent),
		EpochID:      strings.TrimSpace(epochID),
		AttachedAt:   now,
		Source:       firstNonEmpty(source, "handoff"),
	}
}

func (r *controllerRun) beginTurn(req controller.TurnRequest, handle *turnHandle) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnID = strings.TrimSpace(req.TurnID)
	r.turnSession = session.CloneSession(req.Session)
	r.turnStream = req.Stream
	r.turnMode = strings.TrimSpace(req.Mode)
	if req.ContextSyncSeq > r.binding.ContextSyncSeq {
		r.binding.ContextSyncSeq = req.ContextSyncSeq
	}
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
	return prependACPContextPrelude(prompt, prelude)
}

func prependACPContextPrelude(prompt []json.RawMessage, prelude string) []json.RawMessage {
	prelude = strings.TrimSpace(prelude)
	if prelude == "" {
		return prompt
	}
	raw, _ := json.Marshal(client.TextContent{
		Type: "text",
		Text: prelude,
	})
	out := make([]json.RawMessage, 0, len(prompt)+1)
	out = append(out, raw)
	out = append(out, prompt...)
	return out
}

func (r *controllerRun) finishTurn() ([]*session.Event, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buffered := make([]*session.Event, 0, len(r.events))
	for _, event := range r.events {
		buffered = append(buffered, session.CloneEvent(event))
	}
	stream := r.turnStream
	r.turnID = ""
	r.turnSession = session.Session{}
	r.turnStream = false
	r.turnMode = ""
	r.approvalRequester = nil
	r.handle = nil
	r.events = nil
	return buffered, stream
}

func (r *controllerRun) handleUpdate(clock func() time.Time, env client.UpdateEnvelope) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.applySessionUpdateLocked(clock, env.Update)
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
	r.events = append(r.events, session.CloneEvent(event))
	r.mu.Unlock()
	if stream && handle != nil {
		handle.publishEvent(event)
	}
}

func (r *controllerRun) applySessionUpdateLocked(clock func() time.Time, update client.Update) {
	if r == nil {
		return
	}
	switch typed := update.(type) {
	case client.AvailableCommandsUpdate:
		r.commands = controllerCommandsFromACP(typed.AvailableCommands)
	case client.ConfigOptionUpdate:
		r.configOptions = mergeControllerConfigOptions(r.configOptions, controllerConfigOptionsFromACP(typed.ConfigOptions))
	case client.CurrentModeUpdate:
		r.mode = strings.TrimSpace(typed.CurrentModeID)
	case client.SessionInfoUpdate:
		if typed.Title != nil {
			r.remoteTitle = strings.TrimSpace(*typed.Title)
		}
		if typed.UpdatedAt != nil {
			if parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*typed.UpdatedAt)); err == nil {
				r.updatedAt = parsed
				return
			}
		}
	default:
		return
	}
	if clock != nil {
		r.updatedAt = clock()
	} else {
		r.updatedAt = time.Now()
	}
}

func (r *controllerRun) controllerStatusLocked(ref session.SessionRef) controller.ControllerStatus {
	if r == nil {
		return controller.ControllerStatus{}
	}
	modelOption, _ := pickModelConfigOption(r.configOptions)
	effortOption, _ := pickEffortConfigOption(r.configOptions)
	status := controller.ControllerStatus{
		SessionRef:      session.NormalizeSessionRef(ref),
		Agent:           strings.TrimSpace(r.agent),
		RemoteSessionID: strings.TrimSpace(r.remoteSessionID),
		RemoteTitle:     strings.TrimSpace(r.remoteTitle),
		Commands:        cloneControllerCommands(r.commands),
		ConfigOptions:   cloneControllerConfigOptions(r.configOptions),
		Mode:            strings.TrimSpace(r.mode),
		ModeOptions:     cloneControllerModes(r.modeOptions),
		UpdatedAt:       r.updatedAt,
	}
	if modelOption != nil {
		status.Model = strings.TrimSpace(modelOption.CurrentValue)
		status.ModelOptions = cloneControllerConfigChoices(modelOption.Options)
	}
	if effortOption != nil {
		status.ReasoningEffort = strings.TrimSpace(effortOption.CurrentValue)
		status.EffortOptions = cloneControllerConfigChoices(effortOption.Options)
	}
	status.EffortOptionsByModel = controllerEffortChoicesByModelFromModels(r.models)
	if testModel, effort, ok := splitACPCurrentModelEffort(r.models); ok {
		if status.Model == "" {
			status.Model = testModel
		}
		if status.ReasoningEffort == "" && strings.EqualFold(strings.TrimSpace(status.Model), testModel) {
			status.ReasoningEffort = effort
		}
	}
	if len(status.EffortOptions) == 0 {
		status.EffortOptions = controllerEffortChoicesFromMap(status.EffortOptionsByModel, status.Model)
	}
	return status
}

func (r *controllerRun) setControllerModel(ctx context.Context, req controller.SetControllerModelRequest, clock func() time.Time) (controller.ControllerStatus, error) {
	if r == nil {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: controller run is unavailable")
	}
	testModel := strings.TrimSpace(req.Model)
	effort := strings.TrimSpace(req.ReasoningEffort)
	if testModel == "" && effort == "" {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: model or reasoning effort is required")
	}
	r.mu.Lock()
	acpClient := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	configOptions := cloneControllerConfigOptions(r.configOptions)
	models := cloneACPSessionModelState(r.models)
	r.mu.Unlock()
	if acpClient == nil || remoteSessionID == "" {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: active controller client is unavailable")
	}

	if testModel != "" {
		modelOption, hasModelOption := pickModelConfigOption(configOptions)
		if !hasModelOption || modelOption == nil {
			return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: controller does not declare a model config option")
		}
		choice, ok := matchControllerConfigChoice(modelOption.Options, testModel)
		if !ok {
			return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: model %q is not declared by the controller", testModel)
		}
		resp, err := acpClient.SetConfigOption(ctx, remoteSessionID, modelOption.ID, choice.Value)
		if err != nil {
			return controller.ControllerStatus{}, err
		}
		configOptions = mergeControllerConfigOptions(configOptions, controllerConfigOptionsFromACP(resp.ConfigOptions))
	}
	if effort != "" {
		effortOption, hasEffortOption := pickEffortConfigOption(configOptions)
		if hasEffortOption && effortOption != nil {
			choice, ok := matchControllerConfigChoice(effortOption.Options, effort)
			if !ok {
				return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: reasoning effort %q is not declared by the controller", effort)
			}
			resp, err := acpClient.SetConfigOption(ctx, remoteSessionID, effortOption.ID, choice.Value)
			if err != nil {
				return controller.ControllerStatus{}, err
			}
			configOptions = mergeControllerConfigOptions(configOptions, controllerConfigOptionsFromACP(resp.ConfigOptions))
		} else {
			modelForEffort := firstNonEmpty(testModel, currentModelFromConfigOptions(configOptions))
			modelID, ok := matchACPModelIDForEffort(models, modelForEffort, effort)
			if !ok {
				return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: reasoning effort %q is not declared by the controller", effort)
			}
			if err := acpClient.SetModel(ctx, remoteSessionID, modelID); err != nil {
				return controller.ControllerStatus{}, err
			}
			modelBase, _, ok := splitACPCurrentModelEffort(&client.SessionModelState{CurrentModelID: modelID})
			if !ok {
				modelBase = modelForEffort
			}
			models = withACPCurrentModelID(models, modelID)
			configOptions = setControllerConfigCurrentValue(configOptions, modelBase)
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.configOptions = cloneControllerConfigOptions(configOptions)
	r.models = cloneACPSessionModelState(models)
	if clock != nil {
		r.updatedAt = clock()
	} else {
		r.updatedAt = time.Now()
	}
	return r.controllerStatusLocked(req.SessionRef), nil
}

func (r *controllerRun) setControllerMode(ctx context.Context, req controller.SetControllerModeRequest, clock func() time.Time) (controller.ControllerStatus, error) {
	if r == nil {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: controller run is unavailable")
	}
	requested := strings.TrimSpace(req.Mode)
	if requested == "" {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: mode is required")
	}
	r.mu.Lock()
	client := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	modeOptions := cloneControllerModes(r.modeOptions)
	r.mu.Unlock()
	if client == nil || remoteSessionID == "" {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: active controller client is unavailable")
	}
	if len(modeOptions) == 0 {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: controller does not declare session modes")
	}
	choice, ok := matchControllerMode(modeOptions, requested)
	if !ok {
		return controller.ControllerStatus{}, fmt.Errorf("impl/agent/acp/controller: mode %q is not declared by the controller", requested)
	}
	if err := client.SetMode(ctx, remoteSessionID, strings.TrimSpace(choice.ID)); err != nil {
		return controller.ControllerStatus{}, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = strings.TrimSpace(choice.ID)
	if clock != nil {
		r.updatedAt = clock()
	} else {
		r.updatedAt = time.Now()
	}
	return r.controllerStatusLocked(req.SessionRef), nil
}

func (r *participantRun) beginPrompt(req controller.ParticipantPromptRequest, handle *turnHandle) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.turnID = firstNonEmpty(strings.TrimSpace(req.TurnID), strings.TrimSpace(req.ParticipantID), r.id)
	r.turnSession = session.CloneSession(req.Session)
	r.turnStream = req.Stream
	r.turnMode = strings.TrimSpace(req.Mode)
	r.approvalRequester = req.ApprovalRequester
	r.handle = handle
	r.events = nil
}

func (r *participantRun) finishPrompt() ([]*session.Event, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	buffered := make([]*session.Event, 0, len(r.events))
	for _, event := range r.events {
		buffered = append(buffered, session.CloneEvent(event))
	}
	stream := r.turnStream
	r.turnID = ""
	r.turnSession = session.Session{}
	r.turnStream = false
	r.turnMode = ""
	r.approvalRequester = nil
	r.handle = nil
	r.events = nil
	return buffered, stream
}

func (r *participantRun) permissionHandler(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
	if r == nil {
		return acputil.RejectOnce(), nil
	}
	r.mu.Lock()
	activeSession := session.CloneSession(r.turnSession)
	mode := strings.TrimSpace(r.turnMode)
	requester := r.approvalRequester
	agent := strings.TrimSpace(r.agent)
	r.mu.Unlock()
	if auto, ok := acputil.AutoApproveAllOnce(mode, agent, req); ok {
		return auto, nil
	}
	if requester != nil {
		resp, err := requester.RequestControllerApproval(ctx, translateApprovalRequest(activeSession, agent, mode, req))
		if err != nil {
			return client.RequestPermissionResponse{}, err
		}
		if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
			return selected, nil
		}
	}
	return acputil.RejectOnce(), nil
}

func (r *participantRun) handleUpdate(clock func() time.Time, env client.UpdateEnvelope) {
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
	event := normalizeACPUpdateEvent(clock, session.ControllerBinding{
		Kind:         session.ControllerKindACP,
		ControllerID: r.agent,
		Label:        r.binding.Label,
		EpochID:      r.binding.ControllerRef,
	}, r.remoteSessionID, turnID, env.Update)
	if event == nil {
		r.mu.Unlock()
		return
	}
	event.Actor = session.ActorRef{Kind: session.ActorKindParticipant, ID: r.id, Name: strings.TrimSpace(firstNonEmpty(r.binding.Label, r.agent, r.id))}
	if event.Scope == nil {
		event.Scope = &session.EventScope{}
	}
	event.Scope.Source = "acp_participant"
	event.Scope.Controller = session.ControllerRef{}
	event.Scope.Participant = session.ParticipantRef{
		ID:           r.id,
		Kind:         r.binding.Kind,
		Role:         r.binding.Role,
		DelegationID: r.binding.DelegationID,
	}
	if event.Meta == nil {
		event.Meta = map[string]any{}
	}
	if agent := strings.TrimSpace(r.agent); agent != "" {
		event.Meta["agent"] = agent
	}
	if label := strings.TrimSpace(r.binding.Label); label != "" {
		event.Meta["mention"] = label
		event.Meta["handle"] = strings.TrimPrefix(label, "@")
	}
	r.updatedAt = clock()
	r.events = append(r.events, session.CloneEvent(event))
	r.mu.Unlock()
	if stream && handle != nil {
		handle.publishEvent(event)
	}
}

type turnHandle struct {
	cancelFn  context.CancelFunc
	eventsCh  chan turnHandleEvent
	mu        sync.Mutex
	cancelled bool
	closed    bool
}

type turnHandleEvent struct {
	event *session.Event
	err   error
}

func newTurnHandle(cancel context.CancelFunc) *turnHandle {
	return &turnHandle{
		cancelFn: cancel,
		eventsCh: make(chan turnHandleEvent, 64),
	}
}

func (h *turnHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for item := range h.eventsCh {
			if !yield(session.CloneEvent(item.event), item.err) {
				return
			}
		}
	}
}

func (h *turnHandle) Cancel() controller.CancelResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled {
		return controller.CancelResult{Status: controller.CancelStatusAlreadyCancelled}
	}
	h.cancelled = true
	if h.cancelFn != nil {
		h.cancelFn()
	}
	return controller.CancelResult{Status: controller.CancelStatusCancelled}
}

func (h *turnHandle) Close() error { return nil }

func (h *turnHandle) publishEvent(event *session.Event) {
	if h == nil || event == nil {
		return
	}
	h.publish(turnHandleEvent{event: session.CloneEvent(event)})
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
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	select {
	case h.eventsCh <- item:
	default:
	}
}

func (h *turnHandle) finish() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.eventsCh)
}

func normalizeACPUpdateEvent(
	clock func() time.Time,
	binding session.ControllerBinding,
	remoteSessionID string,
	turnID string,
	update client.Update,
) *session.Event {
	controller := session.ControllerRef{
		Kind:    session.ControllerKindACP,
		ID:      strings.TrimSpace(binding.ControllerID),
		EpochID: strings.TrimSpace(binding.EpochID),
	}
	scope := &session.EventScope{
		TurnID:     strings.TrimSpace(turnID),
		Source:     "acp",
		Controller: controller,
		ACP: session.ACPRef{
			SessionID: strings.TrimSpace(remoteSessionID),
		},
	}
	now := time.Now
	if clock != nil {
		now = clock
	}
	switch typed := update.(type) {
	case client.ContentChunk:
		text := contentChunkText(typed)
		if text == "" {
			return nil
		}
		event := &session.Event{
			Visibility: acpContentChunkVisibility(typed.SessionUpdate),
			Time:       now(),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       text,
			Message:    ptrMessage(messageForContentChunk(typed, text)),
			Protocol: &session.EventProtocol{
				UpdateType: typed.SessionUpdate,
				Update: &session.ProtocolUpdate{
					SessionUpdate: strings.TrimSpace(typed.SessionUpdate),
					Content:       typed.Content,
				},
			},
		}
		switch strings.TrimSpace(typed.SessionUpdate) {
		case client.UpdateUserMessage:
			event.Type = session.EventTypeUser
			event.Actor = session.ActorRef{Kind: session.ActorKindUser, Name: "user"}
		default:
			event.Type = session.EventTypeAssistant
		}
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		return event
	case client.ToolCall:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		targetTool := &session.ProtocolToolCall{
			ID:       strings.TrimSpace(typed.ToolCallID),
			Name:     acpToolDisplayName(typed.Kind, typed.Title),
			Kind:     strings.TrimSpace(typed.Kind),
			Title:    strings.TrimSpace(typed.Title),
			Status:   strings.TrimSpace(typed.Status),
			RawInput: acpToolRawInput(typed.Kind, typed.Title, typed.RawInput),
		}
		return &session.Event{
			Type:       session.EventTypeToolCall,
			Visibility: session.VisibilityUIOnly,
			Time:       now(),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       firstNonEmpty(strings.TrimSpace(typed.Title), strings.TrimSpace(typed.Kind), "tool call"),
			Protocol: &session.EventProtocol{
				UpdateType: typed.SessionUpdate,
				ToolCall:   targetTool,
				Update: &session.ProtocolUpdate{
					SessionUpdate: strings.TrimSpace(typed.SessionUpdate),
					ToolCallID:    targetTool.ID,
					Kind:          targetTool.Kind,
					Title:         targetTool.Title,
					Status:        targetTool.Status,
					RawInput:      maps.Clone(targetTool.RawInput),
					RawOutput:     maps.Clone(targetTool.RawOutput),
					Meta:          maps.Clone(typed.Meta),
				},
			},
		}
	case client.ToolCallUpdate:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		targetTool := &session.ProtocolToolCall{
			ID:        strings.TrimSpace(typed.ToolCallID),
			Name:      acpToolDisplayName(derefString(typed.Kind), derefString(typed.Title)),
			Kind:      strings.TrimSpace(derefString(typed.Kind)),
			Title:     strings.TrimSpace(derefString(typed.Title)),
			Status:    strings.TrimSpace(derefString(typed.Status)),
			RawInput:  acpToolRawInput(derefString(typed.Kind), derefString(typed.Title), typed.RawInput),
			RawOutput: acpToolRawOutput(typed.RawOutput, typed.Content),
		}
		return &session.Event{
			Type:       toolEventTypeFromStatus(derefString(typed.Status)),
			Visibility: session.VisibilityUIOnly,
			Time:       now(),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       firstNonEmpty(strings.TrimSpace(derefString(typed.Title)), strings.TrimSpace(derefString(typed.Kind)), "tool update"),
			Protocol: &session.EventProtocol{
				UpdateType: typed.SessionUpdate,
				ToolCall:   targetTool,
				Update: &session.ProtocolUpdate{
					SessionUpdate: strings.TrimSpace(typed.SessionUpdate),
					ToolCallID:    targetTool.ID,
					Kind:          targetTool.Kind,
					Title:         targetTool.Title,
					Status:        targetTool.Status,
					RawInput:      maps.Clone(targetTool.RawInput),
					RawOutput:     maps.Clone(targetTool.RawOutput),
					Meta:          maps.Clone(typed.Meta),
				},
			},
		}
	case client.PlanUpdate:
		scope.ACP.EventType = strings.TrimSpace(typed.SessionUpdate)
		return &session.Event{
			Type:       session.EventTypePlan,
			Visibility: session.VisibilityUIOnly,
			Time:       now(),
			Actor:      session.ActorRef{Kind: session.ActorKindController, Name: strings.TrimSpace(binding.Label)},
			Scope:      scope,
			Text:       "plan updated",
			Protocol: &session.EventProtocol{
				UpdateType: typed.SessionUpdate,
				Plan:       &session.ProtocolPlan{Entries: planEntries(typed.Entries)},
				Update: &session.ProtocolUpdate{
					SessionUpdate: strings.TrimSpace(typed.SessionUpdate),
					Entries:       planEntries(typed.Entries),
				},
			},
		}
	}
	return nil
}

func acpContentChunkVisibility(updateType string) session.Visibility {
	switch strings.TrimSpace(updateType) {
	case client.UpdateUserMessage:
		return session.VisibilityCanonical
	default:
		return session.VisibilityUIOnly
	}
}

func contentChunkText(chunk client.ContentChunk) string {
	var text client.TextChunk
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

func controllerCommandsFromACP(in []map[string]any) []controller.ControllerCommand {
	if len(in) == 0 {
		return nil
	}
	out := make([]controller.ControllerCommand, 0, len(in))
	seen := map[string]struct{}{}
	for _, item := range in {
		name := normalizeACPCommandName(firstNonEmpty(
			stringMapValue(item, "name"),
			stringMapValue(item, "command"),
			stringMapValue(item, "id"),
			stringMapValue(item, "title"),
		))
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		out = append(out, controller.ControllerCommand{
			Name:        name,
			Description: firstNonEmpty(stringMapValue(item, "description"), stringMapValue(item, "detail")),
		})
		seen[key] = struct{}{}
	}
	return out
}

func normalizeACPCommandName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "/")
	if fields := strings.Fields(name); len(fields) > 0 {
		name = fields[0]
	}
	return strings.ToLower(strings.TrimSpace(name))
}

func stringMapValue(item map[string]any, key string) string {
	if len(item) == 0 {
		return ""
	}
	raw, ok := item[key]
	if !ok || raw == nil {
		return ""
	}
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func controllerConfigOptionsFromACP(in []client.SessionConfigOption) []controller.ControllerConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]controller.ControllerConfigOption, 0, len(in))
	for _, item := range in {
		option := controller.ControllerConfigOption{
			ID:           strings.TrimSpace(item.ID),
			Name:         strings.TrimSpace(item.Name),
			Type:         strings.TrimSpace(item.Type),
			Category:     strings.TrimSpace(item.Category),
			Description:  strings.TrimSpace(item.Description),
			CurrentValue: stringFromACPValue(item.CurrentValue),
			Options:      make([]controller.ControllerConfigChoice, 0, len(item.Options)),
		}
		for _, choice := range item.Options {
			value := strings.TrimSpace(choice.Value)
			if value == "" {
				continue
			}
			option.Options = append(option.Options, controller.ControllerConfigChoice{
				Value:       value,
				Name:        strings.TrimSpace(choice.Name),
				Description: strings.TrimSpace(choice.Description),
			})
		}
		out = append(out, option)
	}
	return out
}

func stringFromACPValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func currentModeID(modes *client.SessionModeState) string {
	if modes == nil {
		return ""
	}
	return strings.TrimSpace(modes.CurrentModeID)
}

func splitACPCurrentModelEffort(models *client.SessionModelState) (string, string, bool) {
	if models == nil {
		return "", "", false
	}
	testModel, effort, hasEffort := splitACPModelIDEffort(models.CurrentModelID)
	if hasEffort {
		return testModel, effort, true
	}
	modelID := strings.TrimSpace(models.CurrentModelID)
	return modelID, "", modelID != ""
}

func splitACPModelIDEffort(modelID string) (string, string, bool) {
	modelID = strings.TrimSpace(modelID)
	idx := strings.LastIndex(modelID, "/")
	if idx <= 0 || idx == len(modelID)-1 {
		return modelID, "", false
	}
	effort := strings.ToLower(strings.TrimSpace(modelID[idx+1:]))
	if !isReasoningEffortValue(effort) {
		return modelID, "", false
	}
	return strings.TrimSpace(modelID[:idx]), effort, true
}

func isReasoningEffortValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

func controllerEffortChoicesFromModels(models *client.SessionModelState, model string) []controller.ControllerConfigChoice {
	return controllerEffortChoicesFromMap(controllerEffortChoicesByModelFromModels(models), model)
}

func controllerEffortChoicesByModelFromModels(models *client.SessionModelState) map[string][]controller.ControllerConfigChoice {
	if models == nil || len(models.AvailableModels) == 0 {
		return nil
	}
	out := map[string][]controller.ControllerConfigChoice{}
	seen := map[string]map[string]struct{}{}
	for _, item := range models.AvailableModels {
		base, effort, hasEffort := splitACPModelIDEffort(item.ModelID)
		base = strings.TrimSpace(base)
		if !hasEffort || base == "" {
			continue
		}
		modelKey := strings.ToLower(base)
		key := strings.ToLower(strings.TrimSpace(effort))
		if key == "" {
			continue
		}
		if seen[modelKey] == nil {
			seen[modelKey] = map[string]struct{}{}
		}
		if _, exists := seen[modelKey][key]; exists {
			continue
		}
		seen[modelKey][key] = struct{}{}
		out[modelKey] = append(out[modelKey], controller.ControllerConfigChoice{
			Value:       key,
			Name:        reasoningEffortDisplayName(key),
			Description: strings.TrimSpace(item.Description),
		})
	}
	return out
}

func controllerEffortChoicesFromMap(options map[string][]controller.ControllerConfigChoice, model string) []controller.ControllerConfigChoice {
	if len(options) == 0 {
		return nil
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	return cloneControllerConfigChoices(options[model])
}

func matchACPModelIDForEffort(models *client.SessionModelState, model string, effort string) (string, bool) {
	if models == nil {
		return "", false
	}
	model = strings.TrimSpace(model)
	effort = strings.ToLower(strings.TrimSpace(effort))
	if model == "" {
		model, _, _ = splitACPCurrentModelEffort(models)
	}
	if model == "" || effort == "" {
		return "", false
	}
	if base, existingEffort, hasEffort := splitACPModelIDEffort(model); hasEffort {
		return model, strings.EqualFold(existingEffort, effort) && base != ""
	}
	for _, item := range models.AvailableModels {
		base, itemEffort, hasEffort := splitACPModelIDEffort(item.ModelID)
		if !hasEffort {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(base), model) && strings.EqualFold(itemEffort, effort) {
			return strings.TrimSpace(item.ModelID), true
		}
	}
	return "", false
}

func withACPCurrentModelID(models *client.SessionModelState, modelID string) *client.SessionModelState {
	out := cloneACPSessionModelState(models)
	if out == nil {
		out = &client.SessionModelState{}
	}
	out.CurrentModelID = strings.TrimSpace(modelID)
	return out
}

func reasoningEffortDisplayName(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "xhigh":
		return "Xhigh"
	case "minimal":
		return "Minimal"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "max":
		return "Max"
	case "none":
		return "None"
	default:
		return strings.TrimSpace(effort)
	}
}

func controllerModesFromACP(modes *client.SessionModeState) []controller.ControllerMode {
	if modes == nil || len(modes.AvailableModes) == 0 {
		return nil
	}
	out := make([]controller.ControllerMode, 0, len(modes.AvailableModes))
	for _, mode := range modes.AvailableModes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		out = append(out, controller.ControllerMode{
			ID:          id,
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func pickModelConfigOption(options []controller.ControllerConfigOption) (*controller.ControllerConfigOption, bool) {
	return pickControllerConfigOption(options, matchModelConfigOption)
}

func pickEffortConfigOption(options []controller.ControllerConfigOption) (*controller.ControllerConfigOption, bool) {
	return pickControllerConfigOption(options, func(option controller.ControllerConfigOption) (bool, int) {
		id := strings.ToLower(strings.TrimSpace(option.ID))
		haystack := controllerConfigOptionHaystack(option)
		switch id {
		case "effort", "reasoning", "reasoning_effort", "reasoningeffort":
			return true, 0
		}
		if strings.Contains(haystack, "effort") || strings.Contains(haystack, "reasoning") {
			return true, 1
		}
		return false, 0
	})
}

func matchModelConfigOption(option controller.ControllerConfigOption) (bool, int) {
	id := strings.ToLower(strings.TrimSpace(option.ID))
	haystack := controllerConfigOptionHaystack(option)
	if id == "model" || id == "model_id" || id == "modelid" {
		return true, 0
	}
	if strings.Contains(haystack, "model") && !strings.Contains(haystack, "reason") && !strings.Contains(haystack, "effort") {
		return true, 1
	}
	return false, 0
}

func currentModelFromConfigOptions(options []controller.ControllerConfigOption) string {
	if option, ok := pickModelConfigOption(options); ok && option != nil {
		return strings.TrimSpace(option.CurrentValue)
	}
	return ""
}

func setControllerConfigCurrentValue(options []controller.ControllerConfigOption, model string) []controller.ControllerConfigOption {
	model = strings.TrimSpace(model)
	if model == "" {
		return cloneControllerConfigOptions(options)
	}
	out := cloneControllerConfigOptions(options)
	bestIndex := -1
	bestScore := 1000
	for i := range out {
		ok, score := matchModelConfigOption(out[i])
		if !ok {
			continue
		}
		if bestIndex < 0 || score < bestScore {
			bestIndex = i
			bestScore = score
		}
	}
	if bestIndex >= 0 {
		out[bestIndex].CurrentValue = model
		return out
	}
	return append(out, controller.ControllerConfigOption{
		ID:           "model",
		Name:         "Model",
		Type:         "select",
		Category:     "model",
		CurrentValue: model,
	})
}

func pickControllerConfigOption(
	options []controller.ControllerConfigOption,
	match func(controller.ControllerConfigOption) (bool, int),
) (*controller.ControllerConfigOption, bool) {
	var picked *controller.ControllerConfigOption
	bestScore := 1000
	for i := range options {
		ok, score := match(options[i])
		if !ok {
			continue
		}
		if picked == nil || score < bestScore {
			picked = &options[i]
			bestScore = score
		}
	}
	return picked, picked != nil
}

func controllerConfigOptionHaystack(option controller.ControllerConfigOption) string {
	return strings.ToLower(strings.Join([]string{
		strings.TrimSpace(option.ID),
		strings.TrimSpace(option.Name),
		strings.TrimSpace(option.Category),
		strings.TrimSpace(option.Description),
	}, " "))
}

func matchControllerConfigChoice(options []controller.ControllerConfigChoice, requested string) (controller.ControllerConfigChoice, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return controller.ControllerConfigChoice{}, false
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Value), requested) || strings.EqualFold(strings.TrimSpace(option.Name), requested) {
			if strings.TrimSpace(option.Value) == "" {
				continue
			}
			return option, true
		}
	}
	return controller.ControllerConfigChoice{}, false
}

func mergeControllerConfigChoices(primary []controller.ControllerConfigChoice, fallback []controller.ControllerConfigChoice) []controller.ControllerConfigChoice {
	if len(primary) == 0 {
		return cloneControllerConfigChoices(fallback)
	}
	out := cloneControllerConfigChoices(primary)
	seen := map[string]struct{}{}
	for _, item := range out {
		if value := strings.ToLower(strings.TrimSpace(item.Value)); value != "" {
			seen[value] = struct{}{}
		}
	}
	for _, item := range fallback {
		value := strings.TrimSpace(item.Value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		out = append(out, item)
		seen[key] = struct{}{}
	}
	return out
}

func matchControllerMode(options []controller.ControllerMode, requested string) (controller.ControllerMode, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return controller.ControllerMode{}, false
	}
	for _, option := range options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			continue
		}
		if strings.EqualFold(id, requested) || strings.EqualFold(strings.TrimSpace(option.Name), requested) {
			return option, true
		}
	}
	return controller.ControllerMode{}, false
}

func mergeControllerConfigOptions(existing []controller.ControllerConfigOption, updates []controller.ControllerConfigOption) []controller.ControllerConfigOption {
	if len(updates) == 0 {
		return cloneControllerConfigOptions(existing)
	}
	if len(existing) == 0 {
		return cloneControllerConfigOptions(updates)
	}
	out := cloneControllerConfigOptions(existing)
	indexByID := map[string]int{}
	for i, item := range out {
		if id := strings.ToLower(strings.TrimSpace(item.ID)); id != "" {
			indexByID[id] = i
		}
	}
	for _, item := range updates {
		id := strings.ToLower(strings.TrimSpace(item.ID))
		if id != "" {
			if idx, exists := indexByID[id]; exists {
				out[idx] = mergeControllerConfigOption(out[idx], item)
				continue
			}
			indexByID[id] = len(out)
		}
		out = append(out, cloneControllerConfigOption(item))
	}
	return out
}

func mergeControllerConfigOption(existing controller.ControllerConfigOption, update controller.ControllerConfigOption) controller.ControllerConfigOption {
	out := cloneControllerConfigOption(existing)
	if value := strings.TrimSpace(update.ID); value != "" {
		out.ID = value
	}
	if value := strings.TrimSpace(update.Name); value != "" {
		out.Name = value
	}
	if value := strings.TrimSpace(update.Type); value != "" {
		out.Type = value
	}
	if value := strings.TrimSpace(update.Category); value != "" {
		out.Category = value
	}
	if value := strings.TrimSpace(update.Description); value != "" {
		out.Description = value
	}
	out.CurrentValue = strings.TrimSpace(update.CurrentValue)
	out.Options = mergeControllerConfigChoices(existing.Options, update.Options)
	return out
}

func fillControllerConfigOptions(existing []controller.ControllerConfigOption, fallback []controller.ControllerConfigOption) []controller.ControllerConfigOption {
	if len(existing) == 0 {
		return cloneControllerConfigOptions(fallback)
	}
	if len(fallback) == 0 {
		return cloneControllerConfigOptions(existing)
	}
	out := cloneControllerConfigOptions(existing)
	indexByID := map[string]int{}
	for i, item := range out {
		if id := strings.ToLower(strings.TrimSpace(item.ID)); id != "" {
			indexByID[id] = i
		}
	}
	for _, item := range fallback {
		id := strings.ToLower(strings.TrimSpace(item.ID))
		if id != "" {
			if idx, exists := indexByID[id]; exists {
				out[idx] = fillControllerConfigOption(out[idx], item)
				continue
			}
			indexByID[id] = len(out)
		}
		out = append(out, cloneControllerConfigOption(item))
	}
	return out
}

func fillControllerConfigOption(existing controller.ControllerConfigOption, fallback controller.ControllerConfigOption) controller.ControllerConfigOption {
	out := cloneControllerConfigOption(existing)
	if strings.TrimSpace(out.ID) == "" {
		out.ID = strings.TrimSpace(fallback.ID)
	}
	if strings.TrimSpace(out.Name) == "" {
		out.Name = strings.TrimSpace(fallback.Name)
	}
	if strings.TrimSpace(out.Type) == "" {
		out.Type = strings.TrimSpace(fallback.Type)
	}
	if strings.TrimSpace(out.Category) == "" {
		out.Category = strings.TrimSpace(fallback.Category)
	}
	if strings.TrimSpace(out.Description) == "" {
		out.Description = strings.TrimSpace(fallback.Description)
	}
	if strings.TrimSpace(out.CurrentValue) == "" {
		out.CurrentValue = strings.TrimSpace(fallback.CurrentValue)
	}
	out.Options = mergeControllerConfigChoices(existing.Options, fallback.Options)
	return out
}

func cloneControllerCommands(in []controller.ControllerCommand) []controller.ControllerCommand {
	if len(in) == 0 {
		return nil
	}
	return append([]controller.ControllerCommand(nil), in...)
}

func mergeControllerCommands(existing []controller.ControllerCommand, fallback []controller.ControllerCommand) []controller.ControllerCommand {
	if len(existing) == 0 {
		return cloneControllerCommands(fallback)
	}
	if len(fallback) == 0 {
		return cloneControllerCommands(existing)
	}
	out := cloneControllerCommands(existing)
	seen := map[string]struct{}{}
	for _, command := range out {
		if name := normalizeACPCommandName(command.Name); name != "" {
			seen[name] = struct{}{}
		}
	}
	for _, command := range fallback {
		name := normalizeACPCommandName(command.Name)
		if name != "" {
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
		}
		out = append(out, command)
	}
	return out
}

func cloneControllerConfigOptions(in []controller.ControllerConfigOption) []controller.ControllerConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]controller.ControllerConfigOption, 0, len(in))
	for _, item := range in {
		out = append(out, cloneControllerConfigOption(item))
	}
	return out
}

func cloneControllerConfigOption(in controller.ControllerConfigOption) controller.ControllerConfigOption {
	out := in
	out.Options = cloneControllerConfigChoices(in.Options)
	return out
}

func cloneControllerConfigChoices(in []controller.ControllerConfigChoice) []controller.ControllerConfigChoice {
	if len(in) == 0 {
		return nil
	}
	return append([]controller.ControllerConfigChoice(nil), in...)
}

func cloneControllerModes(in []controller.ControllerMode) []controller.ControllerMode {
	if len(in) == 0 {
		return nil
	}
	return append([]controller.ControllerMode(nil), in...)
}

func mergeControllerModes(existing []controller.ControllerMode, fallback []controller.ControllerMode) []controller.ControllerMode {
	if len(existing) == 0 {
		return cloneControllerModes(fallback)
	}
	if len(fallback) == 0 {
		return cloneControllerModes(existing)
	}
	out := cloneControllerModes(existing)
	seen := map[string]struct{}{}
	for _, mode := range out {
		if id := strings.ToLower(strings.TrimSpace(mode.ID)); id != "" {
			seen[id] = struct{}{}
		}
	}
	for _, mode := range fallback {
		id := strings.ToLower(strings.TrimSpace(mode.ID))
		if id != "" {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
		}
		out = append(out, mode)
	}
	return out
}

func cloneACPSessionModelState(in *client.SessionModelState) *client.SessionModelState {
	if in == nil {
		return nil
	}
	out := &client.SessionModelState{
		CurrentModelID:  strings.TrimSpace(in.CurrentModelID),
		AvailableModels: make([]client.ModelInfo, 0, len(in.AvailableModels)),
	}
	for _, item := range in.AvailableModels {
		modelID := strings.TrimSpace(item.ModelID)
		if modelID == "" {
			continue
		}
		out.AvailableModels = append(out.AvailableModels, client.ModelInfo{
			ModelID:     modelID,
			Name:        strings.TrimSpace(item.Name),
			Description: strings.TrimSpace(item.Description),
		})
	}
	if out.CurrentModelID == "" && len(out.AvailableModels) == 0 {
		return nil
	}
	return out
}

func acpToolDisplayName(kind string, title string) string {
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func acpToolRawInput(kind string, title string, raw any) map[string]any {
	out := acpRawMap(raw)
	if len(out) == 0 {
		return nil
	}
	return out
}

func acpToolRawOutput(raw any, content []client.ToolCallContent) map[string]any {
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

func acpToolContentText(content []client.ToolCallContent) string {
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

func messageForContentChunk(chunk client.ContentChunk, text string) model.Message {
	role := model.RoleAssistant
	if strings.TrimSpace(chunk.SessionUpdate) == client.UpdateUserMessage {
		role = model.RoleUser
	}
	if strings.TrimSpace(chunk.SessionUpdate) == client.UpdateAgentThought {
		return model.NewReasoningMessage(role, text, model.ReasoningVisibilityVisible)
	}
	return model.NewTextMessage(role, text)
}

func planEntries(in []client.PlanEntry) []session.ProtocolPlanEntry {
	out := make([]session.ProtocolPlanEntry, 0, len(in))
	for _, item := range in {
		out = append(out, session.ProtocolPlanEntry{
			Content:  strings.TrimSpace(item.Content),
			Status:   strings.TrimSpace(item.Status),
			Priority: "",
		})
	}
	return out
}

func toolEventTypeFromStatus(status string) session.EventType {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
		return session.EventTypeToolResult
	default:
		return session.EventTypeToolCall
	}
}

func buildPromptParts(input string, parts []model.ContentPart) []json.RawMessage {
	if len(parts) == 0 {
		input = strings.TrimSpace(input)
		if input == "" {
			return nil
		}
		raw, _ := json.Marshal(client.TextContent{
			Type: "text",
			Text: input,
		})
		return []json.RawMessage{raw}
	}
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case model.ContentPartImage:
			raw, _ := json.Marshal(client.ImageContent{
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
			raw, _ := json.Marshal(client.TextContent{
				Type: "text",
				Text: text,
			})
			out = append(out, raw)
		}
	}
	if len(out) == 0 && strings.TrimSpace(input) != "" {
		raw, _ := json.Marshal(client.TextContent{
			Type: "text",
			Text: strings.TrimSpace(input),
		})
		out = append(out, raw)
	}
	return out
}

func ptrMessage(msg model.Message) *model.Message {
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
