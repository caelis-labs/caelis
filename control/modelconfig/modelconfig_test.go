package modelconfig

import (
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

func TestLookupProviderOwnsEndpointAndAuthenticationPolicy(t *testing.T) {
	t.Parallel()

	template, ok := LookupProvider(XiaomiTokenPlanCNAlias)
	if !ok {
		t.Fatalf("LookupProvider(%q) = false", XiaomiTokenPlanCNAlias)
	}
	if template.Provider != "xiaomi" || template.API != model.APIMimo {
		t.Fatalf("LookupProvider(%q) = %#v, want xiaomi/mimo", XiaomiTokenPlanCNAlias, template)
	}
	if template.DefaultBaseURL != XiaomiTokenPlanCNBaseURL || template.DefaultEndpointID != "token-plan-cn" {
		t.Fatalf("token-plan template = %#v", template)
	}
	endpoint, ok := EndpointForBaseURL(template, template.DefaultBaseURL)
	if !ok || endpoint.ID != "token-plan-cn" || endpoint.API != model.APIMimo {
		t.Fatalf("EndpointForBaseURL() = %#v, %v", endpoint, ok)
	}
}

func TestProviderTemplateOwnsModelSelectionPolicy(t *testing.T) {
	t.Parallel()

	openRouter, ok := LookupProvider("openrouter")
	if !ok || !openRouter.UseModelDirectory {
		t.Fatalf("openrouter template = %#v, want model directory", openRouter)
	}
	for _, provider := range []string{"openai-compatible", "anthropic-compatible"} {
		template, ok := LookupProvider(provider)
		if !ok || template.UseModelDirectory || !template.PromptForBaseURL || len(template.DefaultReasoningLevels) == 0 {
			t.Fatalf("%s template = %#v, want custom endpoint setup with maintained advanced defaults", provider, template)
		}
	}
	if got := CatalogProviderFor("openai-codex", CodexOAuthBaseURL); got != "openai-codex" {
		t.Fatalf("CatalogProviderFor(openai-codex) = %q, want dedicated subscription catalog", got)
	}
}

func TestOllamaProviderOwnsLocalAndCloudEndpointPolicy(t *testing.T) {
	t.Parallel()

	template, ok := LookupProvider("ollama")
	if !ok {
		t.Fatal("LookupProvider(ollama) = false")
	}
	if template.DefaultEndpointID != "local" || template.NoAuthRequired || len(template.Endpoints) != 2 {
		t.Fatalf("ollama template = %#v, want endpoint-specific auth policy", template)
	}
	local, ok := EndpointForBaseURL(template, "http://localhost:11434/")
	if !ok || local.ID != "local" || !local.NoAuthRequired || local.AuthType != model.AuthNone {
		t.Fatalf("local endpoint = %#v, %v", local, ok)
	}
	cloud, ok := EndpointForBaseURL(template, "https://ollama.com/")
	if !ok || cloud.ID != "cloud" || cloud.NoAuthRequired || cloud.AuthType != model.AuthAPIKey ||
		cloud.CatalogProvider != modelcatalog.OllamaCloudProvider {
		t.Fatalf("cloud endpoint = %#v, %v", cloud, ok)
	}
	if got := CatalogProviderFor("ollama", local.BaseURL); got != "ollama" {
		t.Fatalf("CatalogProviderFor(ollama local) = %q, want ollama", got)
	}
	if got := CatalogProviderFor("ollama", cloud.BaseURL); got != modelcatalog.OllamaCloudProvider {
		t.Fatalf("CatalogProviderFor(ollama cloud) = %q, want %s", got, modelcatalog.OllamaCloudProvider)
	}
}

func TestAssembleConnectBuildsCompleteKnownModelConfig(t *testing.T) {
	t.Parallel()

	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "deepseek",
		Models:   []ModelSelection{{Name: "deepseek-v4-flash"}},
		APIKey:   "secret",
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect() error = %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("AssembleConnect() configs = %#v, want one", configs)
	}
	cfg := configs[0]
	if cfg.ID != "deepseek@default/deepseek/deepseek-v4-flash" || cfg.API != model.APIDeepSeek {
		t.Fatalf("assembled identity = %#v", cfg)
	}
	if cfg.BaseURL != "https://api.deepseek.com/anthropic" || cfg.AuthType != model.AuthAPIKey {
		t.Fatalf("assembled endpoint/auth = %#v", cfg)
	}
	if cfg.ContextWindowTokens != 1048576 || cfg.MaxOutputTok != 32768 {
		t.Fatalf("assembled limits = context:%d max:%d", cfg.ContextWindowTokens, cfg.MaxOutputTok)
	}
	if cfg.Timeout != DefaultProviderRequestTimeoutSeconds*time.Second {
		t.Fatalf("assembled timeout = %s, want %ds", cfg.Timeout, DefaultProviderRequestTimeoutSeconds)
	}
	if cfg.ReasoningMode != modelcatalog.ReasoningModeToggle || cfg.ReasoningEffort != "high" || cfg.DefaultReasoningEffort != "high" {
		t.Fatalf("assembled reasoning = mode:%q effort:%q default:%q", cfg.ReasoningMode, cfg.ReasoningEffort, cfg.DefaultReasoningEffort)
	}
	if !slices.Equal(cfg.ReasoningLevels, []string{"none", "high", "max"}) {
		t.Fatalf("assembled reasoning levels = %#v", cfg.ReasoningLevels)
	}
}

