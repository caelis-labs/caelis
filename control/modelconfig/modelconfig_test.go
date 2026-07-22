package modelconfig

import (
	"context"
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
	if got := DefaultTokenEnv(template.Provider, template.DefaultBaseURL); got != "MIMO_TOKEN_PLAN_API_KEY" {
		t.Fatalf("DefaultTokenEnv() = %q, want MIMO_TOKEN_PLAN_API_KEY", got)
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
	if cfg.CredentialRef != CodexOAuthCredentialRef || cfg.Token != "" || cfg.TokenEnv != "" || cfg.PersistToken {
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
		TokenEnv:      "SHOULD_NOT_SURVIVE",
		PersistToken:  true,
	})
	if endpoint.CredentialRef != CodexOAuthCredentialRef || endpoint.Token != "" || endpoint.TokenEnv != "" || endpoint.PersistToken {
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
