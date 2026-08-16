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
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/sandboxpolicy"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelconfig/grokauth"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/control/modelprofile"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acptaskstream "github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type Config struct {
	AppName                   string
	UserID                    string
	StoreDir                  string
	ControlOperationRetention time.Duration // Zero adopts an existing root policy; a fresh root uses the default.
	WorkspaceKey              string
	WorkspaceCWD              string
	ApprovalMode              string
	PolicyProfile             string
	// DangerouslySkipPermissions enables the process-only Host escape mode. It
	// is never loaded from or saved to AppConfig.
	DangerouslySkipPermissions bool
	ContextWindow              int
	SystemPrompt               string
	Assembly                   assembly.ResolvedAssembly
	SkillDirs                  []string
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
	s.mu.Lock()
	s.childControlURL = strings.TrimSpace(controlURL)
	s.childControlTokenFile = strings.TrimSpace(tokenFile)
	s.mu.Unlock()
}

// KernelTurnReader is the read-only live Turn projection required by
// server-side status assembly.
type KernelTurnReader interface {
	ActiveTurns() []kernelimpl.ActiveTurnState
}

// KernelSessionReader is the read-only Session projection required by
// server-side completion assembly.
type KernelSessionReader interface {
	ListSessions(context.Context, kernelimpl.ListSessionsRequest) (session.SessionList, error)
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
	Sessions  session.Service
	AppName   string
	UserID    string
	Workspace session.WorkspaceRef
	lookup    *modelLookup
	// modelCatalog is set only on a detached Session Runtime. It points at the
	// live Host catalog used to validate explicit Session model selections;
	// lookup remains the Runtime-owned pinned resolver used by Turns.
	modelCatalog              *modelLookup
	store                     *appConfigStore
	storeDir                  string
	controlOperationRetention time.Duration
	leaseOwnerID              string
	mu                        sync.RWMutex
	workspaceCloseMu          sync.Mutex
	spawnedSessionPinsMu      sync.Mutex
	hostAuthenticationMu      sync.Mutex
	hostAuthentications       map[string]struct{}
	placementCacheMu          sync.RWMutex
	placementCache            *placementSnapshot
	placementCacheGeneration  uint64
	// appConfigSnapshot is set only on a detached Session Runtime. It keeps
	// plugin and Agent assembly on the same immutable AppConfig document used to
	// resolve that activation's placement snapshot.
	appConfigSnapshot *AppConfig
	runtime           stackRuntimeConfig
	sandbox           SandboxConfig
	sandboxOverride   SandboxConfig
	// sandboxPersisted and sandboxRevision cache the latest observed canonical
	// Host policy for status. The live root and detached Session Runtimes remain
	// bound to their activation snapshots.
	sandboxPersisted          SandboxConfig
	sandboxRevision           uint64
	exec                      sandbox.Runtime
	engine                    *runtime.Runtime
	placement                 controlplane.PlacementExecutor
	acpControlPlane           *acpassembly.ControlPlane
	taskStore                 task.Store
	controlFeeds              appserver.FeedRegistry
	controlClient             appserver.Service
	configurationCommands     appserver.ConfigurationCommandService
	agentCommands             appserver.AgentCommandService
	pluginCommands            appserver.PluginCommandService
	taskStreams               acptaskstream.Service
	operations                *appserver.FileOperationStore
	acpPreparations           *acpPreparationStore
	approvalRecovery          *appserver.ApprovalRecoveryGate
	lifecycleCtx              context.Context
	lifecycleCancel           context.CancelFunc
	closing                   atomic.Bool
	gateway                   *kernelimpl.Gateway
	mcpMgr                    *mcp.Manager
	pluginCacheRelease        func() error
	codexAuth                 *codexauth.Manager
	grokAuth                  *grokauth.Manager
	apiKeyCredentials         *credentialstore.Store
	providerUsage             *providerusage.Registry
	childControlURL           string
	childControlTokenFile     string
	sessionRuntimes           *sessionRuntimeRegistry
	sessionModelPins          *sessionModelPinRegistry
	modelRecovery             *sessionModelRecovery
	spawnedSessionPinReleases map[string]func()
	retainRuntimeWork         func(session.SessionRef) func()
	runtimeTaskChanged        func(session.SessionRef)
	// hostedChildMailbox is the Host-owned parent/sibling route used by spawned
	// child Session Runtimes. Child stacks receive this callback and must not
	// own sessionRuntimes.
	hostedChildMailbox hostedChildMailboxFunc

	// Optional test seam; nil uses the platform lifecycle runtime factory.
	sandboxLifecycleFactory sandboxLifecycleRuntimeFactory
}