func TestSelectableModelsOnlyReturnsMaintainedMetadataBackedModels(t *testing.T) {
	t.Parallel()

	models, err := SelectableModels(context.Background(), "openai-compatible", "https://proxy.example/v1", nil)
	if err != nil {
		t.Fatalf("SelectableModels(openai-compatible) error = %v", err)
	}
	if selectableModelNamesContain(models, "acme-reasoning-model") {
		t.Fatalf("generic compatible models = %#v, configured unknown model must remain custom", models)
	}

	models, err = SelectableModels(context.Background(), "deepseek", "", nil)
	if err != nil {
		t.Fatalf("SelectableModels(deepseek) error = %v", err)
	}
	if selectableModelNamesContain(models, "private-deepseek") || !selectableModelNamesContain(models, "deepseek-v4-flash") {
		t.Fatalf("known provider models = %#v, want only metadata-backed choices", models)
	}
	for _, item := range models {
		if !item.MetadataComplete {
			t.Fatalf("maintained model = %#v, want complete metadata", item)
		}
	}
}

func TestSelectableOllamaModelsUsesLocalInstalledModelsOnly(t *testing.T) {
	t.Parallel()

	requests := 0
	client := &http.Client{Transport: modelconfigRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Path != "/api/tags" {
			return modelconfigHTTPResponse(r, http.StatusNotFound, ""), nil
		}
		if r.URL.Host != "local.test" {
			return modelconfigHTTPResponse(r, http.StatusNotFound, ""), nil
		}
		return modelconfigHTTPResponse(r, http.StatusOK, `{"models":[{"name":"gemma4:12b-mlx"},{"name":"glm-5.2:cloud"}]}`), nil
	})}

	models, err := selectableOllamaModels(context.Background(), "http://local.test", client)
	if err != nil {
		t.Fatalf("selectableOllamaModels() error = %v", err)
	}
	if requests != 1 || len(models) != 2 {
		t.Fatalf("selectableOllamaModels() requests/models = %d/%#v, want one local request and two models", requests, models)
	}
	assertSelectableModel(t, models, "gemma4:12b-mlx", false)
	assertSelectableModel(t, models, "glm-5.2:cloud", false)
}

func TestSelectableOllamaModelsUsesStaticCloudCatalogWithoutRemoteDiscovery(t *testing.T) {
	t.Parallel()

	requests := 0
	client := &http.Client{Transport: modelconfigRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return modelconfigHTTPResponse(r, http.StatusInternalServerError, "must not call remote catalog"), nil
	})}

	models, err := selectableOllamaModels(context.Background(), "https://ollama.com/", client)
	if err != nil {
		t.Fatalf("selectableOllamaModels() error = %v", err)
	}
	want := []string{"glm-5.2", "kimi-k2.7-code", "minimax-m3", "nemotron-3-super"}
	if requests != 0 || !slices.Equal(selectableModelNames(models), want) {
		t.Fatalf("selectableOllamaModels() requests/models = %d/%#v, want static %#v", requests, models, want)
	}
	for _, item := range models {
		if !item.MetadataComplete {
			t.Fatalf("cloud model = %#v, want complete maintained metadata", item)
		}
		if !strings.Contains(item.Detail, "cloud api") {
			t.Fatalf("cloud model = %#v, want endpoint-owned detail", item)
		}
	}
}

