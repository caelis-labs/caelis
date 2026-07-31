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
	controlclient "github.com/caelis-labs/caelis/control/client"
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
	ContextWindow             int
	SystemPrompt              string
	Assembly                  assembly.ResolvedAssembly
	SkillDirs                 []string
	// ModelProfileID selects one Control-owned ModelProfile for this process.
	// Runtime startup is read-only: profiles and provider credentials may only
	// be created or replaced by the /connect owner.
	ModelProfileID     string
	ModelProfileEffort string
	// Model is retained only to reject the former runtime override API with an
	// actionable error. Callers must configure models through /connect and
	// select them with ModelProfileID.
	Model   ModelConfig
	Sandbox SandboxConfig
}

type ModelConfig = modelconfig.Config
type ProviderEndpointConfig = modelconfig.ProviderEndpointConfig
type ModelChoice = modelconfig.Choice

// DefaultControlOperationRetention is the production replay guarantee for
// proven terminal Control operations.
const DefaultControlOperationRetention = controlclient.DefaultOperationTerminalRetention

type Stack struct {
	Sessions                  session.Service
	AppName                   string
	UserID                    string
	Workspace                 session.WorkspaceRef
	lookup                    *modelLookup
	store                     *appConfigStore
	storeDir                  string
	controlOperationRetention time.Duration
	leaseOwnerID              string
	mu                        sync.RWMutex
	workspaceCloseMu          sync.Mutex
	reconfigureMu             sync.Mutex
	reconfigureGate           *sync.Mutex
	// assemblyMutationMu serializes live Agent assembly mutations with durable
	// controller binding changes. Coordinators receive its read side.
	assemblyMutationMu       sync.RWMutex
	assemblyMutationGate     *sync.RWMutex
	placementCacheMu         sync.RWMutex
	placementCache           *placementSnapshot
	placementCacheGeneration uint64
	runtime                  stackRuntimeConfig
	sandbox                  SandboxConfig
	exec                     sandbox.Runtime
	engine                   *runtime.Runtime
	placement                controlplane.PlacementExecutor
	acpControlPlane          *acpassembly.ControlPlane
	taskStore                task.Store
	controlFeeds             controlclient.FeedRegistry
	controlState             controlclient.StateReader
	controlCommands          controlclient.CommandClient
	controlClient            controlclient.Service
	taskStreams              acptaskstream.Service
	operations               *controlclient.FileOperationStore
	approvalRecovery         *controlclient.ApprovalRecoveryGate
	lifecycleCtx             context.Context
	lifecycleCancel          context.CancelFunc
	closing                  atomic.Bool
	gateway                  *kernelimpl.Gateway
	mcpMgr                   *mcp.Manager
	codexAuth                *codexauth.Manager
	grokAuth                 *grokauth.Manager
	apiKeyCredentials        *credentialstore.Store
	providerUsage            *providerusage.Registry
	sessionRuntimes          *sessionRuntimeRegistry

	// Optional test seam; nil uses the platform lifecycle runtime factory.
	sandboxLifecycleFactory sandboxLifecycleRuntimeFactory

	// Optional test seam; nil uses the configured agent refresh path.
	refreshConfiguredAgentsHook func() error
}

// KernelTurns returns the current gateway turn service without exposing the
// broader session/control-plane aggregate to callers that only submit turns.
func (s *Stack) KernelTurns() kernelimpl.TurnService {
	if gw := s.currentGateway(); gw != nil {
		return gw
	}
	return nil
}

// KernelSessions returns the current gateway session service without exposing
// turn or control-plane operations to session-only callers.
func (s *Stack) KernelSessions() kernelimpl.SessionService {
	if gw := s.currentGateway(); gw != nil {
		return gw
	}
	return nil
}

