package controladapter

import (
	"context"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

func TestUnifiedConnectPresetsAndDeclaredModelTypes(t *testing.T) {
	models := []modelconfig.SelectableModel{{Name: "codex/gpt-6-luna", MetadataComplete: true, ImageInputKnown: true}, {Name: "typesafe/jev-latest", Judgment: true, MetadataComplete: true, ImageInputKnown: true}}
	driver := newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{ConnectionModelsFn: func(_ context.Context, req modelconfig.ConnectRequest) ([]modelconfig.SelectableModel, error) {
		if req.IncludeJudgments {
			return models, nil
		}
		return models[:1], nil
	}}}, "", "")
	presets, err := driver.CompleteSlashArg(t.Context(), "connect-presets", "", 100)
	if err != nil || !slices.Equal(slashCandidateValues(presets), []string{"codex", "grok", "deepseek", "xiaomi", "magpie", "ollama", "custom", "acp"}) {
		t.Fatalf("presets: %#v %v", presets, err)
	}
	state := connectwizard.ConnectWizardState{Provider: "magpie"}
	for _, command := range []string{"connect-model:", "connect-models:"} {
		choices, err := driver.CompleteSlashArg(t.Context(), command+state.EncodeCompletionState(), "", 100)
		want := 1
		if command == "connect-models:" {
			want = 2
		}
		if err != nil || len(choices) != want || choices[0].Display != "codex/gpt-6-luna" {
			t.Fatalf("%s: %#v %v", command, choices, err)
		}
	}
	for name, want := range map[string]string{"codex/gpt-6-luna": "chat", "typesafe/jev-latest": "judgment", "codex/gpt-6-luna,typesafe/jev-latest": "auto"} {
		state.Model = name
		choices, err := driver.CompleteSlashArg(t.Context(), "connect-model-type:"+state.EncodeCompletionState(), "", 100)
		if err != nil || len(choices) != 1 || choices[0].Value != want {
			t.Fatalf("type for %s: %#v %v", name, choices, err)
		}
	}
}

func TestMagpieDisplayKeepsExistingSelectorsAndDisconnectTargets(t *testing.T) {
	configs := []modelconfig.Config{
		modelconfig.NormalizeConfig(modelconfig.Config{Provider: "deepseek", Model: "deepseek-flash"}),
		modelconfig.NormalizeConfig(modelconfig.Config{Provider: "magpie", Model: "deepseek/deepseek-flash", BaseURL: "http://127.0.0.1:3425/v1"}),
	}
	choices := []ModelChoice{modelconfig.ChoiceFromConfig(configs[0]), modelconfig.ChoiceFromConfig(configs[1])}
	driver := newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{ListChoicesFn: func(context.Context, session.SessionRef) ([]ModelChoice, error) { return choices, nil }}}, "", "")
	for _, command := range []string{"model", "disconnect-provider"} {
		rows, err := driver.CompleteSlashArg(t.Context(), command, "", 100)
		if err != nil || len(rows) != 2 {
			t.Fatalf("%s: %#v %v", command, rows, err)
		}
		for _, row := range rows {
			if row.Display != "deepseek/deepseek-flash" {
				t.Fatalf("gateway prefix remains: %#v", row)
			}
			resolved, found, err := modelconfig.ResolveSelector(configs, row.Value)
			if err != nil || !found || resolved.ID != row.ModelConfigID {
				t.Fatalf("selection changed: %#v %v", row, err)
			}
		}
		if rows[0].Value == rows[1].Value || rows[0].Detail == rows[1].Detail {
			t.Fatalf("routes collapsed: %#v", rows)
		}
	}
}
