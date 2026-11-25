package gatewayapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	skilltool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/skill"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/experimental"
	controlplacement "github.com/caelis-labs/caelis/control/placement"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/control/toolpolicy"
	"github.com/caelis-labs/caelis/internal/acpbridge"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlplane"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// assembleProfileSnapshot binds execution authority once. Each main request
// resolves its desired profile alongside Core's capability discovery.
func (a *workspaceConfigAssembler) assembleProfileSnapshot(ctx context.Context, active session.Session, activity sessionRuntimeActivity, sessions session.ManagedService) (*sessionRuntimeInstance, error) {
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
	// Only the Session main model is independently selected. Team roles resolve
	// against the current Runtime configuration at each call.
	// Application instructions come from the profile.
	modelDocument, lookup, err := a.loadRuntimeModelSnapshot(ctx, active)
	if err != nil {
		return nil, err
	}
	placement := newPlacementSnapshot(modelDocument)
	if err := controlplacement.ValidateSnapshot(placement.placement); err != nil {
		return nil, err
	}
	process := a.deps.processConfig.snapshot().runtime
	contextWindow := process.ContextWindow
	instance := &sessionRuntimeInstance{runtimeComposition: runtimeComposition{
		authorities: a.deps.authorities, sessions: sessions, workspace: workspace, lookup: lookup,
		placementCache:    placement,
		activation:        &sessionRuntimeActivation{modelCatalog: a.deps.modelCatalog, sessionRef: active.SessionRef},
		activeRuntime:     stackRuntimeConfig{Experimental: experimental.Complete(modelDocument.Experimental), ContextWindow: contextWindow, SkillDirs: process.SkillDirs, ApprovalMode: sessionprofile.EffectiveApprovalMode(binding.Profile)},
		executionConfig:   sandbox.CloneExecutionConfig(active.ExecutionConfig),
		retainRuntimeWork: activity.retainWork, retainIdleWork: activity.retainIdleWork, idleQuiet: activity.idleQuiet, runtimeTaskChanged: activity.taskChanged, taskCommitted: activity.taskCommitted,
	}}
	children := &applicationChildren{composition: &instance.runtimeComposition, binding: binding, active: active,
		mainModelSnapshot: func(ctx context.Context) (AppConfig, *modelLookup, error) {
			return a.loadRuntimeModelSnapshot(ctx, active)
		}}
	instance.applicationChildren = children
	var execRuntime *applicationExecutionRuntime
	if binding.Profile.Execution == "workspace-write" {
		execRuntime, err = newApplicationExecutionRuntime(active.CWD, a.deps.authorities.storeDir, a.deps.authorities.sandboxHostAuthorityDir, binding.Profile, instance.executionConfig)
		if err != nil {
			return nil, err
		}
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
	compaction.Async.Enabled = instance.activeRuntime.Experimental[experimental.AsyncCompact]
	compaction.Idle.Enabled = instance.activeRuntime.Experimental[experimental.IdleCompact]
	estimateCapabilities, err := resolver.capabilitiesFor(ctx, configuration)
	if err != nil {
		return nil, err
	}
	estimateTools, guidance, err := resolver.tools(ctx, configuration.Profile.NativeTools, estimateCapabilities)
	if err != nil {
		return nil, err
	}
	compaction.EstimatedPromptPrefixTokens = estimateModelPromptPrefixTokens(map[string]any{"system_prompt": configuration.Profile.Instructions}, estimateTools) + estimatePromptTextTokens(guidance)
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
		Sessions:     sessions,
		AgentFactory: chat.Factory{},
		Compaction:   compaction,
		Hooks: hook.Set{
			ToolPolicy:     toolpolicy.Registry{Base: policies, Authority: a.deps.authorities.userID, Rules: instance.policyRules},
			StreamMode:     hook.StreamIncrementalRetry,
			ToolProjection: localResultProjection,
		},
		Diagnostics:          a.deps.authorities.diagnostics,
		DefaultPolicyMode:    mode,
		PersistPolicyConsent: instance.persistPolicyConsent,
		SandboxPolicy:        sandboxPolicy,
		TaskStore:            a.deps.authorities.taskStore, TaskOutput: a.deps.authorities.taskOutput,
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
	bundle.CapabilityRefresh = resolver.refreshMCPTools
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
	var approver kernel.ApprovalApprover
	if sessionprofile.EffectiveApprovalMode(binding.Profile) == string(kernel.ApprovalModeAutoReview) {
		bundle.Guardian = newApplicationGuardianApprover(sessions, a.deps.authorities.diagnostics)
		approver = bundle.Guardian
	}
	bundle.Gateway, err = kernel.New(kernel.Config{
		Sessions: sessions, Runtime: fencedRuntime, TurnStartGate: a.deps.authorities.approvalRecovery,
		Resolver: resolver, ExecutionValidator: controlplane.ExecutionValidator{},
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

// applicationLeasedTool guards resource materialization and publication after
// approval resumes, rather than trusting only the Turn's initial lease check.
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

type applicationTurnResolver struct {
	composition *runtimeComposition
	binding     sessionprofile.Binding
	execution   *applicationExecutionRuntime
	// Each request pins the current Host provider catalog, not the catalog
	// detached when this Session's native executor was activated.
	modelLookup        func(context.Context) (*modelLookup, error)
	capabilitiesMu     sync.Mutex
	capabilities       *applicationCapabilities
	mcpManager         *mcp.Manager
	mcpSelection       uint64
	declaredMCP        []mcp.ServerSpec
	desiredRevision    uint64
	failedMCPRevision  uint64
	capabilitiesClosed bool
}

func (r *applicationTurnResolver) tools(ctx context.Context, nativeTools []string, capabilities *applicationCapabilities) ([]tool.Tool, string, error) {
	var tools []tool.Tool
	if r.binding.Profile.Execution == "workspace-write" {
		native, err := newApplicationNativeTools(r.execution, r.composition.authorities.sessionProfiles, r.binding, r.composition.workspace.CWD, nativeTools)
		if err != nil {
			return nil, "", err
		}
		tools = append(tools, native...)
	}
	var hasCodeMode, hasTask bool
	for _, native := range tools {
		switch native.Definition().Name {
		case codemode.ToolName:
			hasCodeMode = true
		case tasktool.ToolName:
			hasTask = true
		}
	}
	if !hasCodeMode {
		tools = append(tools, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}})
	}
	if !hasTask {
		tools = append(tools, tasktool.New())
	}
	tools = append(tools,
		applicationLeasedTool{Tool: skilltool.New(skilltool.Config{ResolveCatalog: r.liveSkillCatalog}), store: r.composition.authorities.sessionProfiles, scope: r.binding.Scope},
	)
	children := r.composition.applicationChildren
	collaborationTools, err := children.tools(ctx)
	if err != nil {
		return nil, "", err
	}
	guidance := r.composition.collaborationInstructions(ctx, children.active, collaborationTools)
	tools = append(tools, collaborationTools...)
	if mcpSource, ok := capabilities.source.(applicationMCPSource); ok {
		tools = append(tools, mcpSource.PrefixTools()...)
	}
	return tools, guidance, nil
}

func (r *applicationTurnResolver) ResolveTurn(ctx context.Context, intent kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	if strings.TrimSpace(intent.ModelHint) != "" || strings.TrimSpace(intent.ModeName) != "" {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: application model changes use configuration updates; permissions are creation-bound")
	}
	source, ok := ctx.Value(applicationSourceContextKey{}).(sessionprofile.Source)
	if !ok || sessionprofile.ValidateSource(source) != nil {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: application Turn is missing trusted source admission")
	}
	snapshot, err := r.resolveModelRequest(ctx)
	if err != nil {
		return kernel.ResolvedTurn{}, err
	}
	if snapshot.Release != nil {
		defer snapshot.Release()
	}
	metadata := map[string]any{}
	hints, err := codeModeHintsForSession(ctx, r.composition.sessions, intent.SessionRef, snapshot.DeferredTools)
	if err != nil {
		return kernel.ResolvedTurn{}, err
	}
	metadata[codemode.MetadataSelectedTools] = hints
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
			ResolveSessionInstructions: sessionSkillInstructions(r.composition.sessions, intent.SessionRef, r.liveSkillCatalog),
			ResolveModelRequest: func(ctx context.Context) (agent.ModelRequestSnapshot, error) {
				return r.resolveModelRequest(ctx)
			}},
	}}, nil
}