// KernelControlPlane returns the current gateway control-plane service without
// exposing turn/session operations to controller-only callers.
func (s *Stack) KernelControlPlane() kernelimpl.ControlPlaneService {
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

// ControlClientFeeds returns the Control-owned Session feed registry shared by
// every in-process and network adapter.
func (s *Stack) ControlClientFeeds() controlclient.FeedRegistry {
	if s == nil {
		return nil
	}
	return s.controlFeeds
}

// ControlClientState returns the typed reconnect bootstrap reader.
func (s *Stack) ControlClientState() controlclient.StateReader {
	if s == nil {
		return nil
	}
	return s.controlState
}

// ControlClientReconnect returns the atomic typed bootstrap/splice service.
func (s *Stack) ControlClientReconnect() controlclient.ReconnectReader {
	if s == nil {
		return nil
	}
	reconnect, _ := s.controlState.(controlclient.ReconnectReader)
	return reconnect
}

// ControlClientCommands returns the request-scoped authorized command service.
func (s *Stack) ControlClientCommands() controlclient.CommandClient {
	if s == nil {
		return nil
	}
	return s.controlCommands
}

// ControlClient returns the complete transport-neutral client service.
func (s *Stack) ControlClient() controlclient.Service {
	if s == nil {
		return nil
	}
	return s.controlClient
}

// ControlParticipants returns the focused in-process participant extension.
// It is intentionally separate from the bounded transport-neutral Service.
func (s *Stack) ControlParticipants() controlclient.ParticipantService {
	if s == nil {
		return nil
	}
	participants, _ := s.controlClient.(controlclient.ParticipantService)
	return participants
}

// TaskStreams returns the Control-owned, Session-authorized Task stream
// service used by the current in-process and ACP presentation adapters.
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
func (s *Stack) ControlClientRuntimeState(ctx context.Context, ref session.SessionRef) (controlclient.RuntimeState, error) {
	runtimeStack := s
	if s != nil && s.sessionRuntimes != nil &&
		!s.sessionRuntimes.defaultSession(ref.SessionID) {
		runtime, ok := s.sessionRuntimes.loaded(ref.SessionID)
		if !ok {
			return controlclient.RuntimeState{}, nil
		}
		runtimeStack = runtime.stack
	}
	gateway := runtimeStack.currentGateway()
	if gateway == nil {
		return controlclient.RuntimeState{}, fmt.Errorf("gatewayapp: control runtime is unavailable")
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

func (s *Stack) reconfigureLock() *sync.Mutex {
	if s == nil {
		return nil
	}
	if s.reconfigureGate != nil {
		return s.reconfigureGate
	}
	return &s.reconfigureMu
}

func (s *Stack) assemblyMutationLock() *sync.RWMutex {
	if s == nil {
		return nil
	}
	if s.assemblyMutationGate != nil {
		return s.assemblyMutationGate
	}
	return &s.assemblyMutationMu
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
		RootDir: filepath.Join(storeDir, "sessions"),
	})
	sessions := sessionStore
	taskStore := sessionfile.NewTaskStore(sessionStore)
	approvalRecovery := controlclient.NewApprovalRecoveryGate(sessions)
	cursorSecret, err := loadOrCreateControlClientCursorSecret(storeDir)
	if err != nil {
		return nil, err
	}
	cursorCodec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: cursorSecret})
	if err != nil {
		return nil, err
	}
	controlFeeds, err := controlclient.NewFeedRegistry(controlclient.FeedRegistryConfig{
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
	lookup.resolveAPIKey = apiKeyCredentials.Get
	sandboxCfg := mergeSandboxConfig(doc.Sandbox, cfg.Sandbox)
	leaseOwnerID, err := newStackLeaseOwnerID()
	if err != nil {
		return nil, err
	}
	stack := &Stack{
		Sessions:          sessions,
		AppName:           appName,
		UserID:            userID,
		Workspace:         workspace,
		lookup:            lookup,
		store:             configStore,
		storeDir:          storeDir,
		leaseOwnerID:      leaseOwnerID,
		taskStore:         taskStore,
		controlFeeds:      controlFeeds,
		approvalRecovery:  approvalRecovery,
		codexAuth:         codexAuth,
		grokAuth:          grokAuth,
		apiKeyCredentials: apiKeyCredentials,
		providerUsage:     providerUsage,
		runtime: stackRuntimeConfig{
			ApprovalMode:       effectiveApprovalMode,
			PolicyProfile:      effectivePolicyProfile,
			ContextWindow:      cfg.ContextWindow,
			SystemPrompt:       cfg.SystemPrompt,
			ModelProfileID:     modelProfileID,
			ModelProfileEffort: modelProfileEffort,
			Model:              runtimeModel,
			SkillDirs:          cloneStringSlicePreserveNil(cfg.SkillDirs),
			Plugins:            clonePluginConfigs(doc.Plugins),
			BaseAssembly:       baseAssembly,
			Assembly:           assembly.CloneResolvedAssembly(baseAssembly),
		},
		sandbox: sandboxCfg,
	}
	stack.placementCache = newPlacementSnapshot(doc)
	configStore.savedHook = stack.invalidatePlacementSnapshot
	controlState, err := controlclient.NewStateService(controlclient.StateServiceConfig{
		Sessions: sessions, Runtime: stack, Feeds: controlFeeds,
	})
	if err != nil {
		return nil, err
	}
	stack.controlState = controlState
	controlOperations, err := controlclient.NewFileOperationStoreWithConfig(
		filepath.Join(storeDir, "control-operations"),
		controlclient.OperationRetentionConfig{TerminalRetention: cfg.ControlOperationRetention},
	)
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
	controlCommands, err := controlclient.NewCommandService(controlclient.CommandServiceConfig{
		Authorizer: controlclient.SessionAuthorizer{Sessions: sessions},
		Operations: controlOperations,
		Backend:    stack,
	})
	if err != nil {
		return nil, err
	}
	stack.controlCommands = controlCommands
	controlClient, err := controlclient.NewClient(controlclient.ClientConfig{
		Commands: controlCommands, State: controlState, Feeds: controlFeeds,
		Authorizer:         controlclient.SessionAuthorizer{Sessions: sessions},
		ParticipantHandles: stack,
		Sessions:           sessions,
	})
	if err != nil {
		return nil, err
	}
	stack.controlClient = controlClient
	controlTaskStreams, err := controltaskstream.New(controltaskstream.Config{
		Tasks:      taskStore,
		Streams:    func() stream.Service { return hostTaskStreamService{host: stack} },
		Authorizer: taskStreamAuthorizer{inner: controlclient.SessionAuthorizer{Sessions: sessions}},
		Secret:     cursorSecret,
	})
	if err != nil {
		return nil, err
	}
	stack.taskStreams = acptaskstream.New(controlTaskStreams)
	stack.lifecycleCtx, stack.lifecycleCancel = context.WithCancel(context.Background())
	if err := stack.rebuildGateway(); err != nil {
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
		}
		if gateway := s.currentGateway(); gateway != nil {
			if err := gateway.Quiesce(ctx); err != nil {
				errs = append(errs, fmt.Errorf("default Runtime: %w", err))
			}
		}
		return errors.Join(errs...)
	}
	if gateway := s.currentGateway(); gateway != nil {
		return gateway.Quiesce(ctx)
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
	}
	return errors.Join(execErr, mcpErr)
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
