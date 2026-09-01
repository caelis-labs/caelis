package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/skill"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/memorybinding"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

// workspaceConfigAssembler assembles one detached Session Runtime from the
// current app configuration and workspace files. It deliberately owns no
// cache: execution activation calls it on demand, while the resulting Session
// Runtime keeps that assembled configuration stable for its lifetime.
type workspaceConfigAssembler struct {
	deps sessionRuntimeAssemblyDeps
}

type sessionRuntimeActivity struct {
	retainWork    func(session.SessionRef) func()
	taskChanged   func(session.SessionRef)
	taskCommitted func(*task.Entry)
}

func newWorkspaceConfigAssembler(deps sessionRuntimeAssemblyDeps) (*workspaceConfigAssembler, error) {
	if deps.authorities.store == nil || deps.modelCatalog == nil || deps.processConfig == nil {
		return nil, errors.New("gatewayapp: workspace config assembler dependencies are required")
	}
	return &workspaceConfigAssembler{deps: deps}, nil
}

// assembleSnapshot builds one Session-scoped composition from a revision-stable
// AppConfig and credential read plus detached process configuration. It does not
// coordinate with later Host mutations; the resulting Runtime owns this snapshot
// until release.
func (a *workspaceConfigAssembler) assembleSnapshot(
	ctx context.Context,
	active session.Session,
	activity sessionRuntimeActivity,
	sessions session.Service,
) (*sessionRuntimeInstance, error) {
	if a == nil || sessions == nil {
		return nil, errors.New("gatewayapp: workspace config assembler is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	deps := a.deps
	authorities := deps.authorities
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return nil, err
	}
	doc, lookup, err := a.loadRuntimeModelSnapshot(ctx, active)
	if err != nil {
		return nil, err
	}
	process := deps.processConfig.snapshot()
	runtimeConfig := process.runtime
	sandboxConfig := mergeSandboxConfig(doc.Sandbox, process.sandboxOverride)
	securityPosture := resolveProcessSecurityPosture(runtimeConfig)
	if securityPosture.RequiredSandboxBackend != "" {
		sandboxConfig.RequestedType = string(securityPosture.RequiredSandboxBackend)
	}
	runtimeModel, err := resolveRuntimeProfile(
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
	memoryBinding, err := selectRuntimeMemoryBinding(ctx, doc.Memory, process, active.SessionRef, workspace)
	if err != nil {
		return nil, err
	}
	if memoryBinding != nil {
		if authorities.memoryHost == nil {
			return nil, errors.New("gatewayapp: enabled Memory binding has no managed appliance")
		}
		if err := authorities.memoryHost.ValidateBinding(*memoryBinding); err != nil {
			return nil, fmt.Errorf("gatewayapp: validate managed Memory endpoint: %w", err)
		}
		if err := memorybinding.ValidateSessionAdmission(ctx, sessions, active.SessionRef, *memoryBinding); err != nil {
			return nil, fmt.Errorf("gatewayapp: validate canonical Session Memory binding: %w", err)
		}
	}

	instance := &sessionRuntimeInstance{
		runtimeComposition: runtimeComposition{
			authorities: authorities,
			sessions:    sessions,
			workspace:   workspace,
			lookup:      lookup,
			activation: &sessionRuntimeActivation{
				modelCatalog:          deps.modelCatalog,
				appConfig:             ptrToRuntimeConfigSnapshot(doc),
				sessionRef:            active.SessionRef,
				memoryBinding:         memoryBinding,
				childControlURL:       process.childControlURL,
				childControlTokenFile: process.childControlTokenFile,
			},
			placementCache:     placement,
			activeRuntime:      runtimeConfig,
			sandbox:            sandboxConfig,
			retainRuntimeWork:  activity.retainWork,
			runtimeTaskChanged: activity.taskChanged,
			taskCommitted:      activity.taskCommitted,
		},
	}
	if err := instance.buildInitialGatewayRuntime(ctx); err != nil {
		return nil, fmt.Errorf(
			"gatewayapp: assemble Session Runtime for workspace %q at %q: %w",
			workspace.Key,
			workspace.CWD,
			err,
		)
	}
	return instance, nil
}

func (a *workspaceConfigAssembler) loadRuntimeModelSnapshot(ctx context.Context, active session.Session) (AppConfig, *modelLookup, error) {
	const maxAttempts = 4
	deps := a.deps
	if err := recoverProviderCredentialRetirements(ctx, deps.authorities.store, deps.authorities.apiKeyCredentials); err != nil {
		return AppConfig{}, nil, err
	}
	for attempt := 0; attempt < maxAttempts; attempt++ {
		doc, err := deps.authorities.store.LoadContext(ctx)
		if err != nil {
			return AppConfig{}, nil, err
		}
		lookup, err := cloneSessionModelLookup(deps.modelCatalog, doc)
		if err != nil {
			return AppConfig{}, nil, err
		}
		credentialRefs := providerProfileAPIKeyCredentialRefs(doc)
		if pinned, ok := deps.authorities.sessionModelPins.config(ctx, active.SessionID); ok {
			if _, err := lookup.upsert(pinned, false); err != nil {
				return AppConfig{}, nil, fmt.Errorf("gatewayapp: inject pinned model for Session %q: %w", active.SessionID, err)
			}
			credentialRefs = append(credentialRefs, pinned.CredentialRef)
		}
		pinErr := lookup.pinAPIKeyCredentials(ctx, credentialRefs)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return AppConfig{}, nil, ctxErr
		}
		observed, err := deps.authorities.store.LoadContext(ctx)
		if err != nil {
			return AppConfig{}, nil, err
		}
		if observed.ConfigurationRevision != doc.ConfigurationRevision {
			continue
		}
		if pinErr != nil {
			return AppConfig{}, nil, fmt.Errorf("gatewayapp: pin Session Runtime model credentials: %w", pinErr)
		}
		return doc, lookup, nil
	}
	return AppConfig{}, nil, errors.New("gatewayapp: AppConfig changed repeatedly while pinning Session Runtime model credentials")
}

func cloneSessionRuntimeConfig(config stackRuntimeConfig) stackRuntimeConfig {
	config.Model = cloneSessionModelConfig(config.Model)
	config.SkillDirs = cloneStringSlicePreserveNil(config.SkillDirs)
	// Plugin configuration is owned by canonical AppConfig and is read in the
	// same activation transaction. It is not duplicated in process state.
	config.Plugins = nil
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

func ptrToRuntimeConfigSnapshot(doc AppConfig) *AppConfig {
	snapshot := configstore.Normalize(doc)
	// The selected Runtime binding is the only Memory authority retained by an
	// activation. Keeping the full configuration here would leave alternate
	// host-selected binding references reachable from Runtime composition.
	snapshot.Memory = memorybinding.Configuration{}
	return &snapshot
}

func resolveRuntimeMemoryBinding(
	configuration memorybinding.Configuration,
	process sessionRuntimeProcessSnapshot,
) (*memorybinding.RuntimeMemoryBindingSnapshot, error) {
	snapshot, enabled, err := memorybinding.Resolve(
		configuration,
		process.memorySelection,
		process.memoryDisabled,
	)
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: resolve Runtime Memory binding: %w", err)
	}
	if !enabled {
		return nil, nil
	}
	return &snapshot, nil
}

func selectRuntimeMemoryBinding(
	ctx context.Context,
	configuration memorybinding.Configuration,
	process sessionRuntimeProcessSnapshot,
	ref session.SessionRef,
	workspace session.WorkspaceRef,
) (*memorybinding.RuntimeMemoryBindingSnapshot, error) {
	if process.memorySelector == nil || process.memoryDisabled || !configuration.Enabled {
		return resolveRuntimeMemoryBinding(configuration, process)
	}
	selected, err := process.memorySelector(ctx, MemoryBindingSelectionContext{
		SessionRef:         ref,
		Workspace:          workspace,
		FallbackBindingRef: process.memorySelection.BindingRef,
	})
	if err != nil {
		return nil, fmt.Errorf("gatewayapp: select Runtime Memory binding: %w", err)
	}
	if selected != "" {
		process.memorySelection.BindingRef = selected
	}
	return resolveRuntimeMemoryBinding(configuration, process)
}

func cloneSessionModelConfig(config ModelConfig) ModelConfig {
	config.ReasoningLevels = append([]string(nil), config.ReasoningLevels...)
	if config.ImageInput != nil {
		imageInput := *config.ImageInput
		config.ImageInput = &imageInput
	}
	return config
}
