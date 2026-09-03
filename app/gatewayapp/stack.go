package gatewayapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	hostacpendpoint "github.com/caelis-labs/caelis/app/gatewayapp/internal/acpendpoint"
	adapterhostimpl "github.com/caelis-labs/caelis/app/gatewayapp/internal/adapterhost"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/memoryhost"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/sandboxpolicy"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	acptaskstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/hostownership"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

// MemoryBindingSelectionContext is the host-neutral input for selecting one
// opaque Memory binding at Session creation and Runtime activation. It carries
// no downstream product identity or Memory authority.
type MemoryBindingSelectionContext struct {
	SessionRef         session.SessionRef
	Workspace          session.WorkspaceRef
	FallbackBindingRef memorybinding.BindingRef
}

// MemoryBindingSelector lets an embedding map future product concepts to an
// opaque binding without adding those concepts to Caelis. Returning an empty
// reference preserves the process fallback and AppConfig default.
type MemoryBindingSelector func(context.Context, MemoryBindingSelectionContext) (memorybinding.BindingRef, error)

type Config struct {
	AppName  string
	UserID   string
	StoreDir string
	// SandboxHostAuthorityDir optionally injects a writable Host-user authority
	// base for platform sandbox coordination. Production normally uses the
	// platform default; tests and restricted embeddings may provide one.
	SandboxHostAuthorityDir   string
	ControlOperationRetention time.Duration // Zero adopts an existing root policy; a fresh root uses the default.
	WorkspaceKey              string
	WorkspaceCWD              string
	ApprovalMode              string
	PolicyProfile             string
	// DangerouslySkipPermissions enables the process-only Host escape mode. It
	// is never loaded from or saved to AppConfig.
	DangerouslySkipPermissions bool
	// HostOwnership is the process-entry authority for StoreDir. NewLocalStack
	// borrows but never closes it; only a live matching authority permits this
	// Host epoch to replace a durable fence left by a prior Host.
	HostOwnership *hostownership.Authority
	ContextWindow int
	SystemPrompt  string
	Assembly      assembly.ResolvedAssembly
	SkillDirs     []string
	// ModelProfileID selects one Control-owned ModelProfile for this process.
	// Runtime startup is read-only: profiles and provider credentials may only
	// be created or replaced by the /connect owner.
	ModelProfileID     string
	ModelProfileEffort string
	// Model is retained only to reject the former runtime override API with an
	// actionable error. Callers must configure models through /connect and
	// select them with ModelProfileID.
	Model ModelConfig
	// ResolveProviderHTTPClient optionally supplies an embedding-owned transport
	// after Control has resolved the durable model configuration and credential.
	// It does not create or mutate model configuration.
	ResolveProviderHTTPClient func(context.Context, ModelConfig) (*http.Client, error)
	Sandbox                   SandboxConfig
	// ChildControlURL and ChildControlTokenFile let an embedding expose this
	// same Host to built-in ACP child processes. They never construct another
	// Host and the token itself is not placed in argv or environment.
	ChildControlURL       string
	ChildControlTokenFile string
	// MemoryBindingRef is an embedding-only extension seam for selecting another
	// opaque Control-owned Memory binding. Product users receive the automatically
	// provisioned private binding and never configure endpoints or grants.
	MemoryBindingRef      memorybinding.BindingRef
	MemoryBindingSelector MemoryBindingSelector
	// MemoryLabelSelector is an embedding-only extension seam for adding opaque
	// product partitions to Caelis' mandatory, hashed workspace label. Labels
	// are fixed at Session admission and never enter model-visible tool data.
	MemoryLabelSelector MemoryLabelSelector
	// memoryHost is a package-private deterministic test seam. Production opens
	// the embedded Memory package synchronously with the Host.
	memoryHost runtimeMemoryHost
}

type ModelConfig = modelconfig.Config
type ProviderEndpointConfig = modelconfig.ProviderEndpointConfig
type ModelChoice = modelconfig.Choice

// SetBuiltInChildControl binds later Session Runtime assembly to the currently
// listening AppServer endpoint. Host bootstrap calls it before client
// admission; it does not mutate an already active Session Runtime.
func (s *Stack) SetBuiltInChildControl(controlURL string, tokenFile string) {
	if s == nil {
		return
	}
	if s.composition.process != nil && s.composition.process.config != nil {
		s.composition.process.config.setChildControl(controlURL, tokenFile)
	}
}