func (r *applicationTurnResolver) resolveModelRequest(ctx context.Context) (agent.ModelRequestSnapshot, error) {
	store := r.composition.authorities.sessionProfiles
	configuration, capabilities, err := r.currentCapabilities(ctx)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	resolved, err := r.resolveProfileModel(ctx, configuration.Profile)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	tools, guidance, err := r.tools(ctx, configuration.Profile.NativeTools, capabilities)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	var instructions []model.Part
	if configuration.Profile.Instructions != "" {
		instructions = append(instructions, model.NewTextPart(configuration.Profile.Instructions))
	}
	if guidance != "" {
		instructions = append(instructions, model.NewTextPart(guidance))
	}
	return agent.ModelRequestSnapshot{
		Revision: strconv.FormatUint(configuration.Revision, 10), Model: resolved.Model, Tools: tools, DeferredTools: capabilities.source,
		Instructions: instructions, Reasoning: model.ReasoningConfig{Effort: resolved.ReasoningEffort},
		Request: agent.ModelRequestOptions{ServiceTier: model.ServiceTier(configuration.Profile.ServiceTier)},
		Validate: func(ctx context.Context) (func(), error) {
			unlock, err := store.GuardConfiguration(ctx, r.binding.Scope, r.binding.SessionID, configuration.Revision)
			if errors.Is(err, sessionprofile.ErrConfigurationStale) {
				return nil, agent.ErrModelRequestSnapshotStale
			}
			return unlock, err
		},
		Admit: func(ctx context.Context, request agent.ModelRequestAdmission) error {
			err := store.AdmitRequest(ctx, r.binding.Scope, r.binding.SessionID, sessionprofile.RequestConfiguration{
				Revision: configuration.Revision, RequestID: request.RequestID, TurnID: request.TurnID, JobID: request.JobID,
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
		if mode == policy.ProfileDangerFullAccess {
			native = presets.DangerFullAccessMode()
		}
	}
	registry, err := policy.NewMemory(policy.NamedMode{ID: mode, Decide: func(ctx context.Context, input policy.ToolContext) (policy.Decision, error) {
		if input.Tool.Name == codemode.ToolName && codemode.IsDefinition(input.Tool) {
			var source struct {
				File string `json:"file"`
			}
			_ = json.Unmarshal(input.Call.Input, &source)
			if source.File == "" {
				return policy.Decision{Action: policy.ActionAllow}, nil
			}
			if native == nil {
				return policy.Decision{Action: policy.ActionDeny, Reason: "file source requires the original filesystem read authority"}, nil
			}
			return native.DecideTool(ctx, input)
		}
		if input.Tool.Name == tasktool.ToolName && native == nil {
			var control struct {
				Action string `json:"action"`
			}
			_ = json.Unmarshal(input.Call.Input, &control)
			if control.Action == "write" {
				return policy.Decision{Action: policy.ActionDeny, Reason: "tools-only execution does not grant native command input"}, nil
			}
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		if mcp.IsDefinition(input.Tool) || mcp.IsSearchDefinition(input.Tool) || input.Tool.Name == "Skill" {
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		switch input.Tool.Name {
		case "StartThread", "ReadMessages", "ListThreads", "SendMessage", "ReadThread", "WaitThread":
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		if native != nil {
			return native.DecideTool(ctx, input)
		}
		return policy.Decision{Action: policy.ActionDeny, Reason: "tool is unavailable in tools-only execution"}, nil
	}})
	return registry, mode, err
}
