package gatewayapp

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/modelregistry"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp/internal/pluginregistry"
	"github.com/OnslaughtSnail/caelis/impl/agent/local"
	sessionfile "github.com/OnslaughtSnail/caelis/impl/session/file"
	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	"github.com/OnslaughtSnail/caelis/impl/tool/mcp"
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
	mcpMgr        *mcp.Manager
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
	skillDirs, err := pluginSkillDirsFromConfig(workspaceCWD, doc.Plugins)
	if err != nil {
		return nil, err
	}
	baseMetadata, err := buildStackBaseMetadata(appName, workspaceCWD, cfg.SystemPrompt, cfg.Model, sandboxCfg, skillDirs)
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
			Plugins:        clonePluginConfigs(doc.Plugins),
			BaseAssembly:   baseAssembly,
			Assembly:       assembly.CloneResolvedAssembly(baseAssembly),
			BaseMetadata:   cloneMap(baseMetadata),
		},
		sandbox: sandboxCfg,
	}
	configuredAssembly, err := stack.configuredAssembly(baseAssembly, doc.Agents, doc.Plugins, stack.runtime)
	if err != nil {
		return nil, err
	}
	stack.runtime.Assembly = configuredAssembly
	if err := stack.rebuildGateway(); err != nil {
		return nil, err
	}
	return stack, nil
}

func pluginSkillDirsFromConfig(workspaceCWD string, plugins []PluginConfig) ([]string, error) {
	skillDirs := DefaultSkillDiscoveryDirs(workspaceCWD)
	for _, pCfg := range plugins {
		if !pCfg.Enabled {
			continue
		}
		p, err := pluginregistry.ParsePlugin(pCfg.Root)
		if err != nil {
			return nil, fmt.Errorf("gatewayapp: parse enabled plugin %q failed: %w", pCfg.ID, err)
		}
		for _, sc := range p.Skills {
			skillDirs = append(skillDirs, sc.Root)
		}
	}
	return skillDirs, nil
}

func buildStackBaseMetadata(appName, workspaceCWD, basePrompt string, model ModelConfig, sandboxCfg SandboxConfig, skillDirs []string) (map[string]any, error) {
	baseMetadata := map[string]any{}
	systemPrompt, err := buildSystemPrompt(promptConfig{
		AppName:           appName,
		WorkspaceDir:      workspaceCWD,
		BasePrompt:        basePrompt,
		SkillDirs:         skillDirs,
		SandboxMode:       promptSandboxContextMode(sandboxCfg),
		DefaultPermission: promptDefaultPermissionSummary(sandboxCfg),
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(systemPrompt) != "" {
		baseMetadata["system_prompt"] = systemPrompt
	}
	if reasoning := strings.TrimSpace(model.ReasoningEffort); reasoning != "" {
		baseMetadata["reasoning_effort"] = reasoning
	}
	return withSandboxPolicyRootMetadata(baseMetadata, sandboxCfg, workspaceCWD), nil
}

func promptSandboxContextMode(cfg SandboxConfig) string {
	requested := strings.ToLower(strings.TrimSpace(cfg.RequestedType))
	switch requested {
	case "host":
		return "host"
	case "", "auto":
		return "restricted sandbox (auto)"
	default:
		return requested + " sandbox"
	}
}

func promptDefaultPermissionSummary(cfg SandboxConfig) string {
	if strings.EqualFold(strings.TrimSpace(cfg.RequestedType), "host") {
		return "host permissions"
	}
	return "workspace-write sandbox; Host execution requires explicit escalation"
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