// KernelTurnReader is the read-only live Turn projection required by
// server-side status assembly.
type KernelTurnReader interface {
	ActiveTurns() []kernelimpl.ActiveTurnState
}

// KernelControlPlaneReader is the read-only controller and participant
// projection required by server-side status assembly.
type KernelControlPlaneReader interface {
	ControlPlaneState(context.Context, kernelimpl.ControlPlaneStateRequest) (kernelimpl.ControlPlaneState, error)
}

// DefaultControlOperationRetention is the production replay guarantee for
// proven terminal Control operations.
const DefaultControlOperationRetention = appserver.DefaultOperationTerminalRetention

type Stack struct {
	composition               runtimeComposition
	adapterHost               *adapterhostimpl.Manager
	controlOperationRetention time.Duration
	commandBackend            *controlCommandBackend
	controlClient             appserver.Service
	configurationCommands     appserver.ConfigurationCommandService
	agentCommands             appserver.AgentCommandService
	pluginCommands            appserver.PluginCommandService
	taskStreams               acptaskstream.Service
	operations                appserver.DurableOperationStore
	lifecycleCancel           context.CancelFunc
	sessionRuntimes           *sessionRuntimeRegistry
	modelRecovery             *sessionModelRecovery
	memoryRuntime             *memoryhost.Host
	memorySteward             *memoryStewardBridge
}

// Sessions returns the Host's process-level Session authority.
func (s *Stack) Sessions() session.Service {
	if runtime := s.runtimeProjection(); runtime != nil {
		return runtime.sessions
	}
	return nil
}

// AppName returns the durable application identity used for new Sessions.
func (s *Stack) AppName() string {
	if runtime := s.runtimeProjection(); runtime != nil {
		return runtime.authorities.appName
	}
	return ""
}

// UserID returns the compatibility Session owner identity bound to this Host.
func (s *Stack) UserID() string {
	if runtime := s.runtimeProjection(); runtime != nil {
		return runtime.authorities.userID
	}
	return ""
}

// Workspace returns the Host's default workspace address.
func (s *Stack) Workspace() session.WorkspaceRef {
	if runtime := s.runtimeProjection(); runtime != nil {
		return runtime.workspace
	}
	return session.WorkspaceRef{}
}

// KernelTurnState returns the current read-only live Turn projection. Turn
// writes remain behind typed AppServer clients.
func (s *runtimeComposition) KernelTurnState() KernelTurnReader {
	if gw := s.currentGateway(); gw != nil {
		return gw
	}
	return nil
}

// KernelControlPlaneState returns the current read-only controller and
// participant projection. Control-plane writes remain behind focused typed
// AppServer clients.
func (s *runtimeComposition) KernelControlPlaneState() KernelControlPlaneReader {
	if gw := s.currentGateway(); gw != nil {
		return gw
	}
	return nil
}

// KernelStreams returns the current gateway stream provider without exposing
// gateway control or session operations.
func (s *runtimeComposition) KernelStreams() kernelimpl.StreamProvider {
	if gw := s.currentGateway(); gw != nil {
		return gw
	}
	return nil
}

// ControlClient returns the complete transport-neutral client service.
func (s *Stack) ControlClient() appserver.Service {
	if s == nil {
		return nil
	}
	return s.controlClient
}

// ConfigurationCommands returns the focused Host configuration mutation
// capability backed by the same command executor and durable operation ledger
// as the Session Control client.
func (s *Stack) ConfigurationCommands() appserver.ConfigurationCommandService {
	if s == nil {
		return nil
	}
	return s.configurationCommands
}

// AgentCommands returns the focused Host Agent-binding mutation capability
// backed by the same command executor and durable operation ledger as the
// Session Control client.
func (s *Stack) AgentCommands() appserver.AgentCommandService {
	if s == nil {
		return nil
	}
	return s.agentCommands
}

// PluginCommands returns the focused Host plugin and marketplace mutation
// capability backed by the same command executor and durable operation ledger
// as the Session Control client.
func (s *Stack) PluginCommands() appserver.PluginCommandService {
	if s == nil {
		return nil
	}
	return s.pluginCommands
}

