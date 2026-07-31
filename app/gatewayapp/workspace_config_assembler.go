package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// workspaceConfigAssembler assembles one detached Session Runtime from the
// current app configuration and workspace files. It deliberately owns no
// cache: execution activation calls it on demand, while the resulting Session
// Runtime keeps that assembled configuration stable for its lifetime.
type workspaceConfigAssembler struct {
	owner *Stack
}

func newWorkspaceConfigAssembler(owner *Stack) (*workspaceConfigAssembler, error) {
	if owner == nil {
		return nil, errors.New("gatewayapp: workspace config assembler owner is required")
	}
	return &workspaceConfigAssembler{owner: owner}, nil
}

// assembleLocked builds one Session-scoped composition while the caller holds
// the Host reconfiguration lock. The lock gives the assembler one coherent
// view of current AppConfig and in-memory runtime configuration.
func (a *workspaceConfigAssembler) assembleLocked(
	ctx context.Context,
	workspace session.WorkspaceRef,
) (*Stack, error) {
	if a == nil || a.owner == nil {
		return nil, errors.New("gatewayapp: workspace config assembler is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	owner := a.owner
	owner.mu.RLock()
	runtimeConfig := cloneSessionRuntimeConfig(owner.runtime)
	sandboxConfig := cloneSandboxConfig(owner.sandbox)
	owner.mu.RUnlock()
	doc, err := owner.store.Load()
	if err != nil {
		return nil, err
	}
	lookup, err := cloneSessionModelLookup(owner.lookup)
	if err != nil {
		return nil, err
	}
	placement := newPlacementSnapshot(doc)
	if err := controlplacement.ValidateSnapshot(placement.placement); err != nil {
		return nil, err
	}

	child := &Stack{
		Workspace:      workspace,
		runtime:        runtimeConfig,
		sandbox:        sandboxConfig,
		lookup:         lookup,
		placementCache: placement,
	}
	owner.shareSessionHostState(child)
	if err := child.rebuildGatewayLockedContext(ctx); err != nil {
		return nil, fmt.Errorf(
			"gatewayapp: assemble Session Runtime for workspace %q at %q: %w",
			workspace.Key,
			workspace.CWD,
			err,
		)
	}
	return child, nil
}

// shareSessionHostState is the single contract for state shared with one
// Session Runtime. Mutable model and placement configuration deliberately stay
// Session-local; durable stores, credentials, feeds, and Host lifecycle remain
// shared authorities.
func (s *Stack) shareSessionHostState(child *Stack) {
	child.Sessions = s.Sessions
	child.AppName = s.AppName
	child.UserID = s.UserID // Compatibility only; not a Runtime partition key.
	child.store = s.store
	child.storeDir = s.storeDir
	child.leaseOwnerID = s.leaseOwnerID
	child.reconfigureGate = s.reconfigureLock()
	child.assemblyMutationGate = s.assemblyMutationLock()
	child.taskStore = s.taskStore
	child.controlFeeds = s.controlFeeds
	child.approvalRecovery = s.approvalRecovery
	child.lifecycleCtx = s.lifecycleCtx
	child.codexAuth = s.codexAuth
	child.grokAuth = s.grokAuth
	child.apiKeyCredentials = s.apiKeyCredentials
	child.providerUsage = s.providerUsage
}

func cloneSessionRuntimeConfig(config stackRuntimeConfig) stackRuntimeConfig {
	config.Model = cloneSessionModelConfig(config.Model)
	config.SkillDirs = cloneStringSlicePreserveNil(config.SkillDirs)
	config.Plugins = clonePluginConfigs(config.Plugins)
	config.BaseAssembly = assembly.CloneResolvedAssembly(config.BaseAssembly)
	config.Assembly = assembly.CloneResolvedAssembly(config.BaseAssembly)
	config.PluginSkills = nil
	config.SkillCatalog = skill.Catalog{}
	config.BaseMetadata = nil
	config.EstimatedPromptPrefixTokens = 0
	return config
}

func cloneSandboxConfig(config SandboxConfig) SandboxConfig {
	config.WritableRoots = append([]string(nil), config.WritableRoots...)
	config.ReadOnlySubpaths = append([]string(nil), config.ReadOnlySubpaths...)
	if config.NetworkEnabled != nil {
		networkEnabled := *config.NetworkEnabled
		config.NetworkEnabled = &networkEnabled
	}
	return config
}

func cloneSessionModelLookup(source *modelLookup) (*modelLookup, error) {
	if source == nil {
		return nil, errors.New("gatewayapp: model lookup is unavailable")
	}
	snapshot := source.Snapshot()
	for index := range snapshot.Configs {
		snapshot.Configs[index] = cloneSessionModelConfig(snapshot.Configs[index])
	}
	source.mu.RLock()
	contextWindow := source.contextWindow
	resolveHTTPClient := source.resolveHTTPClient
	resolveAPIKey := source.resolveAPIKey
	source.mu.RUnlock()

	cloned := &modelLookup{}
	cloned.Restore(snapshot, contextWindow)
	cloned.resolveHTTPClient = resolveHTTPClient
	cloned.resolveAPIKey = resolveAPIKey
	return cloned, nil
}

func cloneSessionModelConfig(config ModelConfig) ModelConfig {
	config.ReasoningLevels = append([]string(nil), config.ReasoningLevels...)
	if config.ImageInput != nil {
		imageInput := *config.ImageInput
		config.ImageInput = &imageInput
	}
	return config
}
