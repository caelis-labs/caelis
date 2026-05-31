// Package services contains the shared application facade consumed by TUI,
// future APP, CLI, and protocol surfaces.
package services

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
)

const (
	StateCurrentModelID          = "caelis.model.current_id"
	StateCurrentReasoningEffort  = "caelis.model.reasoning_effort"
	StateSessionMode             = "caelis.session.mode"
	StateControllerConfigRef     = "caelis.controller.config_ref"
	StateControllerModel         = "caelis.controller.model"
	StateControllerReasoning     = "caelis.controller.reasoning_effort"
	StateControllerMode          = "caelis.controller.mode"
	StateControllerConfigOptions = "caelis.controller.config_options"
)

type Services struct {
	state          *serviceState
	engine         coreruntime.Engine
	sandbox        sandbox.Runtime
	tasks          TaskResolver
	controllerRuns ControllerRunSource
	modelProvider  ModelProviderFactory
	modelCache     *modelDiscoveryCache
	agents         []AgentDescriptor
	builtins       []AgentDescriptor
	invokers       map[string]AgentInvoker
	factory        AgentInvokerFactory
	installer      AgentInstaller
	resources      appresources.Catalog
	settings       *appsettings.Manager
	codefree       CodeFreeAuthenticator
	applyRuntime   RuntimeApplier
}

type serviceState struct {
	mu      sync.RWMutex
	runtime config.Runtime
}

type Config struct {
	Runtime        config.Runtime
	AppName        string
	UserID         string
	Engine         coreruntime.Engine
	Sandbox        sandbox.Runtime
	TaskResolver   TaskResolver
	ControllerRuns ControllerRunSource
	ModelProvider  ModelProviderFactory
	Agents         []AgentDescriptor
	BuiltinAgents  []AgentDescriptor
	Invokers       map[string]AgentInvoker
	InvokerFactory AgentInvokerFactory
	AgentInstaller AgentInstaller
	Resources      appresources.Catalog
	Settings       *appsettings.Manager
	CodeFree       CodeFreeAuthenticator
	ApplyRuntime   RuntimeApplier
}

type RuntimeApplier func(context.Context, config.Runtime) (config.Runtime, error)

func New(cfg Config) (Services, error) {
	if cfg.Engine == nil {
		return Services{}, errors.New("app/services: runtime engine is required")
	}
	runtimeCfg := cloneRuntime(cfg.Runtime)
	runtimeCfg.AppName = firstNonEmpty(cfg.AppName, runtimeCfg.AppName, "caelis")
	runtimeCfg.UserID = firstNonEmpty(cfg.UserID, runtimeCfg.UserID, "local-user")
	return Services{
		state:          &serviceState{runtime: runtimeCfg},
		engine:         cfg.Engine,
		sandbox:        cfg.Sandbox,
		tasks:          cfg.TaskResolver,
		controllerRuns: cfg.ControllerRuns,
		modelProvider:  cfg.ModelProvider,
		modelCache:     newModelDiscoveryCache(),
		agents:         cloneAgents(cfg.Agents),
		builtins:       cloneAgents(cfg.BuiltinAgents),
		invokers:       maps.Clone(cfg.Invokers),
		factory:        cfg.InvokerFactory,
		installer:      cfg.AgentInstaller,
		resources:      appresources.CloneCatalog(cfg.Resources),
		settings:       cfg.Settings,
		codefree:       cfg.CodeFree,
		applyRuntime:   cfg.ApplyRuntime,
	}, nil
}

func (s Services) Engine() coreruntime.Engine {
	return s.engine
}

func (s Services) Runtime() config.Runtime {
	if s.state == nil {
		return config.Runtime{}
	}
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	return cloneRuntime(s.state.runtime)
}