func TestAssembleConnectSupportsOllamaLocalAndCloudEndpoints(t *testing.T) {
	localConfigs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "ollama",
		BaseURL:  "http://localhost:11434",
		Models:   []ModelSelection{{Name: "custom-local"}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(local) error = %v", err)
	}
	if len(localConfigs) != 1 {
		t.Fatalf("AssembleConnect(local) = %#v, want one config", localConfigs)
	}
	local := localConfigs[0]
	if local.EndpointID != "local" || local.AuthType != model.AuthNone || local.Token != "" {
		t.Fatalf("local Ollama config = %#v, want local no-auth endpoint", local)
	}

	customConfigs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "ollama",
		BaseURL:  "http://ollama.lan:11434",
		Models:   []ModelSelection{{Name: "custom-local"}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(custom local) error = %v", err)
	}
	if len(customConfigs) != 1 || customConfigs[0].AuthType != model.AuthNone ||
		!strings.HasPrefix(customConfigs[0].EndpointID, "custom-") {
		t.Fatalf("custom local Ollama config = %#v, want custom no-auth endpoint", customConfigs)
	}

	_, err = AssembleConnect(context.Background(), ConnectRequest{
		Provider: "ollama",
		BaseURL:  "https://ollama.com",
		Models:   []ModelSelection{{Name: "glm-5.2"}},
	}, ConnectOptions{})
	if err == nil || !strings.Contains(err.Error(), "API key is missing") {
		t.Fatalf("AssembleConnect(cloud without key) error = %v, want API-key diagnostic", err)
	}

	cloudConfigs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "ollama",
		BaseURL:  "https://ollama.com",
		APIKey:   "cloud-secret",
		Models:   []ModelSelection{{Name: "glm-5.2"}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(cloud) error = %v", err)
	}
	if len(cloudConfigs) != 1 {
		t.Fatalf("AssembleConnect(cloud) = %#v, want one config", cloudConfigs)
	}
	cloud := cloudConfigs[0]
	if cloud.EndpointID != "cloud" || cloud.AuthType != model.AuthAPIKey || cloud.Token != "cloud-secret" || !cloud.PersistToken {
		t.Fatalf("cloud Ollama endpoint/auth = %#v", cloud)
	}
	if cloud.Model != "glm-5.2" || cloud.ContextWindowTokens != 1000000 || cloud.MaxOutputTok != 32768 {
		t.Fatalf("cloud Ollama model limits = %#v", cloud)
	}
	if cloud.ReasoningMode != modelcatalog.ReasoningModeEffort ||
		!slices.Equal(cloud.ReasoningLevels, []string{"high", "max"}) ||
		cloud.ReasoningEffort != "high" {
		t.Fatalf("cloud Ollama reasoning = %#v", cloud)
	}
}

func TestOllamaCloudCatalogSurvivesConfigPersistenceRoundTrip(t *testing.T) {
	t.Parallel()

	configured := NormalizeConfig(Config{
		Provider:            "ollama",
		Model:               "glm-5.2",
		BaseURL:             "https://ollama.com",
		ContextWindowTokens: 1000000,
		MaxOutputTok:        32768,
	})
	if configured.ReasoningMode != modelcatalog.ReasoningModeEffort {
		t.Fatalf("NormalizeConfig(cloud).ReasoningMode = %q, want effort", configured.ReasoningMode)
	}
	if levels := ReasoningLevelsForConfig(configured); !slices.Equal(levels, []string{"high", "max"}) {
		t.Fatalf("ReasoningLevelsForConfig(cloud) = %#v, want high/max", levels)
	}

	endpoint := SanitizePersistedProviderEndpoint(ProviderEndpointFromConfig(configured))
	modelRecord := SanitizePersistedConfig(configured)
	if modelRecord.ReasoningMode != "" {
		t.Fatalf("SanitizePersistedConfig(cloud).ReasoningMode = %q, want derivable field removed", modelRecord.ReasoningMode)
	}
	rehydrated := MergeConfigProviderEndpoint(modelRecord, endpoint)
	if rehydrated.Provider != "ollama" || rehydrated.BaseURL != "https://ollama.com" ||
		rehydrated.ReasoningMode != modelcatalog.ReasoningModeEffort {
		t.Fatalf("MergeConfigProviderEndpoint(cloud) = %#v, want endpoint-scoped reasoning restored", rehydrated)
	}
}

