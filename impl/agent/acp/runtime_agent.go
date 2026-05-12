package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/impl/agent/acp/loader"
	"github.com/OnslaughtSnail/caelis/impl/agent/acp/terminal"
	"github.com/OnslaughtSnail/caelis/internal/version"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/approval"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
	"github.com/OnslaughtSnail/caelis/protocol/acp/projector"
)

// BuildAgentSpecFunc assembles the runtime-facing agent spec for one ACP
// prompt.
type BuildAgentSpecFunc func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error)

// ApprovalModelResolver resolves the model used by automatic approval review.
type ApprovalModelResolver = approval.ModelResolver

// Config configures one runtime-backed ACP agent adapter.
type Config struct {
	Runtime               agent.Runtime
	Sessions              session.Service
	BuildAgentSpec        BuildAgentSpecFunc
	Projector             projector.Projector
	Loader                acp.SessionLoader
	Modes                 acp.ModeProvider
	Config                acp.ConfigProvider
	Models                acp.ModelProvider
	Commands              acp.CommandProvider
	PromptCaps            acp.PromptCapabilitiesProvider
	ApprovalReviewer      approval.Reviewer
	ApprovalModelResolver ApprovalModelResolver
	AppName               string
	UserID                string
	AgentInfo             *acp.Implementation
}

