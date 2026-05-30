// Package services contains the shared application facade consumed by TUI,
// future APP, CLI, and protocol surfaces.
package services

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

const (
	StateCurrentModelID         = "caelis.model.current_id"
	StateCurrentReasoningEffort = "caelis.model.reasoning_effort"
)

type Services struct {
	runtime   config.Runtime
	engine    coreruntime.Engine
	agents    []AgentDescriptor
	invokers  map[string]AgentInvoker
	resources appresources.Catalog
	settings  *appsettings.Manager
}

type Config struct {
	Runtime   config.Runtime
	AppName   string
	UserID    string
	Engine    coreruntime.Engine
	Agents    []AgentDescriptor
	Invokers  map[string]AgentInvoker
	Resources appresources.Catalog
	Settings  *appsettings.Manager
}

func New(cfg Config) (Services, error) {
	if cfg.Engine == nil {
		return Services{}, errors.New("app/services: runtime engine is required")
	}
	runtimeCfg := cloneRuntime(cfg.Runtime)
	runtimeCfg.AppName = firstNonEmpty(cfg.AppName, runtimeCfg.AppName, "caelis")
	runtimeCfg.UserID = firstNonEmpty(cfg.UserID, runtimeCfg.UserID, "local-user")
	return Services{
		runtime:   runtimeCfg,
		engine:    cfg.Engine,
		agents:    cloneAgents(cfg.Agents),
		invokers:  maps.Clone(cfg.Invokers),
		resources: appresources.CloneCatalog(cfg.Resources),
		settings:  cfg.Settings,
	}, nil
}

func (s Services) Engine() coreruntime.Engine {
	return s.engine
}

func (s Services) Runtime() config.Runtime {
	return cloneRuntime(s.runtime)
}

func (s Services) AppName() string {
	return s.runtime.AppName
}

func (s Services) UserID() string {
	return s.runtime.UserID
}

func (s Services) Sessions() SessionService {
	return SessionService{services: s}
}

func (s Services) Turns() TurnService {
	return TurnService{services: s}
}

func (s Services) Agents() AgentService {
	return AgentService{services: s}
}

func (s Services) Resources() ResourceService {
	return ResourceService{services: s}
}

func (s Services) Views() ViewService {
	return ViewService{services: s}
}

func (s Services) Settings() SettingsService {
	return SettingsService{services: s}
}

func (s Services) Models() ModelService {
	return ModelService{services: s}
}

type SettingsService struct {
	services Services
}

func (s SettingsService) Document(ctx context.Context) (appsettings.Document, error) {
	if s.services.settings == nil {
		return appsettings.Document{Runtime: s.services.Runtime()}, nil
	}
	return s.services.settings.Document(ctx)
}

func (s SettingsService) Save(ctx context.Context, doc appsettings.Document) error {
	if s.services.settings == nil {
		return errors.New("app/services: settings manager is not configured")
	}
	return s.services.settings.Save(ctx, doc)
}

type ModelService struct {
	services Services
}

func (s ModelService) Connect(ctx context.Context, cfg appsettings.ModelConfig) (appsettings.ModelConfig, error) {
	if s.services.settings == nil {
		return appsettings.ModelConfig{}, errors.New("app/services: settings manager is not configured")
	}
	return s.services.settings.UpsertModel(ctx, cfg)
}

func (s ModelService) List(context.Context) ([]appsettings.ModelChoice, error) {
	if s.services.settings == nil {
		return nil, nil
	}
	return s.services.settings.ListModelChoices()
}

func (s ModelService) Resolve(ctx context.Context, ref string) (appsettings.ModelConfig, error) {
	if s.services.settings == nil {
		return appsettings.ModelConfig{}, errors.New("app/services: settings manager is not configured")
	}
	return s.services.settings.ResolveModel(ref)
}

func (s ModelService) SetDefault(ctx context.Context, ref string) (appsettings.ModelConfig, error) {
	if s.services.settings == nil {
		return appsettings.ModelConfig{}, errors.New("app/services: settings manager is not configured")
	}
	return s.services.settings.SetDefaultModel(ctx, ref)
}

func (s ModelService) Delete(ctx context.Context, ref string) error {
	if s.services.settings == nil {
		return errors.New("app/services: settings manager is not configured")
	}
	return s.services.settings.DeleteModel(ctx, ref)
}

