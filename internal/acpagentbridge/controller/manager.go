package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type Config struct {
	Registry   *subagent.Registry
	ClientInfo *client.Implementation
	Clock      func() time.Time
}

type Manager struct {
	registry    *subagent.Registry
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
	cfg subagent.AgentConfig,
	resumeRemoteSessionID string,
	onUpdate func(client.UpdateEnvelope),
	onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
) (*client.Client, string, controllerClientState, error)

type controllerRun struct {
	parentSessionID       string
	agent                 string
	cfg                   subagent.AgentConfig
	cwd                   string
	client                *client.Client
	remoteSessionID       string
	supportsClose         bool
	promptCapabilities    schema.PromptCapabilities
	binding               session.ControllerBinding
	contextPrelude        string
	contextPreludePending bool

	mu                sync.Mutex
	commands          []ControllerCommand
	configOptions     []ControllerConfigOption
	models            *client.SessionModelState
	mode              string
	modeOptions       []ControllerMode
	remoteTitle       string
	turnID            string
	turnSession       session.Session
	turnStream        bool
	turnMode          string
	approvalRequester controller.ApprovalRequester
	handle            *turnHandle
	events            []*session.Event
	updatedAt         time.Time
	reconnectMu       sync.Mutex
}

type controllerClientState struct {
	commands           []ControllerCommand
	configOptions      []ControllerConfigOption
	models             *client.SessionModelState
	mode               string
	modeOptions        []ControllerMode
	agentLabel         string
	supportsClose      bool
	promptCapabilities schema.PromptCapabilities
}

type participantRun struct {
	id                 string
	parentSessionID    string
	agent              string
	client             *client.Client
	remoteSessionID    string
	binding            session.ParticipantBinding
	promptCapabilities schema.PromptCapabilities

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
		return nil, fmt.Errorf("internal/acpagentbridge/controller: registry is required")
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
		return session.ControllerBinding{}, fmt.Errorf("internal/acpagentbridge/controller: parent session id is required")
	}
	cfg, err := m.registry.Resolve(req.Agent)
	if err != nil {
		return session.ControllerBinding{}, err
	}

	run := &controllerRun{
		parentSessionID:       parentSessionID,
		agent:                 strings.TrimSpace(cfg.Name),
		cfg:                   cfg,
		cwd:                   strings.TrimSpace(req.Session.CWD),
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
	contextSyncSeq := req.ContextSyncSeq
	if resumeRemoteSessionID != "" && !strings.EqualFold(strings.TrimSpace(remoteSessionID), resumeRemoteSessionID) {
		contextSyncSeq = 0
		run.contextPrelude = ""
		run.contextPreludePending = false
	}
	run.applyStartupStateLocked(client, remoteSessionID, state, contextSyncSeq)

	m.mu.Lock()
	old := m.controllers[parentSessionID]
	m.controllers[parentSessionID] = run
	m.mu.Unlock()
	if old != nil {
		m.shutdownControllerRun(context.WithoutCancel(ctx), old, false)
	}
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
	if mode := currentModeFromConfigOptions(r.configOptions); mode != "" {
		r.mode = mode
	} else if strings.TrimSpace(r.mode) == "" {
		r.mode = strings.TrimSpace(state.mode)
	}
	r.modeOptions = mergeControllerModes(controllerModesFromConfigOptions(r.configOptions), mergeControllerModes(r.modeOptions, state.modeOptions))
	r.binding.RemoteSessionID = strings.TrimSpace(remoteSessionID)
	r.binding.ContextSyncSeq = contextSyncSeq
	r.promptCapabilities = state.promptCapabilities
	if label := strings.TrimSpace(state.agentLabel); label != "" {
		r.binding.Label = label
	}
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
	if run != nil {
		m.shutdownControllerRun(context.WithoutCancel(ctx), run, true)
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
		return controller.TurnResult{}, fmt.Errorf("%w for session %q", controller.ErrNotActive, sessionID)
	}
	if contentPartsContainImage(req.ContentParts) && !run.supportsPromptImages() {
		return controller.TurnResult{}, fmt.Errorf("internal/acpagentbridge/controller: agent %q does not support image prompts", run.agent)
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
		if err := m.promptControllerRun(turnCtx, run, prompt); err != nil {
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

func (m *Manager) promptControllerRun(ctx context.Context, run *controllerRun, prompt []json.RawMessage) error {
	if _, err := run.promptParts(ctx, prompt); err != nil {
		if !isACPClientConnectionError(err) {
			return err
		}
		if reconnectErr := m.reconnectControllerRun(ctx, run); reconnectErr != nil {
			return fmt.Errorf("%w; reconnect failed: %w", err, reconnectErr)
		}
		_, err = run.promptParts(ctx, prompt)
		return err
	}
	return nil
}

func contentPartsContainImage(parts []model.ContentPart) bool {
	for _, part := range parts {
		if part.Type == model.ContentPartImage {
			return true
		}
	}
	return false
}

func (r *controllerRun) supportsPromptImages() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.promptCapabilities.Image
}

func (r *participantRun) supportsPromptImages() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.promptCapabilities.Image
}

func (m *Manager) ControllerStatus(_ context.Context, ref session.SessionRef) (ControllerStatus, bool, error) {
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return ControllerStatus{}, false, nil
	}
	m.mu.RLock()
	run := m.controllers[ref.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return ControllerStatus{}, false, nil
	}
	run.mu.Lock()
	defer run.mu.Unlock()
	return run.controllerStatusLocked(ref), true, nil
}

