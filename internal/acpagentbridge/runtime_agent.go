package acpagentbridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/loader"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/projector"
	acp "github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/google/uuid"
)

// BuildAgentSpecFunc assembles the runtime-facing agent spec for one ACP
// prompt.
type BuildAgentSpecFunc func(context.Context, session.Session, acp.PromptRequest) (agent.AgentSpec, error)

// ApprovalModelResolver resolves the model used by automatic approval review.
type ApprovalModelResolver = approval.ModelResolver

type PromptRouterFactory func(context.Context, session.Session) (controlprompt.Router, error)

// SlashResultFormatter projects a structured prompt result into the plain-text
// notice required by the ACP surface. Product assembly supplies the formatter
// so this integration package does not depend on presentation packages.
type SlashResultFormatter func(controlprompt.SlashCommandResult) string

// Config configures the lower-level ACP bridge used for protocol conformance.
// Product assembly uses GatewayAgentConfig and supplies typed AppServer clients
// instead of selecting the direct Runtime path.
type Config struct {
	Runtime  agent.Runtime
	Sessions session.Service
	// SessionClient is the principal-bound AppServer client used by product ACP
	// assembly. When present, Session lifecycle mutations and ordinary prompt
	// ingress stay on that client; Runtime and Sessions are inputs only to the
	// lower-level direct Runtime conformance path.
	SessionClient appserver.SessionClient
	// ConfigurationClient owns all product ACP Session configuration writes.
	// PresentationClient remains read-only.
	ConfigurationClient appserver.ConfigurationClient
	// PresentationClient is the remaining product ACP read facet. When
	// SessionClient is set, product assembly supplies the focused typed clients
	// and does not expose Runtime, Stack, or ACP surface providers.
	PresentationClient appserver.PresentationClient
	BuildAgentSpec     BuildAgentSpecFunc
	Projector          projector.Projector
	Loader             SessionLoader
	Modes              SessionModeReader
	ModeWriter         SessionModeWriter
	// ApprovalModes is the dedicated approval-routing mode source. Do not point
	// this at app-owned assembly modes; those are client-visible session modes,
	// while approval routing is restricted to manual/auto-review.
	ApprovalModes       SessionModeReader
	Config              SessionConfigReader
	ConfigWriter        SessionConfigWriter
	Commands            CommandProvider
	PromptRouterFactory PromptRouterFactory
	// SlashResultFormatter is required when PromptRouterFactory is configured.
	SlashResultFormatter SlashResultFormatter
	TaskStreamClient     taskstream.Client
	// TaskStreams and TaskStreamPrincipal are inputs to the lower-level direct
	// Runtime conformance path. Product assembly binds TaskStreamClient once at
	// the AppServer boundary instead of forwarding a selectable principal.
	TaskStreams           taskstream.Service
	TaskStreamPrincipal   taskstream.Principal
	ApprovalReviewer      approval.Reviewer
	ApprovalModelResolver ApprovalModelResolver
	AppName               string
	UserID                string
	WorkspaceKey          string
	// WorkspaceCWD pairs the product Host's stable Workspace key with its
	// canonical directory. ACP identifies a workspace by CWD, so typed Session
	// creation uses this pair to preserve the Host's registered identity.
	WorkspaceCWD string
	// ManagedSessionHistoryToken is a Host-issued process capability used only
	// by a short-lived product ACP bridge to load one managed child history.
	// It does not authorize prompt, resume, discovery, or lifecycle mutations.
	ManagedSessionHistoryToken string
	AgentInfo                  *acp.Implementation
}

// RuntimeAgent adapts Agent SDK runtime and session contracts into the standard
// ACP agent-side methods.
type RuntimeAgent struct {
	runtime               agent.Runtime
	sessions              session.Service
	sessionClient         appserver.SessionClient
	configurationClient   appserver.ConfigurationClient
	presentationClient    appserver.PresentationClient
	buildAgentSpec        BuildAgentSpecFunc
	projector             projector.Projector
	loader                SessionLoader
	modes                 SessionModeReader
	modeWriter            SessionModeWriter
	approvalModes         SessionModeReader
	config                SessionConfigReader
	configWriter          SessionConfigWriter
	commands              CommandProvider
	promptRouterFactory   PromptRouterFactory
	slashResultFormatter  SlashResultFormatter
	taskStreamClient      taskstream.Client
	approvalReviewer      approval.Reviewer
	approvalModelResolver ApprovalModelResolver
	appName               string
	userID                string
	workspaceKey          string
	workspaceCWD          string
	managedHistoryToken   string
	agentInfo             *acp.Implementation

	mu              sync.Mutex
	cancels         map[string]context.CancelFunc
	managedSessions map[string]struct{}
	taskMuxes       map[string]map[*acpTaskStreamMux]struct{}
}

