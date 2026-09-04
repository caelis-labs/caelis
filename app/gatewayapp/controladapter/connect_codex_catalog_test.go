package controladapter

import (
	"context"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

func TestCodexConnectCompletionUsesMaintainedCatalogWithoutAuthentication(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	driver := newHostAssembler(&runtimeDeps{}, "", "")
	state := connectwizard.ConnectWizardState{
		Provider:       "codex",
		BaseURL:        modelconfig.CodexOAuthBaseURL,
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
	}
	models, err := driver.CompleteSlashArg(ctx, "connect-model:"+state.EncodeCompletionState(), "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model:codex) error = %v", err)
	}
	for _, name := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"} {
		if !slashCandidatesHaveValue(models, name) {
			t.Fatalf("Codex model candidates = %#v, missing %q", models, name)
		}
	}
	for _, hidden := range []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark", "gpt-5.2"} {
		if slashCandidatesHaveValue(models, hidden) {
			t.Fatalf("Codex account model candidates retained hidden %q = %#v", hidden, models)
		}
	}
	wantOrder := []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"}
	if got := slashCandidateValues(models); !slices.Equal(got, wantOrder) {
		t.Fatalf("Codex account model candidate order = %#v, want %#v", got, wantOrder)
	}

	for _, modelName := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
		state.Model = modelName
		contexts, err := driver.CompleteSlashArg(ctx, "connect-context:"+state.EncodeCompletionState(), "", 10)
		if err != nil {
			t.Fatalf("CompleteSlashArg(connect-context:codex %s) error = %v", modelName, err)
		}
		if len(contexts) != 1 || contexts[0].Value != "258400" {
			t.Fatalf("Codex %s context candidates = %#v, want 258400", modelName, contexts)
		}
	}
}

func slashCandidateValues(candidates []controlprompt.SlashArgCandidate) []string {
	values := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		values = append(values, candidate.Value)
	}
	return values
}

func TestCodexConnectCompletionFallbackOmitsHiddenModels(t *testing.T) {
	t.Parallel()

	driver := newHostAssembler(&runtimeDeps{}, "", "")
	state := connectwizard.ConnectWizardState{Provider: "codex", BaseURL: modelconfig.CodexOAuthBaseURL}
	models, err := driver.CompleteSlashArg(context.Background(), "connect-model:"+state.EncodeCompletionState(), "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model:codex fallback) error = %v", err)
	}
	for _, name := range []string{"gpt-6-astra", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"} {
		if !slashCandidatesHaveValue(models, name) {
			t.Fatalf("Codex fallback candidates = %#v, missing %q", models, name)
		}
	}
	for _, hidden := range []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark", "gpt-5.2"} {
		if slashCandidatesHaveValue(models, hidden) {
			t.Fatalf("Codex fallback candidates retained hidden %q = %#v", hidden, models)
		}
	}
}

func slashCandidatesHaveValue(candidates []controlprompt.SlashArgCandidate, value string) bool {
	for _, candidate := range candidates {
		if candidate.Value == value {
			return true
		}
	}
	return false
}