// ControlParticipants returns the focused server-side participant capability
// assembled into embedded and HTTP AppServer clients. It stays separate from
// the Session lifecycle and main-Turn Service.
func (s *Stack) ControlParticipants() appserver.ParticipantService {
	if s == nil {
		return nil
	}
	participants, _ := s.controlClient.(appserver.ParticipantService)
	return participants
}

// TaskStreams returns the Control-owned, Session-authorized Task service shared
// by embedded and HTTP AppServer clients independently of the main Session
// feed.
func (s *Stack) TaskStreams() acptaskstream.Service {
	if s == nil {
		return nil
	}
	return s.taskStreams
}

// ControlTerminalStreams returns the Session-routed Runtime terminal stream
// used by AppServer terminal services. Presentation surfaces must consume the
// typed TerminalClient instead of this Runtime-facing controller.
func (s *Stack) ControlTerminalStreams() stream.Controller {
	if s == nil {
		return nil
	}
	return hostTaskStreamService{host: &s.composition, registry: s.sessionRuntimes}
}

func (s *runtimeComposition) currentGateway() *kernelimpl.Gateway {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gateway
}

func (s *runtimeComposition) isClosing() bool {
	if s == nil {
		return true
	}
	if s.authorities.lifecycleCtx != nil {
		select {
		case <-s.authorities.lifecycleCtx.Done():
			return true
		default:
		}
	}
	return s.closing.Load()
}

type SessionRuntimeState = controlstatus.SessionRuntimeState

type SandboxStatus struct {
	RequestedBackend         string
	ResolvedBackend          string
	Route                    string
	FallbackReason           string
	InstallHint              string
	Setup                    sandbox.SetupStatus
	SetupRequired            bool
	SetupError               string
	SetupVersion             int
	SetupMarkerCurrent       bool
	SetupMarkerReason        string
	SetupRunnerHash          string
	SetupPolicyHash          string
	SetupOfflineUser         string
	SetupOnlineUser          string
	SetupOwnerUser           string
	SetupReadRoots           int
	SetupWriteRoots          int
	SetupDenyRead            int
	SetupDenyWrite           int
	SecuritySummary          string
	FullAccessMode           bool
	GlobalSetupCurrent       bool
	GlobalSetupRequired      bool
	GlobalSetupReason        string
	WorkspaceSetupCurrent    bool
	WorkspaceSetupRequired   bool
	WorkspaceSetupReason     string
	WorkspaceSetupRoot       string
	WorkspaceSetupWriteRoots int
	WorkspaceSetupPolicyHash string
	WorkspaceSetupUpdatedAt  time.Time
}

