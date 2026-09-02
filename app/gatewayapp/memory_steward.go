package gatewayapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	managementv1alpha1 "github.com/caelis-labs/memory/api/memory/management/v1alpha1"
	stewardv1alpha1 "github.com/caelis-labs/memory/api/memory/steward/v1alpha1"
	memoryv1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
	"github.com/caelis-labs/memory/appliance"
	"github.com/caelis-labs/memory/sdk/go/memory/stewardworker"
)

const (
	defaultMemoryStewardProfileID      stewardv1alpha1.ProfileID = "caelis-default"
	defaultMemoryStewardProfileVersion uint64                    = 1
	memoryStewardPollInterval                                    = 500 * time.Millisecond
	memoryStewardLeaseDuration                                   = 2 * time.Minute
)

var defaultMemoryStewardProfile = stewardv1alpha1.ProfileSpec{
	ProfileID:         defaultMemoryStewardProfileID,
	Version:           defaultMemoryStewardProfileVersion,
	SystemPrompt:      "Preserve durable user facts and preferences. Merge or supersede only when the supplied record context supports it; otherwise add a new record or ignore non-durable content.",
	MaxContextRecords: 32,
	MaxInputBytes:     256 << 10,
	MaxOutputBytes:    32 << 10,
}

// memoryStewardBridge is a process-private adapter between Memory's
// provider-neutral Generator callback and Caelis' existing model placement. It
// exists only for the automatically provisioned local private Space.
type memoryStewardBridge struct {
	composition *runtimeComposition
	admin       appliance.Management
	worker      stewardworker.Worker
	runner      *systemManagedAgentRuntime
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
			Generator: memoryStewardGenerator{
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
		ProfileID: defaultMemoryStewardProfileID,
		Version:   defaultMemoryStewardProfileVersion,
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

func (b *memoryStewardBridge) closeClients() {
	// Direct embedded interfaces own no transport resources. The Memory runtime
	// is closed once by Stack after the bridge has stopped.
}

type memoryStewardGenerator struct {
	composition *runtimeComposition
	runner      systemManagedAgentRunner
	parent      session.Session
}

type memoryStewardModelInput struct {
	Protocol       string                          `json:"protocol"`
	ProfileID      stewardv1alpha1.ProfileID       `json:"profile_id"`
	ProfileVersion uint64                          `json:"profile_version"`
	Receipt        stewardv1alpha1.ReceiptInput    `json:"receipt"`
	Records        []stewardv1alpha1.RecordContext `json:"records"`
}

func (g memoryStewardGenerator) Generate(
	ctx context.Context,
	request stewardv1alpha1.WorkRequest,
) (stewardv1alpha1.Proposal, error) {
	if g.composition == nil || g.runner == nil {
		return stewardv1alpha1.Proposal{}, memoryStewardGenerationError("bridge_unavailable", true, nil)
	}
	snapshot, err := g.composition.placementSnapshot(ctx)
	if err != nil {
		return stewardv1alpha1.Proposal{}, memoryStewardGenerationError("placement_unavailable", true, err)
	}
	if _, bound := agentbinding.Lookup(snapshot.placement.Bindings, agentbinding.HandleSteward); !bound {
		return stewardv1alpha1.Proposal{}, memoryStewardGenerationError("steward_unbound", true, nil)
	}
	resolved, bound, err := g.composition.resolveSystemAgentModel(ctx, agentbinding.HandleSteward, 0)
	if err != nil || !bound || resolved.Model == nil {
		return stewardv1alpha1.Proposal{}, memoryStewardGenerationError("model_unavailable", true, err)
	}
	input, err := json.Marshal(memoryStewardModelInput{
		Protocol:       request.Protocol,
		ProfileID:      request.Profile.ProfileID,
		ProfileVersion: request.Profile.Version,
		Receipt:        request.Receipt,
		Records:        request.Records,
	})
	if err != nil {
		return stewardv1alpha1.Proposal{}, memoryStewardGenerationError("input_invalid", false, err)
	}
	stewardModel := withSystemAgentReasoningEffort(resolved)
	result, err := g.runner.Run(ctx, systemManagedAgentRunRequest{
		AgentID:            stewardSceneID,
		Purpose:            systemManagedAgentPurposeMemorySteward,
		Model:              stewardModel,
		ParentSession:      g.parent,
		Input:              string(input),
		PolicyInstructions: "Memory appliance policy for this job:\n" + request.Profile.SystemPrompt,
		Output:             memoryStewardOutputSpecForModel(stewardModel, request.Profile.MaxOutputBytes),
		Metadata: map[string]any{
			"memory_steward_profile_id":      string(request.Profile.ProfileID),
			"memory_steward_profile_version": request.Profile.Version,
		},
	})
	if err != nil {
		return stewardv1alpha1.Proposal{}, memoryStewardGenerationError("model_failure", true, err)
	}
	if request.Profile.MaxOutputBytes > 0 && len(result.Text) > request.Profile.MaxOutputBytes {
		return stewardv1alpha1.Proposal{}, memoryStewardGenerationError("output_too_large", false, nil)
	}
	proposal, err := parseMemoryStewardProposal(result.Text)
	if err != nil {
		return stewardv1alpha1.Proposal{}, memoryStewardGenerationError("output_invalid", false, err)
	}
	return proposal, nil
}

func memoryStewardGenerationError(code string, retryable bool, err error) error {
	return &stewardworker.GenerationError{Code: code, Retryable: retryable, Err: err}
}

func memoryStewardOutputSpec(maxOutputBytes int) *model.OutputSpec {
	maxTokens := maxOutputBytes / 4
	if maxTokens < 512 {
		maxTokens = 512
	}
	if maxTokens > 32<<10 {
		maxTokens = 32 << 10
	}
	return &model.OutputSpec{
		Mode:            model.OutputModeSchema,
		MaxOutputTokens: maxTokens,
		JSONSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"operation": map[string]any{
					"type": "string",
					"enum": []any{"ADD", "MERGE", "SUPERSEDE", "IGNORE"},
				},
				"target_record_id":  map[string]any{"type": "string"},
				"expected_revision": map[string]any{"type": "integer", "minimum": 0},
				"kind":              map[string]any{"type": "string"},
				"text":              map[string]any{"type": "string"},
				"evidence_refs": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
				},
			},
			"required": []any{"operation"},
		},
	}
}

func memoryStewardOutputSpecForModel(llm model.LLM, maxOutputBytes int) *model.OutputSpec {
	output := memoryStewardOutputSpec(maxOutputBytes)
	capabilities, declared := model.CapabilitiesOf(llm)
	if declared && capabilities.StructuredOutput {
		return output
	}
	output.Mode = model.OutputModeText
	output.JSONSchema = nil
	return output
}

func parseMemoryStewardProposal(text string) (stewardv1alpha1.Proposal, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(text)))
	decoder.DisallowUnknownFields()
	var proposal stewardv1alpha1.Proposal
	if err := decoder.Decode(&proposal); err != nil {
		return stewardv1alpha1.Proposal{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return stewardv1alpha1.Proposal{}, fmt.Errorf("multiple JSON values")
		}
		return stewardv1alpha1.Proposal{}, err
	}
	if err := proposal.ValidateShape(); err != nil {
		return stewardv1alpha1.Proposal{}, err
	}
	return proposal, nil
}
