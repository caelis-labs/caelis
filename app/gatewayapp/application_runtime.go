package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// assembleApplicationSnapshot binds execution authority once; each model request
// resolves its desired profile separately. Choosing a directory never imports
// workspace instructions, skills, MCP, plugins or the process prompt.
func (a *workspaceConfigAssembler) assembleProfileSnapshot(ctx context.Context, active session.Session, activity sessionRuntimeActivity, sessions session.Service) (*sessionRuntimeInstance, error) {
	store := a.deps.authorities.sessionProfiles
	if store == nil || !sessionvisibility.IsApplicationSession(active) {
		return nil, fmt.Errorf("gatewayapp: application binding store unavailable")
	}
	binding, err := store.BindingForSession(ctx, active.SessionID)
	if err != nil {
		return nil, err
	}
	if binding.Archived || binding.PrincipalID != active.UserID || binding.SessionID != active.SessionID {
		return nil, fmt.Errorf("gatewayapp: application Session binding does not match its canonical owner")
	}
	if err := validateApplicationExecutionPlatform(binding.Profile.Execution); err != nil {
		return nil, err
	}
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return nil, err
	}
	// Model selection is explicit for both the application and its reviewer.
	// No workspace configuration may alter the application prefix.
	modelDocument, lookup, err := a.loadRuntimeModelSnapshot(ctx, active)
	if err != nil {
		return nil, err
	}
	placement := newPlacementSnapshot(modelDocument)
	if err := controlplacement.ValidateSnapshot(placement.placement); err != nil {
		return nil, err
	}
	contextWindow := a.deps.processConfig.snapshot().runtime.ContextWindow
	instance := &sessionRuntimeInstance{runtimeComposition: runtimeComposition{
		authorities: a.deps.authorities, sessions: sessions, workspace: workspace, lookup: lookup,
		placementCache:    placement,
		activation:        &sessionRuntimeActivation{modelCatalog: a.deps.modelCatalog, sessionRef: active.SessionRef},
		activeRuntime:     stackRuntimeConfig{ContextWindow: contextWindow, ApprovalMode: sessionprofile.EffectiveApprovalMode(binding.Profile)},
		executionConfig:   sandbox.CloneExecutionConfig(active.ExecutionConfig),
		retainRuntimeWork: activity.retainWork, runtimeTaskChanged: activity.taskChanged, taskCommitted: activity.taskCommitted,
	}}
	children := &applicationChildren{composition: &instance.runtimeComposition, binding: binding, active: active,
		modelSnapshot: func(ctx context.Context) (AppConfig, *modelLookup, error) {
			return a.loadRuntimeModelSnapshot(ctx, active)
		}}
	instance.applicationChildren = children
	var execRuntime *applicationExecutionRuntime
	var sandboxDescriptor sandbox.DescriptorProvider = applicationNoNativeExecution{}
	if binding.Profile.Execution == "workspace-write" {
		execRuntime, err = newApplicationExecutionRuntime(active.CWD, a.deps.authorities.storeDir, a.deps.authorities.sandboxHostAuthorityDir, binding.Profile, instance.executionConfig)
		if err != nil {
			return nil, err
		}
		sandboxDescriptor = execRuntime
	}
	bundleCreated := false
	defer func() {
		if !bundleCreated && execRuntime != nil {
			_ = execRuntime.Close()
		}
	}()
	resolver := &applicationTurnResolver{
		composition: &instance.runtimeComposition, binding: binding, execution: execRuntime,
		modelLookup: func(ctx context.Context) (*modelLookup, error) {
			_, lookup, err := a.loadRuntimeModelSnapshot(ctx, active)
			return lookup, err
		},
	}
	defer func() {
		if !bundleCreated {
			resolver.closeCapabilities()
		}
	}()
	configuration, err := store.Configuration(ctx, binding.Scope, binding.SessionID)
	if err != nil {
		return nil, err
	}
	if err := sessionprofile.ValidateProfile(configuration.Profile); err != nil {
		return nil, err
	}
	if _, err := resolver.resolveProfileModel(ctx, configuration.Profile); err != nil {
		return nil, err
	}
	policies, mode, err := applicationPolicyRegistry(binding.Profile)
	if err != nil {
		return nil, err
	}
	instance.activeRuntime.PolicyProfile = mode
	compaction := defaultCompactionConfig(contextWindow)
	// The complete tool set is independent of Turn source. An unavailable
	// callback provenance is rejected during resolution, never silently widened.
	estimateCapabilities, releaseEstimate, err := resolver.acquireCapabilities(ctx, configuration)
	if err != nil {
		return nil, err
	}
	estimateTools, err := resolver.tools(ctx, configuration, sessionprofile.Source{Kind: "user", OperationID: "estimate"}, estimateCapabilities)
	releaseEstimate()
	if err != nil {
		return nil, err
	}
	compaction.EstimatedPromptPrefixTokens = estimateModelPromptPrefixTokens(map[string]any{"system_prompt": configuration.Profile.Instructions}, estimateTools)
	var sandboxPolicy sandbox.PolicySnapshot
	if execRuntime != nil {
		writable, _, err := applicationWorkspaceAccess(binding.Profile.Workspace.Access, workspace.CWD)
		if err != nil {
			return nil, err
		}
		sandboxPolicy = presets.EffectiveSandboxPolicy(mode, sandbox.Config{CWD: workspace.CWD, WritableRoots: writable}, execRuntime.Describe(), policy.ModeOptions{WorkspaceRoot: workspace.CWD, TempRoot: os.TempDir(), WritableRoots: writable})
	}
	contextRouter, err := controlplane.NewContextRouter(sessions)
	if err != nil {
		return nil, err
	}
	agents, err := children.agents(ctx)
	if err != nil {
		return nil, err
	}
	instance.activeRuntime.Assembly.Agents = agents
	localConfig := runtime.Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Compaction: compaction,
		Diagnostics: a.deps.authorities.diagnostics, PolicyRegistry: policies, DefaultPolicyMode: mode,
		MCPGrants:     a.deps.authorities.mcpGrants,
		SandboxPolicy: sandboxPolicy,
		TaskStore:     a.deps.authorities.taskStore, TaskOutput: a.deps.authorities.taskOutput,
		TaskActivityChanged: activity.taskChanged, TaskCommitted: activity.taskCommitted,
		ChildApprovalRequester:  backgroundChildApprovalRequester{composition: &instance.runtimeComposition},
		ControllerContextRouter: contextRouter, ControllerEventForwarder: acpbridge.NewControllerForwarder(sessions),
	}
	localConfig, childPlane, err := injectACPControlPlane(localConfig, assembly.ResolvedAssembly{Agents: agents}, children.resolve, children.prepare, children, activity.retainWork)
	if err != nil {
		return nil, err
	}
	bundle := &gatewayRuntimeBundle{ACPControlPlane: childPlane, RuntimeConfig: instance.activeRuntime, EstimatedPromptPrefixTokens: compaction.EstimatedPromptPrefixTokens}
	bundle.CloseCapabilities = resolver.closeCapabilities
	bundle.CapabilityStatus = resolver.capabilityStatus
	bundle.CapabilityUpdated = resolver.configurationCommitted
	if execRuntime != nil {
		bundle.Exec = execRuntime
	}
	bundleCreated = true
	defer func() {
		if bundle != nil {
			bundle.Close()
		}
	}()
	fencedRuntime, sessionControl, err := instance.buildSessionExecution(bundle, localConfig, contextRouter)
	if err != nil {
		return nil, err
	}
	validator, err := controlplane.NewExecutionValidator(controlplane.ExecutionValidatorConfig{Sandbox: sandboxDescriptor})
	if err != nil {
		return nil, err
	}
	var approver kernel.ApprovalApprover
	if binding.Profile.Reviewer != nil {
		bundle.Guardian = newApplicationGuardianApprover(sessions, a.deps.authorities.diagnostics)
		approver = bundle.Guardian
	}
	bundle.Gateway, err = kernel.New(kernel.Config{
		Sessions: sessions, Runtime: fencedRuntime, TurnStartGate: a.deps.authorities.approvalRecovery,
		Resolver: resolver, ExecutionValidator: validator,
		DefaultApprovalMode: kernel.ApprovalMode(sessionprofile.EffectiveApprovalMode(binding.Profile)),
		ApprovalApprover:    approver,
		Control:             sessionControl,
	})
	if err != nil {
		return nil, err
	}
	if err := instance.installGatewayRuntimeBundle(nil, bundle); err != nil {
		return nil, err
	}
	bundle = nil
	return instance, nil
}