func (s ModelService) Use(ctx context.Context, ref session.Ref, modelRef string, reasoningEffort string) (appsettings.ModelConfig, error) {
	if s.services.settings == nil {
		return appsettings.ModelConfig{}, errors.New("app/services: settings manager is not configured")
	}
	cfg, err := s.services.settings.ResolveModel(modelRef)
	if err != nil {
		return appsettings.ModelConfig{}, err
	}
	reasoning := strings.ToLower(strings.TrimSpace(reasoningEffort))
	if reasoning != "" && !appsettings.SupportsReasoningEffort(cfg, reasoning) {
		return appsettings.ModelConfig{}, fmt.Errorf("app/services: model %q does not support reasoning effort %q", modelRef, reasoning)
	}
	if s.services.engine == nil {
		return appsettings.ModelConfig{}, errors.New("app/services: runtime engine is required")
	}
	ref = s.withDefaults(ref)
	if err := s.services.engine.UpdateSessionState(ctx, ref, func(state session.State) (session.State, error) {
		next := cloneState(state)
		if next == nil {
			next = session.State{}
		}
		next[StateCurrentModelID] = cfg.ID
		if reasoning != "" {
			next[StateCurrentReasoningEffort] = reasoning
		} else {
			delete(next, StateCurrentReasoningEffort)
		}
		return next, nil
	}); err != nil {
		return appsettings.ModelConfig{}, err
	}
	return cfg, nil
}

func (s ModelService) ClearSession(ctx context.Context, ref session.Ref) error {
	if s.services.engine == nil {
		return errors.New("app/services: runtime engine is required")
	}
	ref = s.withDefaults(ref)
	return s.services.engine.UpdateSessionState(ctx, ref, func(state session.State) (session.State, error) {
		next := cloneState(state)
		delete(next, StateCurrentModelID)
		delete(next, StateCurrentReasoningEffort)
		return next, nil
	})
}

func (s ModelService) Current(ctx context.Context, ref session.Ref) (appsettings.ModelConfig, bool, error) {
	if s.services.settings == nil {
		return appsettings.ModelConfig{}, false, nil
	}
	ref = s.withDefaults(ref)
	if strings.TrimSpace(ref.SessionID) != "" && s.services.engine != nil {
		snapshot, err := s.services.engine.LoadSession(ctx, ref)
		if err != nil {
			return appsettings.ModelConfig{}, false, err
		}
		if modelID, _ := snapshot.State[StateCurrentModelID].(string); strings.TrimSpace(modelID) != "" {
			cfg, err := s.services.settings.ResolveModel(modelID)
			return cfg, err == nil, err
		}
	}
	cfg, err := s.services.settings.ResolveModel("")
	if err != nil {
		return appsettings.ModelConfig{}, false, err
	}
	return cfg, true, nil
}

func (s ModelService) RuntimeProfile(ctx context.Context, ref session.Ref) (config.ModelProfile, bool, error) {
	cfg, ok, err := s.Current(ctx, ref)
	if err != nil || !ok {
		return config.ModelProfile{}, ok, err
	}
	return appsettings.RuntimeModelProfile(cfg), true, nil
}

func (s ModelService) withDefaults(ref session.Ref) session.Ref {
	ref = session.NormalizeRef(ref)
	if ref.AppName == "" {
		ref.AppName = s.services.runtime.AppName
	}
	if ref.UserID == "" {
		ref.UserID = s.services.runtime.UserID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(s.services.runtime.WorkspaceKey)
	}
	return ref
}

type ResourceService struct {
	services Services
}

func (s ResourceService) Catalog(context.Context) (appresources.Catalog, error) {
	return appresources.CloneCatalog(s.services.resources), nil
}

type ViewService struct {
	services Services
}

func (s ViewService) Session(ctx context.Context, ref session.Ref) (appviewmodel.SessionView, error) {
	snapshot, err := s.services.Sessions().Load(ctx, ref)
	if err != nil {
		return appviewmodel.SessionView{}, err
	}
	return appviewmodel.FromSnapshot(snapshot), nil
}

type AgentKind string

const (
	AgentKindExternalACP AgentKind = "external_acp"
)

