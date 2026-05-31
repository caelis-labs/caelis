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
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	portsession "github.com/OnslaughtSnail/caelis/ports/session"
)

type gatewayDriverTestConfig struct {
	AppName        string
	UserID         string
	StoreDir       string
	WorkspaceKey   string
	WorkspaceCWD   string
	PermissionMode string
	ContextWindow  int
	SystemPrompt   string
	Assembly       assembly.ResolvedAssembly
	Model          ModelConfig
	Sandbox        gatewayDriverTestSandboxConfig
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

	Gateway   *gatewayDriverTestGateway
	Sessions  gatewayDriverTestSessionService
	AppName   string
	UserID    string
	Workspace portsession.WorkspaceRef
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
	stack, err := applocal.NewWithContext(ctx, applocal.Config{
		Runtime:           runtimeCfg,
		Settings:          manager,
		Provider:          gatewayDriverTestProvider{},
		ExternalACPAgents: acpExternalConfigsFromAssembly(cfg.Assembly),
		SystemPrompt:      cfg.SystemPrompt,
	})
	if err != nil {
		return nil, err
	}
	out := &gatewayDriverTestStack{
		local:    stack,
		services: stack.Services(),
		settings: manager,
		storeDir: storeDir,
		AppName:  appName,
		UserID:   userID,
		Workspace: portsession.WorkspaceRef{
			Key: workspaceKey,
			CWD: workspaceCWD,
		},
	}
	out.Gateway = &gatewayDriverTestGateway{stack: out, bindings: map[string]portsession.SessionRef{}}
	out.Sessions = gatewayDriverTestSessionService{stack: out}
	driverStack := BindAppServices(&DriverStack{}, stack.Services())
	baseStart := driverStack.StartSessionFn
	driverStack.StartSessionFn = func(ctx context.Context, preferredSessionID string, bindingKey string) (coresession.Session, error) {
		active, err := baseStart(ctx, preferredSessionID, bindingKey)
		if err == nil {
			out.Gateway.remember(bindingKey, portRefFromCore(active.Ref))
		}
		return active, err
	}
	out.driverStack = driverStack
	return out, nil
}