// New constructs the lower-level ACP bridge in typed-client or direct Runtime
// conformance mode. Product code should use NewGatewayAgent.
func New(cfg Config) (*RuntimeAgent, error) {
	if cfg.SessionClient == nil && cfg.Sessions == nil {
		return nil, errors.New("internal/acpagentbridge: session service is required")
	}
	if cfg.SessionClient == nil {
		if cfg.Runtime == nil {
			return nil, errors.New("internal/acpagentbridge: runtime is required")
		}
		if cfg.BuildAgentSpec == nil {
			return nil, errors.New("internal/acpagentbridge: agent spec builder is required")
		}
	} else if cfg.PromptRouterFactory == nil {
		return nil, errors.New("internal/acpagentbridge: typed Session client mode requires a prompt router")
	} else if cfg.ConfigurationClient == nil || cfg.PresentationClient == nil {
		return nil, errors.New("internal/acpagentbridge: typed Session client mode requires configuration and presentation clients")
	}
	if cfg.PromptRouterFactory != nil && cfg.SlashResultFormatter == nil {
		return nil, errors.New("internal/acpagentbridge: slash result formatter is required with prompt router factory")
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
	if sessionLoader == nil && cfg.SessionClient == nil {
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
	taskStreamClient := cfg.TaskStreamClient
	if taskStreamClient == nil && cfg.TaskStreams != nil {
		var err error
		taskStreamClient, err = taskstream.BindClient(cfg.TaskStreams, cfg.TaskStreamPrincipal)
		if err != nil {
			return nil, fmt.Errorf("internal/acpagentbridge: bind Task stream client: %w", err)
		}
	}
	return &RuntimeAgent{
		runtime:               cfg.Runtime,
		sessions:              cfg.Sessions,
		sessionClient:         cfg.SessionClient,
		configurationClient:   cfg.ConfigurationClient,
		presentationClient:    cfg.PresentationClient,
		buildAgentSpec:        cfg.BuildAgentSpec,
		projector:             eventProjector,
		loader:                sessionLoader,
		modes:                 cfg.Modes,
		modeWriter:            cfg.ModeWriter,
		approvalModes:         approvalModes,
		config:                cfg.Config,
		configWriter:          cfg.ConfigWriter,
		commands:              cfg.Commands,
		promptRouterFactory:   cfg.PromptRouterFactory,
		slashResultFormatter:  cfg.SlashResultFormatter,
		taskStreamClient:      taskStreamClient,
		approvalReviewer:      cfg.ApprovalReviewer,
		approvalModelResolver: cfg.ApprovalModelResolver,
		appName:               appName,
		userID:                userID,
		workspaceKey:          strings.TrimSpace(cfg.WorkspaceKey),
		workspaceCWD:          strings.TrimSpace(cfg.WorkspaceCWD),
		managedHistoryToken:   strings.TrimSpace(cfg.ManagedSessionHistoryToken),
		agentInfo:             normalizeAgentInfo(cfg.AgentInfo, appName),
		cancels:               map[string]context.CancelFunc{},
		managedSessions:       map[string]struct{}{},
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
	if a.presentationClient != nil {
		caps, err := a.presentationClient.PresentationCapabilities(ctx)
		if err != nil {
			return acp.InitializeResponse{}, err
		}
		promptCaps = acp.PromptCapabilities{Audio: caps.Audio, EmbeddedContext: caps.EmbeddedContext, Image: caps.Image}
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
	if a.loader != nil || a.sessionClient != nil {
		caps.LoadSession = true
	}
	caps.SessionCapabilities["list"] = json.RawMessage(`{}`)
	caps.SessionCapabilities["resume"] = json.RawMessage(`{}`)
	caps.SessionCapabilities["close"] = json.RawMessage(`{}`)
	var meta map[string]json.RawMessage
	if a.sessionClient != nil {
		steering, err := json.Marshal(acp.SessionSteeringCapability{Supported: true})
		if err != nil {
			return acp.InitializeResponse{}, err
		}
		meta = map[string]json.RawMessage{acp.SessionSteeringMetaKey: steering}
	}
	return acp.InitializeResponse{
		ProtocolVersion:   acpsdk.ProtocolVersionNumber,
		AgentCapabilities: caps,
		AgentInfo:         a.agentInfo,
		AuthMethods:       []json.RawMessage{},
		Meta:              meta,
	}, nil
}

func (a *RuntimeAgent) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	metadata := normalizedACPSessionMetadata(req.Meta)
	if a.sessionClient != nil {
		created, err := a.sessionClient.CreateSession(ctx, appserver.CreateSessionRequest{
			WriteBase: appserver.WriteBase{
				OperationID: newACPSessionOperationID("create"),
			},
			WorkspaceKey: a.workspaceKeyForCWD(req.CWD),
			CWD:          strings.TrimSpace(req.CWD),
			Metadata:     metadata,
		})
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		if created.Outcome != appserver.OutcomeCommitted && created.Outcome != appserver.OutcomeAccepted {
			return acp.NewSessionResponse{}, fmt.Errorf(
				"internal/acpagentbridge: create Session operation %q ended with outcome %q",
				created.OperationID,
				created.Outcome,
			)
		}
		activeSession, err := a.session(ctx, created.SessionID)
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		return a.newSessionResponse(ctx, activeSession)
	}
	activeSession, err := a.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: a.appName,
		UserID:  a.userID,
		Workspace: session.WorkspaceRef{
			Key: strings.TrimSpace(req.CWD),
			CWD: strings.TrimSpace(req.CWD),
		},
		Metadata: metadata,
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
	return a.newSessionResponse(ctx, activeSession)
}

func (a *RuntimeAgent) newSessionResponse(ctx context.Context, activeSession session.Session) (acp.NewSessionResponse, error) {
	a.rememberManagedSession(activeSession)
	resp := acp.NewSessionResponse{SessionID: activeSession.SessionID}
	if a.presentationClient != nil {
		snapshot, err := a.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: activeSession.SessionID})
		if err != nil {
			return acp.NewSessionResponse{}, err
		}
		resp.Modes, resp.ConfigOptions, _ = acpPresentationSnapshot(snapshot)
		return resp, nil
	}
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
	return resp, nil
}

func (a *RuntimeAgent) ListSessions(ctx context.Context, req acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	cwd := optionalString(req.Cwd)
	cursor := optionalString(req.Cursor)
	var list session.SessionList
	var err error
	if a.sessionClient != nil {
		list, err = a.sessionClient.ListSessions(ctx, appserver.ListSessionsRequest{
			WorkspaceKey: a.workspaceKeyForCWD(cwd),
			Cursor:       cursor,
		})
	} else {
		list, err = a.sessions.ListSessions(ctx, session.ListSessionsRequest{
			AppName:      a.appName,
			UserID:       a.userID,
			WorkspaceKey: cwd,
			Cursor:       cursor,
		})
	}
	if err != nil {
		return acpsdk.ListSessionsResponse{}, err
	}
	resp := acpsdk.ListSessionsResponse{
		Sessions:   make([]acpsdk.SessionInfo, 0, len(list.Sessions)),
		NextCursor: stringPtr(list.NextCursor),
	}
	for _, stored := range list.Sessions {
		if sessionvisibility.IsSystemManagedSummary(stored) {
			continue
		}
		summary := acpsdk.SessionInfo{
			SessionId: acpsdk.SessionId(strings.TrimSpace(stored.SessionID)),
			Cwd:       strings.TrimSpace(stored.CWD),
			Title:     stringPtr(stored.Title),
		}
		if !stored.UpdatedAt.IsZero() {
			summary.UpdatedAt = stringPtr(stored.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"))
		}
		resp.Sessions = append(resp.Sessions, summary)
	}
	return resp, nil
}

func (a *RuntimeAgent) workspaceKeyForCWD(cwd string) string {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" || a.workspaceKey == "" || a.workspaceCWD == "" {
		return cwd
	}
	if canonicalACPWorkspacePath(cwd) == canonicalACPWorkspacePath(a.workspaceCWD) {
		return a.workspaceKey
	}
	return cwd
}

func canonicalACPWorkspacePath(path string) string {
	absolute, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return filepath.Clean(strings.TrimSpace(path))
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		absolute = resolved
	}
	return filepath.Clean(absolute)
}