type AgentDescriptor struct {
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name,omitempty"`
	Kind        AgentKind         `json:"kind,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	WorkDir     string            `json:"work_dir,omitempty"`
	Description string            `json:"description,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

type AgentService struct {
	services Services
}

func (s AgentService) List(context.Context) ([]AgentDescriptor, error) {
	return cloneAgents(s.services.agents), nil
}

type AgentInvoker interface {
	Invoke(context.Context, AgentInvokeRequest) (AgentInvokeResult, error)
}

type AgentInvokerFunc func(context.Context, AgentInvokeRequest) (AgentInvokeResult, error)

func (f AgentInvokerFunc) Invoke(ctx context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
	if f == nil {
		return AgentInvokeResult{}, errors.New("app/services: agent invoker is nil")
	}
	return f(ctx, req)
}

type AgentInvokeRequest struct {
	AgentID      string
	SessionRef   session.Ref
	Input        string
	ContentParts []model.ContentPart
}

type AgentInvokeResult struct {
	StopReason string
	Events     []session.Event
	Recorded   bool
}

func (s AgentService) Invoke(ctx context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		return AgentInvokeResult{}, errors.New("app/services: agent id is required")
	}
	invoker := s.services.invokers[agentID]
	if invoker == nil {
		return AgentInvokeResult{}, errors.New("app/services: agent invoker not found")
	}
	ref := session.NormalizeRef(req.SessionRef)
	if ref.AppName == "" {
		ref.AppName = s.services.runtime.AppName
	}
	if ref.UserID == "" {
		ref.UserID = s.services.runtime.UserID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(s.services.runtime.WorkspaceKey)
	}
	req.SessionRef = ref
	req.AgentID = agentID
	req.ContentParts = model.CloneContentParts(req.ContentParts)
	result, err := invoker.Invoke(ctx, req)
	if err != nil {
		return AgentInvokeResult{}, err
	}
	events := cloneEvents(result.Events)
	if len(events) > 0 && !result.Recorded {
		if _, err := s.services.engine.RecordEvents(ctx, ref, events); err != nil {
			return AgentInvokeResult{}, err
		}
	}
	result.Events = events
	return result, nil
}

type SessionService struct {
	services Services
}

type StartSessionRequest struct {
	Workspace          session.Workspace
	PreferredSessionID string
	Title              string
	Meta               map[string]any
}

type ListSessionsRequest struct {
	Workspace session.Workspace
	Search    string
	After     session.Cursor
	Limit     int
}

func (s SessionService) Start(ctx context.Context, req StartSessionRequest) (session.Session, error) {
	if s.services.engine == nil {
		return session.Session{}, errors.New("app/services: runtime engine is required")
	}
	return s.services.engine.StartSession(ctx, session.StartRequest{
		AppName:            s.services.runtime.AppName,
		UserID:             s.services.runtime.UserID,
		Workspace:          s.workspaceWithDefaults(req.Workspace),
		PreferredSessionID: strings.TrimSpace(req.PreferredSessionID),
		Title:              strings.TrimSpace(req.Title),
		Meta:               req.Meta,
	})
}

func (s SessionService) List(ctx context.Context, req ListSessionsRequest) (session.SessionPage, error) {
	if s.services.engine == nil {
		return session.SessionPage{}, errors.New("app/services: runtime engine is required")
	}
	workspace := s.workspaceWithDefaults(req.Workspace)
	return s.services.engine.ListSessions(ctx, session.ListQuery{
		Ref: session.Ref{
			AppName:      s.services.runtime.AppName,
			UserID:       s.services.runtime.UserID,
			WorkspaceKey: workspace.Key,
		},
		WorkspaceCWD: workspace.CWD,
		Search:       strings.TrimSpace(req.Search),
		After:        req.After,
		Limit:        req.Limit,
	})
}

func (s SessionService) Load(ctx context.Context, ref session.Ref) (session.Snapshot, error) {
	if s.services.engine == nil {
		return session.Snapshot{}, errors.New("app/services: runtime engine is required")
	}
	ref = s.withDefaults(ref)
	return s.services.engine.LoadSession(ctx, ref)
}

func (s SessionService) withDefaults(ref session.Ref) session.Ref {
	ref = session.NormalizeRef(ref)
	if ref.AppName == "" {
		ref.AppName = s.services.runtime.AppName
	}
	if ref.UserID == "" {
		ref.UserID = s.services.runtime.UserID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(s.services.runtime.WorkspaceKey)
	}
	return ref
}

func (s SessionService) workspaceWithDefaults(workspace session.Workspace) session.Workspace {
	workspace.Key = strings.TrimSpace(workspace.Key)
	workspace.CWD = strings.TrimSpace(workspace.CWD)
	if workspace.Key == "" {
		workspace.Key = strings.TrimSpace(s.services.runtime.WorkspaceKey)
	}
	if workspace.CWD == "" {
		workspace.CWD = strings.TrimSpace(s.services.runtime.WorkspaceCWD)
	}
	return workspace
}

type TurnService struct {
	services Services
}

