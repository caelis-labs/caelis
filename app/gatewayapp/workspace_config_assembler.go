package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
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

type sessionRuntimeActivity struct {
	retainWork  func(session.SessionRef) func()
	taskChanged func(session.SessionRef)
}

func newWorkspaceConfigAssembler(owner *Stack) (*workspaceConfigAssembler, error) {
	if owner == nil {
		return nil, errors.New("gatewayapp: workspace config assembler owner is required")
	}
	return &workspaceConfigAssembler{owner: owner}, nil
}

// assembleSnapshot builds one Session-scoped composition from one AppConfig
// read plus detached process configuration. It does not coordinate with later
// Host mutations; the resulting Runtime owns this snapshot until release.
func (a *workspaceConfigAssembler) assembleSnapshot(
	ctx context.Context,
	active session.Session,
	activity sessionRuntimeActivity,
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
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return nil, err
	}
	doc, err := owner.store.LoadContext(ctx)
	if err != nil {
		return nil, err
	}
	owner.mu.RLock()
	runtimeConfig := cloneSessionRuntimeConfig(owner.runtime)
	sandboxOverride := cloneSandboxConfig(owner.sandboxOverride)
	childControlURL := owner.childControlURL
	childControlTokenFile := owner.childControlTokenFile
	owner.mu.RUnlock()
	sandboxConfig := mergeSandboxConfig(doc.Sandbox, sandboxOverride)
	securityPosture := resolveProcessSecurityPosture(runtimeConfig)
	if securityPosture.RequiredSandboxBackend != "" {
		sandboxConfig.RequestedType = string(securityPosture.RequiredSandboxBackend)
	}
	lookup, err := cloneSessionModelLookup(owner.lookup, doc)
	if err != nil {
		return nil, err
	}
	if pinned, ok := owner.sessionModelPins.config(active.SessionID); ok {
		if _, err := lookup.upsert(pinned, false); err != nil {
			return nil, fmt.Errorf("gatewayapp: inject pinned model for Session %q: %w", active.SessionID, err)
		}
	}
	runtimeModel, err := resolveRuntimeProviderProfile(
		doc.ModelProfiles,
		lookup,
		runtimeConfig.ModelProfileID,
		runtimeConfig.ModelProfileEffort,
	)
	if err != nil {
		return nil, err
	}
	runtimeConfig.Model = runtimeModel
	placement := newPlacementSnapshot(doc)
	if err := controlplacement.ValidateSnapshot(placement.placement); err != nil {
		return nil, err
	}

	child := &Stack{
		Workspace:             workspace,
		runtime:               runtimeConfig,
		sandbox:               sandboxConfig,
		lookup:                lookup,
		placementCache:        placement,
		appConfigSnapshot:     ptrToConfigSnapshot(doc),
		retainRuntimeWork:     activity.retainWork,
		runtimeTaskChanged:    activity.taskChanged,
		childControlURL:       childControlURL,
		childControlTokenFile: childControlTokenFile,
		sessionModelPins:      owner.sessionModelPins,
	}
	owner.shareSessionHostState(child)
	if err := child.buildInitialGatewayRuntime(ctx); err != nil {
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
// Session Runtime. The App model catalog remains live for explicit selection,
// while the child lookup and placement configuration stay pinned for Turn
// execution. Durable stores, credentials, feeds, and Host lifecycle remain
// shared authorities.
func (s *Stack) shareSessionHostState(child *Stack) {
	child.Sessions = s.Sessions
	child.AppName = s.AppName
	child.UserID = s.UserID // Compatibility only; not a Runtime partition key.
	child.store = s.store
	child.storeDir = s.storeDir
	child.leaseOwnerID = s.leaseOwnerID
	child.taskStore = s.taskStore
	child.controlFeeds = s.controlFeeds
	child.approvalRecovery = s.approvalRecovery
	child.lifecycleCtx = s.lifecycleCtx
	child.codexAuth = s.codexAuth
	child.grokAuth = s.grokAuth
	child.apiKeyCredentials = s.apiKeyCredentials
	child.providerUsage = s.providerUsage
	child.modelCatalog = s.lookup
	child.sessionModelPins = s.sessionModelPins
	if s.hostedChildMailbox != nil {
		child.hostedChildMailbox = s.hostedChildMailbox
	} else {
		child.hostedChildMailbox = s.routeHostedChildMessage
	}
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

func cloneSessionModelLookup(source *modelLookup, doc AppConfig) (*modelLookup, error) {
	if source == nil {
		return nil, errors.New("gatewayapp: model lookup is unavailable")
	}
	source.mu.RLock()
	contextWindow := source.contextWindow
	resolveHTTPClient := source.resolveHTTPClient
	resolveTransportHTTPClient := source.resolveTransportHTTPClient
	resolveAPIKey := source.resolveAPIKey
	source.mu.RUnlock()

	cloned, err := newModelLookupFromDocument(doc, contextWindow)
	if err != nil {
		return nil, err
	}
	cloned.resolveHTTPClient = resolveHTTPClient
	cloned.resolveTransportHTTPClient = resolveTransportHTTPClient
	cloned.resolveAPIKey = resolveAPIKey
	return cloned, nil
}

func ptrToConfigSnapshot(doc AppConfig) *AppConfig {
	snapshot := configstore.Normalize(doc)
	return &snapshot
}

func cloneSessionModelConfig(config ModelConfig) ModelConfig {
	config.ReasoningLevels = append([]string(nil), config.ReasoningLevels...)
	if config.ImageInput != nil {
		imageInput := *config.ImageInput
		config.ImageInput = &imageInput
	}
	return config
}