// applicationNoNativeExecution advertises no filesystem/process capabilities.
// Tools-only profiles have no native executor to probe, borrow, or accidentally
// expose: a misassembled native tool fails preflight instead of running on Host.
// applicationLeasedTool guards resource materialization and publication after
// approval resumes, rather than trusting only the Turn's initial lease check.
// Callback tools enforce this natively in the application Store.
type applicationLeasedTool struct {
	tool.Tool
	store *sessionprofile.Store
	scope sessionprofile.Scope
}

func (t applicationLeasedTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := t.store.CheckActive(ctx, t.scope); err != nil {
		return tool.Result{}, err
	}
	return t.Tool.Call(ctx, call)
}

type applicationNoNativeExecution struct{}

func (applicationNoNativeExecution) Describe() sandbox.Descriptor { return sandbox.Descriptor{} }

type applicationTurnResolver struct {
	composition *runtimeComposition
	binding     sessionprofile.Binding
	execution   *applicationExecutionRuntime
	// Each request pins the current Host provider catalog, not the catalog
	// detached when this Session's native executor was activated.
	modelLookup        func(context.Context) (*modelLookup, error)
	capabilitiesMu     sync.Mutex
	capabilities       map[uint64]*applicationCapabilities
	currentMCP         *applicationMCPResource
	desiredRevision    uint64
	capabilitiesClosed bool
}

