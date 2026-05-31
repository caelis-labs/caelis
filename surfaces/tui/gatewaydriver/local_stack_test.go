package gatewaydriver

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"

	coreconfig "github.com/OnslaughtSnail/caelis/core/config"
	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	acpexternal "github.com/OnslaughtSnail/caelis/internal/adapters/acpagent/external"
	applocal "github.com/OnslaughtSnail/caelis/internal/app/local"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
)

type gatewayDriverTestACPAgent = acpexternal.Config

type gatewayDriverTestConfig struct {
	AppName           string
	UserID            string
	StoreDir          string
	WorkspaceKey      string
	WorkspaceCWD      string
	PermissionMode    string
	ContextWindow     int
	SystemPrompt      string
	ExternalACPAgents []gatewayDriverTestACPAgent
	Model             ModelConfig
	Sandbox           gatewayDriverTestSandboxConfig
}

type gatewayDriverTestSandboxConfig struct {
	RequestedType string
	HelperPath    string
}

type gatewayDriverTestStack struct {
	local       *applocal.Stack
	services    appservices.Services
	driverStack *DriverStack
	settings    *appsettings.Manager
	storeDir    string

	Sessions  gatewayDriverTestSessionService
	AppName   string
	UserID    string
	Workspace coresession.Workspace

	mu       sync.Mutex
	bindings map[string]coresession.Ref
}

func newGatewayDriverFromTestStack(ctx context.Context, stack *gatewayDriverTestStack, preferredSessionID string, bindingKey string, modelText string) (*GatewayDriver, error) {
	if stack == nil {
		return nil, fmt.Errorf("gatewaydriver test stack is nil")
	}
	return NewGatewayDriver(ctx, stack.driverStack, preferredSessionID, bindingKey, modelText)
}

func gatewayDriverTestRuntimeStack(stack *gatewayDriverTestStack) *DriverStack {
	if stack == nil {
		return nil
	}
	return stack.driverStack
}

func newGatewayDriverTestStack(t *testing.T, cfg gatewayDriverTestConfig) (*gatewayDriverTestStack, error) {
	t.Helper()
	ctx := context.Background()
	if strings.TrimSpace(cfg.Sandbox.RequestedType) == "" {
		cfg.Sandbox.RequestedType = "host"
	}
	storeDir := strings.TrimSpace(cfg.StoreDir)
	if storeDir == "" {
		storeDir = t.TempDir()
	}
	workspaceCWD := firstNonEmpty(strings.TrimSpace(cfg.WorkspaceCWD), t.TempDir())
	workspaceKey := firstNonEmpty(strings.TrimSpace(cfg.WorkspaceKey), workspaceCWD)
	appName := firstNonEmpty(strings.TrimSpace(cfg.AppName), "caelis")
	userID := firstNonEmpty(strings.TrimSpace(cfg.UserID), "driver-test")
	runtimeCfg := coreconfig.Runtime{
		AppName:      appName,
		UserID:       userID,
		WorkspaceKey: workspaceKey,
		WorkspaceCWD: workspaceCWD,
		Model:        firstNonEmpty(cfg.Model.Alias, cfg.Model.ID),
		Store: coreconfig.Store{
			Backend: "memory",
			URI:     storeDir,
		},
		Sandbox: coreconfig.Sandbox{
			Backend:    cfg.Sandbox.RequestedType,
			HelperPath: cfg.Sandbox.HelperPath,
		},
		Meta: map[string]any{},
	}
	if permissionMode := strings.TrimSpace(cfg.PermissionMode); permissionMode != "" {
		runtimeCfg.Meta["permission_mode"] = permissionMode
	}
	settingsStore := appsettings.NewFileStore(storeDir)
	settingsDoc := appsettings.Document{Runtime: runtimeCfg}
	if strings.TrimSpace(cfg.Model.Provider) != "" || strings.TrimSpace(cfg.Model.Model) != "" {
		modelCfg := modelConfigToApp(cfg.Model)
		if cfg.ContextWindow > 0 && modelCfg.ContextWindowTokens <= 0 {
			modelCfg.ContextWindowTokens = cfg.ContextWindow
		}
		settingsDoc.Models.Configs = []appsettings.ModelConfig{modelCfg}
	}
	manager, err := appsettings.NewManager(ctx, settingsStore, settingsDoc)
	if err != nil {
		return nil, err
	}
	local, err := applocal.NewWithContext(ctx, applocal.Config{
		Runtime:           runtimeCfg,
		Settings:          manager,
		Provider:          gatewayDriverTestProvider{},
		ExternalACPAgents: append([]acpexternal.Config(nil), cfg.ExternalACPAgents...),
		SystemPrompt:      cfg.SystemPrompt,
	})
	if err != nil {
		return nil, err
	}
	out := &gatewayDriverTestStack{
		local:     local,
		services:  local.Services(),
		settings:  manager,
		storeDir:  storeDir,
		Sessions:  gatewayDriverTestSessionService{},
		AppName:   appName,
		UserID:    userID,
		Workspace: coresession.Workspace{Key: workspaceKey, CWD: workspaceCWD},
		bindings:  map[string]coresession.Ref{},
	}
	out.Sessions.stack = out
	driverStack := BindAppServices(&DriverStack{}, local.Services())
	baseStart := driverStack.StartSessionFn
	driverStack.StartSessionFn = func(ctx context.Context, preferredSessionID string, bindingKey string) (coresession.Session, error) {
		active, err := baseStart(ctx, preferredSessionID, bindingKey)
		if err == nil {
			out.remember(bindingKey, active.Ref)
		}
		return active, err
	}
	out.driverStack = driverStack
	return out, nil
}

