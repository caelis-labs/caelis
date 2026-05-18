package gatewayapp

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/modelregistry"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/bwrap"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/host"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/landlock"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/seatbelt"
	_ "github.com/OnslaughtSnail/caelis/impl/sandbox/windows"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type Config struct {
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
	Sandbox        SandboxConfig
}

type ModelConfig = modelregistry.Config
type ModelProfileConfig = modelregistry.ProfileConfig
type ModelChoice = modelregistry.Choice

type Stack struct {
	Gateway       *kernel.Gateway
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
}

func (s *Stack) CurrentGateway() *kernel.Gateway {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Gateway
}

type SessionRuntimeState struct {
	ModelID         string
	ModelAlias      string
	ReasoningEffort string
	SessionMode     string
	SandboxMode     string
}

type SandboxStatus struct {
	RequestedBackend   string
	ResolvedBackend    string
	Route              string
	FallbackReason     string
	InstallHint        string
	SetupRequired      bool
	SetupError         string
	SetupVersion       int
	SetupMarkerCurrent bool
	SetupMarkerReason  string
	SetupRunnerHash    string
	SetupPolicyHash    string
	SetupOfflineUser   string
	SetupOnlineUser    string
	SetupOwnerUser     string
	SetupReadRoots     int
	SetupWriteRoots    int
	SetupDenyRead      int
	SetupDenyWrite     int
	SecuritySummary    string
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
	baseAssembly := assembly.CloneResolvedAssembly(cfg.Assembly)
	cfg.Assembly = withConfiguredACPAgents(cfg.Assembly, doc.Agents, defaultSelfACPAgent(defaultSelfACPAgentConfig{
		Config:       cfg,
		AppName:      appName,
		UserID:       userID,
		StoreDir:     storeDir,
		WorkspaceKey: workspaceKey,
		WorkspaceCWD: workspaceCWD,
	}))
	sessions := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{
		RootDir: filepath.Join(storeDir, "sessions"),
	}))
	taskStore := taskfile.NewStore(taskfile.Config{RootDir: filepath.Join(storeDir, "tasks")})
	lookup, err := newModelLookup(configStore, cfg.Model, cfg.ContextWindow)
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
			PermissionMode: cfg.PermissionMode,
			ContextWindow:  cfg.ContextWindow,
			Model:          cfg.Model,
			BaseAssembly:   baseAssembly,
			Assembly:       assembly.CloneResolvedAssembly(cfg.Assembly),
			BaseMetadata:   cloneMap(baseMetadata),
		},
		sandbox: sandboxCfg,
	}
	if err := stack.rebuildGateway(); err != nil {
		return nil, err
	}
	return stack, nil
}
