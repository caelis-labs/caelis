package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
)

type agent struct {
	backend    *Backend
	options    ConnectionOptions
	connection *acp.AgentSideConnection
	// connectionReady closes after the SDK connection has been published. The
	// SDK starts its reader during construction, so handlers must cross this
	// barrier before they can use the back-reference.
	connectionReady chan struct{}

	mu       sync.Mutex
	sessions map[string]*sessionState
}

const sessionSteeringMethod = "_session/steering"

func (a *agent) Initialize(ctx context.Context, request acp.InitializeRequest) (acp.InitializeResponse, error) {
	if err := a.waitConnection(ctx); err != nil {
		return acp.InitializeResponse{}, err
	}
	protocol := acp.ProtocolVersion(acp.WireProtocolVersion)
	if request.ProtocolVersion < protocol {
		protocol = request.ProtocolVersion
	}
	return acp.InitializeResponse{
		ProtocolVersion: protocol,
		Meta: map[string]json.RawMessage{
			"steering": json.RawMessage(`{"supported":true}`),
		},
		AgentInfo: &acp.Implementation{
			Name: "caelis-codex-acp", Title: acp.Ptr("Codex (built in)"), Version: adapterVersion,
		},
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			SessionCapabilities: acp.SessionCapabilities{
				AdditionalDirectories: &acp.SessionAdditionalDirectoriesCapabilities{},
				Close:                 &acp.SessionCloseCapabilities{},
				Resume:                &acp.SessionResumeCapabilities{},
			},
		},
		AuthMethods: []acp.AuthMethod{},
	}, nil
}

func (a *agent) HandleExtensionMethod(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if err := a.waitConnection(ctx); err != nil {
		return nil, err
	}
	if method != sessionSteeringMethod {
		return nil, acp.NewMethodNotFound(method)
	}
	var request struct {
		SessionID string             `json:"sessionId"`
		Prompt    []acp.ContentBlock `json:"prompt"`
	}
	if err := json.Unmarshal(params, &request); err != nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": "invalid steering request"})
	}
	state, err := a.lookupSession(request.SessionID)
	if err != nil {
		return nil, err
	}
	input, err := promptInput(request.Prompt)
	if err != nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	state.steerMu.Lock()
	defer state.steerMu.Unlock()
	state.mu.Lock()
	turnID := state.activeTurnID
	state.mu.Unlock()
	if turnID == "" {
		return map[string]any{"outcome": "promptRequired", "reason": "noRunningTurn"}, nil
	}
	var response struct {
		TurnID string `json:"turnId"`
	}
	err = a.backend.rpc.Request(ctx, "turn/steer", map[string]any{
		"threadId": state.threadID, "turnId": turnID, "input": input,
	}, &response)
	if err != nil {
		state.mu.Lock()
		stillActive := state.activeTurnID == turnID
		state.mu.Unlock()
		if !stillActive {
			return map[string]any{"outcome": "promptRequired", "reason": "noRunningTurn"}, nil
		}
		return nil, err
	}
	return map[string]any{"outcome": "injected"}, nil
}

func (a *agent) NewSession(ctx context.Context, request acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	if err := a.waitConnection(ctx); err != nil {
		return acp.NewSessionResponse{}, err
	}
	if len(request.McpServers) != 0 {
		return acp.NewSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "Codex built-in adapter does not support ACP MCP server injection yet"})
	}
	roots, err := a.options.Workspace.validate(request.Cwd, request.AdditionalDirectories)
	if err != nil {
		return acp.NewSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	var response threadOpenResponse
	err = a.backend.rpc.Request(ctx, "thread/start", map[string]any{
		"cwd": request.Cwd, "runtimeWorkspaceRoots": roots,
		"approvalPolicy": "on-request", "sandbox": "workspace-write",
		"experimentalRawEvents": false, "ephemeral": false,
	}, &response)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	state, err := a.installSession(response, request.Cwd, roots, routeLive)
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	if err := a.loadModels(ctx, state); err != nil {
		a.removeSession(state.threadID, state.route)
		return acp.NewSessionResponse{}, err
	}
	return acp.NewSessionResponse{
		SessionId: acp.SessionId(state.threadID), ConfigOptions: state.configOptions(),
	}, nil
}

