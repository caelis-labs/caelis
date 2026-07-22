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
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/codexauth"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
	"github.com/caelis-labs/caelis/control/modelconfig/providerusage"
	"github.com/caelis-labs/caelis/control/modelprofile"
	modelprofilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"

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
	Model                     ModelConfig
	Sandbox                   SandboxConfig
}

type ModelConfig = modelconfig.Config
type ProviderEndpointConfig = modelconfig.ProviderEndpointConfig
type ModelChoice = modelconfig.Choice

// DefaultControlOperationRetention is the production replay guarantee for
// proven terminal Control operations.
const DefaultControlOperationRetention = internalcontrolclient.DefaultOperationTerminalRetention

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
	reconfigureMu             sync.Mutex
	// assemblyMutationMu serializes live Agent assembly mutations with durable
	// controller binding changes. Coordinators receive its read side.
	assemblyMutationMu       sync.RWMutex
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
	controlFeeds             controlclientport.FeedRegistry
	controlState             controlclientport.StateReader
	controlCommands          controlclientport.CommandClient
	controlClient            controlclientport.Service
	taskStreams              acptaskstream.Service
	operations               *internalcontrolclient.FileOperationStore
	approvalRecovery         *internalcontrolclient.ApprovalRecoveryGate
	gateway                  *kernelimpl.Gateway
	mcpMgr                   *mcp.Manager
	codexAuth                *codexauth.Manager
	apiKeyCredentials        *credentialstore.Store
	providerUsage            *providerusage.Registry

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
func (s *Stack) ControlClientFeeds() controlclientport.FeedRegistry {
	if s == nil {
		return nil
	}
	return s.controlFeeds
}

// ControlClientState returns the typed reconnect bootstrap reader.
func (s *Stack) ControlClientState() controlclientport.StateReader {
	if s == nil {
		return nil
	}
	return s.controlState
}

// ControlClientReconnect returns the atomic typed bootstrap/splice service.
func (s *Stack) ControlClientReconnect() controlclientport.ReconnectReader {
	if s == nil {
		return nil
	}
	reconnect, _ := s.controlState.(controlclientport.ReconnectReader)
	return reconnect
}

// ControlClientCommands returns the request-scoped authorized command service.
func (s *Stack) ControlClientCommands() controlclientport.CommandClient {
	if s == nil {
		return nil
	}
	return s.controlCommands
}

// ControlClient returns the complete transport-neutral client service.
func (s *Stack) ControlClient() controlclientport.Service {
	if s == nil {
		return nil
	}
	return s.controlClient
}

// TaskStreams returns the Control-owned, Session-authorized Task stream
// service used by in-process and HTTP presentation adapters.
func (s *Stack) TaskStreams() acptaskstream.Service {
	if s == nil {
		return nil
	}
	return s.taskStreams
}

