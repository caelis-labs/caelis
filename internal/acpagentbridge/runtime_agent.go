package acpagentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/loader"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/terminal"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

// BuildAgentSpecFunc assembles the runtime-facing agent spec for one ACP
// prompt.
type BuildAgentSpecFunc func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error)

// ApprovalModelResolver resolves the model used by automatic approval review.
type ApprovalModelResolver = approval.ModelResolver

type PromptRouterFactory func(context.Context, session.Session) (controlprompt.Router, error)

// Config configures one runtime-backed ACP agent adapter.
type Config struct {
	Runtime        agent.Runtime
	Sessions       session.Service
	BuildAgentSpec BuildAgentSpecFunc
	Projector      projector.Projector
	Loader         acp.SessionLoader
	Modes          acp.ModeProvider
	// ApprovalModes is the dedicated approval-routing mode source. Do not point
	// this at app-owned assembly modes; those are client-visible session modes,
	// while approval routing is restricted to manual/auto-review.
	ApprovalModes         acp.ModeProvider
	Config                acp.ConfigProvider
	Models                acp.ModelProvider
	Commands              acp.CommandProvider
	PromptRouterFactory   PromptRouterFactory
	PromptCaps            acp.PromptCapabilitiesProvider
	TaskStreams           taskstream.Service
	TaskStreamPrincipal   taskstream.Principal
	ApprovalReviewer      approval.Reviewer
	ApprovalModelResolver ApprovalModelResolver
	AppName               string
	UserID                string
	WorkspaceKey          string
	AgentInfo             *acp.Implementation
}

// RuntimeAgent adapts Agent SDK runtime and session contracts into the standard
// ACP agent-side methods.
type RuntimeAgent struct {
	runtime               agent.Runtime
	sessions              session.Service
	buildAgentSpec        BuildAgentSpecFunc
	projector             projector.Projector
	loader                acp.SessionLoader
	modes                 acp.ModeProvider
	approvalModes         acp.ModeProvider
	config                acp.ConfigProvider
	models                acp.ModelProvider
	commands              acp.CommandProvider
	promptRouterFactory   PromptRouterFactory
	promptCaps            acp.PromptCapabilitiesProvider
	taskStreams           taskstream.Service
	taskStreamPrincipal   taskstream.Principal
	approvalReviewer      approval.Reviewer
	approvalModelResolver ApprovalModelResolver
	appName               string
	userID                string
	workspaceKey          string
	agentInfo             *acp.Implementation

	mu           sync.Mutex
	cancels      map[string]context.CancelFunc
	terminalRefs map[string]stream.Ref
	taskMuxes    map[string]map[*acpTaskStreamMux]struct{}
}

// New constructs one runtime-backed ACP agent.
func New(cfg Config) (*RuntimeAgent, error) {
	if cfg.Runtime == nil {
		return nil, errors.New("internal/acpagentbridge: runtime is required")
	}
	if cfg.Sessions == nil {
		return nil, errors.New("internal/acpagentbridge: session service is required")
	}
	if cfg.BuildAgentSpec == nil {
		return nil, errors.New("internal/acpagentbridge: agent spec builder is required")
	}
	eventProjector := cfg.Projector
	if eventProjector == nil {
		eventProjector = projector.EventProjector{}
	}
	appName := strings.TrimSpace(cfg.AppName)
	if appName == "" {
		appName = "caelis"
	}
	userID := strings.TrimSpace(cfg.UserID)
	if userID == "" {
		userID = "acp"
	}
	sessionLoader := cfg.Loader
	if sessionLoader == nil {
		sessionLoader = defaultSessionLoader{inner: loader.NewSessionServiceLoader(loader.SessionServiceLoaderConfig{
			Sessions:     cfg.Sessions,
			Projector:    eventProjector,
			AppName:      appName,
			UserID:       userID,
			WorkspaceKey: strings.TrimSpace(cfg.WorkspaceKey),
			Modes:        cfg.Modes,
			Config:       cfg.Config,
		})}
	}
	approvalModes := cfg.ApprovalModes
	if approvalModes == nil {
		approvalModes = cfg.Modes
	}
	return &RuntimeAgent{
		runtime:               cfg.Runtime,
		sessions:              cfg.Sessions,
		buildAgentSpec:        cfg.BuildAgentSpec,
		projector:             eventProjector,
		loader:                sessionLoader,
		modes:                 cfg.Modes,
		approvalModes:         approvalModes,
		config:                cfg.Config,
		models:                cfg.Models,
		commands:              cfg.Commands,
		promptRouterFactory:   cfg.PromptRouterFactory,
		promptCaps:            cfg.PromptCaps,
		taskStreams:           cfg.TaskStreams,
		taskStreamPrincipal:   cfg.TaskStreamPrincipal,
		approvalReviewer:      cfg.ApprovalReviewer,
		approvalModelResolver: cfg.ApprovalModelResolver,
		appName:               appName,
		userID:                userID,
		workspaceKey:          strings.TrimSpace(cfg.WorkspaceKey),
		agentInfo:             normalizeAgentInfo(cfg.AgentInfo, appName),
		cancels:               map[string]context.CancelFunc{},
		terminalRefs:          map[string]stream.Ref{},
		taskMuxes:             map[string]map[*acpTaskStreamMux]struct{}{},
	}, nil
}