// KernelTurnState returns the current read-only live Turn projection. Turn
// writes remain behind typed AppServer clients.
func (s *Stack) KernelTurnState() KernelTurnReader {
	if gw := s.currentGateway(); gw != nil {
		return gw
	}
	return nil
}

// KernelSessionState returns the current read-only Session projection. Session
// writes remain behind typed AppServer clients.
func (s *Stack) KernelSessionState() KernelSessionReader {
	if gw := s.currentGateway(); gw != nil {
		return gw
	}
	return nil
}

// KernelControlPlaneState returns the current read-only controller and
// participant projection. Control-plane writes remain behind focused typed
// AppServer clients.
func (s *Stack) KernelControlPlaneState() KernelControlPlaneReader {
	if gw := s.currentGateway(); gw != nil {
		return gw
	}
	return nil
}

// KernelStreams returns the current gateway stream provider without exposing
// gateway control or session operations.
func (s *Stack) KernelStreams() kernelimpl.StreamProvider {
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
	return hostTaskStreamService{host: s}
}

// ControlClientRuntimeState reads live state only from an already activated
// Session Runtime. Observation must not assemble or retain execution state.
func (s *Stack) ControlClientRuntimeState(ctx context.Context, ref session.SessionRef) (appserver.RuntimeState, error) {
	runtimeStack := s
	if s != nil && s.sessionRuntimes != nil {
		runtime, ok := s.sessionRuntimes.loaded(ref.SessionID)
		if !ok {
			return appserver.RuntimeState{}, nil
		}
		runtimeStack = runtime.stack
	}
	gateway := runtimeStack.currentGateway()
	if gateway == nil {
		return appserver.RuntimeState{}, fmt.Errorf("gatewayapp: control runtime is unavailable")
	}
	return gateway.ControlClientRuntimeState(ctx, ref)
}

func (s *Stack) currentGateway() *kernelimpl.Gateway {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.gateway
}

func (s *Stack) isClosing() bool {
	if s == nil {
		return true
	}
	if s.lifecycleCtx != nil {
		select {
		case <-s.lifecycleCtx.Done():
			return true
		default:
		}
	}
	return s.closing.Load()
}

type SessionRuntimeState struct {
	ModelID         string
	ModelAlias      string
	ReasoningEffort string
	SessionMode     string
	PolicyProfile   string
	SandboxMode     string
}

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

