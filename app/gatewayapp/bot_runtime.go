package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/control/memorybinding"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlplane"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// assembleBotSnapshot shares canonical Session and Turn ownership with work
// Sessions, but admits no workspace, Memory, plugin, or collaboration capability,
// and gives every Bot the same private notebook. The embedded Host Memory service
// remains available to its other consumers.
func (a *workspaceConfigAssembler) assembleBotSnapshot(
	ctx context.Context,
	active session.Session,
	activity sessionRuntimeActivity,
	sessions session.Service,
) (*sessionRuntimeInstance, error) {
	if active.Controller.Kind != "" && active.Controller.Kind != session.ControllerKindKernel {
		return nil, fmt.Errorf("gatewayapp: Bot conversation requires the built-in controller")
	}
	state, err := sessions.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		return nil, err
	}
	config, err := botRuntimeConfig(state)
	if err != nil {
		return nil, err
	}
	workspace, err := canonicalSessionWorkspace(active)
	if err != nil {
		return nil, err
	}
	doc, lookup, err := a.loadRuntimeModelSnapshot(ctx, active)
	if err != nil {
		return nil, err
	}
	contextWindow := a.deps.processConfig.snapshot().runtime.ContextWindow
	instance := &sessionRuntimeInstance{runtimeComposition: runtimeComposition{
		authorities: a.deps.authorities,
		sessions:    sessions,
		workspace:   workspace,
		lookup:      lookup,
		activation: &sessionRuntimeActivation{
			modelCatalog: a.deps.modelCatalog,
			sessionRef:   active.SessionRef,
		},
		placementCache:     newPlacementSnapshot(doc),
		activeRuntime:      stackRuntimeConfig{ContextWindow: contextWindow},
		retainRuntimeWork:  activity.retainWork,
		runtimeTaskChanged: activity.taskChanged,
		taskCommitted:      activity.taskCommitted,
	}}
	id, _, err := bot.ReadState(state)
	if err != nil {
		return nil, err
	}
	if id != active.Metadata[bot.MetadataID] {
		return nil, fmt.Errorf("gatewayapp: Bot identity does not match its conversation")
	}
	notebook, err := bot.NewNotebook(a.deps.authorities.storeDir, id)
	if err != nil {
		return nil, err
	}
	tools, err := notebook.Tools()
	if err != nil {
		return nil, err
	}
	resolver := &botTurnResolver{composition: &instance.runtimeComposition, notebookTools: tools}
	if _, err := resolver.resolveModel(ctx, config); err != nil {
		return nil, err
	}
	compaction := defaultCompactionConfig(contextWindow)
	compaction.EstimatedPromptPrefixTokens = estimateModelPromptPrefixTokens(map[string]any{"system_prompt": buildBotSystemPrompt(a.deps.authorities.appName)}, tools)
	policies, err := notebookPolicyRegistry()
	if err != nil {
		return nil, err
	}
	rt, err := runtime.New(runtime.Config{
		Sessions:          sessions,
		AgentFactory:      chat.Factory{},
		Compaction:        compaction,
		Diagnostics:       a.deps.authorities.diagnostics,
		PolicyRegistry:    policies,
		DefaultPolicyMode: botNotebookPolicy,
	})
	if err != nil {
		return nil, err
	}
	bundle := &gatewayRuntimeBundle{Engine: rt, Exec: notebook, RuntimeConfig: instance.activeRuntime, EstimatedPromptPrefixTokens: compaction.EstimatedPromptPrefixTokens}
	defer func() {
		if bundle != nil {
			bundle.Close()
		}
	}()
	fences, ok := sessions.(session.SessionFenceService)
	if !ok {
		return nil, fmt.Errorf("gatewayapp: production Bot Session service does not support execution fences")
	}
	fenced, err := controlplane.NewFencedRuntime(controlplane.FencedRuntimeConfig{
		Runtime:          rt,
		Fences:           fences,
		OwnerID:          strings.TrimSpace(a.deps.authorities.fenceOwnerID),
		LifecycleContext: a.deps.authorities.lifecycleCtx,
		Diagnostics:      a.deps.authorities.diagnostics,
	})
	if err != nil {
		return nil, err
	}
	bundle.Placement = fenced
	validator, err := controlplane.NewExecutionValidator(controlplane.ExecutionValidatorConfig{Sandbox: notebook})
	if err != nil {
		return nil, err
	}
	bundle.Gateway, err = kernel.New(kernel.Config{
		Sessions:           sessions,
		Runtime:            fenced,
		TurnStartGate:      a.deps.authorities.approvalRecovery,
		Resolver:           resolver,
		ExecutionValidator: validator,
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

func botRuntimeConfig(state map[string]any) (bot.Config, error) {
	config, err := bot.Decode(state)
	if err != nil {
		return bot.Config{}, err
	}
	if config.Model == "" {
		return bot.Config{}, fmt.Errorf("gatewayapp: Bot model must be explicitly configured")
	}
	if _, bound := state[memorybinding.SessionStateKey]; bound {
		return bot.Config{}, fmt.Errorf("gatewayapp: Bot conversation cannot have a Workspace Memory binding")
	}
	return config, nil
}

// botTurnResolver reads only accepted Bot configuration. Description and name
// enter through canonical user events; they never modify this system prefix.
// The instruction baseline is resolved from the compiled Bot prompt on every
// Turn, so a Bot keeps one prompt and one tool set regardless of when it was
// created.
type botTurnResolver struct {
	composition   *runtimeComposition
	notebookTools []tool.Tool
}

func (r *botTurnResolver) ResolveTurn(ctx context.Context, intent kernel.TurnIntent) (kernel.ResolvedTurn, error) {
	if strings.TrimSpace(intent.ModelHint) != "" || strings.TrimSpace(intent.ModeName) != "" {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: Bot model and mode require explicit Bot configuration")
	}
	state, err := r.composition.sessions.SnapshotState(ctx, intent.SessionRef)
	if err != nil {
		return kernel.ResolvedTurn{}, err
	}
	config, err := botRuntimeConfig(state)
	if err != nil {
		return kernel.ResolvedTurn{}, err
	}
	resolved, err := r.resolveModel(ctx, config)
	if err != nil {
		return kernel.ResolvedTurn{}, err
	}
	if len(r.notebookTools) == 0 {
		return kernel.ResolvedTurn{}, fmt.Errorf("gatewayapp: Bot notebook tools unavailable")
	}
	instructions := buildBotSystemPrompt(r.composition.authorities.appName)
	request := agent.ModelRequestOptions{}
	if config.Fast {
		request.ServiceTier = model.ServiceTierPriority
	}
	return kernel.ResolvedTurn{RunRequest: agent.RunRequest{
		SessionRef:   intent.SessionRef,
		InputKind:    intent.InputKind,
		Input:        intent.Input,
		DisplayInput: strings.TrimSpace(intent.DisplayInput),
		ContentParts: append([]model.ContentPart(nil), intent.ContentParts...),
		InputActor:   session.CloneActorRef(intent.InputActor),
		AgentSpec: agent.AgentSpec{
			Name:    "bot",
			Model:   resolved.Model,
			Request: request,
			Tools:   r.notebookTools,
			Metadata: map[string]any{
				"system_prompt":    instructions,
				"reasoning_effort": resolved.ReasoningEffort,
			},
		},
	}}, nil
}

func (r *botTurnResolver) resolveModel(ctx context.Context, config bot.Config) (kernel.ModelResolution, error) {
	s := r.composition
	configured, err := s.lookup.ResolveConfig(config.Model)
	if err != nil {
		return kernel.ModelResolution{}, err
	}
	if config.Fast && !modelconfig.SupportsSpeedMode(configured, "fast") {
		return kernel.ModelResolution{}, fmt.Errorf("gatewayapp: Bot model %q does not support fast mode", config.Model)
	}
	if config.Effort != "" && !modelConfigSupportsReasoningEffort(configured, config.Effort) {
		return kernel.ModelResolution{}, fmt.Errorf("gatewayapp: Bot model %q does not support reasoning effort %q", config.Model, config.Effort)
	}
	configured.ReasoningEffort = config.Effort
	return s.lookup.ResolveModelConfig(ctx, configured, s.activeRuntime.ContextWindow)
}