type BeginTurnRequest struct {
	SessionRef   session.Ref
	Input        string
	ContentParts []model.ContentPart
	Model        string
	Surface      string
	Mode         string
	Meta         map[string]any
}

func (s TurnService) Begin(ctx context.Context, req BeginTurnRequest) (coreruntime.Turn, error) {
	if s.services.engine == nil {
		return nil, errors.New("app/services: runtime engine is required")
	}
	ref := session.NormalizeRef(req.SessionRef)
	if ref.AppName == "" {
		ref.AppName = s.services.runtime.AppName
	}
	if ref.UserID == "" {
		ref.UserID = s.services.runtime.UserID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(s.services.runtime.WorkspaceKey)
	}
	modelRef := strings.TrimSpace(req.Model)
	if modelRef == "" && s.services.settings != nil {
		if cfg, ok, err := s.services.Models().Current(ctx, ref); err == nil && ok {
			modelRef = cfg.ID
		}
	}
	return s.services.engine.BeginTurn(ctx, coreruntime.TurnRequest{
		SessionRef:   ref,
		Input:        req.Input,
		ContentParts: model.CloneContentParts(req.ContentParts),
		Model:        modelRef,
		Surface:      strings.TrimSpace(req.Surface),
		Mode:         strings.TrimSpace(req.Mode),
		Meta:         maps.Clone(req.Meta),
	})
}

func (s TurnService) Replay(ctx context.Context, req coreruntime.ReplayRequest) (<-chan coreruntime.EventEnvelope, error) {
	if s.services.engine == nil {
		return nil, errors.New("app/services: runtime engine is required")
	}
	ref := session.NormalizeRef(req.SessionRef)
	if ref.AppName == "" {
		ref.AppName = s.services.runtime.AppName
	}
	if ref.UserID == "" {
		ref.UserID = s.services.runtime.UserID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(s.services.runtime.WorkspaceKey)
	}
	req.SessionRef = ref
	return s.services.engine.Replay(ctx, req)
}

func (s TurnService) Interrupt(ctx context.Context, ref session.Ref) error {
	if s.services.engine == nil {
		return errors.New("app/services: runtime engine is required")
	}
	ref = session.NormalizeRef(ref)
	if ref.AppName == "" {
		ref.AppName = s.services.runtime.AppName
	}
	if ref.UserID == "" {
		ref.UserID = s.services.runtime.UserID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(s.services.runtime.WorkspaceKey)
	}
	return s.services.engine.Interrupt(ctx, ref)
}

func cloneRuntime(in config.Runtime) config.Runtime {
	out := in
	out.AppName = strings.TrimSpace(in.AppName)
	out.UserID = strings.TrimSpace(in.UserID)
	out.WorkspaceKey = strings.TrimSpace(in.WorkspaceKey)
	out.WorkspaceCWD = strings.TrimSpace(in.WorkspaceCWD)
	out.Model = strings.TrimSpace(in.Model)
	out.Store.Meta = maps.Clone(in.Store.Meta)
	out.Sandbox.ReadableRoots = append([]string(nil), in.Sandbox.ReadableRoots...)
	out.Sandbox.WritableRoots = append([]string(nil), in.Sandbox.WritableRoots...)
	out.Plugins = append([]config.Plugin(nil), in.Plugins...)
	for i := range out.Plugins {
		out.Plugins[i].Meta = maps.Clone(in.Plugins[i].Meta)
	}
	out.Meta = maps.Clone(in.Meta)
	return out
}

func cloneAgents(in []AgentDescriptor) []AgentDescriptor {
	if len(in) == 0 {
		return nil
	}
	out := make([]AgentDescriptor, 0, len(in))
	for _, item := range in {
		next := item
		next.ID = strings.TrimSpace(item.ID)
		next.Name = strings.TrimSpace(item.Name)
		next.Command = strings.TrimSpace(item.Command)
		next.WorkDir = strings.TrimSpace(item.WorkDir)
		next.Description = strings.TrimSpace(item.Description)
		next.Args = append([]string(nil), item.Args...)
		next.Meta = maps.Clone(item.Meta)
		out = append(out, next)
	}
	return out
}

func cloneEvents(in []session.Event) []session.Event {
	if len(in) == 0 {
		return nil
	}
	out := make([]session.Event, 0, len(in))
	for _, event := range in {
		out = append(out, session.CloneEvent(event))
	}
	return out
}

func cloneState(in session.State) session.State {
	if in == nil {
		return nil
	}
	return session.State(maps.Clone(in))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
