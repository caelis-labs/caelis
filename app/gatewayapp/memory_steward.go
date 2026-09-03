package gatewayapp

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	managementv1alpha1 "github.com/caelis-labs/memory/api/memory/management/v1alpha1"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
	"github.com/caelis-labs/memory/appliance"
	"github.com/caelis-labs/memory/sdk/go/memory/stewardworker"
)

const (
	memoryStewardPollInterval  = 500 * time.Millisecond
	memoryStewardLeaseDuration = 2 * time.Minute
)

var defaultMemoryStewardProfile = stewardworker.BuiltInProfile()

// memoryStewardBridge is a process-private adapter between Memory's
// provider-neutral ModelGenerator callback and Caelis' existing model
// placement. It exists only for the automatically provisioned local private
// Space.
type memoryStewardBridge struct {
	composition *runtimeComposition
	admin       appliance.Management
	worker      stewardworker.Worker
	runner      systemManagedAgentRunner
	parent      session.Session
	diagnostics *slog.Logger
	cancel      context.CancelFunc
	done        chan struct{}

	active       atomic.Bool
	policySynced atomic.Bool
}

func startMemoryStewardBridge(stack *Stack) error {
	if stack == nil || stack.memoryRuntime == nil {
		return nil
	}
	admin := stack.memoryRuntime.Management()
	worker := stack.memoryRuntime.StewardWorker()
	if admin == nil || worker == nil {
		return fmt.Errorf("gatewayapp: embedded Memory Steward planes are unavailable")
	}
	parent := session.Session{
		SessionRef: session.SessionRef{
			AppName:      firstNonEmpty(stack.composition.authorities.appName, "caelis"),
			UserID:       "system",
			SessionID:    "memory-steward",
			WorkspaceKey: stack.composition.workspace.Key,
		},
		CWD:   stack.composition.workspace.CWD,
		Title: "Memory Steward",
	}
	bridge := &memoryStewardBridge{
		composition: &stack.composition,
		admin:       admin,
		worker:      worker,
		runner: newSystemManagedAgentRuntimeWithConfig(systemManagedAgentRuntimeConfig{
			Diagnostics: stack.composition.authorities.diagnostics,
		}),
		parent:      parent,
		diagnostics: stack.composition.authorities.diagnostics,
		done:        make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(stack.composition.authorities.lifecycleCtx)
	bridge.cancel = cancel
	if _, err := bridge.syncPolicy(ctx); err != nil {
		// Steward is optional enrichment. A missing or temporarily unavailable
		// bound model must not take the baseline receipt/recall path offline.
		bridge.logState("policy_unavailable")
	}
	stack.memorySteward = bridge
	go bridge.loop(ctx)
	return nil
}

func (b *memoryStewardBridge) loop(ctx context.Context) {
	defer close(b.done)
	defer b.closeClients()
	ticker := time.NewTicker(memoryStewardPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		enabled, err := b.syncPolicy(ctx)
		if err != nil {
			b.logState("policy_unavailable")
			continue
		}
		if !enabled {
			continue
		}
		runner := stewardworker.Runner{
			Client: b.worker,
			ModelGenerator: memoryStewardGenerator{
				composition: b.composition,
				runner:      b.runner,
				parent:      b.parent,
			},
			Options: stewardworker.RunnerOptions{
				LeaseDuration: memoryStewardLeaseDuration,
				PollInterval:  memoryStewardPollInterval,
			},
		}
		if _, err := runner.RunOnce(ctx); err != nil && ctx.Err() == nil {
			b.logState("worker_unavailable")
		}
	}
}

// syncPolicy is the decisive zero-token boundary. Only an explicit Steward
// binding enables semantic Jobs; unlike Guardian and Reviewer, the default
// ModelProfile is never consulted.
func (b *memoryStewardBridge) syncPolicy(ctx context.Context) (bool, error) {
	if b == nil || b.composition == nil || b.admin == nil {
		return false, fmt.Errorf("gatewayapp: Memory Steward bridge is unavailable")
	}
	snapshot, err := b.composition.placementSnapshot(ctx)
	if err != nil {
		return false, err
	}
	if _, explicitlyBound := agentbinding.Lookup(snapshot.placement.Bindings, agentbinding.HandleSteward); !explicitlyBound {
		if !b.policySynced.Load() || b.active.Load() {
			if _, err := b.admin.DisableSteward(ctx, managementv1alpha1.DisableStewardRequest{
				SpaceIDs: []memoryv1alpha1.SpaceID{defaultMemorySpaceID},
			}); err != nil {
				return false, err
			}
			b.active.Store(false)
			b.policySynced.Store(true)
			b.logState("static")
		}
		return false, nil
	}
	resolved, bound, err := b.composition.resolveSystemAgentModel(ctx, agentbinding.HandleSteward, 0)
	if err != nil {
		return false, err
	}
	if !bound || resolved.Model == nil {
		return false, fmt.Errorf("gatewayapp: explicitly bound Memory Steward model is unavailable")
	}
	if b.active.Load() {
		return true, nil
	}
	if _, err := b.admin.PutStewardProfile(ctx, managementv1alpha1.PutStewardProfileRequest{
		Profile: defaultMemoryStewardProfile,
	}); err != nil {
		return false, err
	}
	if _, err := b.admin.BindStewardProfile(ctx, managementv1alpha1.BindStewardProfileRequest{
		ProfileID: defaultMemoryStewardProfile.ProfileID,
		Version:   defaultMemoryStewardProfile.Version,
		SpaceIDs:  []memoryv1alpha1.SpaceID{defaultMemorySpaceID},
	}); err != nil {
		return false, err
	}
	b.active.Store(true)
	b.policySynced.Store(true)
	b.logState("semantic")
	return true, nil
}

func (b *memoryStewardBridge) logState(state string) {
	if b == nil || b.diagnostics == nil {
		return
	}
	b.diagnostics.Info("Memory Steward state", "component", "memory_steward", "state", state)
}

func (b *memoryStewardBridge) wait(ctx context.Context) error {
	if b == nil || b.done == nil {
		return nil
	}
	if b.cancel != nil {
		b.cancel()
	}
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *memoryStewardBridge) drained() bool {
	if b == nil || b.done == nil {
		return true
	}
	select {
	case <-b.done:
		return true
	default:
		return false
	}
}

func (b *memoryStewardBridge) closeClients() {
	// Direct embedded interfaces own no transport resources. The Memory runtime
	// is closed once by Stack after the bridge has stopped.
}

type memoryStewardGenerator struct {
	composition *runtimeComposition
	runner      systemManagedAgentRunner
	parent      session.Session
}

func (g memoryStewardGenerator) Generate(
	ctx context.Context,
	request stewardworker.GenerationRequest,
) (stewardworker.GenerationResponse, error) {
	if g.composition == nil || g.runner == nil {
		return stewardworker.GenerationResponse{}, memoryStewardGenerationError("bridge_unavailable", true, nil)
	}
	snapshot, err := g.composition.placementSnapshot(ctx)
	if err != nil {
		return stewardworker.GenerationResponse{}, memoryStewardGenerationError("placement_unavailable", true, err)
	}
	if _, bound := agentbinding.Lookup(snapshot.placement.Bindings, agentbinding.HandleSteward); !bound {
		return stewardworker.GenerationResponse{}, memoryStewardGenerationError("steward_unbound", true, nil)
	}
	resolved, bound, err := g.composition.resolveSystemAgentModel(ctx, agentbinding.HandleSteward, 0)
	if err != nil || !bound || resolved.Model == nil {
		return stewardworker.GenerationResponse{}, memoryStewardGenerationError("model_unavailable", true, err)
	}
	stewardModel := withSystemAgentReasoningEffort(resolved)
	output, parseMode := memoryStewardOutputSpecForModel(stewardModel, request)
	result, err := g.runner.Run(ctx, systemManagedAgentRunRequest{
		AgentID:            stewardSceneID,
		Purpose:            systemManagedAgentPurposeMemorySteward,
		Model:              stewardModel,
		ParentSession:      g.parent,
		Input:              request.Input,
		PolicyInstructions: request.Instructions,
		Output:             output,
	})
	if err != nil {
		return stewardworker.GenerationResponse{}, memoryStewardGenerationError("model_failure", true, err)
	}
	return stewardworker.GenerationResponse{Text: result.Text, ParseMode: parseMode}, nil
}

func memoryStewardGenerationError(code string, retryable bool, err error) error {
	return &stewardworker.GenerationError{Code: code, Retryable: retryable, Err: err}
}

func memoryStewardOutputSpecForModel(
	llm model.LLM,
	request stewardworker.GenerationRequest,
) (*model.OutputSpec, stewardworker.ParseMode) {
	maxTokens := request.MaxOutputBytes / 4
	if maxTokens < 512 {
		maxTokens = 512
	}
	if maxTokens > 32<<10 {
		maxTokens = 32 << 10
	}
	output := &model.OutputSpec{
		Mode:            model.OutputModeText,
		MaxOutputTokens: maxTokens,
	}
	capabilities, declared := model.CapabilitiesOf(llm)
	if declared && capabilities.StructuredOutput {
		output.Mode = model.OutputModeSchema
		output.JSONSchema = request.JSONSchema
		return output, stewardworker.ParseModeStrict
	}
	return output, stewardworker.ParseModeText
}