func (s Services) setRuntime(runtime config.Runtime) {
	if s.state == nil {
		return
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.state.runtime = cloneRuntime(runtime)
}

func (s Services) AppName() string {
	return s.Runtime().AppName
}

func (s Services) UserID() string {
	return s.Runtime().UserID
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

func (s Services) Controllers() ControllerService {
	return ControllerService{services: s}
}

func (s Services) Resources() ResourceService {
	return ResourceService{services: s}
}

func (s Services) Sandbox() SandboxService {
	return SandboxService{services: s}
}

func (s Services) Views() ViewService {
	return ViewService{services: s}
}

func (s Services) Status() StatusService {
	return StatusService{services: s}
}

func (s Services) Settings() SettingsService {
	return SettingsService{services: s}
}

func (s Services) Models() ModelService {
	return ModelService{services: s}
}

func (s Services) Modes() ModeService {
	return ModeService{services: s}
}

func (s Services) Compaction() CompactionService {
	return CompactionService{services: s}
}

func (s Services) Tasks() TaskService {
	return TaskService{services: s}
}

func (s Services) Approvals() ApprovalService {
	return ApprovalService{services: s}
}

func (s Services) Events() EventService {
	return EventService{services: s}
}

func (s Services) Commands() CommandService {
	return CommandService{services: s}
}

type SettingsService struct {
	services Services
}

type CommandService struct {
	services Services
}

type ModeChoice struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type ModeService struct {
	services Services
}

func (s ModeService) List(context.Context) ([]ModeChoice, error) {
	return sessionModeChoices(), nil
}

func (s ModeService) CurrentID(ctx context.Context, ref session.Ref) (string, error) {
	if s.services.engine == nil {
		return coreruntime.SessionModeAutoReview, nil
	}
	ref = defaultSessionRef(s.services.Runtime(), ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return coreruntime.SessionModeAutoReview, nil
	}
	snapshot, err := s.services.engine.LoadSession(ctx, ref)
	if err != nil {
		return "", err
	}
	value, _ := snapshot.State[StateSessionMode].(string)
	return defaultSessionMode(value), nil
}

func (s ModeService) Current(ctx context.Context, ref session.Ref) (ModeChoice, error) {
	modeID, err := s.CurrentID(ctx, ref)
	if err != nil {
		return ModeChoice{}, err
	}
	mode, ok := lookupSessionMode(modeID)
	if !ok {
		return ModeChoice{}, fmt.Errorf("app/services: unknown session mode %q", modeID)
	}
	return mode, nil
}

func (s ModeService) Set(ctx context.Context, ref session.Ref, mode string) (ModeChoice, error) {
	modeID := coreruntime.NormalizeSessionMode(mode)
	if modeID == "" {
		return ModeChoice{}, fmt.Errorf("app/services: unknown session mode %q", strings.TrimSpace(mode))
	}
	if s.services.engine == nil {
		return ModeChoice{}, errors.New("app/services: runtime engine is required")
	}
	ref = defaultSessionRef(s.services.Runtime(), ref)
	if err := s.services.engine.UpdateSessionState(ctx, ref, func(state session.State) (session.State, error) {
		next := cloneState(state)
		if next == nil {
			next = session.State{}
		}
		next[StateSessionMode] = modeID
		return next, nil
	}); err != nil {
		return ModeChoice{}, err
	}
	modeChoice, _ := lookupSessionMode(modeID)
	return modeChoice, nil
}

func (s ModeService) Toggle(ctx context.Context, ref session.Ref) (ModeChoice, error) {
	current, err := s.CurrentID(ctx, ref)
	if err != nil {
		return ModeChoice{}, err
	}
	next := coreruntime.SessionModeManual
	if current == coreruntime.SessionModeManual {
		next = coreruntime.SessionModeAutoReview
	}
	return s.Set(ctx, ref, next)
}

func (s SettingsService) Document(ctx context.Context) (appsettings.Document, error) {
	if s.services.settings == nil {
		return appsettings.Document{Runtime: s.services.Runtime()}, nil
	}
	return s.services.settings.Document(ctx)
}

func (s SettingsService) Configured() bool {
	return s.services.settings != nil
}

func (s SettingsService) View(ctx context.Context) (appviewmodel.SettingsView, error) {
	doc, err := s.Document(ctx)
	if err != nil {
		return appviewmodel.SettingsView{}, err
	}
	return settingsViewFromDocument(doc), nil
}

func (s SettingsService) SetRuntime(ctx context.Context, runtime config.Runtime) (appviewmodel.SettingsView, error) {
	if s.services.settings == nil {
		return appviewmodel.SettingsView{}, errors.New("app/services: settings manager is not configured")
	}
	previous := s.services.Runtime()
	stored, err := s.services.settings.SetRuntime(ctx, runtime)
	if err != nil {
		return appviewmodel.SettingsView{}, err
	}
	if err := s.services.applyRuntimeUpdate(ctx, previous, stored); err != nil {
		return appviewmodel.SettingsView{}, err
	}
	return s.View(ctx)
}

func (s SettingsService) SetStore(ctx context.Context, store config.Store) (appviewmodel.SettingsView, error) {
	return s.updateRuntime(ctx, func(runtime config.Runtime) config.Runtime {
		runtime.Store = store
		return runtime
	})
}

func (s SettingsService) SetSandbox(ctx context.Context, sandbox config.Sandbox) (appviewmodel.SettingsView, error) {
	return s.updateRuntime(ctx, func(runtime config.Runtime) config.Runtime {
		runtime.Sandbox = sandbox
		return runtime
	})
}

func (s SettingsService) SetSandboxBackend(ctx context.Context, backend string) (appviewmodel.SettingsView, error) {
	normalized, err := normalizeSettingsSandboxBackend(backend)
	if err != nil {
		return appviewmodel.SettingsView{}, err
	}
	return s.updateRuntime(ctx, func(runtime config.Runtime) config.Runtime {
		runtime.Sandbox.Backend = normalized
		return runtime
	})
}

func (s SettingsService) SetCompaction(ctx context.Context, policy appsettings.CompactionPolicy) (appviewmodel.SettingsView, error) {
	if s.services.settings == nil {
		return appviewmodel.SettingsView{}, errors.New("app/services: settings manager is not configured")
	}
	if _, err := s.services.settings.SetCompactionPolicy(ctx, policy); err != nil {
		return appviewmodel.SettingsView{}, err
	}
	return s.View(ctx)
}

func (s SettingsService) SetSkillPolicy(ctx context.Context, policy appsettings.SkillPolicy) (appviewmodel.SettingsView, error) {
	if s.services.settings == nil {
		return appviewmodel.SettingsView{}, errors.New("app/services: settings manager is not configured")
	}
	if _, err := s.services.settings.SetSkillPolicy(ctx, policy); err != nil {
		return appviewmodel.SettingsView{}, err
	}
	return s.View(ctx)
}

func (s SettingsService) Save(ctx context.Context, doc appsettings.Document) error {
	if s.services.settings == nil {
		return errors.New("app/services: settings manager is not configured")
	}
	previousDoc, err := s.services.settings.Document(ctx)
	if err != nil {
		return err
	}
	if err := s.services.settings.Save(ctx, doc); err != nil {
		return err
	}
	if err := s.services.applyRuntimeUpdate(ctx, previousDoc.Runtime, appsettings.NormalizeRuntime(doc.Runtime)); err != nil {
		rollbackErr := s.services.settings.Save(ctx, previousDoc)
		if rollbackErr == nil {
			s.services.setRuntime(previousDoc.Runtime)
		}
		return errors.Join(err, rollbackErr)
	}
	return nil
}

func (s SettingsService) updateRuntime(ctx context.Context, update func(config.Runtime) config.Runtime) (appviewmodel.SettingsView, error) {
	if s.services.settings == nil {
		return appviewmodel.SettingsView{}, errors.New("app/services: settings manager is not configured")
	}
	previous := s.services.Runtime()
	runtime := appsettings.NormalizeRuntime(update(s.services.Runtime()))
	stored, err := s.services.settings.SetRuntime(ctx, runtime)
	if err != nil {
		return appviewmodel.SettingsView{}, err
	}
	if err := s.services.applyRuntimeUpdate(ctx, previous, stored); err != nil {
		return appviewmodel.SettingsView{}, err
	}
	return s.View(ctx)
}

func (s Services) applyRuntimeUpdate(ctx context.Context, previous config.Runtime, next config.Runtime) error {
	next = appsettings.NormalizeRuntime(next)
	if s.applyRuntime == nil {
		s.setRuntime(next)
		return nil
	}
	applied, err := s.applyRuntime(ctx, next)
	if err != nil {
		if s.settings != nil {
			rollback, rollbackErr := s.settings.SetRuntime(ctx, previous)
			if rollbackErr == nil {
				s.setRuntime(rollback)
			} else {
				s.setRuntime(previous)
			}
			return errors.Join(err, rollbackErr)
		}
		s.setRuntime(previous)
		return err
	}
	s.setRuntime(applied)
	return nil
}

func normalizeSettingsSandboxBackend(backend string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "", "auto":
		return "auto", nil
	case "host", "seatbelt", "bwrap", "landlock":
		return strings.ToLower(strings.TrimSpace(backend)), nil
	case "windows", "windows-restricted-token", "windows_restricted_token", "windows-elevated", "windows_elevated", "windows elevated", "elevated":
		return "windows", nil
	default:
		return "", fmt.Errorf("app/services: unknown sandbox backend %q", backend)
	}
}

func settingsViewFromDocument(doc appsettings.Document) appviewmodel.SettingsView {
	runtime := appsettings.NormalizeRuntime(doc.Runtime)
	compaction := appsettings.NormalizeCompactionPolicy(doc.Compaction)
	skills := appsettings.NormalizeSkillPolicy(doc.Skills)
	return appviewmodel.SettingsView{
		Runtime: appviewmodel.RuntimeSettings{
			AppName:      runtime.AppName,
			UserID:       runtime.UserID,
			WorkspaceKey: runtime.WorkspaceKey,
			WorkspaceCWD: runtime.WorkspaceCWD,
			Model:        runtime.Model,
		},
		Store: appviewmodel.StoreSettings{
			Backend: runtime.Store.Backend,
			URI:     runtime.Store.URI,
		},
		Sandbox: appviewmodel.SandboxSettings{
			Backend:       runtime.Sandbox.Backend,
			ReadableRoots: slices.Clone(runtime.Sandbox.ReadableRoots),
			WritableRoots: slices.Clone(runtime.Sandbox.WritableRoots),
			Network:       runtime.Sandbox.Network,
			HelperPath:    runtime.Sandbox.HelperPath,
		},
		Compaction: appviewmodel.CompactionSettings{
			Prompt:               compaction.Prompt,
			MaxSourceChars:       compaction.MaxSourceChars,
			AutoMode:             compaction.Auto.Mode,
			AutoWatermarkRatio:   compaction.Auto.WatermarkRatio,
			TaskIndexLimit:       appsettings.CompactionTaskIndexLimit(compaction),
			ControllerIndexLimit: appsettings.CompactionControllerIndexLimit(compaction),
		},
		Skills: appviewmodel.SkillSettings{
			LoadingMode:       appsettings.SkillLoadingMode(skills),
			MaxExpansionChars: appsettings.SkillExpansionBudget(skills),
		},
	}
}

type ModelService struct {
	services Services
}

type ModelProviderFactory func(context.Context, appsettings.ModelConfig) (model.Provider, error)

type CodeFreeAuthRequest struct {
	BaseURL         string        `json:"base_url,omitempty"`
	OpenBrowser     bool          `json:"open_browser,omitempty"`
	CallbackTimeout time.Duration `json:"callback_timeout,omitempty"`
}

type CodeFreeAuthResult struct {
	CredentialPath   string    `json:"credential_path,omitempty"`
	BaseURL          string    `json:"base_url,omitempty"`
	UserID           string    `json:"user_id,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
	HasRefreshToken  bool      `json:"has_refresh_token,omitempty"`
	LoginStarted     bool      `json:"login_started,omitempty"`
}

type CodeFreeAuthenticator interface {
	EnsureAuth(context.Context, CodeFreeAuthRequest) (CodeFreeAuthResult, error)
	EnsureModelSelectionAuth(context.Context, CodeFreeAuthRequest) (CodeFreeAuthResult, error)
	Refresh(context.Context, CodeFreeAuthRequest) (CodeFreeAuthResult, error)
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

func (s ModelService) CurrentReasoningEffort(ctx context.Context, ref session.Ref) (string, error) {
	if s.services.engine == nil {
		return "", nil
	}
	ref = s.withDefaults(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return "", nil
	}
	snapshot, err := s.services.engine.LoadSession(ctx, ref)
	if err != nil {
		return "", err
	}
	value, _ := snapshot.State[StateCurrentReasoningEffort].(string)
	return strings.ToLower(strings.TrimSpace(value)), nil
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
	profile := appsettings.RuntimeModelProfile(cfg)
	if effort, err := s.CurrentReasoningEffort(ctx, ref); err != nil {
		return config.ModelProfile{}, false, err
	} else if effort != "" {
		profile.ReasoningEffort = effort
	}
	return profile, true, nil
}

func (s ModelService) EnsureCodeFreeAuth(ctx context.Context, req CodeFreeAuthRequest) (CodeFreeAuthResult, error) {
	if s.services.codefree == nil {
		return CodeFreeAuthResult{}, errors.New("app/services: codefree auth is not configured")
	}
	return s.services.codefree.EnsureAuth(ctx, req)
}

func (s ModelService) EnsureCodeFreeModelSelectionAuth(ctx context.Context, req CodeFreeAuthRequest) (CodeFreeAuthResult, error) {
	if s.services.codefree == nil {
		return CodeFreeAuthResult{}, errors.New("app/services: codefree auth is not configured")
	}
	return s.services.codefree.EnsureModelSelectionAuth(ctx, req)
}

func (s ModelService) RefreshCodeFreeAuth(ctx context.Context, req CodeFreeAuthRequest) (CodeFreeAuthResult, error) {
	if s.services.codefree == nil {
		return CodeFreeAuthResult{}, errors.New("app/services: codefree auth is not configured")
	}
	return s.services.codefree.Refresh(ctx, req)
}

func (s ModelService) withDefaults(ref session.Ref) session.Ref {
	return defaultSessionRef(s.services.Runtime(), ref)
}

type ResourceService struct {
	services Services
}

func (s ResourceService) Catalog(context.Context) (appresources.Catalog, error) {
	return appresources.CloneCatalog(s.services.resources), nil
}

func (s ResourceService) Diagnostics(context.Context) ([]appresources.Diagnostic, error) {
	return cloneResourceDiagnostics(s.services.resources.Diagnostics), nil
}

type SandboxService struct {
	services Services
}

type sandboxRuntimeUnwrapper interface {
	CurrentSandboxRuntime() (sandbox.Runtime, error)
}

type SandboxDiagnostic struct {
	Severity string            `json:"severity,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	Message  string            `json:"message,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}

type SandboxLifecycleReport struct {
	Action         string `json:"action,omitempty"`
	Backend        string `json:"backend,omitempty"`
	Supported      bool   `json:"supported,omitempty"`
	Attempted      bool   `json:"attempted,omitempty"`
	Noop           bool   `json:"noop,omitempty"`
	FallbackAction string `json:"fallback_action,omitempty"`
	Message        string `json:"message,omitempty"`
	Error          string `json:"error,omitempty"`
}

type SandboxStatus struct {
	RequestedBackend         string                 `json:"requested_backend,omitempty"`
	ResolvedBackend          string                 `json:"resolved_backend,omitempty"`
	Route                    string                 `json:"route,omitempty"`
	Isolation                string                 `json:"isolation,omitempty"`
	DefaultPermission        string                 `json:"default_permission,omitempty"`
	Network                  string                 `json:"network,omitempty"`
	DefaultNetwork           string                 `json:"default_network,omitempty"`
	NetworkControl           bool                   `json:"network_control,omitempty"`
	PathPolicy               bool                   `json:"path_policy,omitempty"`
	ReadableRootCount        int                    `json:"readable_root_count,omitempty"`
	WritableRootCount        int                    `json:"writable_root_count,omitempty"`
	FallbackToHost           bool                   `json:"fallback_to_host,omitempty"`
	FallbackReason           string                 `json:"fallback_reason,omitempty"`
	FallbackInstallHint      string                 `json:"fallback_install_hint,omitempty"`
	Setup                    sandbox.SetupStatus    `json:"setup,omitempty"`
	SetupRequired            bool                   `json:"setup_required,omitempty"`
	SetupError               string                 `json:"setup_error,omitempty"`
	SetupMarkerCurrent       bool                   `json:"setup_marker_current,omitempty"`
	SetupMarkerReason        string                 `json:"setup_marker_reason,omitempty"`
	SandboxRuntimeConfigured bool                   `json:"sandbox_runtime_configured,omitempty"`
	Diagnostics              []SandboxDiagnostic    `json:"diagnostics,omitempty"`
	Lifecycle                SandboxLifecycleReport `json:"lifecycle,omitempty"`
}

func (s SandboxService) Status(ctx context.Context) (SandboxStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SandboxStatus{}, err
	}
	return s.statusFromRuntime(), nil
}

