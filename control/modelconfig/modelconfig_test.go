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

func TestAssembleConnectBuildsCompleteKnownModelConfig(t *testing.T) {
	t.Parallel()

	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "deepseek",
		Models:   []ModelSelection{{Name: "deepseek-flash"}},
		APIKey:   "secret",
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect() error = %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("AssembleConnect() configs = %#v, want one", configs)
	}
	cfg := configs[0]
	if cfg.ID != "deepseek@default/deepseek/deepseek-flash" || cfg.API != model.APIDeepSeek {
		t.Fatalf("assembled identity = %#v", cfg)
	}
	if cfg.BaseURL != "https://api.deepseek.com/anthropic" || cfg.AuthType != model.AuthAPIKey {
		t.Fatalf("assembled endpoint/auth = %#v", cfg)
	}
	if cfg.Timeout != DefaultProviderRequestTimeoutSeconds*time.Second {
		t.Fatalf("assembled timeout = %s, want %ds", cfg.Timeout, DefaultProviderRequestTimeoutSeconds)
	}
	defaults, err := ResolveModelDefaultsForEndpoint("deepseek", cfg.BaseURL, cfg.Model)
	if err != nil {
		t.Fatalf("ResolveModelDefaultsForEndpoint(deepseek) error = %v", err)
	}
	if cfg.ContextWindowTokens != defaults.ContextWindowTokens || cfg.MaxOutputTok != defaults.MaxOutputTokens ||
		cfg.ReasoningMode != defaults.ReasoningMode || cfg.ReasoningEffort != defaults.DefaultReasoningEffort ||
		cfg.DefaultReasoningEffort != defaults.DefaultReasoningEffort || !slices.Equal(cfg.ReasoningLevels, defaults.ReasoningLevels) {
		t.Fatalf("assembled maintained metadata = %#v, want %#v", cfg, defaults)
	}
}