func TestAssembleConnectUsesModernCompatibleDefaultsAndSupportsExplicitNoReasoning(t *testing.T) {
	t.Parallel()

	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "openai-compatible",
		BaseURL:  "https://models.example.test/v1",
		APIKey:   "secret",
		Models:   []ModelSelection{{Name: "future-reasoning-model"}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(defaults) error = %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("AssembleConnect(defaults) = %#v", configs)
	}
	cfg := configs[0]
	if cfg.ContextWindowTokens != 262144 || cfg.MaxOutputTok != 32768 {
		t.Fatalf("compatible defaults = context:%d output:%d", cfg.ContextWindowTokens, cfg.MaxOutputTok)
	}
	if cfg.ReasoningMode != modelcatalog.ReasoningModeEffort || cfg.ReasoningEffort != "medium" || !slices.Equal(cfg.ReasoningLevels, []string{"none", "minimal", "low", "medium", "high", "xhigh"}) {
		t.Fatalf("compatible reasoning defaults = %#v", cfg)
	}

	configs, err = AssembleConnect(context.Background(), ConnectRequest{
		Provider: "openai-compatible",
		BaseURL:  "https://models.example.test/v1",
		APIKey:   "secret",
		Models: []ModelSelection{{
			Name:            "future-reasoning-model",
			ReasoningLevels: []string{},
		}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(no reasoning) error = %v", err)
	}
	if configs[0].ReasoningMode != modelcatalog.ReasoningModeNone || configs[0].ReasoningEffort != "" || len(configs[0].ReasoningLevels) != 0 {
		t.Fatalf("explicit no-reasoning config = %#v", configs[0])
	}
}

func TestAssembleConnectDerivesPreferredEffortForEveryModel(t *testing.T) {
	t.Parallel()

	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "ollama",
		Models: []ModelSelection{
			{Name: "reasoning-toggle", ReasoningLevels: []string{"none", "high"}},
			{Name: "reasoning-levels", ReasoningLevels: []string{"none", "low", "medium", "high", "xhigh"}},
		},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect() error = %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("AssembleConnect() configs = %#v, want two", configs)
	}
	if got := configs[0].ReasoningEffort; got != "high" {
		t.Fatalf("first ReasoningEffort = %q, want high", got)
	}
	if got := configs[1].ReasoningEffort; got != "medium" {
		t.Fatalf("second ReasoningEffort = %q, want medium", got)
	}
	for _, cfg := range configs {
		if cfg.DefaultReasoningEffort != cfg.ReasoningEffort {
			t.Fatalf("assembled reasoning/default mismatch = %q/%q", cfg.ReasoningEffort, cfg.DefaultReasoningEffort)
		}
	}
}

func TestAssembleConnectResolvesReasoningEffortPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		provider  string
		apiKey    string
		selection ModelSelection
		want      string
	}{
		{
			name:      "preserve resolved catalog default",
			provider:  "anthropic",
			apiKey:    "secret",
			selection: ModelSelection{Name: "claude-opus-4-8"},
			want:      "high",
		},
		{
			name:     "rederive unsupported catalog default after levels override",
			provider: "anthropic",
			apiKey:   "secret",
			selection: ModelSelection{
				Name:            "claude-opus-4-8",
				ReasoningLevels: []string{"none", "low"},
			},
			want: "low",
		},
		{
			name:     "preserve explicit reasoning effort",
			provider: "ollama",
			selection: ModelSelection{
				Name:            "reasoning-model",
				ReasoningEffort: "low",
				ReasoningLevels: []string{"none", "low", "medium", "high"},
			},
			want: "low",
		},
		{
			name:     "preserve explicit none",
			provider: "ollama",
			selection: ModelSelection{
				Name:            "reasoning-model",
				ReasoningEffort: "none",
				ReasoningLevels: []string{"none", "low", "medium", "high"},
			},
			want: "none",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			configs, err := AssembleConnect(context.Background(), ConnectRequest{
				Provider: tt.provider,
				APIKey:   tt.apiKey,
				Models:   []ModelSelection{tt.selection},
			}, ConnectOptions{})
			if err != nil {
				t.Fatalf("AssembleConnect() error = %v", err)
			}
			if len(configs) != 1 {
				t.Fatalf("AssembleConnect() configs = %#v, want one", configs)
			}
			if configs[0].ReasoningEffort != tt.want || configs[0].DefaultReasoningEffort != tt.want {
				t.Fatalf(
					"reasoning/default = %q/%q, want %s/%s",
					configs[0].ReasoningEffort,
					configs[0].DefaultReasoningEffort,
					tt.want,
					tt.want,
				)
			}
		})
	}
}