func (s *gatewayDriverTestStack) StartSession(ctx context.Context, preferredSessionID string, bindingKey string) (coresession.Session, error) {
	return s.StartSessionWithTitle(ctx, preferredSessionID, bindingKey, "")
}

func (s *gatewayDriverTestStack) StartSessionWithTitle(ctx context.Context, preferredSessionID string, bindingKey string, title string) (coresession.Session, error) {
	if s == nil {
		return coresession.Session{}, fmt.Errorf("gatewaydriver test stack is unavailable")
	}
	active, err := s.services.Sessions().Start(ctx, appservices.StartSessionRequest{
		Workspace:          s.Workspace,
		PreferredSessionID: preferredSessionID,
		Title:              title,
	})
	if err != nil {
		return coresession.Session{}, err
	}
	s.remember(bindingKey, active.Ref)
	return active, nil
}

func (s *gatewayDriverTestStack) ListSessions(ctx context.Context, workspaceKey string, limit int) (coresession.SessionPage, error) {
	if s == nil {
		return coresession.SessionPage{}, fmt.Errorf("gatewaydriver test stack is unavailable")
	}
	workspace := coresession.Workspace{Key: strings.TrimSpace(workspaceKey)}
	return s.services.Sessions().List(ctx, appservices.ListSessionsRequest{
		Workspace:     workspace,
		AllWorkspaces: workspace.Key == "",
		Limit:         limit,
	})
}

func (s *gatewayDriverTestStack) CurrentSession(bindingKey string) (coresession.Ref, bool) {
	if s == nil {
		return coresession.Ref{}, false
	}
	key := firstNonEmpty(strings.TrimSpace(bindingKey), "cli-tui")
	s.mu.Lock()
	defer s.mu.Unlock()
	ref, ok := s.bindings[key]
	return ref, ok
}

func (s *gatewayDriverTestStack) remember(bindingKey string, ref coresession.Ref) {
	if s == nil {
		return
	}
	key := firstNonEmpty(strings.TrimSpace(bindingKey), "cli-tui")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings == nil {
		s.bindings = map[string]coresession.Ref{}
	}
	s.bindings[key] = coresession.NormalizeRef(ref)
}

func (s *gatewayDriverTestStack) SetSandboxBackend(ctx context.Context, backend string) (SandboxStatus, error) {
	if s == nil || s.driverStack == nil {
		return SandboxStatus{}, fmt.Errorf("gatewaydriver test stack is unavailable")
	}
	return s.driverStack.SetSandboxBackend(ctx, backend)
}

func (s *gatewayDriverTestStack) Connect(cfg ModelConfig) (string, error) {
	if s == nil {
		return "", fmt.Errorf("gatewaydriver test stack is unavailable")
	}
	appCfg, err := s.services.Models().PrepareConnectConfig(context.Background(), modelConfigToApp(cfg))
	if err != nil {
		return "", err
	}
	connected, err := s.services.Models().Connect(context.Background(), appCfg)
	if err != nil {
		return "", err
	}
	return firstNonEmpty(connected.Alias, connected.ID), nil
}

func (s *gatewayDriverTestStack) ModelConfig(ref string) (ModelConfig, bool) {
	if s == nil || s.driverStack == nil {
		return ModelConfig{}, false
	}
	return s.driverStack.ModelConfig(ref)
}

func loadGatewayDriverTestSettings(root string) (appsettings.Document, error) {
	store := appsettings.NewFileStore(root)
	if store == nil {
		return appsettings.Document{}, nil
	}
	return store.Load(context.Background())
}

type gatewayDriverTestSessionService struct {
	stack *gatewayDriverTestStack
}