func (a *RuntimeAgent) LoadSession(ctx context.Context, req acp.LoadSessionRequest, cb PromptCallbacks) (acp.LoadSessionResponse, error) {
	if a.loader == nil && a.sessionClient == nil {
		return acp.LoadSessionResponse{}, ErrCapabilityUnsupported
	}
	activeSession, err := a.session(ctx, req.SessionID)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if !a.authorizeManagedSessionLoad(activeSession, req.Meta) {
		return acp.LoadSessionResponse{}, session.ErrSessionNotFound
	}
	loadCallbacks := cb
	if cb != nil {
		loadCallbacks = normalizingPromptCallbacks{inner: cb}
	}
	var resp acp.LoadSessionResponse
	if a.sessionClient != nil {
		resp, err = a.loadSessionFromClient(ctx, req, loadCallbacks)
	} else {
		resp, err = a.loader.LoadSession(ctx, req, loadCallbacks)
	}
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if a.presentationClient != nil {
		snapshot, err := a.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: req.SessionID})
		if err != nil {
			return acp.LoadSessionResponse{}, err
		}
		resp.Modes, resp.ConfigOptions, _ = acpPresentationSnapshot(snapshot)
	}
	return resp, nil
}

func (a *RuntimeAgent) ResumeSession(ctx context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	activeSession, err := a.session(ctx, req.SessionID)
	if err != nil {
		return acp.ResumeSessionResponse{}, err
	}
	managedSession := sessionvisibility.IsSystemManagedSession(activeSession)
	if !a.authorizeManagedSessionResume(activeSession, req.Meta) {
		return acp.ResumeSessionResponse{}, session.ErrSessionNotFound
	}
	claimManagedSession := managedSession && !a.ownsManagedSession(req.SessionID)
	resp := acp.ResumeSessionResponse{}
	if a.presentationClient != nil {
		snapshot, err := a.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: req.SessionID})
		if err != nil {
			return acp.ResumeSessionResponse{}, err
		}
		resp.Modes, resp.ConfigOptions, _ = acpPresentationSnapshot(snapshot)
		if claimManagedSession {
			a.rememberManagedSession(activeSession)
		}
		return resp, nil
	}
	if a.modes != nil {
		modes, err := a.modes.SessionModes(ctx, activeSession)
		if err != nil {
			return acp.ResumeSessionResponse{}, err
		}
		resp.Modes = modes
	}
	if a.config != nil {
		options, err := a.config.SessionConfigOptions(ctx, activeSession)
		if err != nil {
			return acp.ResumeSessionResponse{}, err
		}
		resp.ConfigOptions = options
	}
	if claimManagedSession {
		a.rememberManagedSession(activeSession)
	}
	return resp, nil
}