func (m *Manager) SetControllerModel(ctx context.Context, req SetControllerModelRequest) (ControllerStatus, error) {
	req.SessionRef = session.NormalizeSessionRef(req.SessionRef)
	if strings.TrimSpace(req.SessionRef.SessionID) == "" {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: session id is required")
	}
	m.mu.RLock()
	run := m.controllers[req.SessionRef.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: no active ACP controller for session %q", req.SessionRef.SessionID)
	}
	status, err := run.setControllerModel(ctx, req, m.clock)
	if err == nil || !isACPClientConnectionError(err) {
		return status, err
	}
	if reconnectErr := m.reconnectControllerRun(ctx, run); reconnectErr != nil {
		return ControllerStatus{}, fmt.Errorf("%w; reconnect failed: %w", err, reconnectErr)
	}
	return run.setControllerModel(ctx, req, m.clock)
}

func (m *Manager) SetControllerMode(ctx context.Context, req SetControllerModeRequest) (ControllerStatus, error) {
	req.SessionRef = session.NormalizeSessionRef(req.SessionRef)
	if strings.TrimSpace(req.SessionRef.SessionID) == "" {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: session id is required")
	}
	m.mu.RLock()
	run := m.controllers[req.SessionRef.SessionID]
	m.mu.RUnlock()
	if run == nil {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: no active ACP controller for session %q", req.SessionRef.SessionID)
	}
	status, err := run.setControllerMode(ctx, req, m.clock)
	if err == nil || !isACPClientConnectionError(err) {
		return status, err
	}
	if reconnectErr := m.reconnectControllerRun(ctx, run); reconnectErr != nil {
		return ControllerStatus{}, fmt.Errorf("%w; reconnect failed: %w", err, reconnectErr)
	}
	return run.setControllerMode(ctx, req, m.clock)
}