func (s SandboxService) Prepare(ctx context.Context) (SandboxStatus, error) {
	return s.lifecycle(ctx, "prepare", func(ctx context.Context, runtime sandbox.Runtime, report *SandboxLifecycleReport) error {
		preparer, ok := runtime.(sandbox.PreparableRuntime)
		if !ok {
			report.Noop = true
			report.Message = "sandbox backend does not require prepare"
			return nil
		}
		report.Supported = true
		report.Attempted = true
		return preparer.Prepare(ctx)
	})
}

func (s SandboxService) Repair(ctx context.Context) (SandboxStatus, error) {
	return s.lifecycle(ctx, "repair", func(ctx context.Context, runtime sandbox.Runtime, report *SandboxLifecycleReport) error {
		if repairer, ok := runtime.(sandbox.RepairableRuntime); ok {
			report.Supported = true
			report.Attempted = true
			return repairer.Repair(ctx)
		}
		preparer, ok := runtime.(sandbox.PreparableRuntime)
		if !ok {
			report.Noop = true
			report.Message = "sandbox backend does not require repair"
			return nil
		}
		report.Supported = true
		report.Attempted = true
		report.FallbackAction = "prepare"
		return preparer.Prepare(ctx)
	})
}

func (s SandboxService) Preflight(ctx context.Context, allowNonElevatedRepair bool) (SandboxStatus, error) {
	return s.lifecycle(ctx, "preflight", func(ctx context.Context, runtime sandbox.Runtime, report *SandboxLifecycleReport) error {
		preflight, ok := runtime.(sandbox.PreflightRuntime)
		if !ok {
			report.Noop = true
			report.Message = "sandbox backend does not require preflight"
			return nil
		}
		report.Supported = true
		report.Attempted = true
		return preflight.Preflight(ctx, sandbox.PreflightOptions{AllowNonElevatedRepair: allowNonElevatedRepair})
	})
}

func (s SandboxService) Reset(ctx context.Context) (SandboxStatus, error) {
	return s.lifecycle(ctx, "reset", func(ctx context.Context, runtime sandbox.Runtime, report *SandboxLifecycleReport) error {
		resetter, ok := runtime.(sandbox.ResettableRuntime)
		if !ok {
			report.Noop = true
			report.Message = "sandbox backend does not require reset"
			return nil
		}
		report.Supported = true
		report.Attempted = true
		return resetter.Reset(ctx)
	})
}

func (s SandboxService) lifecycle(ctx context.Context, action string, run func(context.Context, sandbox.Runtime, *SandboxLifecycleReport) error) (SandboxStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SandboxStatus{}, err
	}
	report := SandboxLifecycleReport{Action: strings.TrimSpace(action)}
	if s.services.sandbox == nil {
		status := s.statusFromRuntime()
		report.Error = "sandbox runtime is not configured"
		status.Lifecycle = finalizeSandboxLifecycleReport(status, report)
		return status, errors.New("app/services: sandbox runtime is not configured")
	}
	runtime := s.services.sandbox
	if unwrapper, ok := runtime.(sandboxRuntimeUnwrapper); ok {
		current, err := unwrapper.CurrentSandboxRuntime()
		if err != nil {
			status := s.statusFromRuntime()
			report.Error = err.Error()
			status.Lifecycle = finalizeSandboxLifecycleReport(status, report)
			return status, err
		}
		runtime = current
	}
	if run != nil {
		if err := run(ctx, runtime, &report); err != nil {
			status := s.statusFromRuntime()
			report.Error = err.Error()
			status.Lifecycle = finalizeSandboxLifecycleReport(status, report)
			return status, err
		}
	}
	status := s.statusFromRuntime()
	status.Lifecycle = finalizeSandboxLifecycleReport(status, report)
	return status, nil
}

func finalizeSandboxLifecycleReport(status SandboxStatus, report SandboxLifecycleReport) SandboxLifecycleReport {
	report.Action = strings.TrimSpace(report.Action)
	report.Backend = firstNonEmpty(strings.TrimSpace(report.Backend), status.ResolvedBackend, status.RequestedBackend)
	report.FallbackAction = strings.TrimSpace(report.FallbackAction)
	report.Message = strings.TrimSpace(report.Message)
	report.Error = strings.TrimSpace(report.Error)
	if report.Action == "" {
		return SandboxLifecycleReport{}
	}
	if report.Message == "" {
		switch {
		case report.Error != "":
			report.Message = "sandbox " + report.Action + " failed"
		case report.Noop:
			report.Message = "sandbox backend does not require " + report.Action
		case report.FallbackAction != "":
			report.Message = "sandbox " + report.Action + " used " + report.FallbackAction
		default:
			report.Message = "sandbox " + report.Action + " complete"
		}
	}
	return report
}

func (s SandboxService) statusFromRuntime() SandboxStatus {
	runtimeCfg := s.services.Runtime()
	configuredBackend := strings.TrimSpace(runtimeCfg.Sandbox.Backend)
	configuredNetwork := sandbox.NormalizeNetwork(sandbox.Network(runtimeCfg.Sandbox.Network))
	out := SandboxStatus{
		RequestedBackend:  configuredBackend,
		Network:           string(configuredNetwork),
		ReadableRootCount: countConfiguredPaths(runtimeCfg.Sandbox.ReadableRoots),
		WritableRootCount: countConfiguredPaths(runtimeCfg.Sandbox.WritableRoots),
	}
	if s.services.sandbox == nil {
		return out
	}
	status := sandbox.CloneStatus(s.services.sandbox.Status())
	descriptor := sandbox.CloneDescriptor(s.services.sandbox.Descriptor())
	out.SandboxRuntimeConfigured = true
	out.RequestedBackend = firstNonEmpty(configuredBackend, string(status.RequestedBackend), string(descriptor.Backend))
	out.ResolvedBackend = firstNonEmpty(string(status.ResolvedBackend), string(descriptor.Backend), out.RequestedBackend)
	out.Route = firstNonEmpty(string(descriptor.DefaultConstraints.Route), sandboxRouteForBackend(out.ResolvedBackend))
	out.Isolation = firstNonEmpty(string(descriptor.Isolation), string(descriptor.DefaultConstraints.Isolation))
	out.DefaultPermission = strings.TrimSpace(string(descriptor.DefaultConstraints.Permission))
	out.DefaultNetwork = strings.TrimSpace(string(descriptor.DefaultConstraints.Network))
	out.NetworkControl = descriptor.Capabilities.NetworkControl
	out.PathPolicy = descriptor.Capabilities.PathPolicy
	out.FallbackToHost = status.FallbackToHost
	out.FallbackReason = strings.TrimSpace(status.FallbackReason)
	out.FallbackInstallHint = strings.TrimSpace(status.FallbackInstallHint)
	out.Setup = sandbox.CloneSetupStatus(status.Setup)
	out.SetupRequired = status.Setup.Required
	out.SetupError = strings.TrimSpace(status.Setup.Error)
	if global, ok := sandboxSetupCheck(status.Setup, sandbox.SetupGlobal); ok {
		out.SetupMarkerCurrent = global.Current
		out.SetupMarkerReason = strings.TrimSpace(global.Reason)
		if out.SetupError == "" {
			out.SetupError = strings.TrimSpace(global.Error)
		}
		if !out.SetupRequired {
			out.SetupRequired = global.Required && !global.Current
		}
	}
	out.Diagnostics = sandboxDiagnostics(runtimeCfg.Sandbox, descriptor, out)
	return out
}

