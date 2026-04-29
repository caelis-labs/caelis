package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/acp"
	bridgeloader "github.com/OnslaughtSnail/caelis/acpbridge/loader"
	bridgeprojector "github.com/OnslaughtSnail/caelis/acpbridge/projector"
	bridgeterminal "github.com/OnslaughtSnail/caelis/acpbridge/terminal"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

// BuildAgentSpecFunc assembles the runtime-facing agent spec for one ACP
// prompt.
type BuildAgentSpecFunc func(context.Context, sdksession.Session, acp.PromptRequest) (sdkruntime.AgentSpec, error)

// Config configures one runtime-backed ACP agent adapter.
type Config struct {
	Runtime        sdkruntime.Runtime
	Sessions       sdksession.Service
	BuildAgentSpec BuildAgentSpecFunc
	Projector      acp.Projector
	Loader         acp.SessionLoader
	Modes          acp.ModeProvider
	Config         acp.ConfigProvider
	AppName        string
	UserID         string
	AgentInfo      *acp.Implementation
}

// RuntimeAgent adapts sdk/runtime + sdk/session into the standard ACP
// agent-side methods.
type RuntimeAgent struct {
	runtime        sdkruntime.Runtime
	sessions       sdksession.Service
	buildAgentSpec BuildAgentSpecFunc
	projector      acp.Projector
	loader         acp.SessionLoader
	modes          acp.ModeProvider
	config         acp.ConfigProvider
	appName        string
	userID         string
	agentInfo      *acp.Implementation

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// New constructs one runtime-backed ACP agent.
func New(cfg Config) (*RuntimeAgent, error) {
	if cfg.Runtime == nil {
		return nil, errors.New("acpbridge/agentruntime: runtime is required")
	}
	if cfg.Sessions == nil {
		return nil, errors.New("acpbridge/agentruntime: session service is required")
	}
	if cfg.BuildAgentSpec == nil {
		return nil, errors.New("acpbridge/agentruntime: agent spec builder is required")
	}
	projector := cfg.Projector
	if projector == nil {
		projector = bridgeprojector.EventProjector{}
	}
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "caelis"
	}
	userID := strings.TrimSpace(cfg.UserID)
	if userID == "" {
		userID = "acp"
	}
	loader := cfg.Loader
	if loader == nil {
		loader = defaultSessionLoader{inner: bridgeloader.NewSessionServiceLoader(bridgeloader.SessionServiceLoaderConfig{
			Sessions:  cfg.Sessions,
			Projector: projector,
			AppName:   appName,
			UserID:    userID,
			Modes:     cfg.Modes,
			Config:    cfg.Config,
		})}
	}
	return &RuntimeAgent{
		runtime:        cfg.Runtime,
		sessions:       cfg.Sessions,
		buildAgentSpec: cfg.BuildAgentSpec,
		projector:      projector,
		loader:         loader,
		modes:          cfg.Modes,
		config:         cfg.Config,
		appName:        appName,
		userID:         userID,
		agentInfo:      cfg.AgentInfo,
		cancels:        map[string]context.CancelFunc{},
	}, nil
}

func (a *RuntimeAgent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	caps := acp.AgentCapabilities{
		Auth: map[string]any{},
		MCPCapabilities: acp.MCPCapabilities{
			HTTP: false,
			SSE:  false,
		},
		PromptCapabilities: acp.PromptCapabilities{
			Audio:           false,
			EmbeddedContext: false,
			Image:           false,
		},
		SessionCapabilities: map[string]json.RawMessage{},
	}
	if a.loader != nil {
		caps.LoadSession = true
	}
	return acp.InitializeResponse{
		ProtocolVersion:   acp.CurrentProtocolVersion,
		AgentCapabilities: caps,
		AgentInfo:         a.agentInfo,
		AuthMethods:       []json.RawMessage{},
	}, nil
}