func (m *Manager) reconnectControllerRun(ctx context.Context, run *controllerRun) error {
	if m == nil || run == nil {
		return fmt.Errorf("internal/acpagentbridge/controller: controller run is unavailable")
	}
	run.reconnectMu.Lock()
	defer run.reconnectMu.Unlock()
	if !m.isActiveControllerRun(run) {
		return fmt.Errorf("internal/acpagentbridge/controller: controller run is no longer active")
	}
	run.mu.Lock()
	cfg := run.cfg
	cwd := strings.TrimSpace(run.cwd)
	resumeRemoteSessionID := strings.TrimSpace(run.remoteSessionID)
	contextSyncSeq := run.binding.ContextSyncSeq
	desired := controllerReconnectConfigFromState(run.configOptions, run.models, run.mode, run.modeOptions)
	oldClient := run.client
	run.mu.Unlock()
	if strings.TrimSpace(cfg.Command) == "" {
		return fmt.Errorf("internal/acpagentbridge/controller: controller agent config is unavailable")
	}
	if oldClient != nil {
		_ = oldClient.Close(context.WithoutCancel(ctx))
	}
	if !m.isActiveControllerRun(run) {
		return fmt.Errorf("internal/acpagentbridge/controller: controller run is no longer active")
	}
	acpClient, remoteSessionID, state, err := m.startClient(ctx, cwd, cfg, resumeRemoteSessionID,
		func(env client.UpdateEnvelope) {
			run.handleUpdate(m.clock, env)
		},
		func(ctx context.Context, in client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
			return run.permissionHandler(ctx, in)
		},
	)
	if err != nil {
		return err
	}
	if !m.isActiveControllerRun(run) {
		_ = acpClient.Close(context.WithoutCancel(ctx))
		return fmt.Errorf("internal/acpagentbridge/controller: controller run is no longer active")
	}
	remote := controllerReconnectConfigFromState(state.configOptions, state.models, state.mode, state.modeOptions)
	if resumeRemoteSessionID != "" && !strings.EqualFold(strings.TrimSpace(remoteSessionID), resumeRemoteSessionID) {
		contextSyncSeq = 0
		run.mu.Lock()
		run.contextPrelude = ""
		run.contextPreludePending = false
		run.mu.Unlock()
	}
	run.applyStartupStateLocked(acpClient, remoteSessionID, state, contextSyncSeq)
	if desired.needsRemoteReapply(remote) {
		if err := run.reapplyControllerRemoteConfig(ctx, desired, m.clock); err != nil {
			return fmt.Errorf("reapply controller config: %w", err)
		}
	}
	return nil
}

func (m *Manager) isActiveControllerRun(run *controllerRun) bool {
	if m == nil || run == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.controllers[strings.TrimSpace(run.parentSessionID)] == run
}

func (m *Manager) shutdownControllerRun(ctx context.Context, run *controllerRun, closeRemote bool) {
	if run == nil {
		return
	}
	run.reconnectMu.Lock()
	defer run.reconnectMu.Unlock()
	m.shutdownControllerRunLocked(ctx, run, closeRemote)
}

func (m *Manager) shutdownControllerRunLocked(ctx context.Context, run *controllerRun, closeRemote bool) {
	if run == nil {
		return
	}
	run.mu.Lock()
	acpClient := run.client
	remoteSessionID := strings.TrimSpace(run.remoteSessionID)
	supportsClose := run.supportsClose
	run.client = nil
	run.mu.Unlock()
	if acpClient == nil {
		return
	}
	if closeRemote && remoteSessionID != "" && supportsClose {
		_ = acpClient.CloseSession(ctx, remoteSessionID)
	}
	_ = acpClient.Close(ctx)
}

type controllerReconnectConfig struct {
	model            string
	reasoningEffort  string
	mode             string
	modeConfigurable bool
}

func (c controllerReconnectConfig) needsRemoteReapply(remote controllerReconnectConfig) bool {
	if c.model != "" && !strings.EqualFold(c.model, remote.model) {
		return true
	}
	if c.reasoningEffort != "" && !strings.EqualFold(c.reasoningEffort, remote.reasoningEffort) {
		return true
	}
	if c.mode != "" && !strings.EqualFold(c.mode, remote.mode) && remote.modeConfigurable {
		return true
	}
	return false
}

func controllerReconnectConfigFromState(options []ControllerConfigOption, models *client.SessionModelState, mode string, modeOptions []ControllerMode) controllerReconnectConfig {
	model := currentModelFromConfigOptions(options)
	var effort string
	if effortOption, ok := pickEffortConfigOption(options); ok && effortOption != nil {
		effort = strings.TrimSpace(effortOption.CurrentValue)
	}
	if modelFromState, effortFromState, ok := splitACPCurrentModelEffort(models); ok {
		if strings.TrimSpace(model) == "" {
			model = modelFromState
		}
		if strings.TrimSpace(effort) == "" {
			effort = effortFromState
		}
	}
	modeFromConfig := currentModeFromConfigOptions(options)
	if modeFromConfig != "" {
		mode = modeFromConfig
	}
	return controllerReconnectConfig{
		model:            strings.TrimSpace(model),
		reasoningEffort:  strings.TrimSpace(effort),
		mode:             strings.TrimSpace(mode),
		modeConfigurable: len(controllerModesFromConfigOptions(options)) > 0 || len(modeOptions) > 0,
	}
}

