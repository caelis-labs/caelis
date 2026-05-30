// Package services contains the shared application facade consumed by TUI,
// future APP, CLI, and protocol surfaces.
package services

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type Services struct {
	runtime   config.Runtime
	engine    coreruntime.Engine
	agents    []AgentDescriptor
	invokers  map[string]AgentInvoker
	resources appresources.Catalog
}

type Config struct {
	Runtime   config.Runtime
	AppName   string
	UserID    string
	Engine    coreruntime.Engine
	Agents    []AgentDescriptor
	Invokers  map[string]AgentInvoker
	Resources appresources.Catalog
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
	return s.services.engine.BeginTurn(ctx, coreruntime.TurnRequest{
		SessionRef:   ref,
		Input:        req.Input,
		ContentParts: model.CloneContentParts(req.ContentParts),
		Model:        strings.TrimSpace(req.Model),
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