func sandboxRouteForBackend(backend string) string {
	if strings.EqualFold(strings.TrimSpace(backend), string(sandbox.BackendHost)) {
		return string(sandbox.RouteHost)
	}
	if strings.TrimSpace(backend) == "" {
		return ""
	}
	return string(sandbox.RouteSandbox)
}

func sandboxSetupCheck(status sandbox.SetupStatus, scope sandbox.SetupScope) (sandbox.SetupCheck, bool) {
	for _, check := range status.Checks {
		if check.Scope == scope {
			return check, true
		}
	}
	return sandbox.SetupCheck{}, false
}

func sandboxDiagnostics(cfg config.Sandbox, descriptor sandbox.Descriptor, status SandboxStatus) []SandboxDiagnostic {
	var out []SandboxDiagnostic
	if status.FallbackToHost {
		out = append(out, SandboxDiagnostic{
			Severity: appresources.DiagnosticWarning,
			Kind:     "fallback",
			Message:  firstNonEmpty(status.FallbackReason, "sandbox backend fell back to host"),
			Meta: sandboxDiagnosticMeta(map[string]string{
				"requested_backend": status.RequestedBackend,
				"resolved_backend":  status.ResolvedBackend,
				"route":             status.Route,
			}),
		})
	}
	if status.SetupRequired || status.SetupError != "" {
		severity := appresources.DiagnosticWarning
		if status.SetupError != "" {
			severity = appresources.DiagnosticError
		}
		out = append(out, SandboxDiagnostic{
			Severity: severity,
			Kind:     "setup",
			Message:  firstNonEmpty(status.SetupError, status.SetupMarkerReason, "sandbox setup is required"),
			Meta: sandboxDiagnosticMeta(map[string]string{
				"requested_backend": status.RequestedBackend,
				"resolved_backend":  status.ResolvedBackend,
			}),
		})
	}
	if strings.EqualFold(strings.TrimSpace(status.Route), string(sandbox.RouteHost)) {
		out = append(out, SandboxDiagnostic{
			Severity: appresources.DiagnosticWarning,
			Kind:     "route",
			Message:  "sandbox commands use host execution",
			Meta: sandboxDiagnosticMeta(map[string]string{
				"backend":    status.ResolvedBackend,
				"isolation":  status.Isolation,
				"permission": status.DefaultPermission,
			}),
		})
	}
	configuredNetwork := sandbox.NormalizeNetwork(sandbox.Network(cfg.Network))
	if configuredNetwork != sandbox.NetworkInherit && !descriptor.Capabilities.NetworkControl {
		out = append(out, SandboxDiagnostic{
			Severity: appresources.DiagnosticWarning,
			Kind:     "network",
			Message:  "sandbox network policy is configured but this backend does not enforce network control",
			Meta: sandboxDiagnosticMeta(map[string]string{
				"configured_network": string(configuredNetwork),
				"default_network":    status.DefaultNetwork,
				"backend":            status.ResolvedBackend,
			}),
		})
	}
	if countConfiguredPaths(cfg.ReadableRoots)+countConfiguredPaths(cfg.WritableRoots) > 0 && !descriptor.Capabilities.PathPolicy {
		out = append(out, SandboxDiagnostic{
			Severity: appresources.DiagnosticWarning,
			Kind:     "roots",
			Message:  "sandbox root policy is configured but this backend does not enforce command path policy",
			Meta: sandboxDiagnosticMeta(map[string]string{
				"readable_roots": fmt.Sprint(countConfiguredPaths(cfg.ReadableRoots)),
				"writable_roots": fmt.Sprint(countConfiguredPaths(cfg.WritableRoots)),
				"backend":        status.ResolvedBackend,
			}),
		})
	}
	return out
}

