package modelconfig

import (
	"context"
	"slices"
	"testing"

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
	}, ConnectOptions{Authenticate: func(_ context.Context, req AuthenticateRequest) error {
		authCalls++
		if req.Purpose != AuthPurposeConnect {
			t.Fatalf("AuthenticateRequest.Purpose = %q", req.Purpose)
		}
		return nil
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
	models, err := SelectableModels(context.Background(), "codefree", "", func(_ context.Context, req AuthenticateRequest) error {
		called = true
		if req.Provider != "codefree" || req.Purpose != AuthPurposeModelSelection || req.BaseURL != "https://www.srdcloud.cn" {
			t.Fatalf("AuthenticateRequest = %#v", req)
		}
		return nil
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

func selectableModelNamesContain(models []SelectableModel, name string) bool {
	for _, item := range models {
		if item.Name == name {
			return true
		}
	}
	return false
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