type StartSubagentOptions struct {
	ApprovalRequester agent.ApprovalRequester
	ApprovalMode      string
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
	configStore := newAppConfigStore(storeDir)
	doc, err := configStore.Load()
	if err != nil {
		return nil, err
	}
	apiKeyCredentials, err := credentialstore.New(storeDir)
	if err != nil {
		return nil, err
	}
	effectiveApprovalMode := approvalMode(firstNonEmpty(cfg.ApprovalMode, doc.Runtime.ApprovalMode))
	effectivePolicyProfile := policyProfile(firstNonEmpty(cfg.PolicyProfile, doc.Runtime.PolicyProfile))
	baseAssembly := assembly.CloneResolvedAssembly(cfg.Assembly)
	sessionStore := sessionfile.NewStore(sessionfile.Config{
		RootDir:     filepath.Join(storeDir, "sessions"),
		Diagnostics: newRuntimeDiagnosticsLogger(storeDir),
	})
	sessions := sessionStore
	taskStore := sessionfile.NewTaskStore(sessionStore)
	approvalRecovery := appserver.NewApprovalRecoveryGate(sessions)
	cursorSecret, err := loadOrCreateControlClientCursorSecret(storeDir)
	if err != nil {
		return nil, err
	}
	cursorCodec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: cursorSecret})
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
	runtimeModel, err := resolveRuntimeProviderProfile(doc.ModelProfiles, lookup, modelProfileID, modelProfileEffort)
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
	leaseOwnerID, err := newStackLeaseOwnerID()
	if err != nil {
		return nil, err
	}
	stack := &Stack{
		Sessions:              sessions,
		AppName:               appName,
		UserID:                userID,
		Workspace:             workspace,
		lookup:                lookup,
		store:                 configStore,
		storeDir:              storeDir,
		leaseOwnerID:          leaseOwnerID,
		taskStore:             taskStore,
		controlFeeds:          controlFeeds,
		approvalRecovery:      approvalRecovery,
		codexAuth:             codexAuth,
		grokAuth:              grokAuth,
		apiKeyCredentials:     apiKeyCredentials,
		providerUsage:         providerUsage,
		sessionModelPins:      newSessionModelPinRegistry(),
		childControlURL:       strings.TrimSpace(cfg.ChildControlURL),
		childControlTokenFile: strings.TrimSpace(cfg.ChildControlTokenFile),
		runtime:               runtimeCfg,
		sandbox:               sandboxCfg,
		sandboxOverride:       cloneSandboxConfig(cfg.Sandbox),
		sandboxPersisted:      cloneSandboxConfig(doc.Sandbox),
		sandboxRevision:       doc.ConfigurationRevision,
	}
	stack.modelRecovery = newSessionModelRecovery(
		stack.store,
		stack.sessionModelPins,
		stack.lookup,
	)
	stack.placementCache = newPlacementSnapshot(doc)
	configStore.savedHook = stack.invalidatePlacementSnapshot
	controlState, err := appserver.NewStateService(appserver.StateServiceConfig{
		Sessions: sessions, Runtime: stack, Feeds: controlFeeds,
		PrepareReconnect:  stack.prepareControlClientReconnect,
		RetainObservation: stack.retainControlClientObservation,
	})
	if err != nil {
		return nil, err
	}
	controlOperations, err := appserver.NewFileOperationStoreWithConfig(
		filepath.Join(storeDir, "control-operations"),
		appserver.OperationRetentionConfig{TerminalRetention: cfg.ControlOperationRetention},
	)
	if err != nil {
		return nil, err
	}
	acpPreparations, err := newACPPreparationStore(storeDir)
	if err != nil {
		return nil, err
	}
	if err := controlOperations.Initialize(context.Background()); err != nil {
		return nil, err
	}
	effectiveOperationRetention, err := controlOperations.EffectiveTerminalRetention(context.Background())
	if err != nil {
		return nil, err
	}
	stack.controlOperationRetention = effectiveOperationRetention
	stack.operations = controlOperations
	stack.acpPreparations = acpPreparations
	sessionAuthorizer := appserver.SessionAuthorizer{Sessions: sessions}
	controlCommands, err := appserver.NewCommandService(appserver.CommandServiceConfig{
		Authorizer: appserver.ProductCommandAuthorizer{Sessions: sessionAuthorizer},
		Operations: controlOperations,
		Backend:    stack,
	})
	if err != nil {
		return nil, err
	}
	controlClient, err := appserver.NewClient(appserver.ClientConfig{
		Commands: controlCommands, State: controlState, Feeds: controlFeeds,
		Authorizer:         sessionAuthorizer,
		ParticipantHandles: stack,
		Sessions:           sessions,
	})
	if err != nil {
		return nil, err
	}
	stack.controlClient = controlClient
	stack.configurationCommands = controlCommands
	stack.agentCommands = controlCommands
	stack.pluginCommands = controlCommands
	controlTaskStreams, err := controltaskstream.New(controltaskstream.Config{
		Tasks:           taskStore,
		Streams:         func() stream.Service { return hostTaskStreamService{host: stack} },
		Sessions:        sessions,
		SubagentHistory: stack,
		Authorizer:      taskStreamAuthorizer{inner: appserver.SessionAuthorizer{Sessions: sessions}},
		Secret:          cursorSecret,
	})
	if err != nil {
		return nil, err
	}
	stack.taskStreams = acptaskstream.New(controlTaskStreams)
	stack.lifecycleCtx, stack.lifecycleCancel = context.WithCancel(context.Background())
	if err := stack.buildInitialGatewayRuntime(context.Background()); err != nil {
		stack.lifecycleCancel()
		return nil, err
	}
	sessionRuntimes, err := newSessionRuntimeRegistry(stack)
	if err != nil {
		_ = stack.Close()
		return nil, err
	}
	stack.sessionRuntimes = sessionRuntimes
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
	if s == nil || s.approvalRecovery == nil {
		return
	}
	s.approvalRecovery.Start(ctx)
}

// WaitApprovalRecovery blocks Host readiness until abandoned durable approval
// mirrors have been settled.
func (s *Stack) WaitApprovalRecovery(ctx context.Context) error {
	if s == nil || s.approvalRecovery == nil {
		return nil
	}
	return s.approvalRecovery.Wait(ctx)
}

func newStackLeaseOwnerID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("gatewayapp: generate runtime lease owner id: %w", err)
	}
	return "gateway-" + hex.EncodeToString(value[:]), nil
}

type stackBaseMetadata struct {
	Metadata     map[string]any
	SkillCatalog skill.Catalog
}