func TestAssembleConnectAuthenticatesOnceForMultipleModels(t *testing.T) {
	t.Parallel()

	authCalls := 0
	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "codefree",
		Models: []ModelSelection{
			{Name: "GLM-4.7"},
			{Name: "GLM-5.1"},
		},
	}, ConnectOptions{Authenticate: func(_ context.Context, req AuthenticateRequest) (AuthenticateResult, error) {
		authCalls++
		if req.Purpose != AuthPurposeConnect {
			t.Fatalf("AuthenticateRequest.Purpose = %q", req.Purpose)
		}
		return AuthenticateResult{}, nil
	}})
	if err != nil {
		t.Fatalf("AssembleConnect(multiple) error = %v", err)
	}
	if authCalls != 1 || len(configs) != 2 || configs[0].Model != "GLM-4.7" || configs[1].Model != "GLM-5.1" {
		t.Fatalf("batch assembly calls/configs = %d/%#v", authCalls, configs)
	}
}

func TestSelectableModelsAuthenticatesCodeFreeBeforeListing(t *testing.T) {
	t.Parallel()

	called := false
	models, err := SelectableModels(context.Background(), "codefree", "", func(_ context.Context, req AuthenticateRequest) (AuthenticateResult, error) {
		called = true
		if req.Provider != "codefree" || req.Purpose != AuthPurposeModelSelection || req.BaseURL != "https://www.srdcloud.cn" {
			t.Fatalf("AuthenticateRequest = %#v", req)
		}
		return AuthenticateResult{}, nil
	})
	if err != nil {
		t.Fatalf("SelectableModels(codefree) error = %v", err)
	}
	if !called {
		t.Fatal("codefree selection did not authenticate")
	}
	if len(models) == 0 {
		t.Fatal("codefree selection returned no maintained models")
	}
}

func TestAssembleConnectBuildsManagedCodexOAuthProfile(t *testing.T) {
	t.Parallel()

	authCalls := 0
	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "codex",
		Models:   []ModelSelection{{Name: "gpt-5.5"}},
	}, ConnectOptions{Authenticate: func(_ context.Context, req AuthenticateRequest) (AuthenticateResult, error) {
		authCalls++
		if req.Provider != "openai-codex" || req.BaseURL != CodexOAuthBaseURL || req.Purpose != AuthPurposeConnect {
			t.Fatalf("AuthenticateRequest = %#v", req)
		}
		return AuthenticateResult{}, nil
	}})
	if err != nil {
		t.Fatalf("AssembleConnect(codex) error = %v", err)
	}
	if authCalls != 1 || len(configs) != 1 {
		t.Fatalf("codex auth/config count = %d/%d", authCalls, len(configs))
	}
	cfg := configs[0]
	if cfg.Provider != "openai-codex" || cfg.API != model.APIOpenAICodex || cfg.AuthType != model.AuthOAuthToken {
		t.Fatalf("codex provider config = %#v", cfg)
	}
	if cfg.CredentialRef != CodexOAuthCredentialRef || cfg.Token != "" || cfg.PersistToken {
		t.Fatalf("codex credentials leaked into model config = %#v", cfg)
	}
	if cfg.BaseURL != CodexOAuthBaseURL || cfg.ProviderEndpointID != "openai-codex@default" {
		t.Fatalf("codex endpoint identity = %#v", cfg)
	}
}

func TestAssembleConnectRejectsCustomCodexOAuthEndpoint(t *testing.T) {
	t.Parallel()

	_, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "codex",
		BaseURL:  "https://proxy.example.test/backend-api/codex",
		Models:   []ModelSelection{{Name: "gpt-5.5"}},
	}, ConnectOptions{Authenticate: func(context.Context, AuthenticateRequest) (AuthenticateResult, error) {
		return AuthenticateResult{}, nil
	}})
	if err == nil || !strings.Contains(err.Error(), "requires the maintained endpoint") {
		t.Fatalf("AssembleConnect(custom codex endpoint) error = %v", err)
	}
}