func (a *RuntimeAgent) CloseSession(ctx context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	activeSession, err := a.targetSession(ctx, req.SessionID)
	if err != nil {
		return acp.CloseSessionResponse{}, err
	}
	if a.sessionClient != nil {
		sessionID := strings.TrimSpace(req.SessionID)
		result, err := a.sessionClient.CloseSession(ctx, appserver.CloseSessionRequest{WriteBase: appserver.WriteBase{
			OperationID:             newACPSessionOperationID("close"),
			SessionID:               sessionID,
			ExpectedRevision:        &activeSession.Revision,
			ExpectedControllerEpoch: strings.TrimSpace(activeSession.Controller.EpochID),
		}})
		if err != nil {
			return acp.CloseSessionResponse{}, err
		}
		if result.Outcome != appserver.OutcomeCommitted && result.Outcome != appserver.OutcomeAccepted {
			return acp.CloseSessionResponse{}, fmt.Errorf(
				"internal/acpagentbridge: close Session operation %q ended with outcome %q",
				result.OperationID,
				result.Outcome,
			)
		}
		a.clearSessionDelivery(sessionID)
		return acp.CloseSessionResponse{}, nil
	}
	a.cancelSession(req.SessionID)
	a.clearSessionDelivery(req.SessionID)
	return acp.CloseSessionResponse{}, nil
}