func normalizeAgentInfo(info *acp.Implementation, appName string) *acp.Implementation {
	normalized := acp.Implementation{}
	if info != nil {
		normalized = *info
	}
	if normalized.Name = strings.TrimSpace(normalized.Name); normalized.Name == "" {
		normalized.Name = strings.TrimSpace(appName)
	}
	normalized.Title = strings.TrimSpace(normalized.Title)
	if normalized.Version = strings.TrimSpace(normalized.Version); normalized.Version == "" {
		normalized.Version = version.String()
	}
	return &normalized
}

func (a *RuntimeAgent) Initialize(ctx context.Context, _ acp.InitializeRequest) (acp.InitializeResponse, error) {
	promptCaps := acp.PromptCapabilities{
		Audio:           false,
		EmbeddedContext: false,
		Image:           false,
	}
	if a.promptCaps != nil {
		caps, err := a.promptCaps.PromptCapabilities(ctx)
		if err != nil {
			return acp.InitializeResponse{}, err
		}
		promptCaps = caps
	}
	caps := acp.AgentCapabilities{
		Auth: map[string]any{},
		MCPCapabilities: acp.MCPCapabilities{
			HTTP: false,
			SSE:  false,
		},
		PromptCapabilities:  promptCaps,
		SessionCapabilities: map[string]json.RawMessage{},
	}
	if a.loader != nil {
		caps.LoadSession = true
	}
	caps.SessionCapabilities["list"] = json.RawMessage(`{}`)
	caps.SessionCapabilities["resume"] = json.RawMessage(`{}`)
	caps.SessionCapabilities["close"] = json.RawMessage(`{}`)
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
	activeSession, err := a.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: a.appName,
		UserID:  a.userID,
		Workspace: session.WorkspaceRef{
			Key: strings.TrimSpace(req.CWD),
			CWD: strings.TrimSpace(req.CWD),
		},
	})
	if err != nil {
		return acp.NewSessionResponse{}, err
	}
	_, _ = a.sessions.BindController(ctx, session.BindControllerRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ControllerBinding{
			Kind:         session.ControllerKindKernel,
			ControllerID: "sdk-runtime",
			Label:        "SDK Runtime",
			EpochID:      "kernel",
			Source:       "acp",
		},
	})
	resp := acp.NewSessionResponse{SessionID: activeSession.SessionID}
	if a.modes != nil {
		modes, err := a.modes.SessionModes(ctx, activeSession)
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		resp.Modes = modes
	}
	if a.config != nil {
		options, err := a.config.SessionConfigOptions(ctx, activeSession)
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		resp.ConfigOptions = options
	}
	if a.models != nil {
		models, err := a.models.SessionModels(ctx, activeSession)
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		resp.Models = models
	}
	return resp, nil
}

func (a *RuntimeAgent) ListSessions(ctx context.Context, req acp.SessionListRequest) (acp.SessionListResponse, error) {
	list, err := a.sessions.ListSessions(ctx, session.ListSessionsRequest{
		AppName:      a.appName,
		UserID:       a.userID,
		WorkspaceKey: strings.TrimSpace(req.CWD),
		Cursor:       strings.TrimSpace(req.Cursor),
	})
	if err != nil {
		return acp.SessionListResponse{}, err
	}
	resp := acp.SessionListResponse{
		Sessions:   make([]acp.SessionSummary, 0, len(list.Sessions)),
		NextCursor: strings.TrimSpace(list.NextCursor),
	}
	for _, session := range list.Sessions {
		summary := acp.SessionSummary{
			SessionID: strings.TrimSpace(session.SessionID),
			CWD:       strings.TrimSpace(session.CWD),
			Title:     strings.TrimSpace(session.Title),
		}
		if !session.UpdatedAt.IsZero() {
			summary.UpdatedAt = session.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
		}
		resp.Sessions = append(resp.Sessions, summary)
	}
	return resp, nil
}

