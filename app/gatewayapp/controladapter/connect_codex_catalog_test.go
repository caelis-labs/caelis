package controladapter

import (
	"context"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

func TestCodexConnectCompletionUsesAccountCatalogAndEffective56Context(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	driver, err := NewAdapter(ctx, &RuntimeStack{
		Model: ModelRuntimeDeps{
			AuthenticateFn: func(_ context.Context, req modelconfig.AuthenticateRequest) (modelconfig.AuthenticateResult, error) {
				if req.Provider != "openai-codex" || req.Purpose != modelconfig.AuthPurposeModelSelection {
					t.Fatalf("AuthenticateRequest = %#v", req)
				}
				return modelconfig.AuthenticateResult{
					SelectableModels: []string{
						"gpt-5.6-sol",
						"gpt-5.6-terra",
						"gpt-5.6-luna",
						"gpt-5.5",
						"gpt-5.4",
						"gpt-5.4-mini",
						"gpt-5.3-codex-spark",
					},
					ModelCatalogAuthoritative: true,
				}, nil
			},
		},
	}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	state := connectwizard.ConnectWizardState{
		Provider:       "codex",
		BaseURL:        modelconfig.CodexOAuthBaseURL,
		TimeoutSeconds: connectwizard.DefaultConnectTimeoutSeconds,
	}
	models, err := driver.CompleteSlashArg(ctx, "connect-model:"+state.EncodeCompletionState(), "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model:codex) error = %v", err)
	}
	for _, name := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"} {
		if !slashCandidatesHaveValue(models, name) {
			t.Fatalf("Codex model candidates = %#v, missing %q", models, name)
		}
	}
	if slashCandidatesHaveValue(models, "gpt-5.2") {
		t.Fatalf("Codex account model candidates retained retired 5.2 = %#v", models)
	}
	wantOrder := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"}
	if got := slashCandidateValues(models); !slices.Equal(got, wantOrder) {
		t.Fatalf("Codex account model candidate order = %#v, want %#v", got, wantOrder)
	}

	state.Model = "gpt-5.6-sol"
	contexts, err := driver.CompleteSlashArg(ctx, "connect-context:"+state.EncodeCompletionState(), "", 10)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-context:codex) error = %v", err)
	}
	if len(contexts) != 1 || contexts[0].Value != "258400" {
		t.Fatalf("Codex 5.6 context candidates = %#v, want 258400", contexts)
	}
}

func slashCandidateValues(candidates []SlashArgCandidate) []string {
	values := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		values = append(values, candidate.Value)
	}
	return values
}

func TestCodexConnectCompletionFallbackOmitsDeprecated52(t *testing.T) {
	t.Parallel()

	driver, err := NewAdapter(context.Background(), &RuntimeStack{
		Model: ModelRuntimeDeps{AuthenticateFn: func(context.Context, modelconfig.AuthenticateRequest) (modelconfig.AuthenticateResult, error) {
			return modelconfig.AuthenticateResult{}, nil
		}},
	}, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	state := connectwizard.ConnectWizardState{Provider: "codex", BaseURL: modelconfig.CodexOAuthBaseURL}
	models, err := driver.CompleteSlashArg(context.Background(), "connect-model:"+state.EncodeCompletionState(), "", 20)
	if err != nil {
		t.Fatalf("CompleteSlashArg(connect-model:codex fallback) error = %v", err)
	}
	for _, name := range []string{"gpt-5.6-sol", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"} {
		if !slashCandidatesHaveValue(models, name) {
			t.Fatalf("Codex fallback candidates = %#v, missing %q", models, name)
		}
	}
	if slashCandidatesHaveValue(models, "gpt-5.2") {
		t.Fatalf("Codex fallback candidates retained deprecated 5.2 = %#v", models)
	}
}