func (a *RuntimeAgent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *RuntimeAgent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	session, err := a.sessions.StartSession(ctx, sdksession.StartSessionRequest{
		AppName: a.appName,
		UserID:  a.userID,
		Workspace: sdksession.WorkspaceRef{
			Key: strings.TrimSpace(req.CWD),
			CWD: strings.TrimSpace(req.CWD),
		},
	})
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	_, _ = a.sessions.BindController(ctx, sdksession.BindControllerRequest{
		SessionRef: session.SessionRef,
		Binding: sdksession.ControllerBinding{
			Kind:         sdksession.ControllerKindKernel,
			ControllerID: "sdk-runtime",
			Label:        "SDK Runtime",
			EpochID:      "kernel",
			Source:       "acp",
		},
	})
	resp := acp.NewSessionResponse{SessionID: session.SessionID}
	if a.modes != nil {
		modes, err := a.modes.SessionModes(ctx, session)
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		resp.Modes = modes
	}
	if a.config != nil {
		options, err := a.config.SessionConfigOptions(ctx, session)
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		resp.ConfigOptions = options
	}
	return resp, nil
}

func (a *RuntimeAgent) LoadSession(ctx context.Context, req acp.LoadSessionRequest, cb acp.PromptCallbacks) (acp.LoadSessionResponse, error) {
	if a.loader == nil {
		return acp.LoadSessionResponse{}, acp.ErrCapabilityUnsupported
	}
	return a.loader.LoadSession(ctx, req, cb)
}

func (a *RuntimeAgent) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	if a.modes == nil {
		return acp.SetSessionModeResponse{}, acp.ErrCapabilityUnsupported
	}
	return a.modes.SetSessionMode(ctx, req)
}

func (a *RuntimeAgent) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if a.config == nil {
		return acp.SetSessionConfigOptionResponse{}, acp.ErrCapabilityUnsupported
	}
	return a.config.SetSessionConfigOption(ctx, req)
}