func (a *agent) ResumeSession(ctx context.Context, request acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	if err := a.waitConnection(ctx); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	if len(request.McpServers) != 0 {
		return acp.ResumeSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "Codex built-in adapter does not support ACP MCP server injection yet"})
	}
	roots, err := a.options.Workspace.validate(request.Cwd, request.AdditionalDirectories)
	if err != nil {
		return acp.ResumeSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	threadID := strings.TrimSpace(string(request.SessionId))
	if threadID == "" {
		return acp.ResumeSessionResponse{}, acp.NewInvalidParams(map[string]any{"error": "sessionId is required"})
	}
	if err := a.authorizeStoredThread(ctx, threadID, request.Cwd); err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	route, err := a.reserveSession(threadID, request.Cwd, roots, routeBuffering)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	var response threadOpenResponse
	err = a.backend.rpc.Request(ctx, "thread/resume", map[string]any{
		"threadId": threadID, "cwd": request.Cwd,
		"runtimeWorkspaceRoots": roots, "excludeTurns": true,
		"approvalPolicy": "on-request", "sandbox": "workspace-write",
	}, &response)
	if err != nil {
		a.removeSession(threadID, route)
		return acp.ResumeSessionResponse{}, err
	}
	route.state.applyOpenResponse(response)
	if err := a.loadModels(ctx, route.state); err != nil {
		a.removeSession(threadID, route)
		return acp.ResumeSessionResponse{}, err
	}
	if err := route.drainBufferedToLive(); err != nil {
		a.removeSession(threadID, route)
		return acp.ResumeSessionResponse{}, err
	}
	return acp.ResumeSessionResponse{ConfigOptions: route.state.configOptions()}, nil
}

func (a *agent) LoadSession(ctx context.Context, request acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	if err := a.waitConnection(ctx); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	return a.loadSession(ctx, request)
}