func TestSelectableModelsOnlyReturnsMetadataBackedModels(t *testing.T) {
	t.Parallel()

	models, err := SelectableModels(context.Background(), ConnectRequest{Provider: "custom-chat", BaseURL: "https://proxy.example/v1"})
	if err != nil {
		t.Fatalf("SelectableModels(custom-chat) error = %v", err)
	}
	if selectableModelNamesContain(models, "acme-reasoning-model") {
		t.Fatalf("generic compatible models = %#v, configured unknown model must remain custom", models)
	}

	models, err = SelectableModels(context.Background(), ConnectRequest{Provider: "deepseek", BaseURL: ""})
	if err != nil {
		t.Fatalf("SelectableModels(deepseek) error = %v", err)
	}
	if got, want := selectableModelNames(models), modelcatalog.ListRecommendedModels("deepseek"); !slices.Equal(got, want) {
		t.Fatalf("known provider models = %#v, want maintained recommendations %#v", got, want)
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

func TestAssembleConnectUsesModernCompatibleDefaultsAndSupportsExplicitNoReasoning(t *testing.T) {
	t.Parallel()

	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "custom-chat",
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
	if cfg.ContextWindowTokens != 128000 || cfg.MaxOutputTok != 8192 {
		t.Fatalf("compatible defaults = context:%d output:%d", cfg.ContextWindowTokens, cfg.MaxOutputTok)
	}
	if cfg.ReasoningMode != modelcatalog.ReasoningModeNone || cfg.ReasoningEffort != "" || len(cfg.ReasoningLevels) != 0 {
		t.Fatalf("compatible reasoning defaults = %#v", cfg)
	}

	configs, err = AssembleConnect(context.Background(), ConnectRequest{
		Provider: "custom-chat",
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
			provider:  "deepseek",
			apiKey:    "secret",
			selection: ModelSelection{Name: "deepseek-flash"},
			want:      "high",
		},
		{
			name:     "rederive unsupported catalog default after levels override",
			provider: "deepseek",
			apiKey:   "secret",
			selection: ModelSelection{
				Name:            "deepseek-flash",
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
		Provider: "grok",
		Models: []ModelSelection{
			{Name: "grok-4.7"},
			{Name: "grok-account-preview"},
		},
	}, ConnectOptions{Authenticate: func(_ context.Context, req AuthenticateRequest) error {
		authCalls++
		return nil
	}})
	if err != nil {
		t.Fatalf("AssembleConnect(multiple) error = %v", err)
	}
	if authCalls != 1 || len(configs) != 2 || configs[0].Model != "grok-4.7" || configs[1].Model != "grok-account-preview" {
		t.Fatalf("batch assembly calls/configs = %d/%#v", authCalls, configs)
	}
}

func TestAssembleConnectBuildsManagedCodexOAuthProfile(t *testing.T) {
	t.Parallel()

	authCalls := 0
	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "codex",
		Models:   []ModelSelection{{Name: "gpt-6-sol"}},
	}, ConnectOptions{Authenticate: func(_ context.Context, req AuthenticateRequest) error {
		authCalls++
		if req.Provider != "openai-codex" || req.BaseURL != CodexOAuthBaseURL {
			t.Fatalf("AuthenticateRequest = %#v", req)
		}
		return nil
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
		Models:   []ModelSelection{{Name: "gpt-6-sol"}},
	}, ConnectOptions{Authenticate: func(context.Context, AuthenticateRequest) error {
		return nil
	}})
	if err == nil || !strings.Contains(err.Error(), "requires the maintained endpoint") {
		t.Fatalf("AssembleConnect(custom codex endpoint) error = %v", err)
	}
}

func TestSelectableModelsUsesCurrentBundledCodexCatalog(t *testing.T) {
	t.Parallel()

	models, err := SelectableModels(context.Background(), ConnectRequest{Provider: "codex", BaseURL: ""})
	if err != nil {
		t.Fatalf("SelectableModels(codex) error = %v", err)
	}
	want := []string{"gpt-6.1-sol", "gpt-6-astra", "gpt-6-sol", "gpt-6-luna"}
	if got := selectableModelNames(models); !slices.Equal(got, want) {
		t.Fatalf("codex selectable models = %#v, want maintained fallback %#v", got, want)
	}
	for _, item := range models {
		if !item.MetadataComplete || !item.ImageInputKnown {
			t.Fatalf("codex selectable model lacks maintained metadata = %#v", item)
		}
	}
}

func TestResolveCodexOAuthModelDefaultsUseSubscriptionCatalog(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"gpt-6.1-sol", "gpt-6-sol", "gpt-6-luna"} {
		t.Run(name, func(t *testing.T) {
			managed, err := ResolveModelDefaultsForEndpoint("codex", "", name)
			if err != nil {
				t.Fatal(err)
			}
			levels := []string{"low", "medium", "high", "xhigh", "max"}
			if name == "gpt-6-sol" || name == "gpt-6.1-sol" {
				levels = append(levels, "ultra")
			}
			defaultEffort := "medium"
			if name == "gpt-6.1-sol" {
				defaultEffort = "low"
			}
			contextWindow := 258400
			if name == "gpt-6.1-sol" {
				contextWindow = 475000
			}
			if managed.ContextWindowTokens != contextWindow || managed.MaxOutputTokens != 32768 ||
				managed.ReasoningMode != modelcatalog.ReasoningModeEffort || managed.DefaultReasoningEffort != defaultEffort ||
				!slices.Equal(managed.ReasoningLevels, levels) {
				t.Fatalf("Codex subscription defaults = %#v", managed)
			}
			if managed.ImageInput == nil || !*managed.ImageInput {
				t.Fatalf("Codex image input = %v, want maintained true", managed.ImageInput)
			}
			if _, err := ResolveModelDefaultsForEndpoint("custom-responses", "", name); err != nil {
				t.Fatal(err)
			}

		})
	}
}

func TestSavedCodexModelPreservesExplicitSettings(t *testing.T) {
	t.Parallel()
	cfg := RestoreSavedCapabilities(Config{
		Provider: "openai-codex", Model: "gpt-5.6-sol", ContextWindowTokens: 258400,
		MaxOutputTok: 32768, ReasoningEffort: "low", ReasoningLevels: []string{"low", "high"},
	})
	if cfg.ContextWindowTokens != 258400 || cfg.MaxOutputTok != 32768 || cfg.DefaultReasoningEffort != "low" || cfg.ReasoningMode != modelcatalog.ReasoningModeEffort || !slices.Equal(cfg.ReasoningLevels, []string{"low", "high"}) || cfg.ImageInput == nil || !*cfg.ImageInput {
		t.Fatalf("saved config = %#v", cfg)
	}
	if slices.Contains(modelcatalog.ListRecommendedModels("openai-codex"), cfg.Model) {
		t.Fatal("saved model appeared in the native directory")
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
			Provider: "deepseek",
			API:      model.APIDeepSeek,
			Model:    "deepseek-flash",
			BaseURL:  "https://api.deepseek.com/anthropic",
			Token:    "test-token",
		},
		{
			Provider: "xai",
			API:      model.APIOpenAI,
			Model:    "grok-4.7",
			BaseURL:  "https://api.x.ai/v1",
			Token:    "test-token",
		},
		{
			Provider: "xai",
			API:      model.APIOpenAI,
			Model:    "grok-4.7",
			BaseURL:  "https://api.x.ai/v1",
			Token:    "test-token",
		},
		{
			Provider:   "openai-codex",
			API:        model.APIOpenAICodex,
			Model:      "gpt-6-sol",
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
		Provider: "custom-chat",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://proxy.example/v1",
	}) {
		t.Fatal("generic OpenAI-compatible endpoint inherited OpenAI image capability")
	}
	if ModelSupportsImages(Config{
		Provider: "openai-responses-compatible",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://proxy.example/v1",
	}) {
		t.Fatal("generic OpenAI Responses-compatible endpoint inherited OpenAI image capability")
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

func TestAcceptedImageCapabilityOverridesPreset(t *testing.T) {
	t.Parallel()

	disabled := false
	if ModelSupportsImages(Config{
		Provider:   "openai-codex",
		Model:      "gpt-6-sol",
		BaseURL:    "https://api.openai.com/v1",
		ImageInput: &disabled,
	}) {
		t.Fatal("accepted capability was overwritten by directory metadata")
	}
}

func TestResolveModelDefaultsCarriesMaintainedImageCapability(t *testing.T) {
	t.Parallel()

	defaults, err := ResolveModelDefaultsForEndpoint("xai", "", "grok-4.7")
	if err != nil {
		t.Fatalf("ResolveModelDefaultsForEndpoint(maintained image model) error = %v", err)
	}
	if defaults.ImageInput == nil || !*defaults.ImageInput {
		t.Fatalf("defaults image input = %v, want maintained true", defaults.ImageInput)
	}
}

func TestAssembleConnectPinsNativeAndCustomImageCapabilities(t *testing.T) {
	t.Parallel()

	enabled := true
	configs, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "custom-chat",
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

	configs, err = AssembleConnect(context.Background(), ConnectRequest{Provider: "deepseek", APIKey: "secret", Models: []ModelSelection{{Name: "deepseek-flash"}}}, ConnectOptions{})
	if err != nil || len(configs) != 1 || configs[0].ImageInput == nil || !*configs[0].ImageInput {
		t.Fatalf("native capability not pinned: %#v %v", configs, err)
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

func TestSpeedModesForConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      Config
		want     bool
		wantHint string
	}{
		{name: "openai Sol 6.1 uses responses", cfg: Config{Provider: "openai", API: model.APIOpenAI, Model: "gpt-6.1-sol"}, want: true, wantHint: "Faster responses, increased usage"},
		{name: "codex Sol 6.1 uses responses", cfg: Config{Provider: "openai-codex", API: model.APIOpenAICodex, Model: "gpt-6.1-sol"}, want: true, wantHint: "Faster responses, increased usage"},
		{name: "openai gpt uses responses", cfg: Config{Provider: "openai", API: model.APIOpenAI, Model: "gpt-6-sol"}, want: true, wantHint: "1.5x faster, more usage"},
		{name: "openai-codex gpt uses responses", cfg: Config{Provider: "openai-codex", API: model.APIOpenAICodex, Model: "gpt-6-sol"}, want: true, wantHint: "1.5x faster, more usage"},
		{name: "openai luna uses responses", cfg: Config{Provider: "openai", API: model.APIOpenAI, Model: "gpt-6-luna"}, want: true, wantHint: "1.5x faster, more usage"},
		{name: "codex luna uses responses", cfg: Config{Provider: "openai-codex", API: model.APIOpenAICodex, Model: "gpt-6-luna"}, want: true, wantHint: "1.5x faster, more usage"},
		{name: "astra has model-specific hint", cfg: Config{Provider: "openai-codex", API: model.APIOpenAICodex, Model: "gpt-6-astra"}, want: true, wantHint: "2x faster, more usage"},
		{name: "normalizes official openai gpt identity", cfg: Config{Provider: " OpenAI ", Model: "GPT-6-SOL", BaseURL: "https://api.openai.com/v1/"}, want: true, wantHint: "1.5x faster, more usage"},
		{name: "rejects unknown gpt model", cfg: Config{Provider: "openai", API: model.APIOpenAI, Model: "gpt-unknown"}},
		{name: "rejects custom openai endpoint", cfg: Config{Provider: "openai", API: model.APIOpenAI, Model: "gpt-6-sol", BaseURL: "https://proxy.example/v1"}},
		{name: "rejects custom openai-codex endpoint", cfg: Config{Provider: "openai-codex", API: model.APIOpenAICodex, Model: "gpt-6-sol", BaseURL: "https://proxy.example/codex"}},
		{name: "rejects non-gpt openai model", cfg: Config{Provider: "openai", API: model.APIOpenAI, Model: "o3"}},
		{name: "rejects openai-compatible gpt", cfg: Config{Provider: "custom-chat", API: model.APIOpenAICompatible, Model: "gpt-6-sol"}},
		{name: "rejects openai-responses-compatible gpt", cfg: Config{Provider: "openai-responses-compatible", API: model.APIOpenAIResponses, Model: "gpt-6-sol"}},
		{name: "rejects openai gpt on compatible api", cfg: Config{Provider: "openai", API: model.APIOpenAICompatible, Model: "gpt-6-sol"}},
		{name: "rejects openai-codex gpt on chat api", cfg: Config{Provider: "openai-codex", API: model.APIOpenAI, Model: "gpt-6-sol"}},
		{name: "rejects xai grok", cfg: Config{Provider: "xai", Model: "grok-4.7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := SupportsSpeedMode(tt.cfg, " FAST "); got != tt.want {
				t.Fatalf("SupportsSpeedMode(%#v, fast) = %v, want %v", tt.cfg, got, tt.want)
			}
			modes := SpeedModesForConfig(tt.cfg)
			if !tt.want {
				if len(modes) != 0 {
					t.Fatalf("SpeedModesForConfig(%#v) = %#v, want no non-default speed modes", tt.cfg, modes)
				}
				return
			}
			if len(modes) != 1 || modes[0].Level != "fast" || modes[0].Description != tt.wantHint {
				t.Fatalf("SpeedModesForConfig(%#v) = %#v; want Fast with %q", tt.cfg, modes, tt.wantHint)
			}
		})
	}
}