// RuntimeAgent adapts ports/agent + ports/session into the standard ACP
// agent-side methods.
type RuntimeAgent struct {
	runtime               agent.Runtime
	sessions              session.Service
	buildAgentSpec        BuildAgentSpecFunc
	projector             projector.Projector
	loader                acp.SessionLoader
	modes                 acp.ModeProvider
	config                acp.ConfigProvider
	models                acp.ModelProvider
	commands              acp.CommandProvider
	promptCaps            acp.PromptCapabilitiesProvider
	approvalReviewer      approval.Reviewer
	approvalModelResolver ApprovalModelResolver
	appName               string
	userID                string
	agentInfo             *acp.Implementation

	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

// New constructs one runtime-backed ACP agent.
func New(cfg Config) (*RuntimeAgent, error) {
	if cfg.Runtime == nil {
		return nil, errors.New("impl/agent/acp: runtime is required")
	}
	if cfg.Sessions == nil {
		return nil, errors.New("impl/agent/acp: session service is required")
	}
	if cfg.BuildAgentSpec == nil {
		return nil, errors.New("impl/agent/acp: agent spec builder is required")
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
			Sessions:  cfg.Sessions,
			Projector: eventProjector,
			AppName:   appName,
			UserID:    userID,
			Modes:     cfg.Modes,
			Config:    cfg.Config,
		})}
	}
	return &RuntimeAgent{
		runtime:               cfg.Runtime,
		sessions:              cfg.Sessions,
		buildAgentSpec:        cfg.BuildAgentSpec,
		projector:             eventProjector,
		loader:                sessionLoader,
		modes:                 cfg.Modes,
		config:                cfg.Config,
		models:                cfg.Models,
		commands:              cfg.Commands,
		promptCaps:            cfg.PromptCaps,
		approvalReviewer:      cfg.ApprovalReviewer,
		approvalModelResolver: cfg.ApprovalModelResolver,
		appName:               appName,
		userID:                userID,
		agentInfo:             normalizeAgentInfo(cfg.AgentInfo, appName),
		cancels:               map[string]context.CancelFunc{},
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
	resp, err := a.loader.LoadSession(ctx, req, cb)
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

func (a *RuntimeAgent) Prompt(ctx context.Context, req acp.PromptRequest, cb acp.PromptCallbacks) (acp.PromptResponse, error) {
	activeSession, err := a.session(ctx, req.SessionID)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	spec, err := a.buildAgentSpec(ctx, activeSession, req)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	input, contentParts, err := promptContent(req.Prompt)
	if err != nil {
		return acp.PromptResponse{}, err
	}
	ref := session.SessionRef{
		AppName:   a.appName,
		UserID:    a.userID,
		SessionID: strings.TrimSpace(req.SessionID),
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.setCancel(req.SessionID, cancel)
	defer a.clearCancel(req.SessionID)
	defer cancel()

	result, err := a.runtime.Run(runCtx, agent.RunRequest{
		SessionRef:   ref,
		Input:        input,
		ContentParts: contentParts,
		Request:      agent.ModelRequestOptions{Stream: boolPtr(true)},
		ApprovalRequester: approvalRequester{
			callbacks:     cb,
			reviewer:      a.approvalReviewer,
			modelResolver: a.approvalModelResolver,
		},
		AgentSpec: spec,
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
		}
		return acp.PromptResponse{}, err
	}
	streamedAssistant := false
	var lastLive liveChunkFingerprint
	hasLastLive := false
	bridgedTerminals := map[string]struct{}{}
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
		if isLiveAssistantChunk(event) {
			streamedAssistant = true
		}
		if fingerprint, ok := liveChunkFingerprintForEvent(event); ok {
			if hasLastLive && fingerprint == lastLive {
				continue
			}
			lastLive = fingerprint
			hasLastLive = true
		} else {
			hasLastLive = false
			if streamedAssistant && isFinalAssistantMessage(event) {
				continue
			}
		}
		if err := a.emitEvent(runCtx, cb, event); err != nil {
			return acp.PromptResponse{}, err
		}
		a.maybeStartTerminalBridge(context.WithoutCancel(ctx), cb, event, bridgedTerminals)
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

func (a *RuntimeAgent) session(ctx context.Context, sessionID string) (session.Session, error) {
	return a.sessions.Session(ctx, session.SessionRef{
		AppName:   a.appName,
		UserID:    a.userID,
		SessionID: strings.TrimSpace(sessionID),
	})
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

func (a *RuntimeAgent) emitEvent(ctx context.Context, cb acp.PromptCallbacks, event *session.Event) error {
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

func (a *RuntimeAgent) maybeStartTerminalBridge(ctx context.Context, cb acp.PromptCallbacks, event *session.Event, active map[string]struct{}) {
	if cb == nil || event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
		return
	}
	call := event.Protocol.ToolCall
	if !terminalBridgeEligibleTool(call.Name) {
		return
	}
	ref, ok := terminal.RefFromEvent(event)
	if !ok {
		return
	}
	displayTerminalID := strings.TrimSpace(call.ID)
	if displayTerminalID == "" {
		return
	}
	key := strings.TrimSpace(event.SessionID) + "\x00" + displayTerminalID
	if _, exists := active[key]; exists {
		return
	}
	active[key] = struct{}{}
	go a.streamTerminalToACP(ctx, cb, strings.TrimSpace(event.SessionID), displayTerminalID, ref)
}

func (a *RuntimeAgent) streamTerminalToACP(ctx context.Context, cb acp.PromptCallbacks, sessionID string, displayTerminalID string, ref stream.Ref) {
	provider, ok := a.runtime.(agent.StreamProvider)
	if !ok || provider.Streams() == nil {
		return
	}
	for frame, err := range provider.Streams().Subscribe(ctx, stream.SubscribeRequest{Ref: ref}) {
		if err != nil {
			return
		}
		if frame == nil {
			continue
		}
		if frame.Text != "" {
			_ = cb.SessionUpdate(ctx, acp.SessionNotification{
				SessionID: sessionID,
				Update: acp.ToolCallUpdate{
					SessionUpdate: acp.UpdateToolCallInfo,
					ToolCallID:    displayTerminalID,
					Meta: map[string]any{
						"terminal_output": map[string]any{
							"terminal_id": displayTerminalID,
							"data":        frame.Text,
						},
					},
				},
			})
		}
		if frame.Closed {
			status := acp.ToolStatusCompleted
			if frame.ExitCode != nil && *frame.ExitCode != 0 {
				status = acp.ToolStatusFailed
			}
			meta := map[string]any{
				"terminal_id": displayTerminalID,
				"signal":      nil,
			}
			if frame.ExitCode != nil {
				meta["exit_code"] = *frame.ExitCode
			}
			_ = cb.SessionUpdate(ctx, acp.SessionNotification{
				SessionID: sessionID,
				Update: acp.ToolCallUpdate{
					SessionUpdate: acp.UpdateToolCallInfo,
					ToolCallID:    displayTerminalID,
					Status:        &status,
					Meta:          map[string]any{"terminal_exit": meta},
				},
			})
			return
		}
	}
}

func terminalBridgeEligibleTool(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "BASH", "SPAWN":
		return true
	default:
		return false
	}
}

func isLiveAssistantChunk(event *session.Event) bool {
	return event != nil &&
		event.Visibility == session.VisibilityUIOnly &&
		event.Protocol != nil &&
		strings.TrimSpace(event.Protocol.UpdateType) == string(session.ProtocolUpdateTypeAgentMessage)
}

func isFinalAssistantMessage(event *session.Event) bool {
	return event != nil &&
		event.Visibility != session.VisibilityUIOnly &&
		event.Protocol != nil &&
		strings.TrimSpace(event.Protocol.UpdateType) == string(session.ProtocolUpdateTypeAgentMessage) &&
		session.EventTypeOf(event) == session.EventTypeAssistant
}

type liveChunkFingerprint struct {
	updateType string
	text       string
}

func liveChunkFingerprintForEvent(event *session.Event) (liveChunkFingerprint, bool) {
	if event == nil || event.Visibility != session.VisibilityUIOnly || event.Protocol == nil {
		return liveChunkFingerprint{}, false
	}
	updateType := strings.TrimSpace(event.Protocol.UpdateType)
	var text string
	switch updateType {
	case string(session.ProtocolUpdateTypeAgentMessage):
		text = assistantTextForEvent(event)
	case string(session.ProtocolUpdateTypeAgentThought):
		text = thoughtTextForEvent(event)
	default:
		return liveChunkFingerprint{}, false
	}
	if text == "" {
		return liveChunkFingerprint{}, false
	}
	if len([]rune(strings.TrimSpace(text))) < 12 {
		return liveChunkFingerprint{}, false
	}
	return liveChunkFingerprint{updateType: updateType, text: text}, true
}

func assistantTextForEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Text != "" {
		return event.Text
	}
	if event.Message != nil {
		return event.Message.TextContent()
	}
	return ""
}

func thoughtTextForEvent(event *session.Event) string {
	if event == nil {
		return ""
	}
	if event.Text != "" {
		return event.Text
	}
	if event.Message == nil {
		return ""
	}
	if text := event.Message.ReasoningText(); text != "" {
		return text
	}
	return event.Message.TextContent()
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
			return "", nil, fmt.Errorf("impl/agent/acp: decode prompt content: %w", err)
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
				return "", nil, fmt.Errorf("impl/agent/acp: image prompt content requires inline data")
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

type approvalRequester struct {
	callbacks     acp.PromptCallbacks
	reviewer      approval.Reviewer
	modelResolver ApprovalModelResolver
}

func (r approvalRequester) RequestApproval(
	ctx context.Context,
	req agent.ApprovalRequest,
) (agent.ApprovalResponse, error) {
	if r.reviewer != nil && approval.NormalizeMode(req.Mode) != approval.ModeManual {
		return r.reviewApproval(ctx, req)
	}
	return r.requestClientPermission(ctx, req)
}

func (r approvalRequester) reviewApproval(ctx context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
	payload := approval.PayloadFromRuntimeRequest(req)
	var reviewModel model.LLM
	if r.modelResolver != nil {
		reviewModel, _ = r.modelResolver.ResolveApprovalModel(ctx, req.SessionRef)
	}
	result, err := r.reviewer.ReviewApproval(ctx, approval.ReviewRequest{
		SessionRef:     req.SessionRef,
		RunID:          strings.TrimSpace(req.RunID),
		TurnID:         strings.TrimSpace(req.TurnID),
		Mode:           approval.NormalizeMode(req.Mode),
		ReviewID:       approval.ReviewID("acp-approval-review", payload),
		Model:          reviewModel,
		Approval:       approval.ClonePayload(payload),
		RuntimeRequest: req,
	})
	if err != nil {
		rationale := "automatic approval review failed: " + err.Error()
		result = approval.ReviewResult{
			Approved:       false,
			Outcome:        string(approval.StatusRejected),
			Risk:           "unknown",
			Authorization:  "unknown",
			Rationale:      rationale,
			DisplayText:    approval.FormatReviewText(false, "unknown", "unknown", rationale),
			DecisionSource: string(approval.ModeAutoReview),
		}
	}
	if strings.TrimSpace(result.DisplayText) == "" {
		result.DisplayText = approval.FormatReviewText(result.Approved, result.Risk, result.Authorization, result.Rationale)
	}
	return approval.RuntimeResponseFromReview(payload, result), nil
}

func (r approvalRequester) requestClientPermission(
	ctx context.Context,
	req agent.ApprovalRequest,
) (agent.ApprovalResponse, error) {
	if r.callbacks == nil || req.Approval == nil {
		return agent.ApprovalResponse{}, nil
	}
	projector := projector.EventProjector{}
	event := &session.Event{
		SessionID: strings.TrimSpace(req.SessionRef.SessionID),
		Protocol: &session.EventProtocol{
			UpdateType: string(session.ProtocolUpdateTypePermission),
			Approval:   cloneProtocolApproval(req.Approval),
		},
	}
	request, ok, err := projector.ProjectPermissionRequest(event)
	if err != nil {
		return agent.ApprovalResponse{}, err
	}
	if !ok || request == nil {
		return agent.ApprovalResponse{}, nil
	}
	response, err := r.callbacks.RequestPermission(ctx, *request)
	if err != nil {
		return agent.ApprovalResponse{}, err
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
	return agent.ApprovalResponse{
		Outcome:  outcome,
		OptionID: optionID,
		Approved: approved,
	}, nil
}

func cloneProtocolApproval(in *session.ProtocolApproval) *session.ProtocolApproval {
	if in == nil {
		return nil
	}
	out := *in
	out.ToolCall.RawInput = maps.Clone(in.ToolCall.RawInput)
	if len(in.Options) > 0 {
		out.Options = append([]session.ProtocolApprovalOption(nil), in.Options...)
	}
	return &out
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
	return terminal.LocalTerminalAdapter{Streams: provider.Streams()}, true
}

var (
	_ acp.Agent           = (*RuntimeAgent)(nil)
	_ acp.TerminalAdapter = (*RuntimeAgent)(nil)
)