func (r *controllerRun) reapplyControllerRemoteConfig(ctx context.Context, desired controllerReconnectConfig, clock func() time.Time) error {
	if r == nil {
		return nil
	}
	if desired.model != "" || desired.reasoningEffort != "" {
		if _, err := r.setControllerModel(ctx, SetControllerModelRequest{
			SessionRef:      session.SessionRef{SessionID: strings.TrimSpace(r.parentSessionID)},
			Model:           desired.model,
			ReasoningEffort: desired.reasoningEffort,
		}, clock); err != nil {
			return err
		}
	}
	if desired.mode != "" {
		if _, err := r.setControllerMode(ctx, SetControllerModeRequest{
			SessionRef: session.SessionRef{SessionID: strings.TrimSpace(r.parentSessionID)},
			Mode:       desired.mode,
		}, clock); err != nil {
			return err
		}
	}
	return nil
}

func (r *controllerRun) promptParts(ctx context.Context, prompt []json.RawMessage) (client.PromptResponse, error) {
	if r == nil {
		return client.PromptResponse{}, fmt.Errorf("internal/acpagentbridge/controller: controller run is unavailable")
	}
	r.mu.Lock()
	acpClient := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	r.mu.Unlock()
	if acpClient == nil {
		return client.PromptResponse{}, fmt.Errorf("internal/acpagentbridge/controller: controller client is unavailable")
	}
	if remoteSessionID == "" {
		return client.PromptResponse{}, fmt.Errorf("internal/acpagentbridge/controller: remote session id is unavailable")
	}
	return acpClient.PromptParts(ctx, remoteSessionID, prompt, nil)
}

func isACPClientConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(text, "broken pipe") ||
		strings.Contains(text, "connection closed before response") ||
		strings.Contains(text, "file already closed") ||
		strings.Contains(text, "use of closed file")
}

