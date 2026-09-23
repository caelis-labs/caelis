package gatewayapp

import (
	"context"
	"fmt"
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
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/controlplane"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// assembleApplicationSnapshot admits only the immutable Store binding. Workspace
// configuration, skills, Memory, MCP, plugin tools and process prompt never enter
// this Runtime; only its explicit execution profile and callbacks do.
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
	if err := application.ValidateProfile(binding.Profile); err != nil {
		return nil, err
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
	var execRuntime *isolatedExecutionRuntime
	var sandboxDescriptor sandbox.DescriptorProvider = applicationNoNativeExecution{}
	if binding.Profile.Execution == "workspace-write" {
		execRuntime, err = newIsolatedExecutionRuntime(workspace.CWD, a.deps.authorities.storeDir, store, binding.Scope)
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
	resolver := &applicationTurnResolver{composition: &instance.runtimeComposition, binding: binding, execution: execRuntime}
	if _, err := resolver.resolveModel(ctx); err != nil {
		return nil, err
	}
	policies, mode, err := applicationPolicyRegistry(binding.Profile)
	if err != nil {
		return nil, err
	}
	compaction := defaultCompactionConfig(contextWindow)
	// The complete tool set is independent of Turn source. An unavailable
	// callback provenance is rejected during resolution, never silently widened.
	estimateTools, err := resolver.tools(ctx, application.Source{Kind: "user", OperationID: "estimate"})
	if err != nil {
		return nil, err
	}
	compaction.EstimatedPromptPrefixTokens = estimateModelPromptPrefixTokens(map[string]any{"system_prompt": binding.Profile.Instructions}, estimateTools)
	rt, err := runtime.New(runtime.Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Compaction: compaction,
		Diagnostics: a.deps.authorities.diagnostics, PolicyRegistry: policies, DefaultPolicyMode: mode,
		TaskStore: a.deps.authorities.taskStore, TaskOutput: a.deps.authorities.taskOutput,
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
	execution   *isolatedExecutionRuntime
}

func (r *applicationTurnResolver) tools(ctx context.Context, source application.Source) ([]tool.Tool, error) {
	tools, err := r.composition.authorities.applications.Tools(ctx, r.binding, source)
	if err != nil {
		return nil, err
	}
	if r.binding.Profile.Execution == "workspace-write" {
		for _, resourceTool := range newApplicationResourceTools(r.composition.authorities.applications, r.binding, r.composition.workspace.CWD) {
			tools = append(tools, applicationLeasedTool{Tool: resourceTool, store: r.composition.authorities.applications, scope: r.binding.Scope})
		}
		command, err := shell.NewRunCommand(shell.RunCommandConfig{Runtime: r.execution})
		if err != nil {
			return nil, err
		}
		tools = append(tools, command, task.New())
	}
	return tools, nil
}

func (r *applicationTurnResolver) ResolveTurn(ctx context.Context, intent kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	if strings.TrimSpace(intent.ModelHint) != "" || strings.TrimSpace(intent.ModeName) != "" {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: application model and mode require a new immutable profile")
	}
	source, ok := ctx.Value(applicationSourceContextKey{}).(application.Source)
	if !ok || source.OperationID == "" || (source.Kind != "user" && source.Kind != "application_summary" && source.Kind != "external_material") {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: application Turn is missing trusted source admission")
	}
	resolved, err := r.resolveModel(ctx)
	if err != nil {
		return kernel.ResolvedTurn{}, err
	}
	tools, err := r.tools(ctx, source)
	if err != nil {
		return kernel.ResolvedTurn{}, err
	}
	return kernel.ResolvedTurn{RunRequest: agent.RunRequest{
		SessionRef: intent.SessionRef, InputKind: intent.InputKind, Input: intent.Input,
		DisplayInput: strings.TrimSpace(intent.DisplayInput), ContentParts: append([]model.ContentPart(nil), intent.ContentParts...),
		Inputs: agent.CloneAgentCommunicationInputs(intent.Inputs), InputActor: session.CloneActorRef(intent.InputActor),
		AgentSpec: agent.AgentSpec{Name: "application", Model: resolved.Model, Tools: tools,
			Metadata: map[string]any{"system_prompt": r.binding.Profile.Instructions, "reasoning_effort": resolved.ReasoningEffort}},
	}}, nil
}

func (r *applicationTurnResolver) resolveModel(ctx context.Context) (kernel.ModelResolution, error) {
	configured, err := r.composition.lookup.ResolveConfig(r.binding.Profile.Model)
	if err != nil {
		return kernel.ModelResolution{}, err
	}
	return r.composition.lookup.ResolveModelConfig(ctx, configured, r.composition.activeRuntime.ContextWindow)
}

func applicationPolicyRegistry(profile application.Profile) (policy.Registry, string, error) {
	if profile.Execution == "workspace-write" {
		registry, err := presets.NewRegistry()
		return registry, presets.ModeWorkspaceWrite, err
	}
	allowed := make(map[string]bool, len(profile.Tools))
	for _, def := range profile.Tools {
		allowed[def.Name] = true
	}
	const mode = "application-tools-only"
	registry, err := policy.NewMemory(policy.NamedMode{ID: mode, Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
		if allowed[input.Tool.Name] {
			return policy.Decision{Action: policy.ActionAllow}, nil
		}
		return policy.Decision{Action: policy.ActionDeny, Reason: "tool is not in the immutable application profile"}, nil
	}})
	return registry, mode, err
}