func buildStackBaseMetadata(appName, workspaceCWD, basePrompt string, model ModelConfig, sandboxCfg SandboxConfig, skillDirs []string, pluginSkills []skill.PluginBundle) (stackBaseMetadata, error) {
	baseMetadata := map[string]any{}
	result, err := buildSystemPromptResult(promptConfig{
		AppName:           appName,
		WorkspaceDir:      workspaceCWD,
		BasePrompt:        basePrompt,
		SkillDirs:         skillDirs,
		PluginSkills:      pluginSkills,
		SandboxMode:       promptSandboxContextMode(sandboxCfg),
		DefaultPermission: promptDefaultPermissionSummary(sandboxCfg),
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

func promptSandboxContextMode(cfg SandboxConfig) string {
	requested := strings.ToLower(strings.TrimSpace(cfg.RequestedType))
	switch requested {
	case "host":
		return "host (no sandbox isolation)"
	case "", "auto":
		return "restricted; workspace-write; network=enabled (auto backend)"
	default:
		return requested + "; workspace-write; network=enabled"
	}
}

func promptDefaultPermissionSummary(cfg SandboxConfig) string {
	if strings.EqualFold(strings.TrimSpace(cfg.RequestedType), "host") {
		return "host permissions; each sensitive action may still require approval"
	}
	return "sandbox default; Host only via one-shot approval"
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
	s.closing.Store(true)
	if s.lifecycleCancel != nil {
		s.lifecycleCancel()
	}
	if s.sessionRuntimes != nil {
		var errs []error
		runtimes, err := s.sessionRuntimes.closeAdmission(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("drain Session Runtime loads: %w", err))
		}
		for _, runtime := range runtimes {
			if runtime == nil || runtime.stack == nil {
				continue
			}
			if gateway := runtime.stack.currentGateway(); gateway != nil {
				if err := gateway.Quiesce(ctx); err != nil {
					errs = append(errs, fmt.Errorf("Session %q: %w", runtime.sessionID, err))
				}
			}
			if runtime.stack.acpControlPlane != nil {
				if err := runtime.stack.acpControlPlane.Quiesce(ctx); err != nil {
					errs = append(errs, fmt.Errorf("Session %q child work: %w", runtime.sessionID, err))
				}
			}
		}
		if gateway := s.currentGateway(); gateway != nil {
			if err := gateway.Quiesce(ctx); err != nil {
				errs = append(errs, fmt.Errorf("default Runtime: %w", err))
			}
		}
		if s.acpControlPlane != nil {
			if err := s.acpControlPlane.Quiesce(ctx); err != nil {
				errs = append(errs, fmt.Errorf("default Runtime child work: %w", err))
			}
		}
		// Runtime work references can depend on Gateway/child cancellation to
		// reach their terminal producer boundary, so wait only after quiescing.
		if err := s.sessionRuntimes.waitForRuntimeWork(ctx, runtimes); err != nil {
			errs = append(errs, fmt.Errorf("drain Session Runtime work: %w", err))
		}
		return errors.Join(errs...)
	}
	if gateway := s.currentGateway(); gateway != nil {
		gatewayErr := gateway.Quiesce(ctx)
		var childErr error
		if s.acpControlPlane != nil {
			childErr = s.acpControlPlane.Quiesce(ctx)
		}
		return errors.Join(gatewayErr, childErr)
	}
	return nil
}

func (s *Stack) Close() error {
	if s == nil {
		return nil
	}
	quiesceCtx, cancelQuiesce := context.WithTimeout(context.Background(), 5*time.Second)
	quiesceErr := s.Quiesce(quiesceCtx)
	cancelQuiesce()
	var sessionCloseErrs []error
	if s.sessionRuntimes != nil {
		for _, runtime := range s.sessionRuntimes.snapshot() {
			if runtime == nil || runtime.stack == nil {
				continue
			}
			if err := runtime.stack.closeWorkspaceResources(); err != nil {
				sessionCloseErrs = append(
					sessionCloseErrs,
					fmt.Errorf("Session %q: %w", runtime.sessionID, err),
				)
			}
		}
	}
	workspaceResourceErr := s.closeWorkspaceResources()
	s.mu.Lock()
	controlOperations := s.operations
	s.operations = nil
	s.mu.Unlock()

	var errs []error
	if quiesceErr != nil {
		errs = append(errs, fmt.Errorf("quiesce: %w", quiesceErr))
	}
	if workspaceResourceErr != nil {
		errs = append(errs, workspaceResourceErr)
	}
	errs = append(errs, sessionCloseErrs...)
	if controlOperations != nil {
		if err := controlOperations.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("gatewayapp stack: close failed: %w", errors.Join(errs...))
	}
	return nil
}

func (s *Stack) closeWorkspaceResources() error {
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

func (s *Stack) MCPServersStatus(pluginID string) []mcp.MCPServerInfo {
	s.mu.RLock()
	mgr := s.mcpMgr
	s.mu.RUnlock()
	if mgr == nil {
		return nil
	}
	return mgr.GetServerInfos(pluginID)
}