func TestSelectableModelsAuthenticatesAndFiltersCodexCatalog(t *testing.T) {
	t.Parallel()

	called := false
	models, err := SelectableModels(context.Background(), "codex", "", func(_ context.Context, req AuthenticateRequest) (AuthenticateResult, error) {
		called = true
		if req.Provider != "openai-codex" || req.Purpose != AuthPurposeModelSelection {
			t.Fatalf("AuthenticateRequest = %#v", req)
		}
		return AuthenticateResult{
			SelectableModels: []string{
				"gpt-5.6-sol",
				"gpt-5.6-terra",
				"gpt-5.6-luna",
				"gpt-5.5",
				"gpt-5.4",
				"gpt-5.4-mini",
				"gpt-5.3-codex-spark",
				"gpt-5.7-unknown",
			},
			ModelCatalogAuthoritative: true,
		}, nil
	})
	if err != nil {
		t.Fatalf("SelectableModels(codex) error = %v", err)
	}
	if !called || !selectableModelNamesContain(models, "gpt-5.5") || !selectableModelNamesContain(models, "gpt-5.6-sol") || !selectableModelNamesContain(models, "gpt-5.4") || !selectableModelNamesContain(models, "gpt-5.4-mini") || !selectableModelNamesContain(models, "gpt-5.3-codex-spark") {
		t.Fatalf("codex selectable models = %#v", models)
	}
	wantOrder := []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"}
	if got := selectableModelNames(models); !slices.Equal(got, wantOrder) {
		t.Fatalf("codex selectable model order = %#v, want %#v", got, wantOrder)
	}
	if selectableModelNamesContain(models, "gpt-5.2") || selectableModelNamesContain(models, "gpt-5.5-pro") || selectableModelNamesContain(models, "gpt-5.6") || selectableModelNamesContain(models, "gpt-5.7-unknown") || selectableModelNamesContain(models, "gpt-5.5-instant") {
		t.Fatalf("codex selectable models include disallowed entries = %#v", models)
	}
	for _, item := range models {
		if !item.MetadataComplete {
			t.Fatalf("codex selectable model requires unnecessary advanced setup = %#v", item)
		}
	}
	if isCodexOAuthModel("gpt-5.7-pro") || isCodexOAuthModel("gpt-5.7-sol") || !isCodexOAuthModel("gpt-5.6-sol") || !isCodexOAuthModel("gpt-5.3-codex-spark") {
		t.Fatalf("codex model allowlist accepted an unknown model or rejected a maintained one")
	}
}

func TestSelectableModelsUsesCurrentBundledCodexFallback(t *testing.T) {
	t.Parallel()

	models, err := SelectableModels(context.Background(), "codex", "", func(context.Context, AuthenticateRequest) (AuthenticateResult, error) {
		return AuthenticateResult{}, nil
	})
	if err != nil {
		t.Fatalf("SelectableModels(codex fallback) error = %v", err)
	}
	for _, name := range []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex-spark"} {
		if !selectableModelNamesContain(models, name) {
			t.Fatalf("codex fallback models = %#v, missing %q", models, name)
		}
	}
	if selectableModelNamesContain(models, "gpt-5.2") {
		t.Fatalf("codex fallback exposes deprecated gpt-5.2 = %#v", models)
	}
}

func TestResolveCodexOAuthModelDefaultsUseCodexCatalogMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		context        int
		defaultEffort  string
		reasoningLevel []string
	}{
		{name: "gpt-5.6-sol", context: codexOAuthEffectiveContextWindowTokens, defaultEffort: "low", reasoningLevel: []string{"low", "medium", "high", "xhigh", "max", "ultra"}},
		{name: "gpt-5.6-luna", context: codexOAuthEffectiveContextWindowTokens, defaultEffort: "medium", reasoningLevel: []string{"low", "medium", "high", "xhigh", "max"}},
		{name: "gpt-5.5", context: 272000, defaultEffort: "medium", reasoningLevel: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4", context: 272000, defaultEffort: "medium", reasoningLevel: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.4-mini", context: 272000, defaultEffort: "medium", reasoningLevel: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.3-codex-spark", context: 128000, defaultEffort: "high", reasoningLevel: []string{"low", "medium", "high", "xhigh"}},
		{name: "gpt-5.2", context: 272000, defaultEffort: "medium", reasoningLevel: []string{"low", "medium", "high", "xhigh"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defaults, err := ResolveModelDefaults("codex", tt.name)
			if err != nil {
				t.Fatalf("ResolveModelDefaults(codex, %q) error = %v", tt.name, err)
			}
			if defaults.ContextWindowTokens != tt.context || defaults.MaxOutputTokens != codexOAuthDefaultMaxOutputTokens || defaults.DefaultReasoningEffort != tt.defaultEffort || defaults.ReasoningMode != modelcatalog.ReasoningModeEffort || !slices.Equal(defaults.ReasoningLevels, tt.reasoningLevel) {
				t.Fatalf("ResolveModelDefaults(codex, %q) = %#v", tt.name, defaults)
			}
			if defaults.ImageInput == nil || !*defaults.ImageInput {
				t.Fatalf("ResolveModelDefaults(codex, %q).ImageInput = %v, want maintained true", tt.name, defaults.ImageInput)
			}
			if slices.Contains(defaults.ReasoningLevels, "none") {
				t.Fatalf("ResolveModelDefaults(codex, %q) advertises unsupported none effort", tt.name)
			}
		})
	}
}