func (r *applicationTurnResolver) tools(ctx context.Context, configuration sessionprofile.Configuration, source sessionprofile.Source, capabilities *applicationCapabilities) ([]tool.Tool, error) {
	tools, err := r.composition.authorities.sessionProfiles.ToolsForConfiguration(ctx, r.binding, configuration, source)
	if err != nil {
		return nil, err
	}
	if r.binding.Profile.Execution == "workspace-write" {
		native, err := newApplicationNativeTools(r.execution, r.composition.authorities.sessionProfiles, r.binding, r.composition.workspace.CWD, configuration.Profile.NativeTools)
		if err != nil {
			return nil, err
		}
		tools = append(tools, native...)
	}
	if len(capabilities.catalog.Metas()) > 0 {
		tools = append(tools, applicationLeasedTool{Tool: capabilities.skillTool, store: r.composition.authorities.sessionProfiles, scope: r.binding.Scope})
	}
	if capabilities.source != nil {
		tools = append(tools, applicationLeasedTool{Tool: capabilities.searchTool, store: r.composition.authorities.sessionProfiles, scope: r.binding.Scope})
	}
	children, err := r.composition.applicationChildren.tools(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(tools))
	for _, t := range tools {
		seen[t.Definition().Name] = true
	}
	for _, t := range children {
		if !seen[t.Definition().Name] {
			tools = append(tools, t)
		}
	}
	return tools, nil
}

func (r *applicationTurnResolver) ResolveTurn(ctx context.Context, intent kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	if strings.TrimSpace(intent.ModelHint) != "" || strings.TrimSpace(intent.ModeName) != "" {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: application model changes use configuration updates; permissions are creation-bound")
	}
	source, ok := ctx.Value(applicationSourceContextKey{}).(sessionprofile.Source)
	if !ok || sessionprofile.ValidateSource(source) != nil {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: application Turn is missing trusted source admission")
	}
	snapshot, err := r.resolveModelRequest(ctx, source)
	if err != nil {
		return kernel.ResolvedTurn{}, err
	}
	if snapshot.Release != nil {
		defer snapshot.Release()
	}
	metadata := map[string]any{}
	if r.execution != nil {
		writable, access, err := applicationWorkspaceAccess(r.binding.Profile.Workspace.Access, r.composition.workspace.CWD)
		if err != nil {
			return kernel.ResolvedTurn{}, err
		}
		metadata[policy.MetadataWritableRoots] = writable
		metadata[policy.MetadataExtraReadRoots] = access
	}
	return kernel.ResolvedTurn{RunRequest: agent.RunRequest{
		SessionRef: intent.SessionRef, InputKind: intent.InputKind, Input: intent.Input,
		DisplayInput: strings.TrimSpace(intent.DisplayInput), ContentParts: append([]model.ContentPart(nil), intent.ContentParts...),
		Inputs: agent.CloneAgentCommunicationInputs(intent.Inputs), InputActor: session.CloneActorRef(intent.InputActor),
		AgentSpec: agent.AgentSpec{Name: "application", Model: snapshot.Model, Tools: snapshot.Tools, Metadata: metadata,
			ResolveModelRequest: func(ctx context.Context) (agent.ModelRequestSnapshot, error) {
				return r.resolveModelRequest(ctx, source)
			}},
	}}, nil
}