func (a *RuntimeAgent) clearSessionDelivery(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	delete(a.cancels, sessionID)
	a.mu.Unlock()
	a.closeACPTaskStreamMuxes(sessionID)
}

func (a *RuntimeAgent) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	if a.configurationClient != nil {
		active, err := a.targetSession(ctx, req.SessionID)
		if err != nil {
			return acp.SetSessionModeResponse{}, err
		}
		snapshot, err := a.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: active.SessionID})
		if err != nil {
			return acp.SetSessionModeResponse{}, err
		}
		if snapshot.Modes == nil {
			return acp.SetSessionModeResponse{}, ErrCapabilityUnsupported
		}
		base := acpSessionConfigurationWriteBase(active, "mode")
		var result appserver.CommandResult
		switch strings.TrimSpace(snapshot.Modes.Target) {
		case appserver.PresentationModeTargetApp:
			result, err = a.configurationClient.ConfigureSessionPresentationMode(ctx, appserver.SessionPresentationModeRequest{
				WriteBase: base,
				Mode:      strings.TrimSpace(req.ModeID),
			})
		case appserver.PresentationModeTargetController:
			result, err = a.configurationClient.ConfigureSessionControllerMode(ctx, appserver.SessionControllerModeRequest{
				WriteBase: base,
				Mode:      strings.TrimSpace(req.ModeID),
			})
		default:
			return acp.SetSessionModeResponse{}, ErrCapabilityUnsupported
		}
		return acp.SetSessionModeResponse{}, requireCommittedACPConfiguration("set mode", result, err)
	}
	if a.modeWriter == nil {
		return acp.SetSessionModeResponse{}, ErrCapabilityUnsupported
	}
	if _, err := a.targetSession(ctx, req.SessionID); err != nil {
		return acp.SetSessionModeResponse{}, err
	}
	return a.modeWriter.SetSessionMode(ctx, req)
}

func (a *RuntimeAgent) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if a.configurationClient != nil {
		active, err := a.targetSession(ctx, req.SessionID)
		if err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		value, ok := req.Value.(string)
		if !ok {
			return acp.SetSessionConfigOptionResponse{}, fmt.Errorf("internal/acpagentbridge: config value for %q must be a string", strings.TrimSpace(req.ConfigID))
		}
		base := acpSessionConfigurationWriteBase(active, "config")
		var result appserver.CommandResult
		switch strings.ToLower(strings.TrimSpace(req.ConfigID)) {
		case "mode":
			snapshot, snapshotErr := a.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: active.SessionID})
			if snapshotErr != nil {
				return acp.SetSessionConfigOptionResponse{}, snapshotErr
			}
			if snapshot.Modes == nil {
				return acp.SetSessionConfigOptionResponse{}, ErrCapabilityUnsupported
			}
			if strings.TrimSpace(snapshot.Modes.Target) == appserver.PresentationModeTargetApp {
				result, err = a.configurationClient.ConfigureSessionPresentationMode(ctx, appserver.SessionPresentationModeRequest{WriteBase: base, Mode: strings.TrimSpace(value)})
			} else if strings.TrimSpace(snapshot.Modes.Target) == appserver.PresentationModeTargetController {
				result, err = a.configurationClient.ConfigureSessionControllerMode(ctx, appserver.SessionControllerModeRequest{WriteBase: base, Mode: strings.TrimSpace(value)})
			} else if strings.TrimSpace(snapshot.Modes.Target) == appserver.PresentationModeTargetApproval {
				result, err = a.configurationClient.ConfigureSessionMode(ctx, appserver.SessionModeRequest{WriteBase: base, Mode: strings.TrimSpace(value)})
			} else {
				return acp.SetSessionConfigOptionResponse{}, ErrCapabilityUnsupported
			}
		case "model":
			result, err = a.configurationClient.UseSessionModel(ctx, appserver.SessionModelRequest{
				WriteBase: base,
				Model:     strings.TrimSpace(value),
			})
		case "reasoning_effort":
			snapshot, snapshotErr := a.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: active.SessionID})
			if snapshotErr != nil {
				return acp.SetSessionConfigOptionResponse{}, snapshotErr
			}
			if snapshot.Models == nil || strings.TrimSpace(snapshot.Models.CurrentModelID) == "" {
				return acp.SetSessionConfigOptionResponse{}, errors.New("internal/acpagentbridge: current Session model is unavailable")
			}
			result, err = a.configurationClient.UseSessionModel(ctx, appserver.SessionModelRequest{
				WriteBase:       base,
				Model:           strings.TrimSpace(snapshot.Models.CurrentModelID),
				ReasoningEffort: strings.TrimSpace(value),
			})
		default:
			result, err = a.configurationClient.ConfigureSessionPresentation(ctx, appserver.SessionPresentationConfigRequest{
				WriteBase: base,
				ConfigID:  strings.TrimSpace(req.ConfigID),
				Value:     strings.TrimSpace(value),
			})
		}
		if err := requireCommittedACPConfiguration("set config option", result, err); err != nil {
			return acp.SetSessionConfigOptionResponse{}, err
		}
		snapshot, err := a.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: active.SessionID})
		if err != nil {
			return acp.SetSessionConfigOptionResponse{}, &appserver.CommandReceiptError{
				Receipt: result,
				Err:     fmt.Errorf("internal/acpagentbridge: configuration committed but presentation observation failed; do not retry blindly: %w", err),
			}
		}
		return acp.SetSessionConfigOptionResponse{ConfigOptions: acpPresentationConfigOptions(snapshot.ConfigOptions)}, nil
	}
	if a.configWriter == nil {
		return acp.SetSessionConfigOptionResponse{}, ErrCapabilityUnsupported
	}
	if _, err := a.targetSession(ctx, req.SessionID); err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	return a.configWriter.SetSessionConfigOption(ctx, req)
}

