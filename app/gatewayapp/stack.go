package gatewayapp

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/modelregistry"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type Config struct {
	AppName       string
	UserID        string
	StoreDir      string
	WorkspaceKey  string
	WorkspaceCWD  string
	ApprovalMode  string
	PolicyProfile string
	// PermissionMode is a legacy approval-mode input kept for compatibility.
	PermissionMode string
	ContextWindow  int
	SystemPrompt   string
	Assembly       assembly.ResolvedAssembly
	Model          ModelConfig
	Sandbox        SandboxConfig
}

type ModelConfig = modelregistry.Config
type ModelProfileConfig = modelregistry.ProfileConfig
type ModelChoice = modelregistry.Choice

type GatewayRuntime interface {
	gateway.Service
	gateway.StreamProvider
}

type Stack struct {
	Sessions      session.Service
	AppName       string
	UserID        string
	Workspace     session.WorkspaceRef
	lookup        *modelLookup
	store         *appConfigStore
	storeDir      string
	mu            sync.RWMutex
	reconfigureMu sync.Mutex
	runtime       stackRuntimeConfig
	sandbox       SandboxConfig
	exec          sandbox.Runtime
	engine        *local.Runtime
	taskStore     *taskfile.Store
	gateway       *kernelimpl.Gateway
}

func (s *Stack) CurrentGateway() GatewayRuntime {
	gw := s.currentGateway()
	if gw == nil {
		return nil
	}
	return gw
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
	if doc.Sandbox.NetworkEnabled == nil {
		networkEnabled := true
		doc.Sandbox.NetworkEnabled = &networkEnabled
		if err := configStore.Save(doc); err != nil {
			return nil, err
		}
	}
	effectiveApprovalMode := approvalMode(firstNonEmpty(cfg.ApprovalMode, cfg.PermissionMode, doc.Runtime.ApprovalMode))
	effectivePolicyProfile := policyProfile(firstNonEmpty(cfg.PolicyProfile, doc.Runtime.PolicyProfile))
	baseAssembly := assembly.CloneResolvedAssembly(cfg.Assembly)
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir: filepath.Join(storeDir, "sessions"),
	}))
	taskStore := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(storeDir, "tasks")})
	lookup, err := newModelLookup(configStore, cfg.Model, cfg.ContextWindow)
	if err != nil {
		return nil, err
	}
	if err := ensureBuiltInAgentProfiles(context.Background(), storeDir, configStore); err != nil {
		return nil, err
	}
	doc, err = configStore.Load()
	if err != nil {
		return nil, err
	}
	sandboxCfg := mergeSandboxConfig(doc.Sandbox, cfg.Sandbox)
	baseMetadata := map[string]any{}
	systemPrompt, err := buildSystemPrompt(promptConfig{
		AppName:      appName,
		WorkspaceDir: workspaceCWD,
		BasePrompt:   cfg.SystemPrompt,
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(systemPrompt) != "" {
		baseMetadata["system_prompt"] = systemPrompt
	}
	if reasoning := strings.TrimSpace(cfg.Model.ReasoningEffort); reasoning != "" {
		baseMetadata["reasoning_effort"] = reasoning
	}
	baseMetadata = withSandboxPolicyRootMetadata(baseMetadata, sandboxCfg, workspaceCWD)
	stack := &Stack{
		Sessions: sessions,
		AppName:  appName,
		UserID:   userID,
		Workspace: session.WorkspaceRef{
			Key: workspaceKey,
			CWD: workspaceCWD,
		},
		lookup:    lookup,
		store:     configStore,
		storeDir:  storeDir,
		taskStore: taskStore,
		runtime: stackRuntimeConfig{
			ApprovalMode:   effectiveApprovalMode,
			PolicyProfile:  effectivePolicyProfile,
			PermissionMode: cfg.PermissionMode,
			ContextWindow:  cfg.ContextWindow,
			SystemPrompt:   cfg.SystemPrompt,
			Model:          cfg.Model,
			BaseAssembly:   baseAssembly,
			Assembly:       assembly.CloneResolvedAssembly(baseAssembly),
			BaseMetadata:   cloneMap(baseMetadata),
		},
		sandbox: sandboxCfg,
	}
	stack.runtime.Assembly = stack.configuredAssembly(baseAssembly, doc.Agents, stack.runtime)
	if err := stack.rebuildGateway(); err != nil {
		return nil, err
	}
	return stack, nil
}