func NewLocalStack(cfg Config) (*Stack, error) {
	if modelConfigSupplied(cfg.Model) {
		return nil, fmt.Errorf("gatewayapp: runtime model overrides are unsupported; configure the model with /connect and select its ModelProfile")
	}
	appName := firstNonEmpty(strings.TrimSpace(cfg.AppName), "caelis")
	userID := firstNonEmpty(strings.TrimSpace(cfg.UserID), "local-user")
	workspaceCWD := firstNonEmpty(strings.TrimSpace(cfg.WorkspaceCWD), mustGetwd())
	workspaceKey := firstNonEmpty(strings.TrimSpace(cfg.WorkspaceKey), "workspace")
	workspace, err := canonicalWorkspaceRef(
		session.WorkspaceRef{Key: workspaceKey, CWD: workspaceCWD},
		session.WorkspaceRef{},
	)
	if err != nil {
		return nil, err
	}
	storeDir := strings.TrimSpace(cfg.StoreDir)
	if storeDir == "" {
		storeDir = defaultStoreDir()
	}
	if cfg.HostOwnership != nil && !cfg.HostOwnership.Authorizes(storeDir) {
		return nil, fmt.Errorf("gatewayapp: Host ownership does not authorize StoreDir")
	}
	configStore := newAppConfigStore(storeDir)
	runtimeDiagnostics := newRuntimeDiagnosticsLogger(storeDir)
	doc, err := configStore.Load()
	if err != nil {
		return nil, err
	}
	hostedCodexAvailable, err := builtInCodexAdapterAvailable(context.Background())
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: inspect built-in Codex adapter before Store migration: %w", err)
	}
	doc, err = migrateRetiredStoreLayout(context.Background(), configStore, storeDir, doc, hostedCodexAvailable)
	if err != nil {
		return nil, err
	}
	preparedMemory, err := prepareEmbeddedMemory(context.Background(), cfg, configStore, storeDir, doc)
	if err != nil {
		return nil, err
	}
	doc = preparedMemory.document
	startupMemoryRuntime := preparedMemory.host
	defer func() {
		if startupMemoryRuntime != nil {
			_ = startupMemoryRuntime.Close()
		}
	}()
	memorySelection := memorybinding.RuntimeSelection{BindingRef: cfg.MemoryBindingRef}
	initialMemorySelection := memorySelection
	if cfg.MemoryBindingSelector != nil {
		if memorySelection.BindingRef != "" {
			if _, _, fallbackErr := memorybinding.Resolve(doc.Memory, memorySelection); fallbackErr != nil {
				return nil, fmt.Errorf("gatewayapp: validate fallback Memory selection: %w", fallbackErr)
			}
		}
		initialMemorySelection = memorybinding.RuntimeSelection{}
	}
	_, memorySelected, err := memorybinding.Resolve(doc.Memory, initialMemorySelection)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: validate process Memory selection: %w", err)
	}
	apiKeyCredentials, err := credentialstore.New(storeDir)
	if err != nil {
		return nil, err
	}
	if err := recoverProviderCredentialRetirements(context.Background(), configStore, apiKeyCredentials); err != nil {
		return nil, err
	}
	effectiveApprovalMode := approvalMode(firstNonEmpty(cfg.ApprovalMode, doc.Runtime.ApprovalMode))
	effectivePolicyProfile := policyProfile(firstNonEmpty(cfg.PolicyProfile, doc.Runtime.PolicyProfile))
	baseAssembly := assembly.CloneResolvedAssembly(cfg.Assembly)
	fenceOwnerID, err := newStackFenceOwnerID()
	if err != nil {
		return nil, err
	}
	sessionStoreConfig := sessionfile.Config{
		RootDir:     filepath.Join(storeDir, "sessions"),
		Diagnostics: runtimeDiagnostics,
	}
	var sessionStore *sessionfile.Store
	var priorHostFenceCapability session.PriorHostFenceService
	if cfg.HostOwnership != nil {
		sessionStore, priorHostFenceCapability = sessionfile.NewStoreWithPriorHostFences(sessionStoreConfig, func(context.Context) (func(), bool) {
			if cfg.HostOwnership == nil {
				return nil, false
			}
			return cfg.HostOwnership.Pin(storeDir)
		})
	} else {
		sessionStore = sessionfile.NewStore(sessionStoreConfig)
	}
	sessions := sessionStore
	taskStore := sessionfile.NewTaskStore(sessionStore)
	var priorHostFences appserver.PriorHostFenceReplacer
	if cfg.HostOwnership != nil {
		replacer := approvalRecoveryFenceReplacer{fences: priorHostFenceCapability}
		priorHostFences = replacer
	}
	approvalRecovery := appserver.NewApprovalRecoveryGate(appserver.ApprovalRecoveryGateConfig{
		Store: sessions, FenceOwnerID: fenceOwnerID, PriorHostFences: priorHostFences, Diagnostics: runtimeDiagnostics,
	})
	cursorSecret, err := loadOrCreateControlClientCursorSecret(storeDir)
	if err != nil {
		return nil, err
	}
	cursorCodec, err := appserver.NewCursorCodec(appserver.CursorCodecConfig{Secret: cursorSecret})
	if err != nil {
		return nil, err
	}
	controlFeeds, err := appserver.NewFeedRegistry(appserver.FeedRegistryConfig{
		Reader: sessions, CursorCodec: cursorCodec,
	})
	if err != nil {
		return nil, err
	}
	codexAuth, err := codexauth.NewManager(codexauth.Options{
		CredentialPath: codexauth.DefaultCredentialPath(storeDir),
	})
	if err != nil {
		return nil, err
	}
	grokAuth, err := grokauth.NewManager(grokauth.Options{
		CredentialPath: grokauth.DefaultCredentialPath(storeDir),
	})
	if err != nil {
		return nil, err
	}
	providerUsage := providerusage.NewRegistry(map[string]providerusage.Reader{
		"openai-codex": codexAuth,
		"xai":          grokAuth,
	})
	lookup, err := newModelLookup(configStore, cfg.ContextWindow)
	if err != nil {
		return nil, err
	}
	defaultProfiles := modelprofile.NormalizeConfiguration(doc.ModelProfiles)
	modelProfileID := modelprofile.NormalizeID(cfg.ModelProfileID)
	if modelProfileID == "" {
		modelProfileID = defaultProfiles.DefaultProfileID
	}
	modelProfileEffort := strings.ToLower(strings.TrimSpace(cfg.ModelProfileEffort))
	if modelProfileEffort == "" && modelProfileID == defaultProfiles.DefaultProfileID {
		modelProfileEffort = defaultProfiles.DefaultEffort
	}
	runtimeModel, err := resolveRuntimeProfile(doc.ModelProfiles, lookup, modelProfileID, modelProfileEffort)
	if err != nil {
		return nil, err
	}
	lookup.resolveHTTPClient = func(ctx context.Context, modelCfg ModelConfig) (*http.Client, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		switch {
		case strings.EqualFold(modelCfg.Provider, "openai-codex") && modelCfg.CredentialRef == modelconfig.CodexOAuthCredentialRef:
			if modelconfig.NormalizeBaseURL(modelCfg.BaseURL) != modelconfig.NormalizeBaseURL(modelconfig.CodexOAuthBaseURL) {
				return nil, fmt.Errorf("gatewayapp: codex OAuth requires the maintained endpoint %s", modelconfig.CodexOAuthBaseURL)
			}
			return codexAuth.AuthenticatedClient(modelCfg.HTTPClient)
		case strings.EqualFold(modelCfg.Provider, "xai") && modelCfg.CredentialRef == modelconfig.GrokOAuthCredentialRef:
			if modelconfig.NormalizeBaseURL(modelCfg.BaseURL) != modelconfig.NormalizeBaseURL(modelconfig.GrokOAuthBaseURL) {
				return nil, fmt.Errorf("gatewayapp: grok OAuth requires the maintained endpoint %s", modelconfig.GrokOAuthBaseURL)
			}
			return grokAuth.AuthenticatedClient(modelCfg.HTTPClient)
		default:
			return nil, fmt.Errorf("gatewayapp: unsupported managed model credential %q for provider %q", modelCfg.CredentialRef, modelCfg.Provider)
		}
	}
	lookup.resolveTransportHTTPClient = cfg.ResolveProviderHTTPClient
	lookup.resolveAPIKey = apiKeyCredentials.Get
	runtimeCfg := stackRuntimeConfig{
		ApprovalMode:               effectiveApprovalMode,
		PolicyProfile:              effectivePolicyProfile,
		DangerouslySkipPermissions: cfg.DangerouslySkipPermissions,
		ContextWindow:              cfg.ContextWindow,
		SystemPrompt:               cfg.SystemPrompt,
		ModelProfileID:             modelProfileID,
		ModelProfileEffort:         modelProfileEffort,
		Model:                      runtimeModel,
		SkillDirs:                  cloneStringSlicePreserveNil(cfg.SkillDirs),
		Plugins:                    clonePluginConfigs(doc.Plugins),
		BaseAssembly:               baseAssembly,
		Assembly:                   assembly.CloneResolvedAssembly(baseAssembly),
	}
	securityPosture := resolveProcessSecurityPosture(runtimeCfg)
	sandboxCfg := mergeSandboxConfig(doc.Sandbox, cfg.Sandbox)
	if securityPosture.RequiredSandboxBackend != "" {
		sandboxCfg.RequestedType = string(securityPosture.RequiredSandboxBackend)
	}
	processConfig := newRuntimeProcessConfigSource(sessionRuntimeProcessSnapshot{
		runtime:               runtimeCfg,
		sandboxOverride:       cfg.Sandbox,
		childControlURL:       cfg.ChildControlURL,
		childControlTokenFile: cfg.ChildControlTokenFile,
		memorySelection:       memorySelection,
		memorySelector:        cfg.MemoryBindingSelector,
		memoryLabelSelector:   cfg.MemoryLabelSelector,
	})
	sessionModelPins := newSessionModelPinRegistry(apiKeyCredentials.Get, lookup.Snapshot().Configs...)
	stack := &Stack{
		composition: runtimeComposition{
			authorities: runtimeHostAuthorities{
				appName:                 appName,
				userID:                  userID,
				store:                   configStore,
				storeDir:                storeDir,
				sandboxHostAuthorityDir: strings.TrimSpace(cfg.SandboxHostAuthorityDir),
				diagnostics:             runtimeDiagnostics,
				configMigration:         configStore.MigrationReport(),
				fenceOwnerID:            fenceOwnerID,
				taskStore:               taskStore,
				controlFeeds:            controlFeeds,
				approvalRecovery:        approvalRecovery,
				codexAuth:               codexAuth,
				grokAuth:                grokAuth,
				apiKeyCredentials:       apiKeyCredentials,
				providerUsage:           providerUsage,
				sessionModelPins:        sessionModelPins,
			},
			sessions:  sessions,
			workspace: workspace,
			lookup:    lookup,
			process: &runtimeProcessState{
				config:           processConfig,
				sandboxPersisted: cloneSandboxConfig(doc.Sandbox),
				sandboxRevision:  doc.ConfigurationRevision,
			},
			sandbox: sandboxCfg,
		},
	}
	if memorySelected {
		if cfg.memoryHost != nil {
			stack.composition.authorities.memoryHost = cfg.memoryHost
		} else {
			stack.memoryRuntime = startupMemoryRuntime
			startupMemoryRuntime = nil
			stack.composition.authorities.memoryHost = stack.memoryRuntime
		}
		if err := validateConfiguredMemoryAuthorities(context.Background(), stack.composition.authorities.memoryHost, doc.Memory); err != nil {
			_ = stack.Close()
			return nil, err
		}
	}
	stack.adapterHost, err = newHostedAdapterManager()
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.composition.authorities.adapterHost = stack.adapterHost
	stack.composition.authorities.acpEndpointResolver, err = hostacpendpoint.New(hostacpendpoint.Config{
		Service:  stack.adapterHost,
		StoreDir: storeDir,
		ControlURL: func() string {
			return processConfig.snapshot().childControlURL
		},
	})
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.modelRecovery = newSessionModelRecovery(
		stack.composition.authorities.store,
		stack.composition.authorities.sessionModelPins,
		stack.composition.lookup,
	)
	stack.commandBackend, err = newControlCommandBackend(&stack.composition, stack.modelRecovery)
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.composition.placementCache = newPlacementSnapshot(doc)
	configStore.savedHook = stack.composition.invalidateOwnPlacementSnapshot
	controlAssembly, err := assembleHostControlServices(stack, cfg, storeDir, cursorSecret)
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	if err := activateHostRuntime(stack, controlAssembly); err != nil {
		_ = stack.Close()
		return nil, err
	}
	if preparedMemory.defaultTopology {
		if err := startMemoryStewardBridge(stack); err != nil {
			_ = stack.Close()
			return nil, fmt.Errorf("gatewayapp: start Memory Steward bridge: %w", err)
		}
	}
	return stack, nil
}

