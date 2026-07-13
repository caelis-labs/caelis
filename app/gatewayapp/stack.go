package gatewayapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/modelregistry"
	acpassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	"github.com/caelis-labs/caelis/internal/controlplane"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type Config struct {
	AppName                     string
	UserID                      string
	StoreDir                    string
	WorkspaceKey                string
	WorkspaceCWD                string
	ApprovalMode                string
	PolicyProfile               string
	ContextWindow               int
	SystemPrompt                string
	Assembly                    assembly.ResolvedAssembly
	SkillDirs                   []string
	DisableBuiltInAgentProfiles bool
	Model                       ModelConfig
	Sandbox                     SandboxConfig
}

type ModelConfig = modelregistry.Config
type ModelProfileConfig = modelregistry.ProfileConfig
type ModelChoice = modelregistry.Choice

type GatewayRuntime interface {
	gateway.Service
	gateway.StreamProvider
}

type Stack struct {
	Sessions         session.Service
	AppName          string
	UserID           string
	Workspace        session.WorkspaceRef
	lookup           *modelLookup
	store            *appConfigStore
	storeDir         string
	leaseOwnerID     string
	mu               sync.RWMutex
	reconfigureMu    sync.Mutex
	runtime          stackRuntimeConfig
	sandbox          SandboxConfig
	exec             sandbox.Runtime
	engine           *runtime.Runtime
	placement        controlplane.PlacementExecutor
	acpControlPlane  *acpassembly.ControlPlane
	taskStore        task.Store
	controlFeeds     controlclientport.FeedRegistry
	controlState     controlclientport.StateReader
	controlCommands  controlclientport.CommandClient
	controlClient    controlclientport.Service
	approvalRecovery *internalcontrolclient.ApprovalRecoveryGate
	gateway          *kernelimpl.Gateway
	mcpMgr           *mcp.Manager

	// Optional test seam; nil uses the platform lifecycle runtime factory.
	sandboxLifecycleFactory sandboxLifecycleRuntimeFactory

	// Optional test seam; nil uses the configured agent refresh path.
	refreshConfiguredAgentsHook func() error
}

// CurrentGateway returns the current aggregate gateway runtime.
//
// Deprecated: use KernelTurns, KernelSessions, KernelControlPlane, or
// KernelStreams so production callers depend on the narrow service they need.
func (s *Stack) CurrentGateway() GatewayRuntime {
	return s.kernelRuntime()
}

func (s *Stack) kernelRuntime() GatewayRuntime {
	gw := s.currentGateway()
	if gw == nil {
		return nil
	}
	return gw
}

// KernelTurns returns the current gateway turn service without exposing the
// broader session/control-plane aggregate to callers that only submit turns.
func (s *Stack) KernelTurns() gateway.TurnService {
	return s.kernelRuntime()
}

// KernelSessions returns the current gateway session service without exposing
// turn or control-plane operations to session-only callers.
func (s *Stack) KernelSessions() gateway.SessionService {
	return s.kernelRuntime()
}

// KernelControlPlane returns the current gateway control-plane service without
// exposing turn/session operations to controller-only callers.
func (s *Stack) KernelControlPlane() gateway.ControlPlaneService {
	return s.kernelRuntime()
}

// KernelStreams returns the current gateway stream provider without exposing
// gateway control or session operations.
func (s *Stack) KernelStreams() gateway.StreamProvider {
	return s.kernelRuntime()
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
	effectiveApprovalMode := approvalMode(firstNonEmpty(cfg.ApprovalMode, doc.Runtime.ApprovalMode))
	effectivePolicyProfile := policyProfile(firstNonEmpty(cfg.PolicyProfile, doc.Runtime.PolicyProfile))
	baseAssembly := assembly.CloneResolvedAssembly(cfg.Assembly)
	sessionStore := sessionfile.NewStore(sessionfile.Config{
		RootDir: filepath.Join(storeDir, "sessions"),
	})
	sessions := sessionfile.NewService(sessionStore)
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
	lookup, err := newModelLookup(configStore, cfg.Model, cfg.ContextWindow)
	if err != nil {
		return nil, err
	}
	if !cfg.DisableBuiltInAgentProfiles {
		if err := ensureBuiltInAgentProfiles(context.Background(), storeDir, configStore); err != nil {
			return nil, err
		}
		doc, err = configStore.Load()
		if err != nil {
			return nil, err
		}
	}
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
		lookup:           lookup,
		store:            configStore,
		storeDir:         storeDir,
		leaseOwnerID:     leaseOwnerID,
		taskStore:        taskStore,
		controlFeeds:     controlFeeds,
		approvalRecovery: approvalRecovery,
		runtime: stackRuntimeConfig{
			ApprovalMode:                effectiveApprovalMode,
			PolicyProfile:               effectivePolicyProfile,
			ContextWindow:               cfg.ContextWindow,
			SystemPrompt:                cfg.SystemPrompt,
			Model:                       cfg.Model,
			SkillDirs:                   cloneStringSlicePreserveNil(cfg.SkillDirs),
			DisableBuiltInAgentProfiles: cfg.DisableBuiltInAgentProfiles,
			Plugins:                     clonePluginConfigs(doc.Plugins),
			BaseAssembly:                baseAssembly,
			Assembly:                    assembly.CloneResolvedAssembly(baseAssembly),
		},
		sandbox: sandboxCfg,
	}
	controlState, err := internalcontrolclient.NewStateService(internalcontrolclient.StateServiceConfig{
		Sessions: sessions, Runtime: stack, Feeds: controlFeeds,
	})
	if err != nil {
		return nil, err
	}
	stack.controlState = controlState
	controlCommands, err := internalcontrolclient.NewCommandService(internalcontrolclient.CommandServiceConfig{
		Authorizer: internalcontrolclient.SessionAuthorizer{Sessions: sessions},
		Operations: internalcontrolclient.NewFileOperationStore(filepath.Join(storeDir, "control-operations")),
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