func (a *agent) Prompt(ctx context.Context, request acp.PromptRequest) (acp.PromptResponse, error) {
	if err := a.waitConnection(ctx); err != nil {
		return acp.PromptResponse{}, err
	}
	state, err := a.lookupSession(string(request.SessionId))
	if err != nil {
		return acp.PromptResponse{}, err
	}
	input, err := promptInput(request.Prompt)
	if err != nil {
		return acp.PromptResponse{}, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	// AgentSideConnection cancels a superseded Prompt but intentionally does
	// not wait for its handler to return. Serialize the app-server turn
	// handshake so a replacement cannot start until the cancelled turn is
	// either known and interrupted or the bounded handshake has failed.
	state.promptMu.Lock()
	defer state.promptMu.Unlock()
	if ctx.Err() != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	if routeErr := state.route.failure(); routeErr != nil {
		return acp.PromptResponse{}, routeErr
	}
	state.mu.Lock()
	if state.activeTurnID != "" {
		state.mu.Unlock()
		return acp.PromptResponse{}, acp.NewInvalidRequest(map[string]any{"error": "session already has an active turn"})
	}
	state.turnDone = make(chan turnResult, 1)
	done := state.turnDone
	model, effort := state.model, state.effort
	state.mu.Unlock()
	params := map[string]any{"threadId": state.threadID, "input": input, "cwd": state.cwd}
	if model != "" {
		params["model"] = model
	}
	if effort != "" {
		params["effort"] = effort
	}
	var response struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	startCtx, cancelStart := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	err = a.backend.rpc.Request(startCtx, "turn/start", params, &response)
	cancelStart()
	if err != nil {
		state.clearTurn(done)
		if errors.Is(err, context.DeadlineExceeded) {
			state.route.close(errors.New("codex adapter: turn/start outcome is unknown after timeout"))
		}
		return acp.PromptResponse{}, err
	}
	turnID := strings.TrimSpace(response.Turn.ID)
	if turnID == "" {
		state.clearTurn(done)
		return acp.PromptResponse{}, errors.New("codex adapter: app-server returned an empty Turn ID")
	}
	state.mu.Lock()
	state.activeTurnID = turnID
	state.mu.Unlock()
	if ctx.Err() != nil {
		return a.finishCancelledTurn(state, done)
	}
	select {
	case <-ctx.Done():
		return a.finishCancelledTurn(state, done)
	case <-a.backend.Done():
		state.clearTurn(done)
		return acp.PromptResponse{}, fmt.Errorf("codex adapter: backend lost: %w", a.backend.Err())
	case result := <-done:
		state.clearTurn(done)
		if result.err != nil {
			return acp.PromptResponse{}, result.err
		}
		return acp.PromptResponse{StopReason: result.stopReason}, nil
	}
}

func (a *agent) finishCancelledTurn(state *sessionState, done chan turnResult) (acp.PromptResponse, error) {
	// A natural terminal may race with cancellation. Its canonical app-server
	// status wins over the cancelled request context.
	select {
	case result := <-done:
		state.clearTurn(done)
		return promptResponse(result)
	default:
	}

	terminalCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	interruptResult := make(chan error, 1)
	go func() { interruptResult <- a.interrupt(terminalCtx, state) }()

	var interruptErr error
	select {
	case result := <-done:
		state.clearTurn(done)
		return promptResponse(result)
	case err := <-interruptResult:
		interruptErr = err
	}

	// An interrupt can be rejected because another cancellation already won.
	// The corresponding terminal notification remains authoritative, so keep
	// waiting for it until the original bounded window expires.
	select {
	case result := <-done:
		state.clearTurn(done)
		return promptResponse(result)
	case <-a.backend.Done():
		select {
		case result := <-done:
			state.clearTurn(done)
			return promptResponse(result)
		default:
		}
		backendErr := fmt.Errorf("codex adapter: backend lost while awaiting cancelled Turn terminal: %w", a.backend.Err())
		return a.failCancelledTurn(state, done, errors.Join(backendErr, interruptErr))
	case <-terminalCtx.Done():
		select {
		case result := <-done:
			state.clearTurn(done)
			return promptResponse(result)
		default:
		}
		terminalErr := errors.New("codex adapter: cancelled Turn did not reach a terminal state")
		return a.failCancelledTurn(state, done, errors.Join(terminalErr, interruptErr))
	}
}

func (a *agent) failCancelledTurn(state *sessionState, done chan turnResult, err error) (acp.PromptResponse, error) {
	state.route.close(err)
	state.clearTurn(done)
	return acp.PromptResponse{}, err
}

func promptResponse(result turnResult) (acp.PromptResponse, error) {
	if result.err != nil {
		return acp.PromptResponse{}, result.err
	}
	return acp.PromptResponse{StopReason: result.stopReason}, nil
}

func (a *agent) Cancel(ctx context.Context, request acp.CancelNotification) error {
	if err := a.waitConnection(ctx); err != nil {
		return err
	}
	state, err := a.lookupSession(string(request.SessionId))
	if err != nil {
		//nolint:nilerr // Cancellation is idempotent; an unknown Session has no remaining work to interrupt.
		return nil
	}
	return a.interrupt(ctx, state)
}

func (a *agent) CloseSession(ctx context.Context, request acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	if err := a.waitConnection(ctx); err != nil {
		return acp.CloseSessionResponse{}, err
	}
	threadID := strings.TrimSpace(string(request.SessionId))
	a.mu.Lock()
	state := a.sessions[threadID]
	delete(a.sessions, threadID)
	a.mu.Unlock()
	if state == nil {
		return acp.CloseSessionResponse{}, nil
	}
	_ = a.interrupt(ctx, state)
	a.backend.release(threadID, state.route)
	state.route.close(errors.New("codex adapter: session closed"))
	_ = a.backend.rpc.Request(ctx, "thread/unsubscribe", map[string]any{"threadId": threadID}, nil)
	return acp.CloseSessionResponse{}, nil
}

func (a *agent) SetSessionConfigOption(ctx context.Context, request acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if err := a.waitConnection(ctx); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	if request.ValueId == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "Codex config options require string values"})
	}
	state, err := a.lookupSession(string(request.ValueId.SessionId))
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	id := string(request.ValueId.ConfigId)
	value := strings.TrimSpace(string(request.ValueId.Value))
	state.mu.Lock()
	switch id {
	case configIDModel:
		if !state.hasModel(value) {
			state.mu.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unknown Codex model"})
		}
		state.model = value
		state.selectDefaultEffortLocked()
	case configIDEffort:
		if !state.hasEffort(value) {
			state.mu.Unlock()
			return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unsupported reasoning effort"})
		}
		state.effort = value
	default:
		state.mu.Unlock()
		return acp.SetSessionConfigOptionResponse{}, acp.NewInvalidParams(map[string]any{"error": "unknown Codex config option"})
	}
	options := state.configOptionsLocked()
	state.mu.Unlock()
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