func (s *gatewayDriverTestStack) StartSession(ctx context.Context, preferredSessionID string, bindingKey string) (portsession.Session, error) {
	if s == nil || s.driverStack == nil {
		return portsession.Session{}, fmt.Errorf("gatewaydriver test stack is unavailable")
	}
	active, err := s.driverStack.StartSession(ctx, preferredSessionID, bindingKey)
	if err != nil {
		return portsession.Session{}, err
	}
	return portSessionFromCore(active), nil
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

type gatewayDriverTestGateway struct {
	stack    *gatewayDriverTestStack
	mu       sync.Mutex
	bindings map[string]portsession.SessionRef
}

func (g *gatewayDriverTestGateway) remember(bindingKey string, ref portsession.SessionRef) {
	if g == nil {
		return
	}
	key := firstNonEmpty(strings.TrimSpace(bindingKey), "cli-tui")
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.bindings == nil {
		g.bindings = map[string]portsession.SessionRef{}
	}
	g.bindings[key] = ref
}

func (g *gatewayDriverTestGateway) CurrentSession(bindingKey string) (portsession.SessionRef, bool) {
	if g == nil {
		return portsession.SessionRef{}, false
	}
	key := firstNonEmpty(strings.TrimSpace(bindingKey), "cli-tui")
	g.mu.Lock()
	defer g.mu.Unlock()
	ref, ok := g.bindings[key]
	return ref, ok
}

func (g *gatewayDriverTestGateway) StartSession(ctx context.Context, req kernel.StartSessionRequest) (portsession.Session, error) {
	if g == nil || g.stack == nil {
		return portsession.Session{}, fmt.Errorf("gatewaydriver test gateway is unavailable")
	}
	active, err := g.stack.services.Sessions().Start(ctx, appservices.StartSessionRequest{
		Workspace: coresession.Workspace{
			Key: firstNonEmpty(req.Workspace.Key, g.stack.Workspace.Key),
			CWD: firstNonEmpty(req.Workspace.CWD, g.stack.Workspace.CWD),
		},
		PreferredSessionID: req.PreferredSessionID,
		Title:              req.Title,
	})
	if err != nil {
		return portsession.Session{}, err
	}
	port := portSessionFromCore(active)
	g.remember(req.BindingKey, port.SessionRef)
	return port, nil
}

func (g *gatewayDriverTestGateway) ListSessions(ctx context.Context, req kernel.ListSessionsRequest) (portsession.SessionList, error) {
	if g == nil || g.stack == nil || g.stack.driverStack == nil {
		return portsession.SessionList{}, fmt.Errorf("gatewaydriver test gateway is unavailable")
	}
	workspaceKey := strings.TrimSpace(req.WorkspaceKey)
	page, err := g.stack.services.Sessions().List(ctx, appservices.ListSessionsRequest{
		Workspace: coresession.Workspace{
			Key: workspaceKey,
		},
		AllWorkspaces: workspaceKey == "",
		After:         coresession.Cursor(req.Cursor),
		Limit:         req.Limit,
	})
	if err != nil {
		return portsession.SessionList{}, err
	}
	return gatewayDriverTestSessionListFromCore(page), nil
}

type gatewayDriverTestSessionService struct {
	stack *gatewayDriverTestStack
}

func (s gatewayDriverTestSessionService) StartSession(ctx context.Context, req portsession.StartSessionRequest) (portsession.Session, error) {
	if s.stack == nil {
		return portsession.Session{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	active, err := s.stack.services.Sessions().Start(ctx, appservices.StartSessionRequest{
		Workspace: coresession.Workspace{
			Key: firstNonEmpty(req.Workspace.Key, s.stack.Workspace.Key),
			CWD: firstNonEmpty(req.Workspace.CWD, s.stack.Workspace.CWD),
		},
		PreferredSessionID: req.PreferredSessionID,
		Title:              req.Title,
		Meta:               maps.Clone(req.Metadata),
	})
	if err != nil {
		return portsession.Session{}, err
	}
	return portSessionFromCore(active), nil
}

func (s gatewayDriverTestSessionService) Session(ctx context.Context, ref portsession.SessionRef) (portsession.Session, error) {
	snapshot, err := s.load(ctx, ref)
	if err != nil {
		return portsession.Session{}, err
	}
	snapshot.Session.Controller = controllerFromCoreSnapshot(snapshot)
	snapshot.Session.Participants = participantsFromCoreSnapshot(snapshot)
	return portSessionFromCore(snapshot.Session), nil
}

func (s gatewayDriverTestSessionService) LoadSession(ctx context.Context, req portsession.LoadSessionRequest) (portsession.LoadedSession, error) {
	snapshot, err := s.load(ctx, req.SessionRef)
	if err != nil {
		return portsession.LoadedSession{}, err
	}
	return gatewayDriverTestLoadedSessionFromCore(snapshot), nil
}

func (s gatewayDriverTestSessionService) ListSessions(ctx context.Context, req portsession.ListSessionsRequest) (portsession.SessionList, error) {
	if s.stack == nil || s.stack.Gateway == nil {
		return portsession.SessionList{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	return s.stack.Gateway.ListSessions(ctx, kernel.ListSessionsRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.WorkspaceKey,
		Cursor:       req.Cursor,
		Limit:        req.Limit,
	})
}

func (s gatewayDriverTestSessionService) AppendEvent(ctx context.Context, req portsession.AppendEventRequest) (*portsession.Event, error) {
	if s.stack == nil {
		return nil, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	event := coreEventFromPort(req.Event)
	if _, err := s.stack.services.Engine().RecordEvents(ctx, coreRefFromPort(req.SessionRef), []coresession.Event{event}); err != nil {
		return nil, err
	}
	return req.Event, nil
}

func (s gatewayDriverTestSessionService) Events(ctx context.Context, req portsession.EventsRequest) ([]*portsession.Event, error) {
	snapshot, err := s.load(ctx, req.SessionRef)
	if err != nil {
		return nil, err
	}
	return gatewayDriverTestPortEventsFromCore(snapshot.Events), nil
}

func gatewayDriverTestLoadedSessionFromCore(snapshot coresession.Snapshot) portsession.LoadedSession {
	snapshot.Session.Controller = controllerFromCoreSnapshot(snapshot)
	snapshot.Session.Participants = participantsFromCoreSnapshot(snapshot)
	return portsession.LoadedSession{
		Session: portSessionFromCore(snapshot.Session),
		Events:  gatewayDriverTestPortEventsFromCore(snapshot.Events),
		State:   maps.Clone(snapshot.State),
	}
}

func gatewayDriverTestPortEventsFromCore(events []coresession.Event) []*portsession.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]*portsession.Event, 0, len(events))
	for _, event := range events {
		next := portsession.Event{
			ID:        strings.TrimSpace(event.ID),
			SessionID: strings.TrimSpace(event.SessionID),
			Type:      portsession.EventType(event.Type),
			Time:      event.Time,
			Meta:      maps.Clone(event.Meta),
		}
		out = append(out, &next)
	}
	return out
}

func gatewayDriverTestSessionListFromCore(page coresession.SessionPage) portsession.SessionList {
	out := portsession.SessionList{
		Sessions:   make([]portsession.SessionSummary, 0, len(page.Sessions)),
		NextCursor: string(page.NextCursor),
	}
	for _, item := range page.Sessions {
		out.Sessions = append(out.Sessions, portsession.SessionSummary{
			SessionRef: portRefFromCore(item.Session.Ref),
			CWD:        strings.TrimSpace(item.Session.Workspace.CWD),
			Title:      strings.TrimSpace(item.Session.Title),
			UpdatedAt:  item.Session.UpdatedAt,
		})
	}
	return out
}

func (s gatewayDriverTestSessionService) ReplaceState(ctx context.Context, ref portsession.SessionRef, state map[string]any) error {
	if s.stack == nil {
		return fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	next := maps.Clone(state)
	return s.stack.services.Engine().UpdateSessionState(ctx, coreRefFromPort(ref), func(coresession.State) (coresession.State, error) {
		return coresession.State(next), nil
	})
}

func (s gatewayDriverTestSessionService) SnapshotState(ctx context.Context, ref portsession.SessionRef) (map[string]any, error) {
	snapshot, err := s.load(ctx, ref)
	if err != nil {
		return nil, err
	}
	return maps.Clone(snapshot.State), nil
}

func (s gatewayDriverTestSessionService) UpdateState(ctx context.Context, ref portsession.SessionRef, patch func(map[string]any) (map[string]any, error)) error {
	if s.stack == nil {
		return fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	return s.stack.services.Engine().UpdateSessionState(ctx, coreRefFromPort(ref), func(state coresession.State) (coresession.State, error) {
		next, err := patch(maps.Clone(state))
		if err != nil {
			return nil, err
		}
		return coresession.State(next), nil
	})
}

func (s gatewayDriverTestSessionService) BindController(ctx context.Context, req portsession.BindControllerRequest) (portsession.Session, error) {
	if s.stack == nil {
		return portsession.Session{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	controller := coreControllerFromPort(req.Binding)
	event := coresession.Event{
		Type:       coresession.EventHandoff,
		Visibility: coresession.VisibilityCanonical,
		Actor: coresession.ActorRef{
			Kind: coresession.ActorSystem,
			ID:   "gatewaydriver-test",
		},
		Scope: &coresession.EventScope{
			Source:     firstNonEmpty(controller.Source, "gatewaydriver-test"),
			Controller: controller,
		},
		Meta: map[string]any{"action": "handoff"},
	}
	if _, err := s.stack.services.Engine().RecordEvents(ctx, coreRefFromPort(req.SessionRef), []coresession.Event{event}); err != nil {
		return portsession.Session{}, err
	}
	snapshot, err := s.load(ctx, req.SessionRef)
	if err != nil {
		return portsession.Session{}, err
	}
	snapshot.Session.Controller = controllerFromCoreSnapshot(snapshot)
	return portSessionFromCore(snapshot.Session), nil
}

func (s gatewayDriverTestSessionService) PutParticipant(ctx context.Context, req portsession.PutParticipantRequest) (portsession.Session, error) {
	if s.stack == nil {
		return portsession.Session{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	participant := coreParticipantFromPort(req.Binding)
	event := participantLifecycleEvent(participant, "attached", firstNonEmpty(participant.Source, "gatewaydriver-test"))
	if _, err := s.stack.services.Engine().RecordEvents(ctx, coreRefFromPort(req.SessionRef), []coresession.Event{event}); err != nil {
		return portsession.Session{}, err
	}
	snapshot, err := s.load(ctx, req.SessionRef)
	if err != nil {
		return portsession.Session{}, err
	}
	snapshot.Session.Participants = participantsFromCoreSnapshot(snapshot)
	return portSessionFromCore(snapshot.Session), nil
}

func (s gatewayDriverTestSessionService) RemoveParticipant(ctx context.Context, req portsession.RemoveParticipantRequest) (portsession.Session, error) {
	if s.stack == nil {
		return portsession.Session{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	snapshot, err := s.load(ctx, req.SessionRef)
	if err != nil {
		return portsession.Session{}, err
	}
	participant, ok := findCoreParticipant(participantsFromCoreSnapshot(snapshot), req.ParticipantID)
	if ok {
		event := participantLifecycleEvent(participant, "detached", firstNonEmpty(participant.Source, "gatewaydriver-test"))
		if _, err := s.stack.services.Engine().RecordEvents(ctx, coreRefFromPort(req.SessionRef), []coresession.Event{event}); err != nil {
			return portsession.Session{}, err
		}
		snapshot, err = s.load(ctx, req.SessionRef)
		if err != nil {
			return portsession.Session{}, err
		}
	}
	snapshot.Session.Participants = participantsFromCoreSnapshot(snapshot)
	return portSessionFromCore(snapshot.Session), nil
}

func (s gatewayDriverTestSessionService) load(ctx context.Context, ref portsession.SessionRef) (coresession.Snapshot, error) {
	if s.stack == nil {
		return coresession.Snapshot{}, fmt.Errorf("gatewaydriver test session service is unavailable")
	}
	return s.stack.services.Sessions().Load(ctx, coreRefFromPort(ref))
}

func coreRefFromPort(ref portsession.SessionRef) coresession.Ref {
	return coresession.Ref{
		AppName:      strings.TrimSpace(ref.AppName),
		UserID:       strings.TrimSpace(ref.UserID),
		SessionID:    strings.TrimSpace(ref.SessionID),
		WorkspaceKey: strings.TrimSpace(ref.WorkspaceKey),
	}
}

func portRefFromCore(ref coresession.Ref) portsession.SessionRef {
	return portsession.SessionRef{
		AppName:      strings.TrimSpace(ref.AppName),
		UserID:       strings.TrimSpace(ref.UserID),
		SessionID:    strings.TrimSpace(ref.SessionID),
		WorkspaceKey: strings.TrimSpace(ref.WorkspaceKey),
	}
}

func coreSessionFromPort(active portsession.Session) coresession.Session {
	return coresession.Session{
		Ref: coreRefFromPort(active.SessionRef),
		Workspace: coresession.Workspace{
			Key: strings.TrimSpace(active.SessionRef.WorkspaceKey),
			CWD: strings.TrimSpace(active.CWD),
		},
		Title:        strings.TrimSpace(active.Title),
		Meta:         maps.Clone(active.Metadata),
		Controller:   coreControllerFromPort(active.Controller),
		Participants: coreParticipantsFromPort(active.Participants),
		CreatedAt:    active.CreatedAt,
		UpdatedAt:    active.UpdatedAt,
	}
}

func coreParticipantsFromPort(in []portsession.ParticipantBinding) []coresession.ParticipantBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]coresession.ParticipantBinding, 0, len(in))
	for _, participant := range in {
		out = append(out, coreParticipantFromPort(participant))
	}
	return out
}

func portSessionFromCore(active coresession.Session) portsession.Session {
	return portsession.Session{
		SessionRef:   portRefFromCore(active.Ref),
		CWD:          strings.TrimSpace(active.Workspace.CWD),
		Title:        strings.TrimSpace(active.Title),
		Metadata:     maps.Clone(active.Meta),
		Controller:   portControllerFromCore(active.Controller),
		Participants: portParticipantsFromCore(active.Participants),
		CreatedAt:    active.CreatedAt,
		UpdatedAt:    active.UpdatedAt,
	}
}

func portControllerFromCore(in coresession.ControllerBinding) portsession.ControllerBinding {
	kind := portsession.ControllerKind(in.Kind)
	if in.Kind == coresession.ControllerBuiltin {
		kind = portsession.ControllerKindKernel
	}
	return portsession.ControllerBinding{
		Kind:            kind,
		ControllerID:    strings.TrimSpace(in.ID),
		AgentName:       strings.TrimSpace(in.AgentName),
		Label:           strings.TrimSpace(in.Label),
		EpochID:         strings.TrimSpace(in.EpochID),
		RemoteSessionID: strings.TrimSpace(in.RemoteSessionID),
		ContextSyncSeq:  in.ContextSyncSeq,
		AttachedAt:      in.AttachedAt,
		Source:          strings.TrimSpace(in.Source),
	}
}

func portParticipantsFromCore(in []coresession.ParticipantBinding) []portsession.ParticipantBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]portsession.ParticipantBinding, 0, len(in))
	for _, participant := range in {
		out = append(out, portParticipantFromCore(participant))
	}
	return out
}

func portParticipantFromCore(in coresession.ParticipantBinding) portsession.ParticipantBinding {
	return portsession.ParticipantBinding{
		ID:             strings.TrimSpace(in.ID),
		Kind:           portsession.ParticipantKind(in.Kind),
		Role:           portsession.ParticipantRole(in.Role),
		AgentName:      strings.TrimSpace(in.AgentName),
		Label:          strings.TrimSpace(in.Label),
		SessionID:      strings.TrimSpace(in.SessionID),
		Source:         strings.TrimSpace(in.Source),
		ParentTurnID:   strings.TrimSpace(in.ParentTurnID),
		DelegationID:   strings.TrimSpace(in.DelegationID),
		ContextSyncSeq: in.ContextSyncSeq,
		AttachedAt:     in.AttachedAt,
		ControllerRef:  strings.TrimSpace(in.ControllerRef),
	}
}

func coreControllerFromPort(in portsession.ControllerBinding) coresession.ControllerBinding {
	kind := coresession.ControllerKind(in.Kind)
	if in.Kind == portsession.ControllerKindKernel {
		kind = coresession.ControllerBuiltin
	}
	return coresession.ControllerBinding{
		Kind:            kind,
		ID:              strings.TrimSpace(in.ControllerID),
		AgentName:       strings.TrimSpace(in.AgentName),
		Label:           strings.TrimSpace(in.Label),
		EpochID:         strings.TrimSpace(in.EpochID),
		RemoteSessionID: strings.TrimSpace(in.RemoteSessionID),
		ContextSyncSeq:  in.ContextSyncSeq,
		AttachedAt:      in.AttachedAt,
		Source:          strings.TrimSpace(in.Source),
	}
}

func coreParticipantFromPort(in portsession.ParticipantBinding) coresession.ParticipantBinding {
	return coresession.ParticipantBinding{
		ID:             strings.TrimSpace(in.ID),
		Kind:           coresession.ParticipantKind(in.Kind),
		Role:           coresession.ParticipantRole(in.Role),
		AgentName:      strings.TrimSpace(in.AgentName),
		Label:          strings.TrimSpace(in.Label),
		SessionID:      strings.TrimSpace(in.SessionID),
		Source:         strings.TrimSpace(in.Source),
		ParentTurnID:   strings.TrimSpace(in.ParentTurnID),
		DelegationID:   strings.TrimSpace(in.DelegationID),
		ContextSyncSeq: in.ContextSyncSeq,
		AttachedAt:     in.AttachedAt,
		ControllerRef:  strings.TrimSpace(in.ControllerRef),
	}
}

func coreEventFromPort(in *portsession.Event) coresession.Event {
	if in == nil {
		return coresession.Event{}
	}
	out := coresession.Event{
		ID:         strings.TrimSpace(in.ID),
		SessionID:  strings.TrimSpace(in.SessionID),
		Type:       coresession.EventType(in.Type),
		Visibility: coresession.Visibility(in.Visibility),
		Time:       in.Time,
		Meta:       maps.Clone(in.Meta),
	}
	if out.Type == "" {
		out.Type = coreEventTypeFromPort(in)
	}
	if in.Message != nil {
		text := strings.TrimSpace(in.Message.TextContent())
		message := coremodel.Message{
			Role:  coremodel.Role(in.Message.Role),
			Parts: []coremodel.Part{},
		}
		if text != "" {
			message.Parts = []coremodel.Part{coremodel.NewTextPart(text)}
		}
		out.Message = &message
	} else if text := strings.TrimSpace(in.Text); text != "" && (out.Type == coresession.EventUser || out.Type == coresession.EventAssistant || out.Type == coresession.EventSystem) {
		role := coremodel.RoleAssistant
		if out.Type == coresession.EventUser {
			role = coremodel.RoleUser
		} else if out.Type == coresession.EventSystem {
			role = coremodel.RoleSystem
		}
		out.Message = &coremodel.Message{Role: role, Parts: []coremodel.Part{coremodel.NewTextPart(text)}}
	}
	if in.Scope != nil {
		out.Scope = &coresession.EventScope{
			TurnID:      strings.TrimSpace(in.Scope.TurnID),
			Source:      strings.TrimSpace(in.Scope.Source),
			Controller:  coreControllerRefFromPort(in.Scope.Controller),
			Participant: coreParticipantRefFromPort(in.Scope.Participant),
			ACP: coresession.ACPRef{
				SessionID: strings.TrimSpace(in.Scope.ACP.SessionID),
				EventType: strings.TrimSpace(in.Scope.ACP.EventType),
			},
		}
	}
	return out
}

func coreEventTypeFromPort(in *portsession.Event) coresession.EventType {
	switch portsession.EventTypeOf(in) {
	case portsession.EventTypeUser:
		return coresession.EventUser
	case portsession.EventTypeAssistant:
		return coresession.EventAssistant
	case portsession.EventTypeSystem:
		return coresession.EventSystem
	case portsession.EventTypeToolCall:
		return coresession.EventToolCall
	case portsession.EventTypeToolResult:
		return coresession.EventToolResult
	case portsession.EventTypePlan:
		return coresession.EventPlan
	case portsession.EventTypeCompact:
		return coresession.EventCompact
	case portsession.EventTypeLifecycle:
		return coresession.EventLifecycle
	case portsession.EventTypeParticipant:
		return coresession.EventParticipant
	case portsession.EventTypeHandoff:
		return coresession.EventHandoff
	case portsession.EventTypeNotice:
		return coresession.EventNotice
	default:
		return ""
	}
}

func coreControllerRefFromPort(in portsession.ControllerRef) coresession.ControllerBinding {
	kind := coresession.ControllerKind(in.Kind)
	if in.Kind == portsession.ControllerKindKernel {
		kind = coresession.ControllerBuiltin
	}
	return coresession.ControllerBinding{
		Kind:    kind,
		ID:      strings.TrimSpace(in.ID),
		EpochID: strings.TrimSpace(in.EpochID),
	}
}

func coreParticipantRefFromPort(in portsession.ParticipantRef) coresession.ParticipantBinding {
	return coresession.ParticipantBinding{
		ID:           strings.TrimSpace(in.ID),
		Kind:         coresession.ParticipantKind(in.Kind),
		Role:         coresession.ParticipantRole(in.Role),
		DelegationID: strings.TrimSpace(in.DelegationID),
	}
}

func acpExternalConfigsFromAssembly(resolved assembly.ResolvedAssembly) []acpexternal.Config {
	out := make([]acpexternal.Config, 0, len(resolved.Agents))
	for _, agent := range resolved.Agents {
		name := firstNonEmpty(agent.Name, agent.Command)
		if name == "" || strings.TrimSpace(agent.Command) == "" {
			continue
		}
		out = append(out, acpexternal.Config{
			AgentID:     name,
			AgentName:   name,
			Description: strings.TrimSpace(agent.Description),
			Command:     strings.TrimSpace(agent.Command),
			Args:        append([]string(nil), agent.Args...),
			WorkDir:     strings.TrimSpace(agent.WorkDir),
			Env:         envListFromMap(agent.Env),
		})
	}
	return out
}

func envListFromMap(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out = append(out, key+"="+value)
	}
	return out
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