func acpSessionConfigurationWriteBase(active session.Session, action string) appserver.WriteBase {
	revision := active.Revision
	return appserver.WriteBase{
		OperationID:             newACPSessionOperationID(action),
		SessionID:               strings.TrimSpace(active.SessionID),
		ExpectedRevision:        &revision,
		ExpectedControllerEpoch: strings.TrimSpace(active.Controller.EpochID),
	}
}

func requireCommittedACPConfiguration(action string, result appserver.CommandResult, err error) error {
	if result.Outcome == appserver.OutcomeCommitted && err == nil {
		return nil
	}
	if err == nil {
		err = fmt.Errorf(
			"internal/acpagentbridge: %s operation %q returned %q: %s",
			strings.TrimSpace(action),
			strings.TrimSpace(result.OperationID),
			result.Outcome,
			strings.TrimSpace(result.Detail),
		)
	}
	return &appserver.CommandReceiptError{Receipt: result, Err: err}
}

func (a *RuntimeAgent) AvailableCommands(ctx context.Context, sessionID string) ([]acpsdk.AvailableCommand, error) {
	if a.presentationClient != nil {
		if _, err := a.targetSession(ctx, sessionID); err != nil {
			return nil, err
		}
		snapshot, err := a.presentationClient.PresentationSnapshot(ctx, appserver.PresentationRequest{SessionID: sessionID})
		if err != nil {
			return nil, err
		}
		_, _, commands := acpPresentationSnapshot(snapshot)
		return commands, nil
	}
	if a.commands == nil {
		return nil, nil
	}
	if _, err := a.targetSession(ctx, sessionID); err != nil {
		return nil, err
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

func (a *RuntimeAgent) Prompt(ctx context.Context, req acp.PromptRequest, cb PromptCallbacks) (acp.PromptResponse, error) {
	activeSession, err := a.targetSession(ctx, req.SessionID)
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
	if a.sessionClient != nil {
		return acp.PromptResponse{}, errors.New("internal/acpagentbridge: typed Session client prompt was not handled by the shared router")
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

func (a *RuntimeAgent) emitRunEvents(runCtx context.Context, _ context.Context, cb PromptCallbacks, ref session.SessionRef, handle agent.Runner, suppressUserEcho bool) error {
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
			base := projector.EnvelopeBaseFromSessionEvent(ref, item.event, projector.SessionEventTransport{})
			published := item.canonicalContentAlreadyPublished
			if taskMux == nil {
				// The direct Runtime conformance bridge may be assembled without a
				// Task-stream client. In that profile the canonical final remains the
				// only deliverable terminal-content source.
				published &^= agent.PublishedTerminal
			}
			projected := projector.ProjectSessionEventEnvelopeWithProjector(base, item.event, a.projector)
			if published != 0 {
				projected = projector.ProjectSessionEventLiveSupplementEnvelopeWithProjector(
					base,
					item.event,
					a.projector,
					published,
				)
			}
			for _, envelope := range projected {
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
	event                            *session.Event
	canonicalContentAlreadyPublished agent.PublishedContent
	err                              error
}

func runtimeRunnerEvents(ctx context.Context, handle agent.Runner) <-chan runtimeRunnerEvent {
	events := make(chan runtimeRunnerEvent)
	go func() {
		defer close(events)
		for sourceEvent, err := range runtime.SourceEvents(handle) {
			select {
			case <-ctx.Done():
				return
			case events <- runtimeRunnerEvent{
				event:                            sourceEvent.Canonical,
				canonicalContentAlreadyPublished: sourceEvent.CanonicalContentAlreadyPublished,
				err:                              err,
			}:
			}
		}
	}()
	return events
}

func (a *RuntimeAgent) Cancel(_ context.Context, req acpsdk.CancelNotification) error {
	a.cancelSession(string(req.SessionId))
	return nil
}

func (a *RuntimeAgent) cancelSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	cancel := a.cancels[sessionID]
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *RuntimeAgent) session(ctx context.Context, sessionID string) (session.Session, error) {
	if a.sessionClient != nil {
		state, err := a.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: strings.TrimSpace(sessionID)})
		if err != nil {
			return session.Session{}, err
		}
		return session.Session{
			SessionRef: session.NormalizeSessionRef(session.SessionRef{
				AppName: a.appName, UserID: a.userID, WorkspaceKey: state.WorkspaceKey, SessionID: state.SessionID,
			}),
			Revision: state.Revision, CWD: state.CWD, Title: state.Title,
			Metadata: state.Metadata, Controller: state.Controller, Participants: state.Participants,
		}, nil
	}
	return a.sessions.Session(ctx, a.sessionRef(sessionID))
}

// targetSession authorizes one exact ACP operation target. System-managed
// Sessions are addressable only by the bridge instance that created them;
// user lifecycle methods apply their stricter hide-all policy separately.
func (a *RuntimeAgent) targetSession(ctx context.Context, sessionID string) (session.Session, error) {
	activeSession, err := a.session(ctx, sessionID)
	if err != nil {
		return session.Session{}, err
	}
	if sessionvisibility.IsSystemManagedSession(activeSession) && !a.ownsManagedSession(sessionID) {
		return session.Session{}, session.ErrSessionNotFound
	}
	return activeSession, nil
}

func (a *RuntimeAgent) sessionRef(sessionID string) session.SessionRef {
	return session.NormalizeSessionRef(session.SessionRef{
		AppName:   a.appName,
		UserID:    a.userID,
		SessionID: strings.TrimSpace(sessionID),
	})
}

func (a *RuntimeAgent) rememberManagedSession(activeSession session.Session) {
	if a == nil || !sessionvisibility.IsSystemManagedSession(activeSession) {
		return
	}
	sessionID := strings.TrimSpace(activeSession.SessionID)
	if sessionID == "" {
		return
	}
	a.mu.Lock()
	a.managedSessions[sessionID] = struct{}{}
	a.mu.Unlock()
}

func (a *RuntimeAgent) ownsManagedSession(sessionID string) bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	_, ok := a.managedSessions[strings.TrimSpace(sessionID)]
	a.mu.Unlock()
	return ok
}

// authorizeManagedSessionTarget keeps ACP request metadata out of durable
// ownership decisions. Product ACP keeps every system-managed Session hidden
// from user lifecycle load/resume even though its principal-bound client can
// inspect the exact target. Only the lower-level direct conformance path may
// access a managed Session, and only when this bridge instance created it.
func (a *RuntimeAgent) authorizeManagedSessionTarget(activeSession session.Session) bool {
	if !sessionvisibility.IsSystemManagedSession(activeSession) {
		return true
	}
	return a.sessionClient == nil && a.ownsManagedSession(activeSession.SessionID)
}

// authorizeManagedSessionResume keeps the read-only history capability and the
// execution reconnect capability disjoint. A normal Host-owned child bridge
// may reclaim its exact durable parent/Task relation after its ACP process is
// rebuilt. The short-lived history bridge carries a non-empty process token and
// is therefore never allowed to resume, prompt, or acquire execution ownership.
func (a *RuntimeAgent) authorizeManagedSessionResume(activeSession session.Session, meta map[string]any) bool {
	if !sessionvisibility.IsSystemManagedSession(activeSession) {
		return true
	}
	if a.sessionClient == nil {
		return a.ownsManagedSession(activeSession.SessionID)
	}
	return strings.TrimSpace(a.managedHistoryToken) == "" &&
		matchesManagedSubagentRelationClaim(activeSession, meta)
}

// authorizeManagedSessionLoad permits a short-lived product ACP bridge to
// replay one exact managed child without acquiring execution ownership.
// Principal authorization has already happened on the bound AppServer client;
// the Host-issued process capability distinguishes this internal read from an
// ordinary ACP Surface, while the relation claim fences it to one durable
// parent/Task. Direct Runtime bridges still require process-local ownership.
func (a *RuntimeAgent) authorizeManagedSessionLoad(activeSession session.Session, meta map[string]any) bool {
	if !sessionvisibility.IsSystemManagedSession(activeSession) {
		return true
	}
	if a.sessionClient == nil {
		return a.ownsManagedSession(activeSession.SessionID)
	}
	return matchesManagedSubagentHistoryCapability(a.managedHistoryToken, meta) &&
		matchesManagedSubagentRelationClaim(activeSession, meta)
}

func matchesManagedSubagentHistoryCapability(configured string, meta map[string]any) bool {
	configured = strings.TrimSpace(configured)
	provided := metautil.String(
		meta,
		metautil.Root,
		metautil.Runtime,
		metautil.RuntimeSession,
		metautil.RuntimeSessionHistoryToken,
	)
	// Tokens are 32 random bytes encoded as 64 hexadecimal characters. Require
	// the exact shape before comparing so malformed metadata fails closed.
	if len(configured) != 64 || len(provided) != len(configured) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(configured)) == 1
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

func optionalString(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func newACPSessionOperationID(action string) string {
	action = strings.TrimSpace(action)
	if action == "" {
		action = "session"
	}
	return "acp-" + action + "-" + uuid.NewString()
}

func (a *RuntimeAgent) loadSessionFromClient(
	ctx context.Context,
	req acp.LoadSessionRequest,
	cb PromptCallbacks,
) (acp.LoadSessionResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	reconnected, err := a.sessionClient.Reconnect(ctx, appserver.ReconnectRequest{SessionID: sessionID})
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	if reconnected.Subscription == nil {
		return acp.LoadSessionResponse{}, errors.New("internal/acpagentbridge: Session reconnect returned no subscription")
	}
	defer reconnected.Subscription.Close()
	if cb != nil {
		filter := newACPNarrativeFilter(false)
		for envelope := range reconnected.Subscription.Backfill() {
			if err := a.emitControlBackfillEnvelope(ctx, cb, sessionID, envelope, filter); err != nil {
				return acp.LoadSessionResponse{}, err
			}
		}
	}
	if err := reconnected.Subscription.Err(); err != nil {
		return acp.LoadSessionResponse{}, err
	}
	activeSession, err := a.session(ctx, sessionID)
	if err != nil {
		return acp.LoadSessionResponse{}, err
	}
	resp := acp.LoadSessionResponse{}
	if a.modes != nil {
		resp.Modes, err = a.modes.SessionModes(ctx, activeSession)
		if err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	if a.config != nil {
		resp.ConfigOptions, err = a.config.SessionConfigOptions(ctx, activeSession)
		if err != nil {
			return acp.LoadSessionResponse{}, err
		}
	}
	return resp, nil
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
	cb PromptCallbacks,
) (acp.LoadSessionResponse, error) {
	if l.inner == nil {
		return acp.LoadSessionResponse{}, ErrCapabilityUnsupported
	}
	return l.inner.LoadSession(ctx, req, cb)
}
