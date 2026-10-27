package controladapter

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
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
	for _, name := range []string{"gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna"} {
		if !slashCandidatesHaveValue(models, name) {
			t.Fatalf("Codex model candidates = %#v, missing %q", models, name)
		}
	}
	for _, hidden := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark", "gpt-5.2"} {
		if slashCandidatesHaveValue(models, hidden) {
			t.Fatalf("Codex account model candidates retained hidden %q = %#v", hidden, models)
		}
	}
	wantOrder := []string{"gpt-6.1-sol", "gpt-6.1-terra", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna"}
	if got := slashCandidateValues(models); !slices.Equal(got, wantOrder) {
		t.Fatalf("Codex account model candidate order = %#v, want %#v", got, wantOrder)
	}

	for _, modelName := range wantOrder {
		state.Model = modelName
		contexts, err := driver.CompleteSlashArg(ctx, "connect-context:"+state.EncodeCompletionState(), "", 10)
		if err != nil {
			t.Fatalf("CompleteSlashArg(connect-context:codex %s) error = %v", modelName, err)
		}
		wantContext := "258400"
		if modelName == "gpt-6.1-terra" {
			wantContext = "475000"
		}
		if len(contexts) != 1 || contexts[0].Value != wantContext {
			t.Fatalf("Codex %s context candidates = %#v, want %s", modelName, contexts, wantContext)
		}
	}
}

func TestOllamaCompletionOffersPositiveAdvancedDefaults(t *testing.T) {
	t.Parallel()
	driver := newHostAssembler(&runtimeDeps{Model: ModelRuntimeDeps{ConnectionModelsFn: func(ctx context.Context, req modelconfig.ConnectRequest) ([]modelconfig.SelectableModel, error) {
		req.HTTPClient = &http.Client{Transport: connectCatalogRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"installed"}]}`)), Header: make(http.Header)}, nil
		})}
		return modelconfig.SelectableModels(ctx, req)
	}}}, "", "")
	state := connectwizard.ConnectWizardState{Provider: "ollama", Model: "installed", BaseURL: "http://ollama.invalid"}
	for _, command := range []string{"connect-context:", "connect-maxout:"} {
		candidates, err := driver.CompleteSlashArg(t.Context(), command+state.EncodeCompletionState(), "", 10)
		if err != nil || len(candidates) != 1 {
			t.Fatalf("%s completion: %#v %v", command, candidates, err)
		}
		value, err := strconv.Atoi(candidates[0].Value)
		if err != nil || value <= 0 {
			t.Fatalf("%s default = %q", command, candidates[0].Value)
		}
	}
}

type connectCatalogRoundTripFunc func(*http.Request) (*http.Response, error)

func (f connectCatalogRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func slashCandidateValues(candidates []controlprompt.SlashArgCandidate) []string {
	values := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		values = append(values, candidate.Value)
	}
	return values
}

func slashCandidatesHaveValue(candidates []controlprompt.SlashArgCandidate, value string) bool {
	for _, candidate := range candidates {
		if candidate.Value == value {
			return true
		}
	}
	return false
}