func (s gatewayDriverTestSessionService) AppendEvent(ctx context.Context, ref coresession.Ref, event coresession.Event) (*coresession.Event, error) {
	if s.stack == nil {
		return nil, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	ref = coresession.NormalizeRef(ref)
	if strings.TrimSpace(event.SessionID) == "" {
		event.SessionID = ref.SessionID
	}
	if event.Visibility == "" {
		event.Visibility = coresession.VisibilityCanonical
	}
	if _, err := s.stack.services.Engine().RecordEvents(ctx, ref, []coresession.Event{event}); err != nil {
		return nil, err
	}
	return &event, nil
}

func (s gatewayDriverTestSessionService) SnapshotState(ctx context.Context, ref coresession.Ref) (map[string]any, error) {
	snapshot, err := s.load(ctx, ref)
	if err != nil {
		return nil, err
	}
	return maps.Clone(snapshot.State), nil
}

func (s gatewayDriverTestSessionService) UpdateState(ctx context.Context, ref coresession.Ref, patch func(map[string]any) (map[string]any, error)) error {
	if s.stack == nil {
		return fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	return s.stack.services.Engine().UpdateSessionState(ctx, coresession.NormalizeRef(ref), func(state coresession.State) (coresession.State, error) {
		next, err := patch(maps.Clone(state))
		if err != nil {
			return nil, err
		}
		return coresession.State(next), nil
	})
}

func (s gatewayDriverTestSessionService) BindController(ctx context.Context, ref coresession.Ref, binding coresession.ControllerBinding) (coresession.Session, error) {
	if s.stack == nil {
		return coresession.Session{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	ref = coresession.NormalizeRef(ref)
	event := coresession.Event{
		SessionID:  ref.SessionID,
		Type:       coresession.EventHandoff,
		Visibility: coresession.VisibilityCanonical,
		Actor:      coresession.ActorRef{Kind: coresession.ActorSystem, ID: "gatewaydriver-test"},
		Scope:      &coresession.EventScope{Source: firstNonEmpty(binding.Source, "gatewaydriver-test"), Controller: binding},
		Meta:       map[string]any{"action": "handoff"},
	}
	if _, err := s.stack.services.Engine().RecordEvents(ctx, ref, []coresession.Event{event}); err != nil {
		return coresession.Session{}, err
	}
	snapshot, err := s.load(ctx, ref)
	if err != nil {
		return coresession.Session{}, err
	}
	snapshot.Session.Controller = controllerFromCoreSnapshot(snapshot)
	return snapshot.Session, nil
}

func (s gatewayDriverTestSessionService) PutParticipant(ctx context.Context, ref coresession.Ref, binding coresession.ParticipantBinding) (coresession.Session, error) {
	if s.stack == nil {
		return coresession.Session{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	ref = coresession.NormalizeRef(ref)
	event := participantLifecycleEvent(binding, "attached", firstNonEmpty(binding.Source, "gatewaydriver-test"))
	event.SessionID = ref.SessionID
	if _, err := s.stack.services.Engine().RecordEvents(ctx, ref, []coresession.Event{event}); err != nil {
		return coresession.Session{}, err
	}
	snapshot, err := s.load(ctx, ref)
	if err != nil {
		return coresession.Session{}, err
	}
	snapshot.Session.Participants = participantsFromCoreSnapshot(snapshot)
	return snapshot.Session, nil
}

func (s gatewayDriverTestSessionService) load(ctx context.Context, ref coresession.Ref) (coresession.Snapshot, error) {
	if s.stack == nil {
		return coresession.Snapshot{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	return s.stack.services.Sessions().Load(ctx, coresession.NormalizeRef(ref))
}

type gatewayDriverTestProvider struct{}

func (gatewayDriverTestProvider) ID() string {
	return "gatewaydriver-test"
}

func (gatewayDriverTestProvider) Models(context.Context) ([]coremodel.ModelInfo, error) {
	return []coremodel.ModelInfo{
		{ID: "llama3", Provider: "ollama"},
		{ID: "deepseek-v4-pro", Provider: "deepseek"},
		{ID: "deepseek-v4-flash", Provider: "deepseek"},
		{ID: "MiniMax-M2.7-highspeed", Provider: "minimax"},
		{ID: "GLM-4.7", Provider: "codefree"},
	}, nil
}

func (gatewayDriverTestProvider) Stream(context.Context, coremodel.Request) (coremodel.Stream, error) {
	return &coremodel.StaticStream{Events: []coremodel.StreamEvent{{
		Type: coremodel.StreamTurnDone,
		Response: &coremodel.Response{
			Status: coremodel.ResponseCompleted,
			Message: coremodel.Message{
				Role:  coremodel.RoleAssistant,
				Parts: []coremodel.Part{coremodel.NewTextPart("gatewaydriver test response")},
			},
		},
	}}}, nil
}