func (a *RuntimeAgent) LoadSession(ctx context.Context, req acp.LoadSessionRequest, cb acp.PromptCallbacks) (acp.LoadSessionResponse, error) {
	if a.loader == nil {
		return acp.LoadSessionResponse{}, acp.ErrCapabilityUnsupported
	}
	loadCallbacks := cb
	if cb != nil {
		loadCallbacks = normalizingPromptCallbacks{inner: cb}
	}
	resp, err := a.loader.LoadSession(ctx, req, loadCallbacks)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if a.models != nil {
		session, err := a.session(ctx, req.SessionID)
		if err != nil {
			return acp.LoadSessionResponse{}, err
		}
		models, err := a.models.SessionModels(ctx, session)
		if err != nil {
			return acp.LoadSessionResponse{}, err
		}
		resp.Models = models
	}
	return resp, nil
}

func (a *RuntimeAgent) ResumeSession(ctx context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	session, err := a.session(ctx, req.SessionID)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	resp := acp.ResumeSessionResponse{}
	if a.modes != nil {
		modes, err := a.modes.SessionModes(ctx, session)
		if err != nil {
			return acp.ResumeSessionResponse{}, err
		}
		resp.Modes = modes
	}
	if a.config != nil {
		options, err := a.config.SessionConfigOptions(ctx, session)
		if err != nil {
			return acp.ResumeSessionResponse{}, err
		}
		resp.ConfigOptions = options
	}
	if a.models != nil {
		models, err := a.models.SessionModels(ctx, session)
		if err != nil {
			return acp.ResumeSessionResponse{}, err
		}
		resp.Models = models
	}
	return resp, nil
}

