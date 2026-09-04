package controladapter

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

func TestConnectModelCompletionOmitsConnectedModelsOnSelectedEndpoint(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		configured ModelChoice
		provider   string
		model      string
		baseURL    string
		wantModel  bool
	}{
		{name: "default endpoint", configured: ModelChoice{Provider: "openai-codex", Model: "gpt-6-astra", BaseURL: modelconfig.CodexOAuthBaseURL}},
		{name: "implicit default", configured: ModelChoice{Provider: "openai-codex", Model: "gpt-6-astra"}, baseURL: modelconfig.CodexOAuthBaseURL},
		{name: "normalized identity", configured: ModelChoice{Provider: " OPENAI-CODEX ", Model: " GPT-6-ASTRA ", BaseURL: modelconfig.CodexOAuthBaseURL + "/"}},
		{name: "other provider", configured: ModelChoice{Provider: "openai", Model: "gpt-6-astra", BaseURL: modelconfig.CodexOAuthBaseURL}, wantModel: true},
		{name: "other endpoint", configured: ModelChoice{Provider: "openai-codex", Model: "gpt-6-astra", BaseURL: "https://proxy.example.test/v1"}, wantModel: true},
		{name: "ACP model", configured: ModelChoice{Backend: "acp", Provider: "openai-codex", Model: "gpt-6-astra"}, wantModel: true},
		{name: "maintained endpoint alias", provider: "deepseek", model: "deepseek-v4-pro", configured: ModelChoice{Provider: "deepseek", Model: "deepseek-v4-pro", BaseURL: "https://api.deepseek.com/v1"}},
		{name: "alias selected", provider: "deepseek", model: "deepseek-v4-pro", baseURL: "https://api.deepseek.com/v1", configured: ModelChoice{Provider: "deepseek", Model: "deepseek-v4-pro", BaseURL: "https://api.deepseek.com/anthropic"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			driver := newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{
				ListChoicesFn: func(_ context.Context, ref session.SessionRef) ([]ModelChoice, error) {
					if ref.SessionID != "" {
						t.Fatalf("Connect catalog requested Session override %q", ref.SessionID)
					}
					return []ModelChoice{tt.configured}, nil
				},
			}}, "", "")
			state := connectwizard.ConnectWizardState{Provider: firstNonEmpty(tt.provider, "codex"), BaseURL: tt.baseURL}
			modelName := firstNonEmpty(tt.model, "gpt-6-astra")
			models, err := driver.CompleteSlashArg(context.Background(), "connect-model:"+state.EncodeCompletionState(), modelName, 20)
			if err != nil {
				t.Fatal(err)
			}
			if got := slashCandidatesHaveValue(models, modelName); got != tt.wantModel {
				t.Fatalf("%s selectable = %v, want %v; candidates = %#v", modelName, got, tt.wantModel, models)
			}
		})
	}
}

func TestConnectModelCompletionRefreshesAfterConnectAndDisconnect(t *testing.T) {
	t.Parallel()

	configured := []ModelChoice{{Provider: "openai-codex", Model: "gpt-6-astra"}}
	driver := newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{
		ListChoicesFn: func(context.Context, session.SessionRef) ([]ModelChoice, error) {
			return slices.Clone(configured), nil
		},
	}}, "", "")
	state := connectwizard.ConnectWizardState{Provider: "openai-codex"}
	command := "connect-model:" + state.EncodeCompletionState()
	assertFirst := func(want string) {
		t.Helper()
		models, err := driver.CompleteSlashArg(context.Background(), command, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := slashCandidateValues(models); !slices.Equal(got, []string{want}) {
			t.Fatalf("limited model candidates = %#v, want %q", got, want)
		}
	}
	assertFirst("gpt-5.6-sol")
	configured = append(configured, ModelChoice{Provider: "openai-codex", Model: "gpt-5.6-sol"})
	assertFirst("gpt-5.6-terra")
	configured = configured[1:]
	assertFirst("gpt-6-astra")
}

func TestConnectModelCompletionReportsConfiguredCatalogFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("configured catalog unavailable")
	driver := newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{
		ListChoicesFn: func(context.Context, session.SessionRef) ([]ModelChoice, error) {
			return nil, wantErr
		},
	}}, "", "")
	state := connectwizard.ConnectWizardState{Provider: "codex"}
	models, err := driver.CompleteSlashArg(context.Background(), "connect-model:"+state.EncodeCompletionState(), "", 20)
	if !errors.Is(err, wantErr) || len(models) != 0 {
		t.Fatalf("model candidates = %#v, error = %v; want catalog failure", models, err)
	}
}