// ControlClientRuntimeState delegates bootstrap live-state reads to the
// currently installed Control gateway.
func (s *Stack) ControlClientRuntimeState(ctx context.Context, ref session.SessionRef) (controlclientport.RuntimeState, error) {
	gateway := s.currentGateway()
	if gateway == nil {
		return controlclientport.RuntimeState{}, fmt.Errorf("gatewayapp: control runtime is unavailable")
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
	appName := firstNonEmpty(strings.TrimSpace(cfg.AppName), "caelis")
	userID := firstNonEmpty(strings.TrimSpace(cfg.UserID), "local-user")
	workspaceCWD := firstNonEmpty(strings.TrimSpace(cfg.WorkspaceCWD), mustGetwd())
	workspaceKey := firstNonEmpty(strings.TrimSpace(cfg.WorkspaceKey), "workspace")
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
	credentialBootstrap := &providerCredentialTransaction{}
	if normalized := modelconfig.NormalizeConfig(cfg.Model); normalized.Provider != "" && normalized.Model != "" {
		prepared, txn, prepareErr := (&Stack{apiKeyCredentials: apiKeyCredentials}).prepareProviderCredentials([]ModelConfig{normalized})
		credentialBootstrap = txn
		if prepareErr != nil {
			return nil, errors.Join(prepareErr, credentialBootstrap.rollback())
		}
		cfg.Model = prepared[0]
	}
	effectiveApprovalMode := approvalMode(firstNonEmpty(cfg.ApprovalMode, doc.Runtime.ApprovalMode))
	effectivePolicyProfile := policyProfile(firstNonEmpty(cfg.PolicyProfile, doc.Runtime.PolicyProfile))
	baseAssembly := assembly.CloneResolvedAssembly(cfg.Assembly)
	sessionStore := sessionfile.NewStore(sessionfile.Config{
		RootDir: filepath.Join(storeDir, "sessions"),
	})
	sessions := sessionStore
	taskStore := sessionfile.NewTaskStore(sessionStore)
	approvalRecovery := internalcontrolclient.NewApprovalRecoveryGate(sessions)
	cursorSecret, err := loadOrCreateControlClientCursorSecret(storeDir)
	if err != nil {
		return nil, err
	}
	cursorCodec, err := eventstream.NewCursorCodec(eventstream.CursorCodecConfig{Secret: cursorSecret})
	if err != nil {
		return nil, err
	}
	controlFeeds, err := internalcontrolclient.NewFeedRegistry(internalcontrolclient.FeedRegistryConfig{
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
	providerUsage := providerusage.NewRegistry(map[string]providerusage.Reader{
		"openai-codex": codexAuth,
	})
	lookup, err := newModelLookup(configStore, cfg.Model, cfg.ContextWindow)
	if err != nil {
		return nil, errors.Join(err, credentialBootstrap.rollback())
	}
	modelSnapshot := lookup.Snapshot()
	providerProfiles := make([]modelprofile.ModelProfile, 0, len(modelSnapshot.Configs))
	for _, configured := range modelSnapshot.Configs {
		profile, profileErr := modelprofilebuilder.FromProvider(configured)
		if profileErr != nil {
			return nil, errors.Join(profileErr, credentialBootstrap.rollback())
		}
		providerProfiles = append(providerProfiles, profile)
	}
	if len(providerProfiles) > 0 {
		doc.Models = modelSnapshot
		doc.ModelProfiles, err = modelprofile.Upsert(doc.ModelProfiles, providerProfiles...)
		if err != nil {
			return nil, errors.Join(err, credentialBootstrap.rollback())
		}
		doc.ModelProfiles.DefaultProfileID = modelprofile.BuildProviderID(modelSnapshot.DefaultID)
		if err := configStore.Save(doc); err != nil {
			if configstore.WriteCommitted(err) {
				credentialBootstrap.commit()
			}
			return nil, errors.Join(err, credentialBootstrap.rollback())
		}
	}
	credentialBootstrap.commit()
	lookup.resolveHTTPClient = func(ctx context.Context, modelCfg ModelConfig) (*http.Client, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.EqualFold(modelCfg.Provider, "openai-codex") || modelCfg.CredentialRef != modelconfig.CodexOAuthCredentialRef {
			return nil, fmt.Errorf("gatewayapp: unsupported managed model credential %q for provider %q", modelCfg.CredentialRef, modelCfg.Provider)
		}
		if modelconfig.NormalizeBaseURL(modelCfg.BaseURL) != modelconfig.NormalizeBaseURL(modelconfig.CodexOAuthBaseURL) {
			return nil, fmt.Errorf("gatewayapp: codex OAuth requires the maintained endpoint %s", modelconfig.CodexOAuthBaseURL)
		}
		return codexAuth.AuthenticatedClient(modelCfg.HTTPClient)
	}
	lookup.resolveAPIKey = apiKeyCredentials.Get
	sandboxCfg := mergeSandboxConfig(doc.Sandbox, cfg.Sandbox)
	leaseOwnerID, err := newStackLeaseOwnerID()
	if err != nil {
		return nil, err
	}
	stack := &Stack{
		Sessions: sessions,
		AppName:  appName,
		UserID:   userID,
		Workspace: session.WorkspaceRef{
			Key: workspaceKey,
			CWD: workspaceCWD,
		},
		lookup:            lookup,
		store:             configStore,
		storeDir:          storeDir,
		leaseOwnerID:      leaseOwnerID,
		taskStore:         taskStore,
		controlFeeds:      controlFeeds,
		approvalRecovery:  approvalRecovery,
		codexAuth:         codexAuth,
		apiKeyCredentials: apiKeyCredentials,
		providerUsage:     providerUsage,
		runtime: stackRuntimeConfig{
			ApprovalMode:  effectiveApprovalMode,
			PolicyProfile: effectivePolicyProfile,
			ContextWindow: cfg.ContextWindow,
			SystemPrompt:  cfg.SystemPrompt,
			Model:         cfg.Model,
			SkillDirs:     cloneStringSlicePreserveNil(cfg.SkillDirs),
			Plugins:       clonePluginConfigs(doc.Plugins),
			BaseAssembly:  baseAssembly,
			Assembly:      assembly.CloneResolvedAssembly(baseAssembly),
		},
		sandbox: sandboxCfg,
	}
	stack.placementCache = newPlacementSnapshot(doc)
	configStore.savedHook = stack.invalidatePlacementSnapshot
	controlState, err := internalcontrolclient.NewStateService(internalcontrolclient.StateServiceConfig{
		Sessions: sessions, Runtime: stack, Feeds: controlFeeds,
	})
	if err != nil {
		return nil, err
	}
	stack.controlState = controlState
	controlOperations, err := internalcontrolclient.NewFileOperationStoreWithConfig(
		filepath.Join(storeDir, "control-operations"),
		internalcontrolclient.OperationRetentionConfig{TerminalRetention: cfg.ControlOperationRetention},
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
	controlCommands, err := internalcontrolclient.NewCommandService(internalcontrolclient.CommandServiceConfig{
		Authorizer: internalcontrolclient.SessionAuthorizer{Sessions: sessions},
		Operations: controlOperations,
		Backend:    stack,
	})
	if err != nil {
		return nil, err
	}
	stack.controlCommands = controlCommands
	controlClient, err := internalcontrolclient.NewClient(internalcontrolclient.ClientConfig{
		Commands: controlCommands, State: controlState, Feeds: controlFeeds,
		Authorizer: internalcontrolclient.SessionAuthorizer{Sessions: sessions}, Sessions: sessions,
	})
	if err != nil {
		return nil, err
	}
	stack.controlClient = controlClient
	controlTaskStreams, err := controltaskstream.New(controltaskstream.Config{
		Tasks: taskStore,
		Streams: func() stream.Service {
			provider := stack.KernelStreams()
			if provider == nil {
				return nil
			}
			return provider.Streams()
		},
		Authorizer: taskStreamAuthorizer{inner: internalcontrolclient.SessionAuthorizer{Sessions: sessions}},
		Secret:     cursorSecret,
	})
	if err != nil {
		return nil, err
	}
	stack.taskStreams = acptaskstream.New(controlTaskStreams)
	if err := stack.rebuildGateway(); err != nil {
		return nil, err
	}
	return stack, nil
}

// StartApprovalRecovery begins the Control-owned abandoned-approval sweep.
// Turn entry remains gated until the sweep completes.
func (s *Stack) StartApprovalRecovery(ctx context.Context) {
	if s == nil || s.approvalRecovery == nil {
		return
	}
	s.approvalRecovery.Start(ctx)
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
		Metadata:     withSandboxPolicyRootMetadata(baseMetadata, sandboxCfg, workspaceCWD),
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

func (s *Stack) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	exec := s.exec
	s.exec = nil
	mcpMgr := s.mcpMgr
	s.mcpMgr = nil
	controlOperations := s.operations
	s.operations = nil
	s.mu.Unlock()

	var errs []error
	if exec != nil {
		if err := exec.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if mcpMgr != nil {
		if err := mcpMgr.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if controlOperations != nil {
		if err := controlOperations.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("gatewayapp stack: close failed: %v", errs)
	}
	return nil
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