func (a *RuntimeAgent) Prompt(ctx context.Context, req acp.PromptRequest, cb acp.PromptCallbacks) (acp.PromptResponse, error) {
	ref := sdksession.SessionRef{
		AppName:   a.appName,
		UserID:    a.userID,
		SessionID: strings.TrimSpace(req.SessionID),
	}
	session, err := a.sessions.Session(ctx, ref)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	spec, err := a.buildAgentSpec(ctx, session, req)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	input, err := promptText(req.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.setCancel(req.SessionID, cancel)
	defer a.clearCancel(req.SessionID)
	defer cancel()

	result, err := a.runtime.Run(runCtx, sdkruntime.RunRequest{
		SessionRef:        ref,
		Input:             input,
		Request:           sdkruntime.ModelRequestOptions{Stream: boolPtr(true)},
		ApprovalRequester: approvalRequester{callbacks: cb},
		AgentSpec:         spec,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			if errors.Is(seqErr, context.Canceled) {
				return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
			}
			return acp.PromptResponse{}, seqErr
		}
		if event == nil {
			continue
		}
		if err := a.emitEvent(runCtx, cb, event); err != nil {
			return acp.PromptResponse{}, err
		}
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *RuntimeAgent) Cancel(_ context.Context, req acp.CancelNotification) error {
	a.mu.Lock()
	cancel := a.cancels[strings.TrimSpace(req.SessionID)]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (a *RuntimeAgent) Output(ctx context.Context, req acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	adapter, ok := a.terminalAdapter()
	if !ok {
		return acp.TerminalOutputResponse{}, acp.ErrCapabilityUnsupported
	}
	return adapter.Output(ctx, req)
}

func (a *RuntimeAgent) WaitForExit(ctx context.Context, req acp.TerminalWaitForExitRequest) (acp.TerminalWaitForExitResponse, error) {
	adapter, ok := a.terminalAdapter()
	if !ok {
		return acp.TerminalWaitForExitResponse{}, acp.ErrCapabilityUnsupported
	}
	return adapter.WaitForExit(ctx, req)
}

func (a *RuntimeAgent) Kill(ctx context.Context, req acp.TerminalKillRequest) error {
	adapter, ok := a.terminalAdapter()
	if !ok {
		return acp.ErrCapabilityUnsupported
	}
	return adapter.Kill(ctx, req)
}

func (a *RuntimeAgent) Release(ctx context.Context, req acp.TerminalReleaseRequest) error {
	adapter, ok := a.terminalAdapter()
	if !ok {
		return acp.ErrCapabilityUnsupported
	}
	return adapter.Release(ctx, req)
}

func (a *RuntimeAgent) emitEvent(ctx context.Context, cb acp.PromptCallbacks, event *sdksession.Event) error {
	if cb == nil || event == nil {
		return nil
	}
	if permission, ok, err := a.projector.ProjectPermissionRequest(event); err != nil {
		return err
	} else if ok && permission != nil {
		_, err := cb.RequestPermission(ctx, *permission)
		return err
	}
	notifications, err := a.projector.ProjectNotifications(event)
	if err != nil {
		return err
	}
	for _, notification := range notifications {
		if err := cb.SessionUpdate(ctx, notification); err != nil {
			return err
		}
	}
	return nil
}

func (a *RuntimeAgent) setCancel(sessionID string, cancel context.CancelFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancels[strings.TrimSpace(sessionID)] = cancel
}

func (a *RuntimeAgent) clearCancel(sessionID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.cancels, strings.TrimSpace(sessionID))
}

func boolPtr(v bool) *bool { return &v }

func promptText(prompt []json.RawMessage) (string, error) {
	parts := make([]string, 0, len(prompt))
	for _, raw := range prompt {
		if len(raw) == 0 {
			continue
		}
		var item struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return "", fmt.Errorf("acpbridge/agentruntime: decode prompt content: %w", err)
		}
		switch strings.TrimSpace(item.Type) {
		case "", "text":
			if text := strings.TrimSpace(item.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), nil
}

type approvalRequester struct {
	callbacks acp.PromptCallbacks
}

func (r approvalRequester) RequestApproval(
	ctx context.Context,
	req sdkruntime.ApprovalRequest,
) (sdkruntime.ApprovalResponse, error) {
	if r.callbacks == nil || req.Approval == nil {
		return sdkruntime.ApprovalResponse{}, nil
	}
	projector := bridgeprojector.EventProjector{}
	event := &sdksession.Event{
		SessionID: strings.TrimSpace(req.SessionRef.SessionID),
		Protocol: &sdksession.EventProtocol{
			UpdateType: string(sdksession.ProtocolUpdateTypePermission),
			Approval:   cloneProtocolApproval(req.Approval),
		},
	}
	request, ok, err := projector.ProjectPermissionRequest(event)
	if err != nil {
		return sdkruntime.ApprovalResponse{}, err
	}
	if !ok || request == nil {
		return sdkruntime.ApprovalResponse{}, nil
	}
	response, err := r.callbacks.RequestPermission(ctx, *request)
	if err != nil {
		return sdkruntime.ApprovalResponse{}, err
	}
	outcome := strings.TrimSpace(response.Outcome.Outcome)
	optionID := strings.TrimSpace(response.Outcome.OptionID)
	approved := false
	if outcome == "selected" {
		for _, item := range request.Options {
			if item.OptionID == optionID && strings.HasPrefix(strings.ToLower(strings.TrimSpace(item.Kind)), "allow") {
				approved = true
				break
			}
		}
	}
	return sdkruntime.ApprovalResponse{
		Outcome:  outcome,
		OptionID: optionID,
		Approved: approved,
	}, nil
}

func cloneProtocolApproval(in *sdksession.ProtocolApproval) *sdksession.ProtocolApproval {
	if in == nil {
		return nil
	}
	out := *in
	if len(in.Options) > 0 {
		out.Options = append([]sdksession.ProtocolApprovalOption(nil), in.Options...)
	}
	return &out
}

type defaultSessionLoader struct {
	inner *bridgeloader.SessionServiceLoader
}

func (l defaultSessionLoader) LoadSession(
	ctx context.Context,
	req acp.LoadSessionRequest,
	cb acp.PromptCallbacks,
) (acp.LoadSessionResponse, error) {
	if l.inner == nil {
		return acp.LoadSessionResponse{}, acp.ErrCapabilityUnsupported
	}
	return l.inner.LoadSession(ctx, req, cb)
}

func (a *RuntimeAgent) terminalAdapter() (acp.TerminalAdapter, bool) {
	provider, ok := a.runtime.(sdkruntime.StreamProvider)
	if !ok || provider.Streams() == nil {
		return nil, false
	}
	return bridgeterminal.LocalTerminalAdapter{Streams: provider.Streams()}, true
}

var (
	_ acp.Agent           = (*RuntimeAgent)(nil)
	_ acp.TerminalAdapter = (*RuntimeAgent)(nil)
)
