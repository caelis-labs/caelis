package gatewayapp

import (
	"context"
	"iter"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	modelprofilebuilder "github.com/caelis-labs/caelis/control/modelprofile/builder"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

func TestAgentBindingServicePersistsSystemBindingAndRefreshesReviewerAssembly(t *testing.T) {
	configuredModel := modelconfig.NormalizeConfig(modelconfig.Config{
		Alias: "openai-codex/gpt-5.6-sol", Provider: "openai-codex", Model: "gpt-5.6-sol",
		ReasoningLevels: []string{"high", "xhigh"},
	})
	store := newAppConfigStore(t.TempDir())
	profile, err := modelprofilebuilder.FromProvider(configuredModel)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(AppConfig{
		Models:        persistedModelConfig{Configs: []ModelConfig{configuredModel}},
		ModelProfiles: modelprofile.Configuration{DefaultProfileID: profile.ID, Profiles: []modelprofile.ModelProfile{profile}},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	refreshes := 0
	stack := &Stack{store: store, refreshConfiguredAgentsHook: func() error {
		refreshes++
		return nil
	}}
	service := stack.AgentBindings()
	status, err := service.BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleReviewer, ProfileID: profile.ID, Effort: "xhigh",
	})
	if err != nil {
		t.Fatalf("BindAgentBinding() error = %v", err)
	}
	if refreshes != 1 {
		t.Fatalf("runtime refreshes = %d, want 1", refreshes)
	}
	assertAgentBindingTarget(t, status, agentbinding.HandleReviewer, profile.ID)
	if len(status.Targets) != 1 || status.Targets[0].ID != profile.ID {
		t.Fatalf("eligible targets = %#v, want only provider profile %q", status.Targets, profile.ID)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	binding, ok := agentbinding.Lookup(loaded.AgentBindings, agentbinding.HandleReviewer)
	if !ok || binding.ProfileID != profile.ID || binding.Effort != "xhigh" {
		t.Fatalf("persisted Reviewer binding = %#v, ok=%v", binding, ok)
	}

	status, err = service.ResetAgentBinding(context.Background(), agentbinding.HandleReviewer)
	if err != nil {
		t.Fatalf("ResetAgentBinding() error = %v", err)
	}
	if refreshes != 2 {
		t.Fatalf("runtime refreshes = %d, want 2", refreshes)
	}
	assertAgentBindingTarget(t, status, agentbinding.HandleReviewer, "")
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
