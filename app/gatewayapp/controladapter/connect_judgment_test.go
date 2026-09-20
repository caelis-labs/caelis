package controladapter

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/judgment/typesafe"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

func TestConnectJudgmentCatalogKeepsManagementAndConversationChoicesDistinct(t *testing.T) {
	if !slashCandidatesHaveValue(completeConnectSources("", 100), "judgment") {
		t.Fatal("missing judgment source")
	}
	for _, source := range []string{"account", "api-key", "judgment"} {
		candidates := completeConnectProviders(t.Context(), nil, source, "", 100)
		if slashCandidatesHaveValue(candidates, "typesafe") != (source == "judgment") {
			t.Fatalf("typesafe routed to %s: %v", source, candidates)
		}
	}
	choices := []ModelChoice{{ID: "typesafe/jev", Alias: "typesafe/jev", Provider: "typesafe", Model: typesafe.DefaultModel, Judgment: true}}
	driver := newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{ListChoicesFn: func(context.Context, session.SessionRef) ([]ModelChoice, error) { return choices, nil }}}, "", "")
	models, err := driver.completeModelAliases(t.Context(), "", 100)
	if err != nil || len(models) != 0 {
		t.Fatalf("/model includes judgment: %v %v", models, err)
	}
	disconnect, err := driver.completeDisconnectProviderModels(t.Context(), "", 100)
	if err != nil || len(disconnect) != 1 {
		t.Fatalf("/disconnect omitted judgment: %v %v", disconnect, err)
	}
	state := connectwizard.ConnectWizardState{Provider: "typesafe"}
	models, err = completeConnectModels(t.Context(), driver, state, "", 100)
	if err != nil || len(models) != 0 {
		t.Fatalf("reconnect offers already connected judgment: %v %v", models, err)
	}
	choices = nil
	models, err = completeConnectModels(t.Context(), driver, state, "", 100)
	if err != nil || len(models) != 1 || models[0].Value != typesafe.DefaultModel {
		t.Fatalf("missing Jev model: %v %v", models, err)
	}
}
