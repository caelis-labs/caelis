package gatewayapp

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
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