func modelConfigSupplied(cfg ModelConfig) bool {
	return strings.TrimSpace(cfg.ID) != "" ||
		strings.TrimSpace(cfg.Alias) != "" ||
		strings.TrimSpace(cfg.Provider) != "" ||
		strings.TrimSpace(cfg.ProviderEndpointID) != "" ||
		strings.TrimSpace(cfg.EndpointID) != "" ||
		cfg.API != "" ||
		strings.TrimSpace(cfg.Model) != "" ||
		strings.TrimSpace(cfg.BaseURL) != "" ||
		cfg.HTTPClient != nil ||
		strings.TrimSpace(cfg.Token) != "" ||
		strings.TrimSpace(cfg.CredentialRef) != "" ||
		cfg.PersistToken ||
		cfg.AuthType != "" ||
		strings.TrimSpace(cfg.HeaderKey) != "" ||
		cfg.ContextWindowTokens != 0 ||
		strings.TrimSpace(cfg.ReasoningEffort) != "" ||
		strings.TrimSpace(cfg.DefaultReasoningEffort) != "" ||
		len(cfg.ReasoningLevels) > 0 ||
		strings.TrimSpace(cfg.ReasoningMode) != "" ||
		cfg.MaxOutputTok != 0 ||
		cfg.Timeout != 0 ||
		cfg.StreamFirstEventTimeout != 0
}

