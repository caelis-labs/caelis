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
	commands          []sdkcontroller.ControllerCommand
	configOptions     []sdkcontroller.ControllerConfigOption
	models            *sdkacpclient.SessionModelState
	mode              string
	modeOptions       []sdkcontroller.ControllerMode
	turnID            string
	turnSession       sdksession.Session
	turnStream        bool
	turnMode          string
	approvalRequester sdkcontroller.ApprovalRequester
	handle            *turnHandle
	events            []*sdksession.Event
	updatedAt         time.Time
}

type controllerClientState struct {
	commands      []sdkcontroller.ControllerCommand
	configOptions []sdkcontroller.ControllerConfigOption
	models        *sdkacpclient.SessionModelState
	mode          string
	modeOptions   []sdkcontroller.ControllerMode
	agentLabel    string
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
	client, remoteSessionID, state, err := m.startClient(ctx, req.Session.CWD, cfg,
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
	run.applyStartupStateLocked(client, remoteSessionID, state, req.ContextSyncSeq)

	m.mu.Lock()
	if old := m.controllers[parentSessionID]; old != nil && old.client != nil {
		_ = old.client.Close(ctx)
	}
	m.controllers[parentSessionID] = run
	m.mu.Unlock()
	return sdksession.CloneControllerBinding(run.binding), nil
}

func (r *controllerRun) applyStartupStateLocked(client *sdkacpclient.Client, remoteSessionID string, state controllerClientState, contextSyncSeq int) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.client = client
	r.remoteSessionID = strings.TrimSpace(remoteSessionID)
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

func (m *Manager) ControllerStatus(_ context.Context, ref sdksession.SessionRef) (sdkcontroller.ControllerStatus, bool, error) {
	ref = sdksession.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return sdkcontroller.ControllerStatus{}, false, nil
	}
	m.mu.RLock()
	run := m.controllers[ref.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return sdkcontroller.ControllerStatus{}, false, nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.controllerStatusLocked(ref), true, nil
}

func (m *Manager) SetControllerModel(ctx context.Context, req sdkcontroller.SetControllerModelRequest) (sdkcontroller.ControllerStatus, error) {
	req.SessionRef = sdksession.NormalizeSessionRef(req.SessionRef)
	if strings.TrimSpace(req.SessionRef.SessionID) == "" {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: session id is required")
	}
	m.mu.RLock()
	run := m.controllers[req.SessionRef.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: no active ACP controller for session %q", req.SessionRef.SessionID)
	}
	return run.setControllerModel(ctx, req, m.clock)
}

func (m *Manager) SetControllerMode(ctx context.Context, req sdkcontroller.SetControllerModeRequest) (sdkcontroller.ControllerStatus, error) {
	req.SessionRef = sdksession.NormalizeSessionRef(req.SessionRef)
	if strings.TrimSpace(req.SessionRef.SessionID) == "" {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: session id is required")
	}
	m.mu.RLock()
	run := m.controllers[req.SessionRef.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: no active ACP controller for session %q", req.SessionRef.SessionID)
	}
	return run.setControllerMode(ctx, req, m.clock)
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
	client, remoteSessionID, _, err := m.startClient(ctx, session.CWD, cfg, func(env sdkacpclient.UpdateEnvelope) {
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
) (*sdkacpclient.Client, string, controllerClientState, error) {
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
		return nil, "", controllerClientState{}, err
	}
	initResp, err := client.Initialize(ctx)
	if err != nil {
		_ = client.Close(ctx)
		return nil, "", controllerClientState{}, err
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
	}
	if initResp.AgentInfo != nil {
		state.agentLabel = strings.TrimSpace(firstNonEmpty(initResp.AgentInfo.Title, initResp.AgentInfo.Name, initResp.AgentInfo.Version))
	}
	return client, strings.TrimSpace(resp.SessionID), state, nil
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
	r.events = append(r.events, sdksession.CloneEvent(event))
	r.mu.Unlock()
	if stream && handle != nil {
		handle.publishEvent(event)
	}
}

func (r *controllerRun) applySessionUpdateLocked(clock func() time.Time, update sdkacpclient.Update) {
	if r == nil {
		return
	}
	switch typed := update.(type) {
	case sdkacpclient.AvailableCommandsUpdate:
		r.commands = controllerCommandsFromACP(typed.AvailableCommands)
	case sdkacpclient.ConfigOptionUpdate:
		r.configOptions = mergeControllerConfigOptions(r.configOptions, controllerConfigOptionsFromACP(typed.ConfigOptions))
	case sdkacpclient.CurrentModeUpdate:
		r.mode = strings.TrimSpace(typed.CurrentModeID)
	default:
		return
	}
	if clock != nil {
		r.updatedAt = clock()
	} else {
		r.updatedAt = time.Now()
	}
}

func (r *controllerRun) controllerStatusLocked(ref sdksession.SessionRef) sdkcontroller.ControllerStatus {
	if r == nil {
		return sdkcontroller.ControllerStatus{}
	}
	modelOption, _ := pickModelConfigOption(r.configOptions)
	effortOption, _ := pickEffortConfigOption(r.configOptions)
	status := sdkcontroller.ControllerStatus{
		SessionRef:      sdksession.NormalizeSessionRef(ref),
		Agent:           strings.TrimSpace(r.agent),
		RemoteSessionID: strings.TrimSpace(r.remoteSessionID),
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
	if model, effort, ok := splitACPCurrentModelEffort(r.models); ok {
		if status.Model == "" {
			status.Model = model
		}
		if status.ReasoningEffort == "" && strings.EqualFold(strings.TrimSpace(status.Model), model) {
			status.ReasoningEffort = effort
		}
	}
	if len(status.EffortOptions) == 0 {
		status.EffortOptions = controllerEffortChoicesFromMap(status.EffortOptionsByModel, status.Model)
	}
	return status
}

func (r *controllerRun) setControllerModel(ctx context.Context, req sdkcontroller.SetControllerModelRequest, clock func() time.Time) (sdkcontroller.ControllerStatus, error) {
	if r == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: controller run is unavailable")
	}
	model := strings.TrimSpace(req.Model)
	effort := strings.TrimSpace(req.ReasoningEffort)
	if model == "" && effort == "" {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: model or reasoning effort is required")
	}
	r.mu.Lock()
	client := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	configOptions := cloneControllerConfigOptions(r.configOptions)
	models := cloneACPSessionModelState(r.models)
	r.mu.Unlock()
	if client == nil || remoteSessionID == "" {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: active controller client is unavailable")
	}

	if model != "" {
		modelOption, hasModelOption := pickModelConfigOption(configOptions)
		if !hasModelOption || modelOption == nil {
			return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: controller does not declare a model config option")
		}
		choice, ok := matchControllerConfigChoice(modelOption.Options, model)
		if !ok {
			return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: model %q is not declared by the controller", model)
		}
		resp, err := client.SetConfigOption(ctx, remoteSessionID, modelOption.ID, choice.Value)
		if err != nil {
			return sdkcontroller.ControllerStatus{}, err
		}
		configOptions = mergeControllerConfigOptions(configOptions, controllerConfigOptionsFromACP(resp.ConfigOptions))
	}
	if effort != "" {
		effortOption, hasEffortOption := pickEffortConfigOption(configOptions)
		if hasEffortOption && effortOption != nil {
			choice, ok := matchControllerConfigChoice(effortOption.Options, effort)
			if !ok {
				return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: reasoning effort %q is not declared by the controller", effort)
			}
			resp, err := client.SetConfigOption(ctx, remoteSessionID, effortOption.ID, choice.Value)
			if err != nil {
				return sdkcontroller.ControllerStatus{}, err
			}
			configOptions = mergeControllerConfigOptions(configOptions, controllerConfigOptionsFromACP(resp.ConfigOptions))
		} else {
			modelForEffort := firstNonEmpty(model, currentModelFromConfigOptions(configOptions))
			modelID, ok := matchACPModelIDForEffort(models, modelForEffort, effort)
			if !ok {
				return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: reasoning effort %q is not declared by the controller", effort)
			}
			if err := client.SetModel(ctx, remoteSessionID, modelID); err != nil {
				return sdkcontroller.ControllerStatus{}, err
			}
			modelBase, _, ok := splitACPCurrentModelEffort(&sdkacpclient.SessionModelState{CurrentModelID: modelID})
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

func (r *controllerRun) setControllerMode(ctx context.Context, req sdkcontroller.SetControllerModeRequest, clock func() time.Time) (sdkcontroller.ControllerStatus, error) {
	if r == nil {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: controller run is unavailable")
	}
	requested := strings.TrimSpace(req.Mode)
	if requested == "" {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: mode is required")
	}
	r.mu.Lock()
	client := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	modeOptions := cloneControllerModes(r.modeOptions)
	r.mu.Unlock()
	if client == nil || remoteSessionID == "" {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: active controller client is unavailable")
	}
	if len(modeOptions) == 0 {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: controller does not declare session modes")
	}
	choice, ok := matchControllerMode(modeOptions, requested)
	if !ok {
		return sdkcontroller.ControllerStatus{}, fmt.Errorf("sdk/controller/acp: mode %q is not declared by the controller", requested)
	}
	if err := client.SetMode(ctx, remoteSessionID, strings.TrimSpace(choice.ID)); err != nil {
		return sdkcontroller.ControllerStatus{}, err
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

func controllerCommandsFromACP(in []map[string]any) []sdkcontroller.ControllerCommand {
	if len(in) == 0 {
		return nil
	}
	out := make([]sdkcontroller.ControllerCommand, 0, len(in))
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
		out = append(out, sdkcontroller.ControllerCommand{
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

func controllerConfigOptionsFromACP(in []sdkacpclient.SessionConfigOption) []sdkcontroller.ControllerConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]sdkcontroller.ControllerConfigOption, 0, len(in))
	for _, item := range in {
		option := sdkcontroller.ControllerConfigOption{
			ID:           strings.TrimSpace(item.ID),
			Name:         strings.TrimSpace(item.Name),
			Type:         strings.TrimSpace(item.Type),
			Category:     strings.TrimSpace(item.Category),
			Description:  strings.TrimSpace(item.Description),
			CurrentValue: stringFromACPValue(item.CurrentValue),
			Options:      make([]sdkcontroller.ControllerConfigChoice, 0, len(item.Options)),
		}
		for _, choice := range item.Options {
			value := strings.TrimSpace(choice.Value)
			if value == "" {
				continue
			}
			option.Options = append(option.Options, sdkcontroller.ControllerConfigChoice{
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

func currentModeID(modes *sdkacpclient.SessionModeState) string {
	if modes == nil {
		return ""
	}
	return strings.TrimSpace(modes.CurrentModeID)
}

func splitACPCurrentModelEffort(models *sdkacpclient.SessionModelState) (string, string, bool) {
	if models == nil {
		return "", "", false
	}
	model, effort, hasEffort := splitACPModelIDEffort(models.CurrentModelID)
	if hasEffort {
		return model, effort, true
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

func controllerEffortChoicesFromModels(models *sdkacpclient.SessionModelState, model string) []sdkcontroller.ControllerConfigChoice {
	return controllerEffortChoicesFromMap(controllerEffortChoicesByModelFromModels(models), model)
}

func controllerEffortChoicesByModelFromModels(models *sdkacpclient.SessionModelState) map[string][]sdkcontroller.ControllerConfigChoice {
	if models == nil || len(models.AvailableModels) == 0 {
		return nil
	}
	out := map[string][]sdkcontroller.ControllerConfigChoice{}
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
		out[modelKey] = append(out[modelKey], sdkcontroller.ControllerConfigChoice{
			Value:       key,
			Name:        reasoningEffortDisplayName(key),
			Description: strings.TrimSpace(item.Description),
		})
	}
	return out
}

func controllerEffortChoicesFromMap(options map[string][]sdkcontroller.ControllerConfigChoice, model string) []sdkcontroller.ControllerConfigChoice {
	if len(options) == 0 {
		return nil
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil
	}
	return cloneControllerConfigChoices(options[model])
}

func matchACPModelIDForEffort(models *sdkacpclient.SessionModelState, model string, effort string) (string, bool) {
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

func withACPCurrentModelID(models *sdkacpclient.SessionModelState, modelID string) *sdkacpclient.SessionModelState {
	out := cloneACPSessionModelState(models)
	if out == nil {
		out = &sdkacpclient.SessionModelState{}
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

func controllerModesFromACP(modes *sdkacpclient.SessionModeState) []sdkcontroller.ControllerMode {
	if modes == nil || len(modes.AvailableModes) == 0 {
		return nil
	}
	out := make([]sdkcontroller.ControllerMode, 0, len(modes.AvailableModes))
	for _, mode := range modes.AvailableModes {
		id := strings.TrimSpace(mode.ID)
		if id == "" {
			continue
		}
		out = append(out, sdkcontroller.ControllerMode{
			ID:          id,
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func pickModelConfigOption(options []sdkcontroller.ControllerConfigOption) (*sdkcontroller.ControllerConfigOption, bool) {
	return pickControllerConfigOption(options, matchModelConfigOption)
}

func pickEffortConfigOption(options []sdkcontroller.ControllerConfigOption) (*sdkcontroller.ControllerConfigOption, bool) {
	return pickControllerConfigOption(options, func(option sdkcontroller.ControllerConfigOption) (bool, int) {
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

func matchModelConfigOption(option sdkcontroller.ControllerConfigOption) (bool, int) {
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

func currentModelFromConfigOptions(options []sdkcontroller.ControllerConfigOption) string {
	if option, ok := pickModelConfigOption(options); ok && option != nil {
		return strings.TrimSpace(option.CurrentValue)
	}
	return ""
}

func setControllerConfigCurrentValue(options []sdkcontroller.ControllerConfigOption, model string) []sdkcontroller.ControllerConfigOption {
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
	return append(out, sdkcontroller.ControllerConfigOption{
		ID:           "model",
		Name:         "Model",
		Type:         "select",
		Category:     "model",
		CurrentValue: model,
	})
}

func pickControllerConfigOption(
	options []sdkcontroller.ControllerConfigOption,
	match func(sdkcontroller.ControllerConfigOption) (bool, int),
) (*sdkcontroller.ControllerConfigOption, bool) {
	var picked *sdkcontroller.ControllerConfigOption
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

func controllerConfigOptionHaystack(option sdkcontroller.ControllerConfigOption) string {
	return strings.ToLower(strings.Join([]string{
		strings.TrimSpace(option.ID),
		strings.TrimSpace(option.Name),
		strings.TrimSpace(option.Category),
		strings.TrimSpace(option.Description),
	}, " "))
}

func matchControllerConfigChoice(options []sdkcontroller.ControllerConfigChoice, requested string) (sdkcontroller.ControllerConfigChoice, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return sdkcontroller.ControllerConfigChoice{}, false
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(option.Value), requested) || strings.EqualFold(strings.TrimSpace(option.Name), requested) {
			if strings.TrimSpace(option.Value) == "" {
				continue
			}
			return option, true
		}
	}
	return sdkcontroller.ControllerConfigChoice{}, false
}

func mergeControllerConfigChoices(primary []sdkcontroller.ControllerConfigChoice, fallback []sdkcontroller.ControllerConfigChoice) []sdkcontroller.ControllerConfigChoice {
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

func matchControllerMode(options []sdkcontroller.ControllerMode, requested string) (sdkcontroller.ControllerMode, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return sdkcontroller.ControllerMode{}, false
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
	return sdkcontroller.ControllerMode{}, false
}

func mergeControllerConfigOptions(existing []sdkcontroller.ControllerConfigOption, updates []sdkcontroller.ControllerConfigOption) []sdkcontroller.ControllerConfigOption {
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

func mergeControllerConfigOption(existing sdkcontroller.ControllerConfigOption, update sdkcontroller.ControllerConfigOption) sdkcontroller.ControllerConfigOption {
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

func fillControllerConfigOptions(existing []sdkcontroller.ControllerConfigOption, fallback []sdkcontroller.ControllerConfigOption) []sdkcontroller.ControllerConfigOption {
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

func fillControllerConfigOption(existing sdkcontroller.ControllerConfigOption, fallback sdkcontroller.ControllerConfigOption) sdkcontroller.ControllerConfigOption {
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

func cloneControllerCommands(in []sdkcontroller.ControllerCommand) []sdkcontroller.ControllerCommand {
	if len(in) == 0 {
		return nil
	}
	return append([]sdkcontroller.ControllerCommand(nil), in...)
}

func mergeControllerCommands(existing []sdkcontroller.ControllerCommand, fallback []sdkcontroller.ControllerCommand) []sdkcontroller.ControllerCommand {
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

func cloneControllerConfigOptions(in []sdkcontroller.ControllerConfigOption) []sdkcontroller.ControllerConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]sdkcontroller.ControllerConfigOption, 0, len(in))
	for _, item := range in {
		out = append(out, cloneControllerConfigOption(item))
	}
	return out
}

func cloneControllerConfigOption(in sdkcontroller.ControllerConfigOption) sdkcontroller.ControllerConfigOption {
	out := in
	out.Options = cloneControllerConfigChoices(in.Options)
	return out
}

func cloneControllerConfigChoices(in []sdkcontroller.ControllerConfigChoice) []sdkcontroller.ControllerConfigChoice {
	if len(in) == 0 {
		return nil
	}
	return append([]sdkcontroller.ControllerConfigChoice(nil), in...)
}

func cloneControllerModes(in []sdkcontroller.ControllerMode) []sdkcontroller.ControllerMode {
	if len(in) == 0 {
		return nil
	}
	return append([]sdkcontroller.ControllerMode(nil), in...)
}

func mergeControllerModes(existing []sdkcontroller.ControllerMode, fallback []sdkcontroller.ControllerMode) []sdkcontroller.ControllerMode {
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

func cloneACPSessionModelState(in *sdkacpclient.SessionModelState) *sdkacpclient.SessionModelState {
	if in == nil {
		return nil
	}
	out := &sdkacpclient.SessionModelState{
		CurrentModelID:  strings.TrimSpace(in.CurrentModelID),
		AvailableModels: make([]sdkacpclient.ModelInfo, 0, len(in.AvailableModels)),
	}
	for _, item := range in.AvailableModels {
		modelID := strings.TrimSpace(item.ModelID)
		if modelID == "" {
			continue
		}
		out.AvailableModels = append(out.AvailableModels, sdkacpclient.ModelInfo{
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
	if strings.TrimSpace(chunk.SessionUpdate) == sdkacpclient.UpdateAgentThought {
		return sdkmodel.NewReasoningMessage(role, text, sdkmodel.ReasoningVisibilityVisible)
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