func (a *RuntimeAgent) CloseSession(ctx context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	if err := a.Cancel(ctx, acp.CancelNotification(req)); err != nil {
		return acp.CloseSessionResponse{}, err
	}
	sessionID := strings.TrimSpace(req.SessionID)
	a.mu.Lock()
	delete(a.cancels, sessionID)
	a.mu.Unlock()
	a.closeACPTaskStreamMuxes(sessionID)
	return acp.CloseSessionResponse{}, nil
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

func (a *RuntimeAgent) SetSessionModel(ctx context.Context, req acp.SetSessionModelRequest) (acp.SetSessionModelResponse, error) {
	if a.models == nil {
		return acp.SetSessionModelResponse{}, acp.ErrCapabilityUnsupported
	}
	return a.models.SetSessionModel(ctx, req)
}

func (a *RuntimeAgent) AvailableCommands(ctx context.Context, sessionID string) ([]acp.AvailableCommand, error) {
	if a.commands == nil {
		return nil, nil
	}
	return a.commands.AvailableCommands(ctx, sessionID)
}

func (a *RuntimeAgent) promptApprovalMode(ctx context.Context, activeSession session.Session) (approval.Mode, error) {
	if a == nil || a.approvalModes == nil {
		return approval.ModeAutoReview, nil
	}
	modes, err := a.approvalModes.SessionModes(ctx, activeSession)
	if err != nil {
		return approval.ModeAutoReview, err
	}
	if modes == nil {
		return approval.ModeAutoReview, nil
	}
	return approval.NormalizeMode(modes.CurrentModeID), nil
}

func (a *RuntimeAgent) Prompt(ctx context.Context, req acp.PromptRequest, cb acp.PromptCallbacks) (acp.PromptResponse, error) {
	activeSession, err := a.session(ctx, req.SessionID)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	input, contentParts, err := promptContent(req.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	ref := a.activeSessionRef(activeSession, req.SessionID)

	runCtx, cancel := context.WithCancel(ctx)
	a.setCancel(req.SessionID, cancel)
	defer a.clearCancel(req.SessionID)
	defer cancel()

	handled, err := a.runPromptRouter(runCtx, ctx, activeSession, input, contentParts, cb)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}
	if handled {
		return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
	}

	approvalMode, err := a.promptApprovalMode(ctx, activeSession)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	spec, err := a.buildAgentSpec(ctx, activeSession, req)
	if err != nil {
		return acp.PromptResponse{}, err
	}

	result, err := a.runtime.Run(runCtx, agent.RunRequest{
		SessionRef:   ref,
		Input:        input,
		ContentParts: contentParts,
		Request:      agent.ModelRequestOptions{Stream: boolPtr(true)},
		ApprovalRequester: approvalRequester{
			callbacks:     cb,
			reviewer:      a.approvalReviewer,
			modelResolver: a.approvalModelResolver,
			mode:          approvalMode,
		},
		AgentSpec: spec,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}
	if err := a.emitRunEvents(runCtx, ctx, cb, ref, result.Handle, true); err != nil {
		if errors.Is(err, context.Canceled) {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

func (a *RuntimeAgent) emitRunEvents(runCtx context.Context, _ context.Context, cb acp.PromptCallbacks, ref session.SessionRef, handle agent.Runner, suppressUserEcho bool) error {
	if handle == nil {
		return nil
	}
	outboundFilter := newACPNarrativeFilter(suppressUserEcho)
	taskMux := a.startACPTaskStreamMux(runCtx, ref.SessionID)
	taskEvents := taskMux.Events()
	defer a.detachACPTaskStreamMux(runCtx, taskMux, cb, ref.SessionID, outboundFilter)
	eventCtx, cancelEvents := context.WithCancel(runCtx)
	defer func() {
		cancelEvents()
		_ = handle.Close()
	}()
	runEvents := runtimeRunnerEvents(eventCtx, handle)
	var observationGapSequence uint64
	for runEvents != nil {
		select {
		case <-runCtx.Done():
			return context.Canceled
		case taskEnvelope, ok := <-taskEvents:
			if !ok {
				taskEvents = nil
				continue
			}
			if err := a.emitControlEnvelope(runCtx, cb, ref.SessionID, nil, taskEnvelope, outboundFilter); err != nil {
				return err
			}
		case item, ok := <-runEvents:
			if !ok {
				runEvents = nil
				continue
			}
			if item.err != nil {
				if gap, ok := agent.AsEventStreamGap(item.err); ok {
					observationGapSequence++
					notice := projector.ProjectRuntimeObservationGap(gap.Dropped)
					notice.SessionID = strings.TrimSpace(ref.SessionID)
					if err := emitACPNotice(
						runCtx,
						cb,
						notice.SessionID,
						notice,
						fmt.Sprintf("caelis-runtime-observation-%d", observationGapSequence),
						acpFilterSourceFromEnvelope(notice, ref.SessionID),
						outboundFilter,
					); err != nil {
						return err
					}
					continue
				}
				if errors.Is(item.err, context.Canceled) {
					return context.Canceled
				}
				return item.err
			}
			if item.event == nil {
				continue
			}
			a.rememberTerminalRefFromEvent(item.event)
			base := projector.EnvelopeBaseFromSessionEvent(ref, item.event, projector.SessionEventTransport{})
			for _, envelope := range projector.ProjectSessionEventEnvelopeWithProjector(base, item.event, a.projector) {
				if err := a.emitTaskAwareControlEnvelope(
					runCtx,
					cb,
					ref.SessionID,
					nil,
					taskMux,
					&taskEvents,
					envelope,
					outboundFilter,
				); err != nil {
					return err
				}
			}
		}
	}
	return a.drainReadyACPTaskStream(runCtx, cb, ref.SessionID, &taskEvents, outboundFilter)
}

type runtimeRunnerEvent struct {
	event *session.Event
	err   error
}

func runtimeRunnerEvents(ctx context.Context, handle agent.Runner) <-chan runtimeRunnerEvent {
	events := make(chan runtimeRunnerEvent)
	go func() {
		defer close(events)
		for event, err := range handle.Events() {
			select {
			case <-ctx.Done():
				return
			case events <- runtimeRunnerEvent{event: event, err: err}:
			}
		}
	}()
	return events
}

func (a *RuntimeAgent) Cancel(_ context.Context, req acp.CancelNotification) error {
	ref := semantic.DecodeCancelNotification(req)
	a.mu.Lock()
	cancel := a.cancels[ref.SessionID]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (a *RuntimeAgent) session(ctx context.Context, sessionID string) (session.Session, error) {
	return a.sessions.Session(ctx, a.sessionRef(sessionID))
}

func (a *RuntimeAgent) sessionRef(sessionID string) session.SessionRef {
	return session.NormalizeSessionRef(session.SessionRef{
		AppName:   a.appName,
		UserID:    a.userID,
		SessionID: strings.TrimSpace(sessionID),
	})
}

func (a *RuntimeAgent) activeSessionRef(activeSession session.Session, sessionID string) session.SessionRef {
	ref := session.NormalizeSessionRef(activeSession.SessionRef)
	if ref.SessionID == "" {
		ref.SessionID = strings.TrimSpace(sessionID)
	}
	if ref.AppName == "" {
		ref.AppName = a.appName
	}
	if ref.UserID == "" {
		ref.UserID = a.userID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(activeSession.WorkspaceKey)
	}
	return ref
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

func (a *RuntimeAgent) rememberTerminalRefFromEvent(event *session.Event) {
	if a == nil || event == nil {
		return
	}
	ref, ok := terminal.RefFromEvent(event)
	if !ok {
		return
	}
	displayTerminalID := ""
	if toolPayload := session.EventToolProjection(event); toolPayload != nil {
		displayTerminalID = strings.TrimSpace(toolPayload.ID)
	}
	if displayTerminalID == "" {
		if update := session.ProtocolUpdateOf(event); update != nil {
			displayTerminalID = strings.TrimSpace(update.ToolCallID)
		}
	}
	a.rememberTerminalRef(event.SessionID, displayTerminalID, ref)
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

func stringPtr(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

func promptContent(prompt []json.RawMessage) (string, []model.ContentPart, error) {
	texts := make([]string, 0, len(prompt))
	contentParts := make([]model.ContentPart, 0, len(prompt))
	hasMedia := false
	for _, raw := range prompt {
		if len(raw) == 0 {
			continue
		}
		var item struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Data     string `json:"data"`
			Name     string `json:"name"`
			URI      string `json:"uri"`
		}
		if err := json.Unmarshal(raw, &item); err != nil {
			return "", nil, fmt.Errorf("internal/acpagentbridge: decode prompt content: %w", err)
		}
		switch strings.TrimSpace(item.Type) {
		case "", "text":
			if text := strings.TrimSpace(item.Text); text != "" {
				texts = append(texts, text)
				contentParts = append(contentParts, model.ContentPart{
					Type: model.ContentPartText,
					Text: text,
				})
			}
		case "image":
			data := strings.TrimSpace(item.Data)
			if data == "" && strings.TrimSpace(item.URI) != "" {
				return "", nil, fmt.Errorf("internal/acpagentbridge: image prompt content requires inline data")
			}
			if data == "" {
				continue
			}
			mimeType, data := splitDataURL(strings.TrimSpace(item.MimeType), data)
			contentParts = append(contentParts, model.ContentPart{
				Type:     model.ContentPartImage,
				MimeType: mimeType,
				Data:     data,
				FileName: strings.TrimSpace(item.Name),
			})
			hasMedia = true
		}
	}
	if !hasMedia {
		contentParts = nil
	}
	return strings.TrimSpace(strings.Join(texts, "\n")), contentParts, nil
}

func splitDataURL(mimeType string, data string) (string, string) {
	if strings.HasPrefix(data, "data:") {
		header, payload, ok := strings.Cut(data, ",")
		if ok {
			if prefix, suffix, ok := strings.Cut(header, ";base64"); ok && suffix == "" {
				mimeType = strings.TrimPrefix(prefix, "data:")
			}
			data = payload
		}
	}
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "image/png"
	}
	return strings.TrimSpace(mimeType), strings.TrimSpace(data)
}

type defaultSessionLoader struct {
	inner *loader.SessionServiceLoader
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
	provider, ok := a.runtime.(agent.StreamProvider)
	if !ok || provider.Streams() == nil {
		return nil, false
	}
	return terminal.LocalTerminalAdapter{Streams: provider.Streams(), ResolveRef: a.resolveTerminalRef}, true
}

func (a *RuntimeAgent) rememberTerminalRef(sessionID string, displayTerminalID string, ref stream.Ref) {
	if a == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	displayTerminalID = strings.TrimSpace(displayTerminalID)
	ref = stream.NormalizeRef(ref)
	if sessionID == "" || displayTerminalID == "" || ref.TerminalID == "" {
		return
	}
	if ref.SessionID == "" {
		ref.SessionID = sessionID
	}
	a.mu.Lock()
	if a.terminalRefs == nil {
		a.terminalRefs = map[string]stream.Ref{}
	}
	a.terminalRefs[terminalRefKey(sessionID, displayTerminalID)] = ref
	a.mu.Unlock()
}

func (a *RuntimeAgent) resolveTerminalRef(sessionID string, terminalID string) (stream.Ref, bool) {
	if a == nil {
		return stream.Ref{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	terminalID = strings.TrimSpace(terminalID)
	if sessionID == "" || terminalID == "" {
		return stream.Ref{}, false
	}
	a.mu.Lock()
	ref, ok := a.terminalRefs[terminalRefKey(sessionID, terminalID)]
	a.mu.Unlock()
	if !ok {
		return stream.Ref{}, false
	}
	ref = stream.NormalizeRef(ref)
	if ref.SessionID == "" {
		ref.SessionID = sessionID
	}
	return ref, true
}

func terminalRefKey(sessionID string, terminalID string) string {
	return strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(terminalID)
}

var (
	_ acp.Agent           = (*RuntimeAgent)(nil)
	_ acp.TerminalAdapter = (*RuntimeAgent)(nil)
)