// StartApprovalRecovery begins the Control-owned abandoned-approval sweep.
// Turn entry remains gated until the sweep completes.
func (s *Stack) StartApprovalRecovery(ctx context.Context) {
	if s == nil || s.composition.authorities.approvalRecovery == nil {
		return
	}
	s.composition.authorities.approvalRecovery.Start(ctx)
}

// WaitApprovalRecovery blocks Host readiness until abandoned durable approval
// mirrors have been settled.
func (s *Stack) WaitApprovalRecovery(ctx context.Context) error {
	if s == nil || s.composition.authorities.approvalRecovery == nil {
		return nil
	}
	return s.composition.authorities.approvalRecovery.Wait(ctx)
}

func newStackFenceOwnerID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("gatewayapp: generate runtime fence owner id: %w", err)
	}
	return "gateway-" + hex.EncodeToString(value[:]), nil
}

type stackBaseMetadata struct {
	Metadata     map[string]any
	SkillCatalog skill.Catalog
}

func buildStackBaseMetadata(appName, workspaceCWD, basePrompt string, model ModelConfig, sandboxCfg SandboxConfig, sandboxPolicy sandbox.PolicySnapshot, skillDirs []string, pluginSkills []skill.PluginBundle) (stackBaseMetadata, error) {
	baseMetadata := map[string]any{}
	result, err := buildSystemPromptResult(promptConfig{
		AppName:       appName,
		WorkspaceDir:  workspaceCWD,
		BasePrompt:    basePrompt,
		SkillDirs:     skillDirs,
		PluginSkills:  pluginSkills,
		SandboxPolicy: sandbox.ClonePolicySnapshot(sandboxPolicy),
	})
	if err != nil {
		return stackBaseMetadata{}, err
	}
	if strings.TrimSpace(result.Prompt) != "" {
		baseMetadata["system_prompt"] = result.Prompt
	}
	if reasoning := strings.TrimSpace(model.ReasoningEffort); reasoning != "" {
		baseMetadata["reasoning_effort"] = reasoning
	}
	return stackBaseMetadata{
		Metadata:     sandboxpolicy.WithPolicyMetadata(baseMetadata, sandboxCfg),
		SkillCatalog: result.SkillCatalog,
	}, nil
}