func TestSanitizePersistedCodexProviderEndpointKeepsReferenceOnly(t *testing.T) {
	t.Parallel()

	endpoint := SanitizePersistedProviderEndpoint(ProviderEndpointConfig{
		Provider:      "openai-codex",
		BaseURL:       CodexOAuthBaseURL,
		CredentialRef: CodexOAuthCredentialRef,
		Token:         "access-secret",
		PersistToken:  true,
	})
	if endpoint.CredentialRef != CodexOAuthCredentialRef || endpoint.Token != "" || endpoint.PersistToken {
		t.Fatalf("SanitizePersistedProviderEndpoint(codex) = %#v", endpoint)
	}
}

func selectableModelNamesContain(models []SelectableModel, name string) bool {
	for _, item := range models {
		if item.Name == name {
			return true
		}
	}
	return false
}

func selectableModelNames(models []SelectableModel) []string {
	names := make([]string, 0, len(models))
	for _, item := range models {
		names = append(names, item.Name)
	}
	return names
}

func assertSelectableModel(t *testing.T, models []SelectableModel, name string, metadataComplete bool) {
	t.Helper()
	for _, model := range models {
		if model.Name == name {
			if model.MetadataComplete != metadataComplete {
				t.Fatalf("selectable model %q metadata complete = %v, want %v", name, model.MetadataComplete, metadataComplete)
			}
			return
		}
	}
	t.Fatalf("selectable models = %#v, missing %q", models, name)
}

type modelconfigRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelconfigRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func modelconfigHTTPResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestBuildModelConstructsSDKModelFromControlConfig(t *testing.T) {
	t.Parallel()

	resolved, err := BuildModel(Config{
		Provider:            "ollama",
		Model:               "llama3.2",
		BaseURL:             "http://localhost:11434",
		ContextWindowTokens: 131072,
		MaxOutputTok:        16384,
	}, 128000, 0)
	if err != nil {
		t.Fatalf("BuildModel() error = %v", err)
	}
	if resolved.Model == nil || resolved.Model.Name() != "llama3.2" {
		t.Fatalf("BuildModel() model = %#v", resolved.Model)
	}
	type contextWindowModel interface {
		ContextWindowTokens() int
	}
	withContext, ok := resolved.Model.(contextWindowModel)
	if !ok || withContext.ContextWindowTokens() != 131072 {
		t.Fatalf("built context model = %#v, %v", withContext, ok)
	}
}

func TestBuildModelPropagatesMaintainedImageInputCapability(t *testing.T) {
	t.Parallel()

	tests := []Config{
		{
			Provider: "xai",
			API:      model.APIOpenAI,
			Model:    "grok-4.5",
			BaseURL:  "https://api.x.ai/v1",
			Token:    "test-token",
		},
		{
			Provider:   "openai-codex",
			API:        model.APIOpenAICodex,
			Model:      "gpt-5.6-sol",
			BaseURL:    CodexOAuthBaseURL,
			Token:      "test-token",
			AuthType:   model.AuthOAuthToken,
			HTTPClient: &http.Client{},
		},
	}
	for _, cfg := range tests {
		resolved, err := BuildModel(cfg, 0, 0)
		if err != nil {
			t.Fatalf("BuildModel(%s/%s) error = %v", cfg.Provider, cfg.Model, err)
		}
		capabilities, declared := model.CapabilitiesOf(resolved.Model)
		if !declared || !capabilities.ImageInput {
			t.Fatalf("built %s/%s capabilities = %+v, declared=%v, want image input", cfg.Provider, cfg.Model, capabilities, declared)
		}
	}
}

