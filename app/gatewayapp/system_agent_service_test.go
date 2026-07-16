package gatewayapp

import (
	"context"
	"iter"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controlsystemagent "github.com/caelis-labs/caelis/control/systemagent"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

func TestSystemAgentServicePersistsModelBindingAndRefreshesReviewerAssembly(t *testing.T) {
	configuredModel := modelconfig.NormalizeConfig(modelconfig.Config{
		Alias: "openai-codex/gpt-5.6-sol", Provider: "openai-codex", Model: "gpt-5.6-sol",
		ReasoningLevels: []string{"high", "xhigh"},
	})
	store := newAppConfigStore(t.TempDir())
	if err := store.Save(AppConfig{
		Models: persistedModelConfig{Configs: []ModelConfig{configuredModel}},
		AgentRoster: controlagents.Configuration{
			Connections: []controlagents.Connection{{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"}}},
			Agents: []controlagents.Agent{
				{ID: "sol", Backing: controlagents.AgentBacking{ModelAlias: configuredModel.ID}},
				{ID: "claude", Backing: controlagents.AgentBacking{ConnectionID: "claude"}},
			},
		},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	refreshes := 0
	stack := &Stack{store: store, refreshConfiguredAgentsHook: func() error {
		refreshes++
		return nil
	}}
	service := stack.SystemAgents()
	status, err := service.BindSystemAgent(context.Background(), controlsystemagent.BindRequest{
		ID: controlsystemagent.Reviewer, AgentID: "sol", ReasoningEffort: "xhigh",
	})
	if err != nil {
		t.Fatalf("BindSystemAgent() error = %v", err)
	}
	if refreshes != 1 {
		t.Fatalf("runtime refreshes = %d, want 1", refreshes)
	}
	assertSystemAgentTarget(t, status, controlsystemagent.Reviewer, "sol")
	if len(status.Targets) != 1 || status.Targets[0].Agent.ID != "sol" || status.Targets[0].Model.ID != configuredModel.ID {
		t.Fatalf("eligible targets = %#v, want only model-backed sol", status.Targets)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	binding, ok := controlsystemagent.LookupBinding(loaded.SystemAgents, controlsystemagent.Reviewer)
	if !ok || binding.AgentID != "sol" || binding.ReasoningEffort != "xhigh" {
		t.Fatalf("persisted Reviewer binding = %#v, ok=%v", binding, ok)
	}

	status, err = service.ResetSystemAgent(context.Background(), controlsystemagent.Reviewer)
	if err != nil {
		t.Fatalf("ResetSystemAgent() error = %v", err)
	}
	if refreshes != 2 {
		t.Fatalf("runtime refreshes = %d, want 2", refreshes)
	}
	assertSystemAgentTarget(t, status, controlsystemagent.Reviewer, "")
}

func TestSystemAgentReasoningModelOverridesSceneFallback(t *testing.T) {
	t.Parallel()

	inner := &systemAgentReasoningRecorder{}
	wrapped := withSystemAgentReasoningEffort(kernelimpl.ModelResolution{
		Model: inner, ReasoningEffort: "xhigh",
	})
	for _, err := range wrapped.Generate(context.Background(), &model.Request{
		Reasoning: model.ReasoningConfig{Effort: "none"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	if inner.effort != "xhigh" {
		t.Fatalf("reasoning effort = %q, want bound xhigh", inner.effort)
	}
	capabilities, declared := model.CapabilitiesOf(wrapped)
	if !declared || !capabilities.StructuredOutput {
		t.Fatalf("wrapped capabilities = %#v, declared=%v", capabilities, declared)
	}
}

type systemAgentReasoningRecorder struct {
	effort string
}

func (*systemAgentReasoningRecorder) Name() string { return "system-agent-reasoning-recorder" }

func (m *systemAgentReasoningRecorder) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if req != nil {
		m.effort = req.Reasoning.Effort
	}
	return func(func(*model.StreamEvent, error) bool) {}
}

func (*systemAgentReasoningRecorder) Capabilities() model.Capabilities {
	return model.Capabilities{StructuredOutput: true}
}

func assertSystemAgentTarget(t *testing.T, status controlsystemagent.Status, id controlsystemagent.ID, agentID string) {
	t.Helper()
	for _, item := range status.Agents {
		if item.Definition.ID != id {
			continue
		}
		if item.Binding.AgentID != agentID || (agentID != "" && item.Agent.ID != agentID) {
			t.Fatalf("system Agent %q status = %#v, want Agent %q", id, item, agentID)
		}
		return
	}
	t.Fatalf("system Agent %q missing from status %#v", id, status)
}