// Quiesce permanently closes Control Turn admission and waits for every active
// producer to release its Gateway handle.
func (s *Stack) Quiesce(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
	s.composition.closing.Store(true)
	var memoryStewardErr error
	if s.memorySteward != nil {
		memoryStewardErr = s.memorySteward.wait(ctx)
	}
	if s.sessionRuntimes != nil {
		var errs []error
		if memoryStewardErr != nil {
			errs = append(errs, fmt.Errorf("memory Steward: %w", memoryStewardErr))
		}
		drain, err := s.sessionRuntimes.beginQuiesce(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		if gateway := s.composition.currentGateway(); gateway != nil {
			if err := gateway.Quiesce(ctx); err != nil {
				errs = append(errs, fmt.Errorf("default Runtime: %w", err))
			}
		}
		if s.composition.acpControlPlane != nil {
			if err := s.composition.acpControlPlane.Quiesce(ctx); err != nil {
				errs = append(errs, fmt.Errorf("default Runtime child work: %w", err))
			}
		}
		// Session producer completion may depend on cancellation from both its
		// activated Runtime and the process-default Runtime.
		if err := drain.wait(ctx); err != nil {
			errs = append(errs, err)
		}
		if s.adapterHost != nil {
			if err := s.adapterHost.Quiesce(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	var errs []error
	if memoryStewardErr != nil {
		errs = append(errs, fmt.Errorf("memory Steward: %w", memoryStewardErr))
	}
	if gateway := s.composition.currentGateway(); gateway != nil {
		if err := gateway.Quiesce(ctx); err != nil {
			errs = append(errs, err)
		}
		if s.composition.acpControlPlane != nil {
			if err := s.composition.acpControlPlane.Quiesce(ctx); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if s.adapterHost != nil {
		if err := s.adapterHost.Quiesce(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Stack) Close() error {
	return s.closeWithQuiesceTimeout(5 * time.Second)
}

func (s *Stack) closeWithQuiesceTimeout(timeout time.Duration) error {
	if s == nil {
		return nil
	}
	quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), timeout)
	quiesceErr := s.Quiesce(quiesceCtx)
	cancelQuiesce()
	if s.memorySteward != nil && !s.memorySteward.drained() {
		return fmt.Errorf("gatewayapp stack: close deferred until Memory Steward drains: %w", quiesceErr)
	}
	if s.composition.authorities.approvalRecovery != nil {
		s.composition.authorities.approvalRecovery.Close()
	}
	var sessionCloseErr error
	if s.sessionRuntimes != nil {
		sessionCloseErr = s.sessionRuntimes.closeRuntimeResources()
	}
	workspaceResourceErr := s.composition.closeWorkspaceResources()
	s.composition.mu.Lock()
	controlOperations := s.operations
	s.operations = nil
	s.composition.mu.Unlock()

	var errs []error
	if quiesceErr != nil {
		errs = append(errs, fmt.Errorf("quiesce: %w", quiesceErr))
	}
	if workspaceResourceErr != nil {
		errs = append(errs, workspaceResourceErr)
	}
	if sessionCloseErr != nil {
		errs = append(errs, sessionCloseErr)
	}
	if s.memorySteward != nil {
		s.memorySteward.closeClients()
		s.memorySteward = nil
	}
	if s.memoryRuntime != nil {
		if err := s.memoryRuntime.Close(); err != nil {
			errs = append(errs, err)
		}
		s.memoryRuntime = nil
		s.composition.authorities.memoryHost = nil
	}
	if controlOperations != nil {
		if err := controlOperations.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.commandBackend != nil && s.commandBackend.acpPreparations != nil {
		if err := s.commandBackend.acpPreparations.Close(); err != nil {
			errs = append(errs, err)
		}
		s.commandBackend.acpPreparations = nil
	}
	if s.adapterHost != nil {
		if err := s.adapterHost.Close(); err != nil {
			errs = append(errs, err)
		}
		s.adapterHost = nil
	}
	if len(errs) > 0 {
		return fmt.Errorf("gatewayapp stack: close failed: %w", errors.Join(errs...))
	}
	return nil
}

func (s *runtimeComposition) closeWorkspaceResources() error {
	if s == nil {
		return nil
	}
	s.workspaceCloseMu.Lock()
	defer s.workspaceCloseMu.Unlock()

	s.mu.Lock()
	exec := s.exec
	s.exec = nil
	mcpMgr := s.mcpMgr
	s.mcpMgr = nil
	pluginCacheRelease := s.pluginCacheRelease
	s.mu.Unlock()

	var execErr error
	if exec != nil {
		execErr = exec.Close()
	}
	var mcpErr error
	if mcpMgr != nil {
		mcpErr = mcpMgr.Close()
	}
	if execErr != nil || mcpErr != nil {
		s.mu.Lock()
		if execErr != nil && s.exec == nil {
			s.exec = exec
		}
		if mcpErr != nil && s.mcpMgr == nil {
			s.mcpMgr = mcpMgr
		}
		s.mu.Unlock()
		return errors.Join(execErr, mcpErr)
	}
	var pluginCacheErr error
	if pluginCacheRelease != nil {
		pluginCacheErr = pluginCacheRelease()
		if pluginCacheErr == nil {
			s.mu.Lock()
			s.pluginCacheRelease = nil
			s.mu.Unlock()
		}
	}
	if pluginCacheErr == nil {
		s.releaseSpawnedSessionModelPins()
	}
	return pluginCacheErr
}

func (s *runtimeComposition) MCPServersStatus(pluginID string) []mcp.MCPServerInfo {
	s.mu.RLock()
	mgr := s.mcpMgr
	s.mu.RUnlock()
	if mgr == nil {
		return nil
	}
	return mgr.GetServerInfos(pluginID)
}