func (m *Manager) Attach(ctx context.Context, req controller.AttachRequest) (session.ParticipantBinding, error) {
	req = controller.NormalizeAttachRequest(req)
	if strings.TrimSpace(req.Session.SessionID) == "" {
		return session.ParticipantBinding{}, fmt.Errorf("internal/acpagentbridge/controller: session id is required")
	}
	if id := strings.TrimSpace(req.Binding.ID); id != "" {
		m.mu.RLock()
		run := m.participants[id]
		m.mu.RUnlock()
		if run != nil {
			return run.refreshBinding(req.Binding), nil
		}
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
		return controller.TurnResult{}, fmt.Errorf("internal/acpagentbridge/controller: participant id is required")
	}
	m.mu.RLock()
	run := m.participants[req.ParticipantID]
	m.mu.RUnlock()
	if run == nil {
		return controller.TurnResult{}, fmt.Errorf("internal/acpagentbridge/controller: participant %q not found", req.ParticipantID)
	}
	if contentPartsContainImage(req.ContentParts) && !run.supportsPromptImages() {
		return controller.TurnResult{}, fmt.Errorf("internal/acpagentbridge/controller: participant %q does not support image prompts", req.ParticipantID)
	}
	prompt := buildPromptParts(req.Input, req.ContentParts)
	prompt = prependACPContextPrelude(prompt, req.ContextPrelude)
	if len(prompt) == 0 {
		return controller.TurnResult{}, fmt.Errorf("internal/acpagentbridge/controller: participant prompt is required")
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
	cfg subagent.AgentConfig,
	req controller.AttachRequest,
) (*participantRun, error) {
	var run *participantRun
	existing := session.CloneParticipantBinding(req.Binding)
	resumeRemoteSessionID := strings.TrimSpace(existing.SessionID)
	client, remoteSessionID, state, err := m.startClient(ctx, parentSession.CWD, cfg, resumeRemoteSessionID, func(env client.UpdateEnvelope) {
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
	id := strings.TrimSpace(existing.ID)
	if id == "" {
		id = m.nextID(firstNonEmpty(req.Agent, existing.AgentName, "participant"))
	}
	role := req.Role
	if role == "" {
		role = existing.Role
	}
	if role == "" {
		role = session.ParticipantRoleSidecar
	}
	label := strings.TrimSpace(req.Label)
	if label == "" {
		label = strings.TrimSpace(existing.Label)
	}
	if label == "" {
		label = strings.TrimSpace(req.Agent)
	}
	agentName := firstNonEmpty(strings.TrimSpace(req.Agent), strings.TrimSpace(existing.AgentName), strings.TrimSpace(cfg.Name))
	attachedAt := existing.AttachedAt
	if attachedAt.IsZero() {
		attachedAt = m.clock()
	}
	contextSyncSeq := existing.ContextSyncSeq
	if resumeRemoteSessionID != "" && !strings.EqualFold(strings.TrimSpace(remoteSessionID), resumeRemoteSessionID) {
		contextSyncSeq = 0
	}
	run = &participantRun{
		id:                 id,
		parentSessionID:    strings.TrimSpace(parentSession.SessionID),
		agent:              agentName,
		client:             client,
		remoteSessionID:    remoteSessionID,
		promptCapabilities: state.promptCapabilities,
		binding: session.ParticipantBinding{
			ID:             id,
			Kind:           session.ParticipantKindACP,
			Role:           role,
			AgentName:      agentName,
			Label:          label,
			SessionID:      remoteSessionID,
			Source:         firstNonEmpty(req.Source, existing.Source, "user_attach"),
			ParentTurnID:   strings.TrimSpace(existing.ParentTurnID),
			DelegationID:   strings.TrimSpace(existing.DelegationID),
			ContextSyncSeq: contextSyncSeq,
			AttachedAt:     attachedAt,
			ControllerRef:  firstNonEmpty(strings.TrimSpace(existing.ControllerRef), strings.TrimSpace(parentSession.Controller.EpochID)),
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
	cfg subagent.AgentConfig,
	resumeRemoteSessionID string,
	onUpdate func(client.UpdateEnvelope),
	onPermission func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error),
) (*client.Client, string, controllerClientState, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", controllerClientState{}, err
	}
	client, err := client.Start(context.WithoutCancel(ctx), client.Config{
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
		if err == nil {
			state := controllerClientState{
				configOptions:      controllerConfigOptionsFromACP(resp.ConfigOptions),
				models:             cloneACPSessionModelState(resp.Models),
				mode:               currentModeID(resp.Modes),
				modeOptions:        controllerModesFromACP(resp.Modes),
				supportsClose:      acpSessionCapability(initResp, "close"),
				promptCapabilities: initResp.AgentCapabilities.PromptCapabilities,
			}
			if initResp.AgentInfo != nil {
				state.agentLabel = strings.TrimSpace(firstNonEmpty(initResp.AgentInfo.Title, initResp.AgentInfo.Name, initResp.AgentInfo.Version))
			}
			return client, remoteSessionID, state, nil
		}
		if ctx.Err() != nil {
			_ = client.Close(ctx)
			return nil, "", controllerClientState{}, err
		}
	}
	resp, err := client.NewSession(ctx, strings.TrimSpace(cwd), nil)
	if err != nil {
		_ = client.Close(ctx)
		return nil, "", controllerClientState{}, err
	}
	state := controllerClientState{
		configOptions:      controllerConfigOptionsFromACP(resp.ConfigOptions),
		models:             cloneACPSessionModelState(resp.Models),
		mode:               currentModeID(resp.Modes),
		modeOptions:        controllerModesFromACP(resp.Modes),
		supportsClose:      acpSessionCapability(initResp, "close"),
		promptCapabilities: initResp.AgentCapabilities.PromptCapabilities,
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
			RawInput: schema.NormalizeRawMap(req.ToolCall.RawInput),
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
	env.Update = acputil.StripTerminalConsoleFenceUpdate(env.Update)
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
	acpEnv := acpEnvelopeFromUpdate(env, event, nil)
	if event == nil && acpEnv == nil {
		r.mu.Unlock()
		return
	}
	r.updatedAt = clock()
	if event != nil {
		r.events = append(r.events, session.CloneEvent(event))
	}
	r.mu.Unlock()
	if stream && handle != nil {
		handle.publishSourceEvent(event, acpEnv)
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
		if mode := currentModeFromConfigOptions(r.configOptions); mode != "" {
			r.mode = mode
		}
		r.modeOptions = mergeControllerModes(controllerModesFromConfigOptions(r.configOptions), r.modeOptions)
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

func (r *controllerRun) controllerStatusLocked(ref session.SessionRef) ControllerStatus {
	if r == nil {
		return ControllerStatus{}
	}
	modelOption, _ := pickModelConfigOption(r.configOptions)
	effortOption, _ := pickEffortConfigOption(r.configOptions)
	status := ControllerStatus{
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

func (r *controllerRun) setControllerModel(ctx context.Context, req SetControllerModelRequest, clock func() time.Time) (ControllerStatus, error) {
	if r == nil {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: controller run is unavailable")
	}
	testModel := strings.TrimSpace(req.Model)
	effort := strings.TrimSpace(req.ReasoningEffort)
	if testModel == "" && effort == "" {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: model or reasoning effort is required")
	}
	r.mu.Lock()
	acpClient := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	configOptions := cloneControllerConfigOptions(r.configOptions)
	models := cloneACPSessionModelState(r.models)
	r.mu.Unlock()
	if acpClient == nil || remoteSessionID == "" {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: active controller client is unavailable")
	}

	if testModel != "" {
		modelOption, hasModelOption := pickModelConfigOption(configOptions)
		if !hasModelOption || modelOption == nil {
			return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: controller does not declare a model config option")
		}
		choice, ok := matchControllerConfigChoice(modelOption.Options, testModel)
		if !ok {
			return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: model %q is not declared by the controller", testModel)
		}
		resp, err := acpClient.SetConfigOption(ctx, remoteSessionID, modelOption.ID, choice.Value)
		if err != nil {
			return ControllerStatus{}, err
		}
		configOptions = mergeControllerConfigOptions(configOptions, controllerConfigOptionsFromACP(resp.ConfigOptions))
	}
	if effort != "" {
		effortOption, hasEffortOption := pickEffortConfigOption(configOptions)
		if hasEffortOption && effortOption != nil {
			choice, ok := matchControllerConfigChoice(effortOption.Options, effort)
			if !ok {
				return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: reasoning effort %q is not declared by the controller", effort)
			}
			resp, err := acpClient.SetConfigOption(ctx, remoteSessionID, effortOption.ID, choice.Value)
			if err != nil {
				return ControllerStatus{}, err
			}
			configOptions = mergeControllerConfigOptions(configOptions, controllerConfigOptionsFromACP(resp.ConfigOptions))
		} else {
			modelForEffort := firstNonEmpty(testModel, currentModelFromConfigOptions(configOptions))
			modelID, ok := matchACPModelIDForEffort(models, modelForEffort, effort)
			if !ok {
				return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: reasoning effort %q is not declared by the controller", effort)
			}
			if err := acpClient.SetModel(ctx, remoteSessionID, modelID); err != nil {
				return ControllerStatus{}, err
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

func (r *controllerRun) setControllerMode(ctx context.Context, req SetControllerModeRequest, clock func() time.Time) (ControllerStatus, error) {
	if r == nil {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: controller run is unavailable")
	}
	requested := strings.TrimSpace(req.Mode)
	if requested == "" {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: mode is required")
	}
	r.mu.Lock()
	client := r.client
	remoteSessionID := strings.TrimSpace(r.remoteSessionID)
	configOptions := cloneControllerConfigOptions(r.configOptions)
	modeOptions := cloneControllerModes(r.modeOptions)
	r.mu.Unlock()
	if client == nil || remoteSessionID == "" {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: active controller client is unavailable")
	}
	if modeOption, hasModeOption := pickModeConfigOption(configOptions); hasModeOption && modeOption != nil && len(modeOption.Options) > 0 {
		choice, ok := matchControllerConfigChoice(modeOption.Options, requested)
		if !ok {
			return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: mode %q is not declared by the controller", requested)
		}
		resp, err := client.SetConfigOption(ctx, remoteSessionID, modeOption.ID, choice.Value)
		if err != nil {
			return ControllerStatus{}, err
		}
		configOptions = mergeControllerConfigOptions(configOptions, controllerConfigOptionsFromACP(resp.ConfigOptions))
		modeOptions = mergeControllerModes(controllerModesFromConfigOptions(configOptions), modeOptions)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.configOptions = cloneControllerConfigOptions(configOptions)
		r.modeOptions = cloneControllerModes(modeOptions)
		r.mode = firstNonEmpty(currentModeFromConfigOptions(configOptions), strings.TrimSpace(choice.Value), strings.TrimSpace(choice.Name))
		if clock != nil {
			r.updatedAt = clock()
		} else {
			r.updatedAt = time.Now()
		}
		return r.controllerStatusLocked(req.SessionRef), nil
	}
	if len(modeOptions) == 0 {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: controller does not declare session modes")
	}
	choice, ok := matchControllerMode(modeOptions, requested)
	if !ok {
		return ControllerStatus{}, fmt.Errorf("internal/acpagentbridge/controller: mode %q is not declared by the controller", requested)
	}
	if err := client.SetMode(ctx, remoteSessionID, strings.TrimSpace(choice.ID)); err != nil {
		return ControllerStatus{}, err
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

func (r *participantRun) refreshBinding(binding session.ParticipantBinding) session.ParticipantBinding {
	if r == nil {
		return session.ParticipantBinding{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if binding.ContextSyncSeq > r.binding.ContextSyncSeq {
		r.binding.ContextSyncSeq = binding.ContextSyncSeq
	}
	if label := strings.TrimSpace(binding.Label); label != "" {
		r.binding.Label = label
	}
	if agentName := strings.TrimSpace(binding.AgentName); agentName != "" {
		r.binding.AgentName = agentName
		r.agent = agentName
	}
	if source := strings.TrimSpace(binding.Source); source != "" {
		r.binding.Source = source
	}
	if controllerRef := strings.TrimSpace(binding.ControllerRef); controllerRef != "" {
		r.binding.ControllerRef = controllerRef
	}
	return session.CloneParticipantBinding(r.binding)
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
	env.Update = acputil.StripTerminalConsoleFenceUpdate(env.Update)
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
	if event != nil {
		applyACPParticipantEventScope(event, r.binding, r.agent)
	}
	acpEnv := acpEnvelopeFromUpdate(env, event, &acpEnvelopeParticipantScope{
		binding: r.binding,
		agent:   r.agent,
		turnID:  turnID,
	})
	if event == nil && acpEnv == nil {
		r.mu.Unlock()
		return
	}
	r.updatedAt = clock()
	if event != nil {
		r.events = append(r.events, session.CloneEvent(event))
	}
	r.mu.Unlock()
	if stream && handle != nil {
		handle.publishSourceEvent(event, acpEnv)
	}
}

func applyACPParticipantEventScope(event *session.Event, binding session.ParticipantBinding, agent string) {
	if event == nil {
		return
	}
	participantID := strings.TrimSpace(binding.ID)
	event.Actor = session.ActorRef{Kind: session.ActorKindParticipant, ID: participantID, Name: strings.TrimSpace(firstNonEmpty(binding.Label, agent, participantID))}
	if event.Scope == nil {
		event.Scope = &session.EventScope{}
	}
	event.Scope.Source = "acp_participant"
	event.Scope.Controller = session.ControllerRef{}
	event.Scope.Participant = session.ParticipantRef{
		ID:           participantID,
		Kind:         binding.Kind,
		Role:         binding.Role,
		DelegationID: binding.DelegationID,
	}
	if event.Meta == nil {
		event.Meta = map[string]any{}
	}
	if agent := strings.TrimSpace(agent); agent != "" {
		event.Meta["agent"] = agent
	}
	if label := strings.TrimSpace(binding.Label); label != "" {
		event.Meta["mention"] = label
		event.Meta["handle"] = strings.TrimPrefix(label, "@")
	}
}

func (m *Manager) nextID(prefix string) string {
	n := m.counter.Add(1)
	return fmt.Sprintf("%s-%d", prefix, n)
}