func sandboxDiagnosticMeta(in map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range in {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func countConfiguredPaths(paths []string) int {
	count := 0
	for _, path := range paths {
		if strings.TrimSpace(path) != "" {
			count++
		}
	}
	return count
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

type StatusRequest struct {
	SessionRef         session.Ref `json:"session_ref,omitempty"`
	IncludeDiagnostics bool        `json:"include_diagnostics,omitempty"`
}

type StatusService struct {
	services Services
}

func (s StatusService) View(ctx context.Context, req StatusRequest) (appviewmodel.StatusView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runtimeCfg := s.services.Runtime()
	ref := defaultSessionRef(runtimeCfg, req.SessionRef)
	view := appviewmodel.StatusView{
		Runtime:   appviewmodel.RuntimeStatusFromConfig(runtimeCfg),
		Resources: statusResourceView(s.services.resources),
	}
	if req.IncludeDiagnostics {
		sandboxStatus, err := s.services.Sandbox().Status(ctx)
		if err != nil {
			return appviewmodel.StatusView{}, err
		}
		view.Sandbox = statusSandboxView(sandboxStatus)
	}
	agents, err := s.services.Agents().List(ctx)
	if err != nil {
		return appviewmodel.StatusView{}, err
	}
	view.Agents = statusAgentView(agents)
	var state session.State
	if strings.TrimSpace(ref.SessionID) != "" {
		snapshot, err := s.services.Sessions().Load(ctx, ref)
		if err != nil {
			return appviewmodel.StatusView{}, err
		}
		state = cloneState(snapshot.State)
		sessionView := appviewmodel.FromSnapshot(snapshot)
		sessionStatus := appviewmodel.SessionStatusFromView(sessionView)
		view.Session = &sessionStatus
		view.Usage = statusUsageView(snapshot)
		view.Permissions = statusPermissionView(snapshot)
		budget, err := s.contextBudget(ctx, snapshot)
		if err != nil {
			return appviewmodel.StatusView{}, err
		}
		view.Usage.ContextBudget = budget
		controller := controllerFromSnapshot(snapshot)
		if controller.Kind == session.ControllerACP {
			controllerStatus, err := s.services.Controllers().statusFromSnapshot(ctx, snapshot, controller)
			if err != nil {
				return appviewmodel.StatusView{}, err
			}
			view.Controller = controllerStatusView(controllerStatus)
		}
	}
	modelStatus, err := s.modelStatus(ctx, state)
	if err != nil {
		return appviewmodel.StatusView{}, err
	}
	view.Model = modelStatus
	view.Mode = s.modeStatus(state)
	return view, nil
}

func (s StatusService) modelStatus(ctx context.Context, state session.State) (appviewmodel.ModelStatus, error) {
	choices, err := s.services.Models().List(ctx)
	if err != nil {
		return appviewmodel.ModelStatus{}, err
	}
	status := appviewmodel.ModelStatus{
		Configured: len(choices) > 0,
		Count:      len(choices),
		Choices:    statusModelChoices(choices),
	}
	if s.services.settings == nil || len(choices) == 0 {
		return status, nil
	}
	modelID := stateString(state, StateCurrentModelID)
	var current appsettings.ModelConfig
	if modelID != "" {
		current, err = s.services.settings.ResolveModel(modelID)
	} else {
		current, err = s.services.settings.ResolveModel("")
	}
	if err != nil {
		return appviewmodel.ModelStatus{}, err
	}
	choice := statusModelChoice(appsettings.ModelChoiceFromConfig(current, modelChoiceIsDefault(choices, current.ID)))
	status.Current = &choice
	status.ReasoningEffort = firstNonEmpty(
		strings.ToLower(stateString(state, StateCurrentReasoningEffort)),
		current.ReasoningEffort,
		current.DefaultReasoningEffort,
	)
	if tokenEnv := strings.TrimSpace(current.TokenEnv); tokenEnv != "" {
		status.MissingAPIKey = strings.TrimSpace(os.Getenv(tokenEnv)) == ""
	}
	return status, nil
}

func (s StatusService) modeStatus(state session.State) appviewmodel.ModeStatus {
	choices := sessionModeChoices()
	modeID := defaultSessionMode(stateString(state, StateSessionMode))
	current, _ := lookupSessionMode(modeID)
	return appviewmodel.ModeStatus{
		Current: statusModeChoice(current),
		Choices: statusModeChoices(choices),
	}
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
	Env         map[string]string `json:"env,omitempty"`
	WorkDir     string            `json:"work_dir,omitempty"`
	Description string            `json:"description,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

type RegisterBuiltinAgentOptions struct {
	Install bool
}

type AgentInstallOption struct {
	Value   string `json:"value,omitempty"`
	Display string `json:"display,omitempty"`
	Detail  string `json:"detail,omitempty"`
}

type AgentInstaller interface {
	InstallBuiltinACPAgent(context.Context, AgentDescriptor) (AgentDescriptor, error)
	InstallableBuiltinACPAgentOptions(context.Context, []AgentDescriptor) ([]AgentInstallOption, error)
}

type AgentInstallError struct {
	Agent   string
	Command []string
	Output  string
	Err     error
}

func (e *AgentInstallError) Error() string {
	if e == nil {
		return ""
	}
	agent := strings.TrimSpace(e.Agent)
	if agent == "" {
		agent = "unknown"
	}
	errText := "failed"
	if e.Err != nil {
		errText = e.Err.Error()
	}
	msg := fmt.Sprintf("app/services: install ACP agent %q: %s", agent, errText)
	if out := strings.TrimSpace(e.Output); out != "" {
		msg += "\n" + out
	}
	return msg
}

func (e *AgentInstallError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *AgentInstallError) CommandString() string {
	if e == nil {
		return ""
	}
	return strings.Join(e.Command, " ")
}

type AgentService struct {
	services Services
}

func (s AgentService) List(context.Context) ([]AgentDescriptor, error) {
	disabled := map[string]struct{}{}
	if s.services.settings == nil {
		return cloneAgents(s.services.agents), nil
	}
	for _, name := range s.services.settings.ListDisabledACPAgents() {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			disabled[name] = struct{}{}
		}
	}
	agents := make([]AgentDescriptor, 0, len(s.services.agents))
	for _, agent := range cloneAgents(s.services.agents) {
		if agentDisabled(agent, disabled) {
			continue
		}
		agents = append(agents, agent)
	}
	for _, agent := range s.services.settings.ListACPAgents() {
		descriptor := agentDescriptorFromPlugin(agent)
		if agentDisabled(descriptor, disabled) {
			continue
		}
		agents = upsertAgentDescriptor(agents, descriptor)
	}
	return agents, nil
}

func (s AgentService) RegisterCustom(ctx context.Context, agent AgentDescriptor) (AgentDescriptor, error) {
	if s.services.settings == nil {
		return AgentDescriptor{}, errors.New("app/services: settings manager is not configured")
	}
	agent = normalizeAgentDescriptor(agent)
	agent.Kind = AgentKindExternalACP
	if agent.ID == "" {
		agent.ID = firstNonEmpty(agent.Name, agent.Command)
	}
	if agent.Name == "" {
		agent.Name = agent.ID
	}
	if agent.ID == "" || agent.Name == "" {
		return AgentDescriptor{}, errors.New("app/services: ACP agent name is required")
	}
	if reservedSlashCommandName(agent.Name) || reservedSlashCommandName(agent.ID) {
		return AgentDescriptor{}, fmt.Errorf("app/services: ACP agent %q conflicts with an existing slash command", agent.Name)
	}
	if strings.TrimSpace(agent.Command) == "" {
		return AgentDescriptor{}, fmt.Errorf("app/services: command is required for ACP agent %q", agent.Name)
	}
	stored, err := s.services.settings.UpsertACPAgent(ctx, pluginDescriptorFromAgent(agent))
	if err != nil {
		return AgentDescriptor{}, err
	}
	return agentDescriptorFromPlugin(stored), nil
}

func (s AgentService) ListBuiltins(context.Context) ([]AgentDescriptor, error) {
	return cloneAgents(s.services.builtins), nil
}

func (s AgentService) RegisterBuiltin(ctx context.Context, name string) (AgentDescriptor, error) {
	return s.RegisterBuiltinWithOptions(ctx, name, RegisterBuiltinAgentOptions{})
}

func (s AgentService) RegisterBuiltinWithOptions(ctx context.Context, name string, opts RegisterBuiltinAgentOptions) (AgentDescriptor, error) {
	if s.services.settings == nil {
		return AgentDescriptor{}, errors.New("app/services: settings manager is not configured")
	}
	agent, ok := s.lookupBuiltin(name)
	if !ok {
		return AgentDescriptor{}, fmt.Errorf("app/services: unknown builtin ACP agent %q", strings.TrimSpace(name))
	}
	if reservedSlashCommandName(agent.Name) || reservedSlashCommandName(agent.ID) {
		return AgentDescriptor{}, fmt.Errorf("app/services: ACP agent %q conflicts with an existing slash command", agent.Name)
	}
	if strings.TrimSpace(agent.Command) == "" {
		return AgentDescriptor{}, fmt.Errorf("app/services: command is required for ACP agent %q", agent.Name)
	}
	if opts.Install {
		if s.services.installer == nil {
			return AgentDescriptor{}, fmt.Errorf("app/services: ACP agent %q does not support local install", agent.Name)
		}
		installed, err := s.services.installer.InstallBuiltinACPAgent(ctx, agent)
		if err != nil {
			return AgentDescriptor{}, err
		}
		agent = normalizeAgentDescriptor(installed)
		if agent.ID == "" {
			agent.ID = firstNonEmpty(agent.Name, name)
		}
		if agent.Name == "" {
			agent.Name = agent.ID
		}
		if strings.TrimSpace(agent.Command) == "" {
			return AgentDescriptor{}, fmt.Errorf("app/services: installed ACP agent %q has no command", agent.Name)
		}
	}
	stored, err := s.services.settings.UpsertACPAgent(ctx, pluginDescriptorFromAgent(agent))
	if err != nil {
		return AgentDescriptor{}, err
	}
	return agentDescriptorFromPlugin(stored), nil
}

func (s AgentService) ListInstallableBuiltins(ctx context.Context) ([]AgentInstallOption, error) {
	if s.services.installer == nil {
		return nil, nil
	}
	return s.services.installer.InstallableBuiltinACPAgentOptions(ctx, cloneAgents(s.services.builtins))
}

func (s AgentService) Remove(ctx context.Context, name string) error {
	if s.services.settings == nil {
		return errors.New("app/services: settings manager is not configured")
	}
	if tombstones := s.staticAgentDisableNames(name); len(tombstones) > 0 {
		for _, tombstone := range tombstones {
			if err := s.services.settings.DisableACPAgent(ctx, tombstone); err != nil {
				return err
			}
		}
		return nil
	}
	return s.services.settings.DeleteACPAgent(ctx, name)
}

func (s AgentService) lookupBuiltin(name string) (AgentDescriptor, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return AgentDescriptor{}, false
	}
	for _, agent := range s.services.builtins {
		agent = normalizeAgentDescriptor(agent)
		if strings.EqualFold(strings.TrimSpace(agent.ID), name) || strings.EqualFold(strings.TrimSpace(agent.Name), name) {
			return agent, true
		}
	}
	return AgentDescriptor{}, false
}

type AgentInvoker interface {
	Invoke(context.Context, AgentInvokeRequest) (AgentInvokeResult, error)
}

type AgentInvokerFactory func(AgentDescriptor) (AgentInvoker, error)

type AgentInvokerFunc func(context.Context, AgentInvokeRequest) (AgentInvokeResult, error)

func (f AgentInvokerFunc) Invoke(ctx context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
	if f == nil {
		return AgentInvokeResult{}, errors.New("app/services: agent invoker is nil")
	}
	return f(ctx, req)
}

type AgentInvokeRequest struct {
	AgentID                   string
	SessionRef                session.Ref
	TurnID                    string
	Controller                session.ControllerBinding
	ControllerModel           string
	ControllerReasoningEffort string
	ControllerMode            string
	ControllerConfigIntent    map[string]string
	Participant               session.ParticipantBinding
	Input                     string
	ContentParts              []model.ContentPart
	// DeferRecord returns normalized events without writing them, so callers can
	// persist a larger atomic event batch.
	DeferRecord bool
}

type AgentInvokeResult struct {
	StopReason              string
	Events                  []session.Event
	Cursor                  session.Cursor
	Recorded                bool
	ControllerConfigOptions []control.ConfigOption
}

func (s AgentService) Invoke(ctx context.Context, req AgentInvokeRequest) (AgentInvokeResult, error) {
	agentID := strings.TrimSpace(req.AgentID)
	if agentID == "" {
		return AgentInvokeResult{}, errors.New("app/services: agent id is required")
	}
	invoker := s.services.invokers[agentID]
	if invoker == nil {
		var err error
		invoker, err = s.invokerForAgent(ctx, agentID)
		if err != nil {
			return AgentInvokeResult{}, err
		}
	}
	if invoker == nil {
		return AgentInvokeResult{}, errors.New("app/services: agent invoker not found")
	}
	ref := session.NormalizeRef(req.SessionRef)
	runtimeCfg := s.services.Runtime()
	if ref.AppName == "" {
		ref.AppName = runtimeCfg.AppName
	}
	if ref.UserID == "" {
		ref.UserID = runtimeCfg.UserID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(runtimeCfg.WorkspaceKey)
	}
	req.SessionRef = ref
	req.AgentID = agentID
	req.ContentParts = model.CloneContentParts(req.ContentParts)
	req.ControllerConfigIntent = cloneStringMap(req.ControllerConfigIntent)
	controllerMode := req.Controller.Kind != "" || strings.TrimSpace(req.Controller.ID) != "" || strings.TrimSpace(req.Controller.AgentName) != ""
	if controllerMode {
		req.Controller = normalizeAgentController(req.Controller, agentID)
		if status, ok, err := s.services.Controllers().Status(ctx, ref); err != nil {
			return AgentInvokeResult{}, err
		} else if ok {
			req.ControllerModel = firstNonEmpty(strings.TrimSpace(req.ControllerModel), strings.TrimSpace(status.Model))
			req.ControllerReasoningEffort = firstNonEmpty(strings.TrimSpace(req.ControllerReasoningEffort), strings.TrimSpace(status.ReasoningEffort))
			req.ControllerMode = firstNonEmpty(strings.TrimSpace(req.ControllerMode), strings.TrimSpace(status.Mode))
			req.ControllerConfigIntent = mergeStringMaps(controllerConfigIntent(status.ConfigOptions), req.ControllerConfigIntent)
		}
	} else {
		req.Participant = normalizeAgentParticipant(req.Participant, agentID)
	}
	result, err := invoker.Invoke(ctx, req)
	if err != nil {
		return AgentInvokeResult{}, err
	}
	var events []session.Event
	if controllerMode {
		events = normalizeAgentControllerEvents(ref.SessionID, req.Controller, req.TurnID, cloneEvents(result.Events))
	} else {
		events = normalizeAgentInvokeEvents(ref.SessionID, req.Participant, req.TurnID, cloneEvents(result.Events))
	}
	if len(events) > 0 && !result.Recorded && !req.DeferRecord {
		cursor, err := s.services.engine.RecordEvents(ctx, ref, events)
		if err != nil {
			return AgentInvokeResult{}, err
		}
		result.Cursor = cursor
	}
	if controllerMode && len(result.ControllerConfigOptions) > 0 {
		if err := s.persistControllerConfigOptions(ctx, ref, req.Controller, result.ControllerConfigOptions); err != nil {
			return AgentInvokeResult{}, err
		}
	}
	result.Events = events
	return result, nil
}

func (s AgentService) persistControllerConfigOptions(ctx context.Context, ref session.Ref, controller session.ControllerBinding, options []control.ConfigOption) error {
	if s.services.engine == nil || len(options) == 0 {
		return nil
	}
	configRef := controllerConfigRef(controller)
	return s.services.engine.UpdateSessionState(ctx, ref, func(state session.State) (session.State, error) {
		next := cloneState(state)
		if next == nil {
			next = session.State{}
		}
		next[StateControllerConfigRef] = configRef
		next[StateControllerConfigOptions] = cloneControllerConfigOptions(options)
		if value, ok := currentControllerConfigValue(options, "model"); ok {
			next[StateControllerModel] = value
		}
		if value, ok := currentControllerConfigValue(options, "reasoning"); ok {
			next[StateControllerReasoning] = value
		}
		if value, ok := currentControllerConfigValue(options, "mode"); ok {
			next[StateControllerMode] = value
		}
		return next, nil
	})
}

func (s AgentService) invokerForAgent(ctx context.Context, agentID string) (AgentInvoker, error) {
	if s.services.factory == nil {
		return nil, fmt.Errorf("app/services: agent invoker %q not found", agentID)
	}
	agents, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, agent := range agents {
		if strings.EqualFold(strings.TrimSpace(agent.ID), agentID) || strings.EqualFold(strings.TrimSpace(agent.Name), agentID) {
			return s.services.factory(agent)
		}
	}
	return nil, fmt.Errorf("app/services: agent invoker %q not found", agentID)
}

func normalizeAgentController(in session.ControllerBinding, agentID string) session.ControllerBinding {
	out := in
	out.ID = firstNonEmpty(out.ID, agentID)
	out.AgentName = firstNonEmpty(out.AgentName, agentID)
	out.Label = firstNonEmpty(out.Label, out.AgentName, out.ID)
	out.Source = firstNonEmpty(out.Source, "app_agent")
	if out.Kind == "" {
		out.Kind = session.ControllerACP
	}
	return out
}

func normalizeAgentParticipant(in session.ParticipantBinding, agentID string) session.ParticipantBinding {
	out := in
	out.ID = firstNonEmpty(out.ID, agentID)
	out.AgentName = firstNonEmpty(out.AgentName, agentID)
	out.Label = firstNonEmpty(out.Label, out.AgentName, out.ID)
	out.Source = firstNonEmpty(out.Source, "app_agent")
	if out.Kind == "" {
		out.Kind = session.ParticipantACP
	}
	if out.Role == "" {
		out.Role = session.ParticipantDelegated
	}
	return out
}

func normalizeAgentControllerEvents(sessionID string, controller session.ControllerBinding, turnID string, events []session.Event) []session.Event {
	if len(events) == 0 {
		return nil
	}
	controller = normalizeAgentController(controller, controller.ID)
	turnID = strings.TrimSpace(turnID)
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		next := session.CloneEvent(event)
		if strings.TrimSpace(next.SessionID) == "" {
			next.SessionID = strings.TrimSpace(sessionID)
		}
		if next.Visibility == "" {
			next.Visibility = session.VisibilityCanonical
		}
		if next.Scope == nil {
			next.Scope = &session.EventScope{}
		}
		if next.Scope.TurnID == "" {
			next.Scope.TurnID = turnID
		}
		next.Scope.Source = firstNonEmpty(next.Scope.Source, controller.Source, "app_agent")
		if next.Scope.Controller.Kind == "" && strings.TrimSpace(next.Scope.Controller.ID) == "" {
			next.Scope.Controller = controller
		}
		next.Scope.Participant = session.ParticipantBinding{}
		if next.Actor.Kind == "" || next.Actor.Kind == session.ActorParticipant {
			next.Actor = session.ActorRef{
				Kind: session.ActorController,
				ID:   controller.ID,
				Name: firstNonEmpty(controller.Label, controller.AgentName, controller.ID),
			}
		}
		out = append(out, next)
	}
	return out
}

func normalizeAgentInvokeEvents(sessionID string, participant session.ParticipantBinding, turnID string, events []session.Event) []session.Event {
	if len(events) == 0 {
		return nil
	}
	participant = normalizeAgentParticipant(participant, participant.ID)
	turnID = strings.TrimSpace(turnID)
	out := make([]session.Event, 0, len(events))
	for _, event := range events {
		next := session.CloneEvent(event)
		if strings.TrimSpace(next.SessionID) == "" {
			next.SessionID = strings.TrimSpace(sessionID)
		}
		if next.Visibility == "" {
			next.Visibility = session.VisibilityCanonical
		}
		if next.Scope == nil {
			next.Scope = &session.EventScope{}
		}
		if next.Scope.TurnID == "" {
			next.Scope.TurnID = turnID
		}
		next.Scope.Source = firstNonEmpty(next.Scope.Source, participant.Source, "app_agent")
		if strings.TrimSpace(next.Scope.Participant.ID) == "" {
			next.Scope.Participant = participant
		}
		if next.Actor.Kind == "" {
			next.Actor = session.ActorRef{
				Kind: session.ActorParticipant,
				ID:   participant.ID,
				Name: firstNonEmpty(participant.Label, participant.AgentName, participant.ID),
			}
		}
		out = append(out, next)
	}
	return out
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
	Workspace     session.Workspace
	AllWorkspaces bool
	Search        string
	After         session.Cursor
	Limit         int
}

func (s SessionService) Start(ctx context.Context, req StartSessionRequest) (session.Session, error) {
	if s.services.engine == nil {
		return session.Session{}, errors.New("app/services: runtime engine is required")
	}
	runtimeCfg := s.services.Runtime()
	return s.services.engine.StartSession(ctx, session.StartRequest{
		AppName:            runtimeCfg.AppName,
		UserID:             runtimeCfg.UserID,
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
	runtimeCfg := s.services.Runtime()
	workspace := session.Workspace{}
	if !req.AllWorkspaces {
		workspace = s.workspaceWithDefaults(req.Workspace)
	}
	page, err := s.services.engine.ListSessions(ctx, session.ListQuery{
		Ref: session.Ref{
			AppName:      runtimeCfg.AppName,
			UserID:       runtimeCfg.UserID,
			WorkspaceKey: workspace.Key,
		},
		WorkspaceCWD: workspace.CWD,
		Search:       strings.TrimSpace(req.Search),
		After:        req.After,
		Limit:        req.Limit,
	})
	if err != nil {
		return session.SessionPage{}, err
	}
	return s.enrichListTitles(ctx, page), nil
}

func (s SessionService) Load(ctx context.Context, ref session.Ref) (session.Snapshot, error) {
	if s.services.engine == nil {
		return session.Snapshot{}, errors.New("app/services: runtime engine is required")
	}
	ref = s.withDefaults(ref)
	return s.services.engine.LoadSession(ctx, ref)
}

func (s SessionService) withDefaults(ref session.Ref) session.Ref {
	return defaultSessionRef(s.services.Runtime(), ref)
}

func (s SessionService) workspaceWithDefaults(workspace session.Workspace) session.Workspace {
	workspace.Key = strings.TrimSpace(workspace.Key)
	workspace.CWD = strings.TrimSpace(workspace.CWD)
	runtimeCfg := s.services.Runtime()
	if workspace.Key == "" {
		workspace.Key = strings.TrimSpace(runtimeCfg.WorkspaceKey)
	}
	if workspace.CWD == "" {
		workspace.CWD = strings.TrimSpace(runtimeCfg.WorkspaceCWD)
	}
	return workspace
}

func (s SessionService) enrichListTitles(ctx context.Context, page session.SessionPage) session.SessionPage {
	out := session.CloneSessionPage(page)
	for i := range out.Sessions {
		if strings.TrimSpace(out.Sessions[i].Session.Title) != "" {
			continue
		}
		snapshot, err := s.Load(ctx, out.Sessions[i].Session.Ref)
		if err != nil {
			continue
		}
		out.Sessions[i].Session.Title = deriveSessionTitle(snapshot, 96)
	}
	return out
}

func deriveSessionTitle(snapshot session.Snapshot, limit int) string {
	for _, event := range snapshot.Events {
		if event.Type != session.EventUser || session.IsTransient(event) {
			continue
		}
		if title := compactSessionTitle(session.EventText(event), limit); title != "" {
			return title
		}
	}
	for _, event := range snapshot.Events {
		if session.IsTransient(event) {
			continue
		}
		if title := compactSessionTitle(session.EventText(event), limit); title != "" {
			return title
		}
	}
	return ""
}

func compactSessionTitle(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	suffix := []rune("...")
	if limit <= len(suffix) {
		return string(runes[:limit])
	}
	return strings.TrimSpace(string(runes[:limit-len(suffix)])) + string(suffix)
}

type TurnService struct {
	services Services
}

type BeginTurnRequest struct {
	SessionRef   session.Ref
	Input        string
	ContentParts []model.ContentPart
	Instructions []string
	Model        string
	Reasoning    model.ReasoningConfig
	Surface      string
	Mode         string
	Meta         map[string]any
}

func (s TurnService) Begin(ctx context.Context, req BeginTurnRequest) (coreruntime.Turn, error) {
	if s.services.engine == nil {
		return nil, errors.New("app/services: runtime engine is required")
	}
	ref := defaultSessionRef(s.services.Runtime(), req.SessionRef)
	if turn, ok, err := s.beginControllerTurn(ctx, ref, req); err != nil || ok {
		return turn, err
	}
	modelRef := strings.TrimSpace(req.Model)
	if modelRef == "" && s.services.settings != nil {
		if cfg, ok, err := s.services.Models().Current(ctx, ref); err == nil && ok {
			modelRef = cfg.ID
		}
	}
	reasoning := req.Reasoning
	if reasoning.Effort == "" {
		effort, err := s.services.Models().CurrentReasoningEffort(ctx, ref)
		if err != nil {
			return nil, err
		}
		reasoning.Effort = effort
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		var err error
		mode, err = s.services.Modes().CurrentID(ctx, ref)
		if err != nil {
			return nil, err
		}
	} else {
		mode = coreruntime.NormalizeSessionMode(mode)
		if mode == "" {
			return nil, fmt.Errorf("app/services: unknown session mode %q", strings.TrimSpace(req.Mode))
		}
	}
	prefixEvents, err := s.autoCompactBeforeTurn(ctx, ref, req, modelRef)
	if err != nil {
		return nil, err
	}
	instructions := cloneStrings(req.Instructions)
	skillInstructions, err := s.skillInstructions(ctx, req)
	if err != nil {
		return nil, err
	}
	instructions = append(instructions, skillInstructions...)
	turn, err := s.services.engine.BeginTurn(ctx, coreruntime.TurnRequest{
		SessionRef:   ref,
		Input:        req.Input,
		ContentParts: model.CloneContentParts(req.ContentParts),
		Instructions: instructions,
		Model:        modelRef,
		Reasoning:    reasoning,
		Surface:      strings.TrimSpace(req.Surface),
		Mode:         mode,
		Meta:         maps.Clone(req.Meta),
	})
	if err != nil {
		return nil, err
	}
	return turnWithPrefixedEvents(turn, prefixEvents), nil
}

func (s TurnService) beginControllerTurn(ctx context.Context, ref session.Ref, req BeginTurnRequest) (coreruntime.Turn, bool, error) {
	snapshot, controller, ok, err := s.services.Controllers().activeControllerSnapshot(ctx, ref)
	if err != nil || !ok {
		return nil, false, err
	}
	agentID := firstNonEmpty(controller.AgentName, controller.ID, controller.Label)
	if agentID == "" {
		return nil, true, errors.New("app/services: active ACP controller agent is unavailable")
	}
	controller = normalizeAgentController(controller, agentID)
	turnID, runID, startedAt := newCompletedTurnIdentity("controller")
	turnEvents := make([]completedTurnEvent, 0, 2)
	if event := controllerTurnUserEvent(snapshot.Session.Ref.SessionID, controller, req, turnID, startedAt); event.Type != "" {
		cursor, err := s.services.engine.RecordEvents(ctx, snapshot.Session.Ref, []session.Event{event})
		if err != nil {
			return nil, true, err
		}
		turnEvents = append(turnEvents, completedTurnEvent{cursor: cursor, event: event})
	}
	result, err := s.services.Agents().Invoke(ctx, AgentInvokeRequest{
		AgentID:      agentID,
		SessionRef:   snapshot.Session.Ref,
		TurnID:       turnID,
		Controller:   controller,
		Input:        req.Input,
		ContentParts: model.CloneContentParts(req.ContentParts),
	})
	if err != nil {
		return nil, true, err
	}
	turnEvents = append(turnEvents, completedTurnEventsFromAgentResult(result)...)
	return newCompletedTurn(snapshot.Session.Ref, turnID, runID, startedAt, turnEvents), true, nil
}

func (s TurnService) skillInstructions(ctx context.Context, req BeginTurnRequest) ([]string, error) {
	refs := skillRefsFromTurn(req)
	if len(refs) == 0 || len(s.services.resources.Skills) == 0 {
		return nil, nil
	}
	policy := appsettings.SkillPolicy{}
	if s.services.settings != nil {
		policy = s.services.settings.SkillPolicy()
	}
	remaining := appsettings.SkillExpansionBudget(policy)
	if remaining <= 0 {
		return nil, nil
	}
	byName := map[string]plugin.SkillDescriptor{}
	for _, skill := range s.services.resources.Skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			continue
		}
		byName[strings.ToLower(name)] = skill
	}
	var out []string
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		skill, ok := byName[strings.ToLower(ref)]
		if !ok || len(skill.Paths) == 0 || remaining <= 0 {
			continue
		}
		path := strings.TrimSpace(skill.Paths[0])
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("app/services: load skill %q from %s: %w", skill.Name, path, err)
		}
		content := strings.TrimSpace(string(raw))
		if content == "" {
			continue
		}
		content, consumed := boundedSkillContent(content, remaining)
		remaining -= consumed
		out = append(out, renderSkillInstruction(skill, path, content))
	}
	return out, nil
}

func controllerTurnUserEvent(sessionID string, controller session.ControllerBinding, req BeginTurnRequest, turnID string, startedAt time.Time) session.Event {
	parts := commandMessageParts(req.Input, req.ContentParts)
	if len(parts) == 0 {
		return session.Event{}
	}
	return session.Event{
		Type:       session.EventUser,
		SessionID:  strings.TrimSpace(sessionID),
		Visibility: session.VisibilityCanonical,
		Time:       startedAt,
		Actor:      session.ActorRef{Kind: session.ActorUser, ID: "user", Name: "user"},
		Scope: &session.EventScope{
			TurnID:     strings.TrimSpace(turnID),
			Source:     firstNonEmpty(req.Surface, controller.Source, "app_controller_turn"),
			Controller: controller,
		},
		Message: &model.Message{
			Role:  model.RoleUser,
			Parts: parts,
		},
		Meta: map[string]any{
			"agent":  strings.TrimSpace(controller.AgentName),
			"handle": strings.TrimSpace(firstNonEmpty(controller.Label, controller.ID)),
		},
	}
}

func completedTurnEventsFromAgentResult(result AgentInvokeResult) []completedTurnEvent {
	if len(result.Events) == 0 {
		return nil
	}
	out := make([]completedTurnEvent, 0, len(result.Events))
	for _, event := range result.Events {
		cursor := session.Cursor(strings.TrimSpace(event.ID))
		if cursor == "" {
			cursor = result.Cursor
		}
		out = append(out, completedTurnEvent{cursor: cursor, event: event})
	}
	return out
}

func boundedSkillContent(content string, remaining int) (string, int) {
	if remaining <= 0 {
		return "", 0
	}
	runes := []rune(content)
	if len(runes) <= remaining {
		return content, len(runes)
	}
	return string(runes[:remaining]) + "\n\n[Skill content truncated by prompt budget.]", remaining
}

func renderSkillInstruction(skill plugin.SkillDescriptor, path string, content string) string {
	name := strings.TrimSpace(skill.Name)
	return strings.TrimSpace(fmt.Sprintf(`## Skill: %s
The user explicitly referenced $%s. Apply this skill when it is relevant to the turn.
Source: %s

<skill_content>
%s
</skill_content>`, name, name, strings.TrimSpace(path), strings.TrimSpace(content)))
}

func skillRefsFromTurn(req BeginTurnRequest) []string {
	var texts []string
	if strings.TrimSpace(req.Input) != "" {
		texts = append(texts, req.Input)
	}
	for _, part := range req.ContentParts {
		if part.Type == model.ContentPartText && strings.TrimSpace(part.Text) != "" {
			texts = append(texts, part.Text)
		}
	}
	return skillRefsFromText(strings.Join(texts, "\n"))
}

func skillRefsFromText(text string) []string {
	runes := []rune(text)
	var out []string
	seen := map[string]struct{}{}
	for i := 0; i < len(runes); i++ {
		if runes[i] != '$' || (i > 0 && !skillRefBoundary(runes[i-1])) {
			continue
		}
		start := i + 1
		end := start
		for end < len(runes) && isSkillRefRune(runes[end]) {
			end++
		}
		if end == start {
			continue
		}
		name := string(runes[start:end])
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			i = end - 1
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
		i = end - 1
	}
	return out
}

func skillRefBoundary(prev rune) bool {
	switch prev {
	case ' ', '\t', '\n', '\r', '(', '[', '{', ',', ';', ':', '"', '\'':
		return true
	default:
		return false
	}
}

func isSkillRefRune(r rune) bool {
	if r == '_' || r == '-' {
		return true
	}
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, value := range in {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (s TurnService) Replay(ctx context.Context, req coreruntime.ReplayRequest) (<-chan coreruntime.EventEnvelope, error) {
	if s.services.engine == nil {
		return nil, errors.New("app/services: runtime engine is required")
	}
	ref := defaultSessionRef(s.services.Runtime(), req.SessionRef)
	req.SessionRef = ref
	return s.services.engine.Replay(ctx, req)
}

func (s TurnService) Interrupt(ctx context.Context, ref session.Ref) error {
	if s.services.engine == nil {
		return errors.New("app/services: runtime engine is required")
	}
	ref = defaultSessionRef(s.services.Runtime(), ref)
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
		next.Env = maps.Clone(item.Env)
		next.Meta = maps.Clone(item.Meta)
		out = append(out, next)
	}
	return out
}

func normalizeAgentDescriptor(agent AgentDescriptor) AgentDescriptor {
	return cloneAgents([]AgentDescriptor{agent})[0]
}

func upsertAgentDescriptor(agents []AgentDescriptor, agent AgentDescriptor) []AgentDescriptor {
	agent = normalizeAgentDescriptor(agent)
	id := agentLookupKey(agent)
	if id == "" {
		return agents
	}
	for i, existing := range agents {
		if agentIdentityMatches(existing, agent) {
			next := cloneAgents(agents)
			next[i] = agent
			return next
		}
	}
	out := cloneAgents(agents)
	out = append(out, agent)
	return out
}

func (s AgentService) staticAgentDisableNames(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	for _, agent := range s.services.agents {
		if agentMatchesName(agent, name) {
			names := []string{name}
			agentName := strings.ToLower(strings.TrimSpace(agent.Name))
			if agentName != "" && agentName != name {
				names = append(names, agentName)
			}
			return dedupeServiceNames(names)
		}
	}
	return nil
}

func agentLookupKey(agent AgentDescriptor) string {
	return strings.ToLower(strings.TrimSpace(firstNonEmpty(agent.ID, agent.Name)))
}

func agentMatchesName(agent AgentDescriptor, name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(agent.ID), name) ||
		strings.EqualFold(strings.TrimSpace(agent.Name), name) ||
		agentLookupKey(agent) == name
}

func agentIdentityMatches(left AgentDescriptor, right AgentDescriptor) bool {
	for _, leftKey := range []string{left.ID, left.Name, agentLookupKey(left)} {
		leftKey = strings.ToLower(strings.TrimSpace(leftKey))
		if leftKey == "" {
			continue
		}
		for _, rightKey := range []string{right.ID, right.Name, agentLookupKey(right)} {
			rightKey = strings.ToLower(strings.TrimSpace(rightKey))
			if rightKey == "" {
				continue
			}
			if leftKey == rightKey {
				return true
			}
		}
	}
	return false
}

func agentDisabled(agent AgentDescriptor, disabled map[string]struct{}) bool {
	if len(disabled) == 0 {
		return false
	}
	for _, key := range []string{agent.ID, agent.Name, agentLookupKey(agent)} {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		if _, ok := disabled[key]; ok {
			return true
		}
	}
	return false
}

func dedupeServiceNames(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, name := range in {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

func agentDescriptorFromPlugin(agent plugin.ACPAgentDescriptor) AgentDescriptor {
	name := strings.ToLower(strings.TrimSpace(agent.Name))
	command := strings.TrimSpace(agent.Command)
	return normalizeAgentDescriptor(AgentDescriptor{
		ID:          firstNonEmpty(name, command),
		Name:        firstNonEmpty(name, command),
		Kind:        AgentKindExternalACP,
		Command:     command,
		Args:        append([]string(nil), agent.Args...),
		Env:         maps.Clone(agent.Env),
		WorkDir:     strings.TrimSpace(agent.WorkDir),
		Description: strings.TrimSpace(agent.Description),
	})
}

func pluginDescriptorFromAgent(agent AgentDescriptor) plugin.ACPAgentDescriptor {
	agent = normalizeAgentDescriptor(agent)
	return plugin.ACPAgentDescriptor{
		Name:        strings.ToLower(firstNonEmpty(agent.Name, agent.ID)),
		Description: strings.TrimSpace(agent.Description),
		Command:     strings.TrimSpace(agent.Command),
		Args:        append([]string(nil), agent.Args...),
		Env:         maps.Clone(agent.Env),
		WorkDir:     strings.TrimSpace(agent.WorkDir),
		Roles:       []string{"participant"},
	}
}

func reservedSlashCommandName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "help", "agent", "connect", "controller", "model", "approval", "sandbox", "status", "settings", "task", "doctor", "new", "resume", "compact", "exit", "quit":
		return true
	default:
		return false
	}
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

func statusModelChoices(in []appsettings.ModelChoice) []appviewmodel.ModelChoice {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ModelChoice, 0, len(in))
	for _, item := range in {
		out = append(out, statusModelChoice(item))
	}
	return out
}

func statusModelChoice(in appsettings.ModelChoice) appviewmodel.ModelChoice {
	return appviewmodel.ModelChoice{
		ID:         strings.TrimSpace(in.ID),
		Alias:      strings.TrimSpace(in.Alias),
		Provider:   strings.TrimSpace(in.Provider),
		Model:      strings.TrimSpace(in.Model),
		ProfileID:  strings.TrimSpace(in.ProfileID),
		EndpointID: strings.TrimSpace(in.EndpointID),
		BaseURL:    strings.TrimSpace(in.BaseURL),
		Detail:     strings.TrimSpace(in.Detail),
		Default:    in.Default,
	}
}

func modelChoiceIsDefault(choices []appsettings.ModelChoice, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, choice := range choices {
		if choice.Default && strings.EqualFold(choice.ID, id) {
			return true
		}
	}
	return false
}

func statusModeChoices(in []ModeChoice) []appviewmodel.ModeChoice {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ModeChoice, 0, len(in))
	for _, item := range in {
		out = append(out, statusModeChoice(item))
	}
	return out
}

func statusModeChoice(in ModeChoice) appviewmodel.ModeChoice {
	return appviewmodel.ModeChoice{
		ID:          strings.TrimSpace(in.ID),
		Name:        strings.TrimSpace(in.Name),
		Description: strings.TrimSpace(in.Description),
	}
}

func statusAgentView(in []AgentDescriptor) appviewmodel.AgentStatus {
	agents := cloneAgents(in)
	status := appviewmodel.AgentStatus{
		Count: len(agents),
	}
	if len(agents) == 0 {
		return status
	}
	status.Items = make([]appviewmodel.AgentItem, 0, len(agents))
	for _, agent := range agents {
		if agent.Kind == AgentKindExternalACP {
			status.ExternalACPCount++
		}
		status.Items = append(status.Items, agentItemFromDescriptor(agent))
	}
	return status
}

func statusResourceView(in appresources.Catalog) appviewmodel.ResourceStatus {
	out := appviewmodel.ResourceStatus{
		Plugins:        len(in.Plugins),
		ModelProviders: len(in.ModelProviders),
		Stores:         len(in.Stores),
		Sandboxes:      len(in.Sandboxes),
		Tools:          len(in.Tools),
		ModelTools:     len(in.ModelTools),
		Prompts:        len(in.Prompts),
		Skills:         len(in.Skills),
		ACPAgents:      len(in.ACPAgents),
		RendererHints:  len(in.RendererHints),
		AgentFiles:     len(in.AgentFiles),
	}
	out.Diagnostics = resourceDiagnosticsView(in.Diagnostics)
	for _, diagnostic := range out.Diagnostics {
		switch diagnostic.Severity {
		case appresources.DiagnosticWarning:
			out.WarningCount++
		case appresources.DiagnosticError:
			out.ErrorCount++
		default:
			out.InfoCount++
		}
	}
	return out
}

func resourceDiagnosticsView(in []appresources.Diagnostic) []appviewmodel.ResourceDiagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ResourceDiagnostic, 0, len(in))
	for _, item := range cloneResourceDiagnostics(in) {
		out = append(out, appviewmodel.ResourceDiagnostic{
			Severity: strings.TrimSpace(item.Severity),
			Kind:     strings.TrimSpace(item.Kind),
			ID:       strings.TrimSpace(item.ID),
			Path:     strings.TrimSpace(item.Path),
			Message:  strings.TrimSpace(item.Message),
			Meta:     maps.Clone(item.Meta),
		})
	}
	return out
}

func cloneResourceDiagnostics(in []appresources.Diagnostic) []appresources.Diagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]appresources.Diagnostic, 0, len(in))
	for _, item := range in {
		item.Meta = maps.Clone(item.Meta)
		out = append(out, item)
	}
	return out
}

func stateString(state session.State, key string) string {
	if state == nil {
		return ""
	}
	value, _ := state[key].(string)
	return strings.TrimSpace(value)
}

func cloneState(in session.State) session.State {
	if in == nil {
		return nil
	}
	return session.State(maps.Clone(in))
}

func defaultSessionRef(runtimeCfg config.Runtime, ref session.Ref) session.Ref {
	ref = session.NormalizeRef(ref)
	if ref.AppName == "" {
		ref.AppName = runtimeCfg.AppName
	}
	if ref.UserID == "" {
		ref.UserID = runtimeCfg.UserID
	}
	if ref.WorkspaceKey == "" {
		ref.WorkspaceKey = strings.TrimSpace(runtimeCfg.WorkspaceKey)
	}
	return ref
}

func sessionModeChoices() []ModeChoice {
	return []ModeChoice{
		{
			ID:          coreruntime.SessionModeAutoReview,
			Name:        "Auto Review",
			Description: "Use the configured approval policy for sensitive actions.",
		},
		{
			ID:          coreruntime.SessionModeManual,
			Name:        "Manual",
			Description: "Ask before every tool action in this session.",
		},
	}
}

func lookupSessionMode(modeID string) (ModeChoice, bool) {
	modeID = defaultSessionMode(modeID)
	for _, mode := range sessionModeChoices() {
		if mode.ID == modeID {
			return mode, true
		}
	}
	return ModeChoice{}, false
}

func defaultSessionMode(mode string) string {
	if modeID := coreruntime.NormalizeSessionMode(mode); modeID != "" {
		return modeID
	}
	return coreruntime.SessionModeAutoReview
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
