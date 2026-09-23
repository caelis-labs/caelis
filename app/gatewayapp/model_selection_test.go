package gatewayapp

import (
	"context"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	assembly "github.com/caelis-labs/caelis/internal/controlassembly"
)

func TestModelChoicesProjectConfirmedSessionSelection(t *testing.T) {
	ctx := context.Background()
	stack, active := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "flash",
		Alias: "same-label", ContextWindowTokens: 32000,
		ReasoningLevels: []string{"none", "low", "high"}, ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "other", Alias: "same-label",
		BaseURL: "http://localhost:11435", ReasoningLevels: []string{"none"}, ReasoningEffort: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	current := mustCurrentSession(t, stack, active.SessionID)
	revision := current.Revision
	result, err := stack.ConfigurationCommands().UseSessionModel(ctx, appserver.Principal{ID: stack.composition.authorities.userID}, appserver.SessionModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "picker-model-selection", SessionID: current.SessionID, ExpectedRevision: &revision, ExpectedControllerEpoch: current.Controller.EpochID},
		Model:     profile.ID, ReasoningEffort: "high",
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("select = %+v, %v", result, err)
	}
	choices, err := stack.Models().ListChoices(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	currentCount := 0
	for _, choice := range choices {
		if choice.Current {
			currentCount++
			if choice.ID != profile.Backend.Provider.ModelConfigID || choice.ReasoningEffort != "high" || choice.FastMode || choice.ContextWindowTokens != 32000 {
				t.Fatalf("current selection = %+v", choice)
			}
		}
		if choice.ID == other.Backend.Provider.ModelConfigID && choice.Current {
			t.Fatal("display alias collision marked the wrong model")
		}
	}
	if currentCount != 1 {
		t.Fatalf("current models = %d: %+v", currentCount, choices)
	}
	defaults, err := stack.Models().ListChoices(ctx, session.SessionRef{})
	if err != nil {
		t.Fatal(err)
	}
	for _, choice := range defaults {
		if choice.ID == profile.Backend.Provider.ModelConfigID && choice.ReasoningEffort != "low" {
			t.Fatalf("session choice changed Host profile default: %+v", choice)
		}
	}
}

func TestModelChoiceFastCapabilityRespectsEndpoint(t *testing.T) {
	for _, baseURL := range []string{"", "https://api.openai.com/v1", "https://custom.invalid/v1"} {
		cfg := ModelConfig{Provider: "openai", API: providers.APIOpenAI, Model: "gpt-5.6-sol", BaseURL: baseURL, ReasoningEffort: "medium"}
		choice := modelChoiceFromConfig(cfg)
		if choice.FastSupported != modelconfig.SupportsSpeedMode(cfg, "fast") || choice.FastSupported != (baseURL != "https://custom.invalid/v1") {
			t.Fatalf("endpoint %q fast = %v", baseURL, choice.FastSupported)
		}
	}
}

// TestModelChoicesProjectLegacyProviderWithoutProfile proves that a selectable
// provider model with no configured ModelProfile still reports honest typed
// reasoning capability and a default. Completion publishes typed selection
// metadata only for a choice that carries both (see
// app/gatewayapp/controladapter.modelChoiceCandidates), so an empty projection
// leaves a client that stages model changes with nothing to apply for that row.
func TestModelChoicesProjectLegacyProviderWithoutProfile(t *testing.T) {
	ctx := context.Background()
	root, workspace := t.TempDir(), t.TempDir()
	cfg := Config{
		AppName:      "caelis",
		UserID:       "legacy-projection",
		StoreDir:     root,
		WorkspaceKey: workspace,
		WorkspaceCWD: workspace,
		ApprovalMode: "auto-review",
		Assembly:     assembly.ResolvedAssembly{},
	}
	stack, err := newGatewayAppTestStack(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	modern, err := stack.connectTestModel(ModelConfig{
		Provider: "ollama", API: providers.APIOllama, Model: "flash",
		ReasoningLevels: []string{"none", "low", "high"}, ReasoningEffort: "low",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}

	// A persisted provider config can outlive or precede its ModelProfile. Keep
	// one without a profile, and free of reasoning metadata, so it is reachable
	// only through the bounded compatibility catalog.
	legacy := modelconfig.NormalizeConfig(ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "legacy-flash"})
	if legacy.ReasoningEffort != "" || len(legacy.ReasoningLevels) != 0 {
		t.Fatalf("legacy fixture carries reasoning metadata: %+v", legacy)
	}
	store := newAppConfigStore(root)
	doc, err := store.Load()
	if err != nil {
		t.Fatalf("Load(config) error = %v", err)
	}
	endpoint := modelconfig.ProviderEndpointFromConfig(legacy)
	if !slices.ContainsFunc(doc.Models.ProviderEndpoints, func(existing modelconfig.ProviderEndpointConfig) bool {
		return existing.ID == endpoint.ID
	}) {
		doc.Models.ProviderEndpoints = append(doc.Models.ProviderEndpoints, endpoint)
	}
	doc.Models.Configs = append(doc.Models.Configs, legacy)
	if err := store.Save(doc); err != nil {
		t.Fatalf("Save(profile-less provider config) error = %v", err)
	}
	if slices.ContainsFunc(doc.ModelProfiles.Profiles, func(profile modelprofile.ModelProfile) bool {
		return profile.Backend.Provider != nil && profile.Backend.Provider.ModelConfigID == legacy.ID
	}) {
		t.Fatalf("fixture unexpectedly has a ModelProfile for %q", legacy.ID)
	}

	reloaded, err := newGatewayAppTestStack(t, cfg)
	if err != nil {
		t.Fatal(err)
	}
	choices, err := reloaded.Models().ListChoices(ctx, session.SessionRef{})
	if err != nil {
		t.Fatal(err)
	}
	var legacyChoice, modernChoice *ModelChoice
	for i := range choices {
		switch choices[i].ID {
		case legacy.ID:
			legacyChoice = &choices[i]
		case modern.Backend.Provider.ModelConfigID:
			modernChoice = &choices[i]
		}
	}
	if legacyChoice == nil || modernChoice == nil {
		t.Fatalf("ListChoices() = %#v, want the profile-backed and profile-less provider model", choices)
	}
	if !slices.Equal(legacyChoice.ReasoningLevels, []string{"none"}) || legacyChoice.ReasoningEffort != "none" {
		t.Fatalf("profile-less projection = %v/%q, want none/none", legacyChoice.ReasoningLevels, legacyChoice.ReasoningEffort)
	}
	if legacyChoice.FastSupported {
		t.Fatalf("profile-less projection invented Fast support: %+v", legacyChoice)
	}
	if !slices.Equal(modernChoice.ReasoningLevels, []string{"none", "low", "high"}) || modernChoice.ReasoningEffort != "low" {
		t.Fatalf("profile-backed projection changed: %v/%q", modernChoice.ReasoningLevels, modernChoice.ReasoningEffort)
	}
}