func TestCompatibleEndpointsDoNotInheritVendorImageCapabilities(t *testing.T) {
	t.Parallel()

	if ModelSupportsImages(Config{
		Provider: "openai-compatible",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://proxy.example/v1",
	}) {
		t.Fatal("generic OpenAI-compatible endpoint inherited OpenAI image capability")
	}
	if ModelSupportsImages(Config{
		Provider: "anthropic-compatible",
		Model:    "claude-sonnet-4",
		BaseURL:  "https://proxy.example/anthropic",
	}) {
		t.Fatal("generic Anthropic-compatible endpoint inherited Anthropic image capability")
	}
}

func TestUnknownModelUsesExplicitImageInputCapability(t *testing.T) {
	t.Parallel()

	enabled := true
	if !ModelSupportsImages(Config{
		Provider:   "openai-compatible",
		Model:      "local-vision",
		BaseURL:    "http://localhost:8080/v1",
		ImageInput: &enabled,
	}) {
		t.Fatal("explicit image-input override was ignored")
	}
	disabled := false
	if ModelSupportsImages(Config{
		Provider:   "openai-compatible",
		Model:      "local-text",
		BaseURL:    "http://localhost:8080/v1",
		ImageInput: &disabled,
	}) {
		t.Fatal("explicit text-only capability was ignored")
	}
}

func TestMaintainedImageCapabilityOverridesConfig(t *testing.T) {
	t.Parallel()

	disabled := false
	if !ModelSupportsImages(Config{
		Provider:   "openai",
		Model:      "gpt-4o",
		BaseURL:    "https://api.openai.com/v1",
		ImageInput: &disabled,
	}) {
		t.Fatal("explicit custom capability overrode maintained OpenAI metadata")
	}
}

func TestResolveModelDefaultsCarriesSuggestedImageCapability(t *testing.T) {
	t.Parallel()

	defaults, err := ResolveModelDefaults("codefree", "Qwen3.5-122B-A10B")
	if err != nil {
		t.Fatalf("ResolveModelDefaults(codefree image model) error = %v", err)
	}
	if defaults.ImageInput == nil || !*defaults.ImageInput {
		t.Fatalf("codefree defaults image input = %v, want maintained true", defaults.ImageInput)
	}
}

func TestAssembleConnectPersistsImageInputOnlyForUnknownModels(t *testing.T) {
	t.Parallel()

	enabled := true
	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "openai-compatible",
		BaseURL:  "https://models.acme.example/v1",
		APIKey:   "secret",
		Models: []ModelSelection{{
			Name:       "acme-vision",
			ImageInput: &enabled,
		}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(custom image model) error = %v", err)
	}
	if len(configs) != 1 || configs[0].ImageInput == nil || !*configs[0].ImageInput {
		t.Fatalf("custom image config = %#v, want explicit image input", configs)
	}
	resolved, err := BuildModel(configs[0], 0, 0)
	if err != nil {
		t.Fatalf("BuildModel(custom image model) error = %v", err)
	}
	capabilities, declared := model.CapabilitiesOf(resolved.Model)
	if !declared || !capabilities.ImageInput {
		t.Fatalf("custom image model capabilities = %+v, declared=%v", capabilities, declared)
	}

	disabled := false
	configs, err = AssembleConnect(context.Background(), ConnectRequest{
		Provider: "openai",
		APIKey:   "secret",
		Models: []ModelSelection{{
			Name:       "gpt-4o",
			ImageInput: &disabled,
		}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(maintained image model) error = %v", err)
	}
	if len(configs) != 1 || configs[0].ImageInput != nil || !ModelSupportsImages(configs[0]) {
		t.Fatalf("maintained image config = %#v, want directory-owned capability without persisted override", configs)
	}
}

func TestApplyConfigProviderEndpointFieldsRetainsOmittedCredential(t *testing.T) {
	t.Parallel()

	current := NormalizeProviderEndpoint(ProviderEndpointConfig{
		Provider:      "ollama",
		CredentialRef: "apikey:ollama/default",
	})
	got := ApplyConfigProviderEndpointFields(current, Config{
		Provider:                "ollama",
		Model:                   "qwen3",
		BaseURL:                 "http://localhost:11434",
		StreamFirstEventTimeout: 5 * time.Minute,
	})
	if got.ID != current.ID || got.CredentialRef != current.CredentialRef {
		t.Fatalf("ApplyConfigProviderEndpointFields() identity/credential = %#v, want ID %q and credential %q", got, current.ID, current.CredentialRef)
	}
	if got.BaseURL != "http://localhost:11434" || got.StreamFirstEventTimeout != 5*time.Minute {
		t.Fatalf("ApplyConfigProviderEndpointFields() settings = %#v", got)
	}
}
