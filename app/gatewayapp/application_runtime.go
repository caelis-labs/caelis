package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/controlplane"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// assembleApplicationSnapshot binds execution authority once; each model request
// resolves its desired profile separately. Choosing a directory never imports
// workspace instructions, skills, Memory, MCP, plugins or the process prompt.
func (a *workspaceConfigAssembler) assembleApplicationSnapshot(ctx context.Context, active session.Session, activity sessionRuntimeActivity, sessions session.Service) (*sessionRuntimeInstance, error) {
	store := a.deps.authorities.applications
	if store == nil || !sessionvisibility.IsApplicationSession(active) {
		return nil, fmt.Errorf("gatewayapp: application binding store unavailable")
	}
	binding, err := store.BindingForSession(ctx, active.SessionID)
	if err != nil {
		return nil, err
	}
	if binding.Archived || binding.PrincipalID != active.UserID || binding.SessionID != active.SessionID || active.Metadata[application.StateKey] != binding.ApplicationID {
		return nil, fmt.Errorf("gatewayapp: application Session binding does not match its canonical owner")
	}
	if err := validateApplicationExecutionPlatform(binding.Profile.Execution); err != nil {
		return nil, err
	}
	state, err := sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		return nil, err
	}
	if _, bound := state[memorybinding.SessionStateKey]; bound {
		return nil, fmt.Errorf("gatewayapp: application Session cannot inherit Workspace Memory")
	}
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return nil, err
	}
	// Only the selected provider model and its credential are loaded from Host
	// configuration. No workspace configuration may alter the application prefix.
	_, lookup, err := a.loadRuntimeModelSnapshot(ctx, active)
	if err != nil {
		return nil, err
	}
	contextWindow := a.deps.processConfig.snapshot().runtime.ContextWindow
	instance := &sessionRuntimeInstance{runtimeComposition: runtimeComposition{
		authorities: a.deps.authorities, sessions: sessions, workspace: workspace, lookup: lookup,
		activation:        &sessionRuntimeActivation{modelCatalog: a.deps.modelCatalog, sessionRef: active.SessionRef},
		activeRuntime:     stackRuntimeConfig{ContextWindow: contextWindow},
		retainRuntimeWork: activity.retainWork, runtimeTaskChanged: activity.taskChanged, taskCommitted: activity.taskCommitted,
	}}
	var execRuntime *applicationExecutionRuntime
	var sandboxDescriptor sandbox.DescriptorProvider = applicationNoNativeExecution{}
	if binding.Profile.Execution == "workspace-write" {
		execRuntime, err = newApplicationExecutionRuntime(workspace.CWD, a.deps.authorities.storeDir, store, binding.Scope, binding.Profile)
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
	configuration, err := store.Configuration(ctx, binding.Scope, binding.SessionID)
	if err != nil {
		return nil, err
	}
	if err := application.ValidateProfile(configuration.Profile); err != nil {
		return nil, err
	}
	if _, err := resolver.resolveProfileModel(ctx, configuration.Profile); err != nil {
		return nil, err
	}
	policies, mode, err := applicationPolicyRegistry(binding.Profile)
	if err != nil {
		return nil, err
	}
	compaction := defaultCompactionConfig(contextWindow)
	// The complete tool set is independent of Turn source. An unavailable
	// callback provenance is rejected during resolution, never silently widened.
	estimateTools, err := resolver.tools(ctx, configuration, application.Source{Kind: "user", OperationID: "estimate"})
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
	rt, err := runtime.New(runtime.Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Compaction: compaction,
		Diagnostics: a.deps.authorities.diagnostics, PolicyRegistry: policies, DefaultPolicyMode: mode,
		SandboxPolicy: sandboxPolicy,
		TaskStore:     a.deps.authorities.taskStore, TaskOutput: a.deps.authorities.taskOutput,
		TaskActivityChanged: activity.taskChanged, TaskCommitted: activity.taskCommitted,
		ChildApprovalRequester: backgroundChildApprovalRequester{composition: &instance.runtimeComposition},
	})
	if err != nil {
		return nil, err
	}
	bundle := &gatewayRuntimeBundle{Engine: rt, RuntimeConfig: instance.activeRuntime, EstimatedPromptPrefixTokens: compaction.EstimatedPromptPrefixTokens}
	if execRuntime != nil {
		bundle.Exec = execRuntime
	}
	bundleCreated = true
	defer func() {
		if bundle != nil {
			bundle.Close()
		}
	}()
	fences, ok := sessions.(session.SessionFenceService)
	if !ok {
		return nil, fmt.Errorf("gatewayapp: application Session service requires execution fences")
	}
	fenced, err := controlplane.NewFencedRuntime(controlplane.FencedRuntimeConfig{
		Runtime: rt, Fences: fences, OwnerID: strings.TrimSpace(a.deps.authorities.fenceOwnerID),
		LifecycleContext: a.deps.authorities.lifecycleCtx, Diagnostics: a.deps.authorities.diagnostics,
	})
	if err != nil {
		return nil, err
	}
	bundle.Placement = fenced
	validator, err := controlplane.NewExecutionValidator(controlplane.ExecutionValidatorConfig{Sandbox: sandboxDescriptor})
	if err != nil {
		return nil, err
	}
	bundle.Gateway, err = kernel.New(kernel.Config{
		Sessions: sessions, Runtime: fenced, TurnStartGate: a.deps.authorities.approvalRecovery,
		Resolver: resolver, ExecutionValidator: validator, DefaultApprovalMode: kernel.ApprovalModeManual,
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
	store *application.Store
	scope application.Scope
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
	binding     application.Binding
	execution   *applicationExecutionRuntime
	// Each request pins the current Host provider catalog, not the catalog
	// detached when this Session's native executor was activated.
	modelLookup func(context.Context) (*modelLookup, error)
}

func (r *applicationTurnResolver) tools(ctx context.Context, configuration application.Configuration, source application.Source) ([]tool.Tool, error) {
	tools, err := r.composition.authorities.applications.ToolsForConfiguration(ctx, r.binding, configuration, source)
	if err != nil {
		return nil, err
	}
	if r.binding.Profile.Execution == "workspace-write" {
		native, err := newApplicationNativeTools(r.execution, r.composition.authorities.applications, r.binding, r.composition.workspace.CWD, configuration.Profile.NativeTools)
		if err != nil {
			return nil, err
		}
		tools = append(tools, native...)
	}
	return tools, nil
}

func (r *applicationTurnResolver) ResolveTurn(ctx context.Context, intent kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	if strings.TrimSpace(intent.ModelHint) != "" || strings.TrimSpace(intent.ModeName) != "" {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: application model changes use configuration updates; permissions are creation-bound")
	}
	source, ok := ctx.Value(applicationSourceContextKey{}).(application.Source)
	if !ok || application.ValidateSource(source) != nil {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: application Turn is missing trusted source admission")
	}
	snapshot, err := r.resolveModelRequest(ctx, source)
	if err != nil {
		return kernel.ResolvedTurn{}, err
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

func (r *applicationTurnResolver) resolveModelRequest(ctx context.Context, source application.Source) (agent.ModelRequestSnapshot, error) {
	store := r.composition.authorities.applications
	configuration, err := store.Configuration(ctx, r.binding.Scope, r.binding.SessionID)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	resolved, err := r.resolveProfileModel(ctx, configuration.Profile)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	tools, err := r.tools(ctx, configuration, source)
	if err != nil {
		return agent.ModelRequestSnapshot{}, err
	}
	var instructions []model.Part
	if configuration.Profile.Instructions != "" {
		instructions = []model.Part{model.NewTextPart(configuration.Profile.Instructions)}
	}
	return agent.ModelRequestSnapshot{
		Revision: strconv.FormatUint(configuration.Revision, 10), Model: resolved.Model, Tools: tools,
		Instructions: instructions, Reasoning: model.ReasoningConfig{Effort: resolved.ReasoningEffort},
		Request: agent.ModelRequestOptions{ServiceTier: model.ServiceTier(configuration.Profile.ServiceTier)},
		Admit: func(ctx context.Context, request agent.ModelRequestAdmission) error {
			err := store.AdmitRequest(ctx, r.binding.Scope, r.binding.SessionID, application.RequestConfiguration{
				Revision: configuration.Revision, RequestID: request.RequestID, TurnID: request.TurnID,
				Model: resolved.Model.Name(), ReasoningEffort: resolved.ReasoningEffort,
				ServiceTier: configuration.Profile.ServiceTier, ToolsVersion: configuration.Profile.ToolsVersion,
			})
			if errors.Is(err, application.ErrConfigurationStale) {
				return agent.ErrModelRequestSnapshotStale
			}
			return err
		},
	}, nil
}

func applicationPolicyRegistry(profile application.Profile) (policy.Registry, string, error) {
	if profile.Execution == "workspace-write" {
		if profile.Permissions.Mode == presets.ModeDangerFullAccess {
			registry, err := policy.NewMemory(presets.DangerFullAccessMode())
			return registry, presets.ModeDangerFullAccess, err
		}
		registry, err := presets.NewRegistry()
		return registry, presets.ModeWorkspaceWrite, err
	}
	const mode = "application-tools-only"
	registry, err := policy.NewMemory(policy.NamedMode{ID: mode, Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		// The selected, stored catalog is the admission owner. Each request
		// receives only its immutable callbacks, including after hot updates.
		if external, _ := input.Tool.Metadata[tool.MetadataExternalCapability].(bool); external {
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		return policy.Decision{Action: policy.ActionDeny, Reason: "tools-only application execution admits only stored callbacks"}, nil
	}})
	return registry, mode, err
}
