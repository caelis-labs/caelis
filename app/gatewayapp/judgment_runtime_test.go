package gatewayapp

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/typesafe"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/placement"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type guardianModelState struct{ alias string }

func (s guardianModelState) SnapshotState(context.Context, session.SessionRef) (map[string]any, error) {
	return map[string]any{kernel.StateCurrentModelAlias: s.alias}, nil
}

func TestGuardianPrimaryAndScreeningBindingsComposeAndResetIndependently(t *testing.T) {
	stack := newLocalStateTestHost(t)
	var profiles []modelprofile.ModelProfile
	for _, name := range []string{"host-default", "session-main", "guardian-specialist"} {
		profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", API: model.APIOllama, Model: name})
		if err != nil {
			t.Fatal(err)
		}
		profiles = append(profiles, profile)
	}
	screen, err := stack.connectTestModel(ModelConfig{Provider: "typesafe", API: modelconfig.APISystemOne, Model: typesafe.DefaultModel, Token: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	bindings := stack.testAgentBindings()
	if _, err := bindings.BindAgentBinding(t.Context(), agentbinding.Binding{Handle: agentbinding.HandleGuardianScreen, ProfileID: screen.ID, Effort: "none"}); err != nil {
		t.Fatal(err)
	}
	resolver, err := kernel.NewAssemblyResolver(kernel.AssemblyResolverConfig{
		Sessions:          guardianModelState{alias: profiles[1].Backend.Provider.ModelConfigID},
		ModelLookup:       stack.composition.lookup,
		DefaultModelAlias: profiles[0].Backend.Provider.ModelConfigID,
		ApprovalModelResolver: func(ctx context.Context, _ session.SessionRef) (model.LLM, bool, error) {
			return stack.composition.resolveGuardianAgentModel(ctx, 0)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertModel := func(want string) {
		t.Helper()
		resolved, err := resolver.ResolveApprovalModel(t.Context(), session.SessionRef{SessionID: "s"})
		if err != nil || resolved == nil || resolved.Name() != want {
			t.Fatalf("model=%v want=%s err=%v", resolved, want, err)
		}
	}
	assertModel("session-main")
	if _, err := bindings.BindAgentBinding(t.Context(), agentbinding.Binding{Handle: agentbinding.HandleGuardian, ProfileID: profiles[2].ID, Effort: "none"}); err != nil {
		t.Fatal(err)
	}
	assertModel("guardian-specialist")
	if evaluator, err := stack.composition.boundJudgment(t.Context(), agentbinding.HandleGuardianScreen); err != nil || evaluator == nil {
		t.Fatalf("missing auxiliary: %v", err)
	}
	if _, err := bindings.ResetAgentBinding(t.Context(), agentbinding.HandleGuardianScreen); err != nil {
		t.Fatal(err)
	}
	assertModel("guardian-specialist")
	if evaluator, err := stack.composition.boundJudgment(t.Context(), agentbinding.HandleGuardianScreen); err != nil || evaluator != nil {
		t.Fatalf("auxiliary not disabled: %v", err)
	}
	if _, err := bindings.BindAgentBinding(t.Context(), agentbinding.Binding{Handle: agentbinding.HandleGuardianScreen, ProfileID: screen.ID, Effort: "none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := bindings.ResetAgentBinding(t.Context(), agentbinding.HandleGuardian); err != nil {
		t.Fatal(err)
	}
	assertModel("session-main")
	if evaluator, err := stack.composition.boundJudgment(t.Context(), agentbinding.HandleGuardianScreen); err != nil || evaluator == nil {
		t.Fatalf("Agent reset disabled auxiliary: %v", err)
	}
}

func TestJudgmentConnectionBindsOnlySupportedScenesAndNeverBecomesDefault(t *testing.T) {
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceKey: t.TempDir(), WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer stack.Close()
	configs, err := modelconfig.AssembleConnect(t.Context(), modelconfig.ConnectRequest{Provider: "custom-judgment", BaseURL: "https://api.typesafe.ai/v1", Models: []modelconfig.ModelSelection{{Name: typesafe.DefaultModel}}, APIKey: "fixture-secret"}, modelconfig.ConnectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := stack.connectTestModels(configs)
	if err != nil {
		t.Fatal(err)
	}
	profile := profiles[0]
	if !profile.Judgment || stack.composition.lookup.DefaultID() != "" {
		t.Fatalf("judgment connected as conversation model: %+v", profile)
	}
	if err := stack.useTestHostModel(t.Context(), session.SessionRef{}, profile.ID); err == nil {
		t.Fatal("judgment selected as conversation default")
	}
	if _, err := modelprofile.SelectDefault(modelprofile.Configuration{Profiles: profiles}, profile.ID, ""); err == nil {
		t.Fatal("profile accepted judgment default")
	}
	for _, handle := range []agentbinding.Handle{agentbinding.HandleGuardianScreen} {
		if _, err := stack.testAgentBindings().BindAgentBinding(t.Context(), agentbinding.Binding{Handle: handle, ProfileID: profile.ID, Effort: "none"}); err != nil {
			t.Fatalf("bind %s: %v", handle, err)
		}
		evaluator, err := stack.composition.boundJudgment(t.Context(), handle)
		if err != nil || evaluator == nil || evaluator.Name() != typesafe.DefaultModel {
			t.Fatalf("resolve %s = %v, %v", handle, evaluator, err)
		}
	}
	for _, handle := range []agentbinding.Handle{agentbinding.HandleBreeze, agentbinding.HandleGuardian, agentbinding.HandleReviewer} {
		if _, err := stack.testAgentBindings().BindAgentBinding(t.Context(), agentbinding.Binding{Handle: handle, ProfileID: profile.ID, Effort: "none"}); err == nil {
			t.Fatalf("judgment accepted by generation scene %s", handle)
		}
	}
	snapshot, err := stack.composition.placementSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := placement.ResolveProfile(snapshot.placement, profile.ID, "none"); err == nil {
		t.Fatal("judgment froze as executable placement")
	}
	choices, err := stack.Models().ListChoices(t.Context(), session.SessionRef{})
	if err != nil {
		t.Fatal(err)
	}
	if len(choices) != 1 || !choices[0].Judgment {
		t.Fatal("judgment missing from connection management")
	}
	aliases, err := stack.composition.ListModelAliases(t.Context(), session.SessionRef{})
	if err != nil || len(aliases) != 0 {
		t.Fatalf("judgment exposed as conversation alias: %v %v", aliases, err)
	}
	chat, err := stack.connectTestModel(ModelConfig{Provider: "ollama", API: model.APIOllama, Model: "conversation"})
	if err != nil {
		t.Fatal(err)
	}
	if stack.composition.lookup.DefaultID() != chat.Backend.Provider.ModelConfigID {
		t.Fatal("conversation did not become default")
	}
	if err := stack.deleteTestHostModel(context.Background(), session.SessionRef{}, chat.Backend.Provider.ModelConfigID); err != nil {
		t.Fatal(err)
	}
	if stack.composition.lookup.DefaultID() != "" {
		t.Fatal("deleting conversation selected judgment fallback")
	}
	if err := stack.deleteTestHostModel(t.Context(), session.SessionRef{}, profile.Backend.Provider.ModelConfigID); err != nil {
		t.Fatal(err)
	}
	document, err := stack.composition.authorities.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(document.ModelProfiles.Profiles) != 0 || len(document.AgentBindings.Bindings) != 0 || len(document.Models.ProviderEndpoints) != 0 {
		t.Fatal("disconnect retained judgment profile, bindings or endpoint")
	}
}