func (r *applicationTurnResolver) resolveModelRequest(ctx context.Context, source sessionprofile.Source) (agent.ModelRequestSnapshot, error) {
	store := r.composition.authorities.sessionProfiles
	configuration, err := store.Configuration(ctx, r.binding.Scope, r.binding.SessionID)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	resolved, err := r.resolveProfileModel(ctx, configuration.Profile)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	capabilities, release, err := r.acquireCapabilities(ctx, configuration)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	keep := false
	defer func() {
		if !keep {
			release()
		}
	}()
	tools, err := r.tools(ctx, configuration, source, capabilities)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	var instructions []model.Part
	if configuration.Profile.Instructions != "" {
		instructions = []model.Part{model.NewTextPart(configuration.Profile.Instructions)}
	}
	if metadata := applicationSkillMetadata(capabilities.catalog); metadata != "" {
		instructions = append(instructions, model.NewTextPart(metadata))
	}
	keep = true
	return agent.ModelRequestSnapshot{
		Revision: strconv.FormatUint(configuration.Revision, 10), Model: resolved.Model, Tools: tools, DeferredTools: capabilities.source,
		Instructions: instructions, Reasoning: model.ReasoningConfig{Effort: resolved.ReasoningEffort},
		Request: agent.ModelRequestOptions{ServiceTier: model.ServiceTier(configuration.Profile.ServiceTier)},
		Release: release,
		Admit: func(ctx context.Context, request agent.ModelRequestAdmission) error {
			err := store.AdmitRequest(ctx, r.binding.Scope, r.binding.SessionID, sessionprofile.RequestConfiguration{
				Revision: configuration.Revision, RequestID: request.RequestID, TurnID: request.TurnID,
				Model: resolved.Model.Name(), ReasoningEffort: resolved.ReasoningEffort,
				ServiceTier: configuration.Profile.ServiceTier, ToolsVersion: configuration.Profile.ToolsVersion,
			})
			if errors.Is(err, sessionprofile.ErrConfigurationStale) {
				return agent.ErrModelRequestSnapshotStale
			}
			return err
		},
	}, nil
}

func applicationPolicyRegistry(profile sessionprofile.Profile) (policy.Registry, string, error) {
	mode := applicationPolicyMode(profile)
	var native policy.Mode
	if mode != "application-tools-only" {
		native = presets.WorkspaceWriteMode()
		if mode == presets.ModeDangerFullAccess {
			native = presets.DangerFullAccessMode()
		}
	}
	registry, err := policy.NewMemory(policy.NamedMode{ID: mode, Decide: func(ctx context.Context, input policy.ToolContext) (policy.Decision, error) {
		// Only the stored, request-pinned callback catalog supplies callback
		// policy; names and model arguments cannot opt into this path.
		if decision, handled, err := sessionprofile.CallbackPolicyDecision(input); handled || err != nil {
			return decision, err
		}
		if tool.IsMCPDefinition(input.Tool) || tool.IsToolSearchDefinition(input.Tool) || input.Tool.Name == "Skill" {
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		switch input.Tool.Name {
		case "StartThread", "ReadMessages", "ListThreads", "SendMessage", "ReadThread", "WaitThread":
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		if native != nil {
			return native.DecideTool(ctx, input)
		}
		return policy.Decision{Action: policy.ActionDeny, Reason: "tools-only application execution admits only stored callbacks"}, nil
	}})
	return registry, mode, err
}