func (a *agent) waitConnection(ctx context.Context) error {
	if a.connectionReady == nil {
		return nil
	}
	select {
	case <-a.connectionReady:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (a *agent) interrupt(ctx context.Context, state *sessionState) error {
	state.mu.Lock()
	turnID := state.activeTurnID
	state.mu.Unlock()
	if turnID == "" {
		return nil
	}
	return a.backend.rpc.Request(ctx, "turn/interrupt", map[string]any{
		"threadId": state.threadID, "turnId": turnID,
	}, nil)
}

func (a *agent) installSession(response threadOpenResponse, cwd string, roots []string, mode routeMode) (*sessionState, error) {
	threadID := strings.TrimSpace(response.Thread.ID)
	if threadID == "" {
		return nil, errors.New("codex adapter: app-server returned an empty Thread ID")
	}
	route, err := a.reserveSession(threadID, cwd, roots, mode)
	if err != nil {
		return nil, err
	}
	route.state.applyOpenResponse(response)
	return route.state, nil
}

func (a *agent) reserveSession(threadID, cwd string, roots []string, mode routeMode) (*sessionRoute, error) {
	state := &sessionState{threadID: threadID, cwd: strings.TrimSpace(cwd), roots: append([]string(nil), roots...)}
	route := newSessionRoute(a, state, mode)
	state.route = route
	if err := a.backend.acquire(threadID, route); err != nil {
		route.close(err)
		return nil, err
	}
	a.mu.Lock()
	if existing := a.sessions[threadID]; existing != nil {
		a.mu.Unlock()
		a.backend.release(threadID, route)
		route.close(fmt.Errorf("codex adapter: session %q is already open", threadID))
		return nil, fmt.Errorf("codex adapter: session %q is already open", threadID)
	}
	a.sessions[threadID] = state
	a.mu.Unlock()
	return route, nil
}

func (a *agent) removeSession(threadID string, route *sessionRoute) {
	a.mu.Lock()
	if state := a.sessions[threadID]; state != nil && state.route == route {
		delete(a.sessions, threadID)
	}
	a.mu.Unlock()
	a.backend.release(threadID, route)
	route.close(errors.New("codex adapter: Session open failed"))
}

func (a *agent) releaseClosedSession(threadID string, route *sessionRoute) {
	a.mu.Lock()
	if state := a.sessions[threadID]; state != nil && state.route == route {
		delete(a.sessions, threadID)
	}
	a.mu.Unlock()
	a.backend.release(threadID, route)
}

func (a *agent) lookupSession(threadID string) (*sessionState, error) {
	a.mu.Lock()
	state := a.sessions[strings.TrimSpace(threadID)]
	a.mu.Unlock()
	if state == nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": "unknown or unopened Codex session"})
	}
	return state, nil
}

func (a *agent) closeAll() {
	a.mu.Lock()
	sessions := a.sessions
	a.sessions = make(map[string]*sessionState)
	a.mu.Unlock()
	for threadID, state := range sessions {
		a.backend.release(threadID, state.route)
		state.route.close(errors.New("codex adapter: ACP connection closed"))
	}
}

func (a *agent) authorizeStoredThread(ctx context.Context, threadID, requestedCWD string) error {
	var response struct {
		Thread struct {
			ID  string `json:"id"`
			CWD string `json:"cwd"`
		} `json:"thread"`
	}
	if err := a.backend.rpc.Request(ctx, "thread/read", map[string]any{
		"threadId": threadID, "includeTurns": false,
	}, &response); err != nil {
		return err
	}
	stored, err := cleanAbsolute(response.Thread.CWD)
	if err != nil {
		return acp.NewInvalidRequest(map[string]any{"error": "stored Codex Thread has an invalid cwd"})
	}
	requested, err := cleanAbsolute(requestedCWD)
	if err != nil || stored != requested {
		return acp.NewInvalidParams(map[string]any{"error": "requested cwd does not match the stored Codex Thread"})
	}
	if _, err := a.options.Workspace.validate(stored, nil); err != nil {
		return acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	return nil
}

func (a *agent) loadModels(ctx context.Context, state *sessionState) error {
	var models []codexModel
	var cursor any
	for range 20 {
		params := map[string]any{"includeHidden": false, "limit": 100}
		if cursor != nil {
			params["cursor"] = cursor
		}
		var response struct {
			Data       []codexModel `json:"data"`
			NextCursor any          `json:"nextCursor"`
		}
		if err := a.backend.rpc.Request(ctx, "model/list", params, &response); err != nil {
			return fmt.Errorf("codex adapter: list models: %w", err)
		}
		models = append(models, response.Data...)
		if response.NextCursor == nil || strings.TrimSpace(fmt.Sprint(response.NextCursor)) == "" || fmt.Sprint(response.NextCursor) == "<nil>" {
			break
		}
		cursor = response.NextCursor
	}
	state.mu.Lock()
	state.models = models
	if state.model == "" && len(models) > 0 {
		state.model = modelName(models[0])
	}
	if state.effort == "" {
		state.selectDefaultEffortLocked()
	}
	state.mu.Unlock()
	return nil
}

var _ acp.Agent = (*agent)(nil)
var _ acp.AgentLoader = (*agent)(nil)
var _ acp.AgentSessionResumer = (*agent)(nil)
var _ acp.AgentSessionCloser = (*agent)(nil)
var _ acp.AgentSessionConfig = (*agent)(nil)
var _ acp.ExtensionMethodHandler = (*agent)(nil)
