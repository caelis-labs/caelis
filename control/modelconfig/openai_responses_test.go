package modelconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

func TestOpenAIProviderTemplatesSeparateResponsesAndChatCompletions(t *testing.T) {
	t.Parallel()

	official, ok := LookupProvider("openai")
	if !ok {
		t.Fatal("LookupProvider(openai) = false")
	}
	if official.Label != "openai" || official.Provider != "openai" || official.API != model.APIOpenAI {
		t.Fatalf("official openai template = %#v, want APIOpenAI identity", official)
	}
	if official.PromptForBaseURL || official.UseModelDirectory {
		t.Fatalf("official openai template = %#v, want hosted OpenAI without custom catalog inheritance", official)
	}
	if official.Description != "OpenAI-hosted models through the Responses API" {
		t.Fatalf("official openai description = %q, want Responses", official.Description)
	}

	chatByProvider, ok := LookupProvider("openai-compatible")
	if !ok {
		t.Fatal("LookupProvider(openai-compatible) = false")
	}
	chatByLabel, ok := LookupProvider("openai-chat-compatible")
	if !ok {
		t.Fatal("LookupProvider(openai-chat-compatible) = false")
	}
	if chatByProvider.Provider != "openai-compatible" || chatByLabel.Provider != "openai-compatible" {
		t.Fatalf("chat compatible durable provider = %q/%q, want openai-compatible", chatByProvider.Provider, chatByLabel.Provider)
	}
	if chatByProvider.Label != "openai-chat-compatible" || chatByLabel.Label != "openai-chat-compatible" {
		t.Fatalf("chat compatible label = %q/%q, want openai-chat-compatible", chatByProvider.Label, chatByLabel.Label)
	}
	if chatByProvider.API != model.APIOpenAICompatible || chatByLabel.API != model.APIOpenAICompatible {
		t.Fatalf("chat compatible API = %q/%q, want APIOpenAICompatible", chatByProvider.API, chatByLabel.API)
	}
	if !strings.Contains(chatByProvider.Description, "Chat Completions") {
		t.Fatalf("chat compatible description = %q, want Chat Completions", chatByProvider.Description)
	}

	responses, ok := LookupProvider("openai-responses-compatible")
	if !ok {
		t.Fatal("LookupProvider(openai-responses-compatible) = false")
	}
	if responses.Label != "openai-responses-compatible" || responses.Provider != "openai-responses-compatible" {
		t.Fatalf("responses compatible identity = %#v", responses)
	}
	if responses.API != model.APIOpenAIResponses || responses.AuthType != model.AuthAPIKey || !responses.PromptForBaseURL {
		t.Fatalf("responses compatible protocol/auth = %#v", responses)
	}
	if responses.UseModelDirectory {
		t.Fatalf("responses compatible template = %#v, want no vendor catalog inheritance", responses)
	}
	assertCompatibleConservativeDefaults(t, responses)
	assertCompatibleConservativeDefaults(t, chatByProvider)

	deepseek, ok := LookupProvider("deepseek")
	if !ok || deepseek.API != model.APIDeepSeek || deepseek.DefaultBaseURL != "https://api.deepseek.com/anthropic" {
		t.Fatalf("deepseek template = %#v, want unchanged DeepSeek identity", deepseek)
	}
}

func TestAssembleConnectSeparatesOpenAIProtocolIdentities(t *testing.T) {
	t.Parallel()

	official, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "openai",
		APIKey:   "secret",
		Models:   []ModelSelection{{Name: "gpt-4o-mini"}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(openai) error = %v", err)
	}
	if len(official) != 1 || official[0].Provider != "openai" || official[0].API != model.APIOpenAI {
		t.Fatalf("assembled openai = %#v, want persisted APIOpenAI identity", official)
	}

	chatByLegacy, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "openai-compatible",
		BaseURL:  "https://models.example.test/v1",
		APIKey:   "secret",
		Models:   []ModelSelection{{Name: "acme-chat"}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(openai-compatible) error = %v", err)
	}
	if len(chatByLegacy) != 1 || chatByLegacy[0].Provider != "openai-compatible" || chatByLegacy[0].API != model.APIOpenAICompatible {
		t.Fatalf("assembled legacy chat compatible = %#v", chatByLegacy)
	}

	chatByLabel, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "openai-chat-compatible",
		BaseURL:  "https://models.example.test/v1",
		APIKey:   "secret",
		Models:   []ModelSelection{{Name: "acme-chat"}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(openai-chat-compatible) error = %v", err)
	}
	if len(chatByLabel) != 1 || chatByLabel[0].Provider != "openai-compatible" || chatByLabel[0].API != model.APIOpenAICompatible {
		t.Fatalf("assembled labeled chat compatible = %#v, want durable openai-compatible identity", chatByLabel)
	}

	responses, err := AssembleConnect(context.Background(), ConnectRequest{
		Provider: "openai-responses-compatible",
		BaseURL:  "https://models.example.test/v1",
		APIKey:   "secret",
		Models:   []ModelSelection{{Name: "acme-responses"}},
	}, ConnectOptions{})
	if err != nil {
		t.Fatalf("AssembleConnect(openai-responses-compatible) error = %v", err)
	}
	if len(responses) != 1 {
		t.Fatalf("assembled responses compatible = %#v, want one", responses)
	}
	cfg := responses[0]
	if cfg.Provider != "openai-responses-compatible" || cfg.API != model.APIOpenAIResponses {
		t.Fatalf("assembled responses identity = %#v", cfg)
	}
	if cfg.AuthType != model.AuthAPIKey || cfg.BaseURL != "https://models.example.test/v1" {
		t.Fatalf("assembled responses endpoint/auth = %#v", cfg)
	}
	if cfg.ContextWindowTokens != 262144 || cfg.MaxOutputTok != 32768 {
		t.Fatalf("assembled responses defaults = context:%d output:%d", cfg.ContextWindowTokens, cfg.MaxOutputTok)
	}
	if cfg.ReasoningMode != modelcatalog.ReasoningModeEffort || cfg.ReasoningEffort != "medium" ||
		!slices.Equal(cfg.ReasoningLevels, []string{"none", "minimal", "low", "medium", "high", "xhigh"}) {
		t.Fatalf("assembled responses reasoning = %#v", cfg)
	}
	if CatalogProviderFor(cfg.Provider, cfg.BaseURL) != "openai-responses-compatible" {
		t.Fatalf("CatalogProviderFor(responses compatible) = %q, want no vendor inheritance", CatalogProviderFor(cfg.Provider, cfg.BaseURL))
	}
}

func TestOpenAICompatibleConfigsJSONRoundTripKeepProtocolIdentities(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name     string
		provider string
		baseURL  string
		model    string
		wantAPI  model.APIType
		wantProv string
	}{
		{name: "official openai", provider: "openai", model: "gpt-4o-mini", wantAPI: model.APIOpenAI, wantProv: "openai"},
		{name: "chat compatible", provider: "openai-chat-compatible", baseURL: "https://models.example.test/v1", model: "acme-chat", wantAPI: model.APIOpenAICompatible, wantProv: "openai-compatible"},
		{name: "legacy chat command", provider: "openai-compatible", baseURL: "https://models.example.test/v1", model: "acme-chat", wantAPI: model.APIOpenAICompatible, wantProv: "openai-compatible"},
		{name: "responses compatible", provider: "openai-responses-compatible", baseURL: "https://models.example.test/v1", model: "acme-responses", wantAPI: model.APIOpenAIResponses, wantProv: "openai-responses-compatible"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assembled, err := AssembleConnect(context.Background(), ConnectRequest{
				Provider: tt.provider,
				BaseURL:  tt.baseURL,
				APIKey:   "secret",
				Models:   []ModelSelection{{Name: tt.model}},
			}, ConnectOptions{})
			if err != nil {
				t.Fatalf("AssembleConnect() error = %v", err)
			}
			if len(assembled) != 1 {
				t.Fatalf("AssembleConnect() = %#v, want one", assembled)
			}
			raw, err := json.Marshal(assembled[0])
			if err != nil {
				t.Fatalf("json.Marshal(config) error = %v", err)
			}
			var decoded Config
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("json.Unmarshal(config) error = %v", err)
			}
			decoded = NormalizeConfig(decoded)
			if decoded.Provider != tt.wantProv || decoded.API != tt.wantAPI {
				t.Fatalf("round-trip identity = provider:%q api:%q, want %q %q; json=%s", decoded.Provider, decoded.API, tt.wantProv, tt.wantAPI, raw)
			}

		})
	}
}

func TestOpenAIConnectRoundTripDispatchesResponsesVersusChatCompletions(t *testing.T) {
	t.Parallel()

	const (
		responsesBody = `{"model":"probe","status":"completed","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"responses-ok"}]}]}`
		chatBody      = `{"model":"probe","choices":[{"message":{"role":"assistant","content":"chat-ok"},"finish_reason":"stop"}]}`
	)
	for _, tt := range []struct {
		name              string
		provider          string
		baseURL           string
		modelName         string
		persistedEndpoint string
		persistedModel    string
		wantAPI           model.APIType
		wantProvider      string
		wantPath          string
		wantText          string
	}{
		{
			name:         "official openai",
			provider:     "openai",
			modelName:    "gpt-4o-mini",
			wantAPI:      model.APIOpenAI,
			wantProvider: "openai",
			wantPath:     "/responses",
			wantText:     "responses-ok",
		},
		{
			name:              "official openai missing persisted api",
			persistedEndpoint: `{"provider":"openai","base_url":"https://api.openai.com/v1","token":"secret","auth_type":"api_key"}`,
			persistedModel:    `{"model":"gpt-4o-mini"}`,
			wantAPI:           model.APIOpenAI,
			wantProvider:      "openai",
			wantPath:          "/responses",
			wantText:          "responses-ok",
		},
		{
			name:         "generic responses compatible",
			provider:     "openai-responses-compatible",
			baseURL:      "https://models.example.test/v1",
			modelName:    "acme-responses",
			wantAPI:      model.APIOpenAIResponses,
			wantProvider: "openai-responses-compatible",
			wantPath:     "/responses",
			wantText:     "responses-ok",
		},
		{
			name:         "chat compatible label",
			provider:     "openai-chat-compatible",
			baseURL:      "https://models.example.test/v1",
			modelName:    "acme-chat",
			wantAPI:      model.APIOpenAICompatible,
			wantProvider: "openai-compatible",
			wantPath:     "/chat/completions",
			wantText:     "chat-ok",
		},
		{
			name:         "chat compatible durable provider",
			provider:     "openai-compatible",
			baseURL:      "https://models.example.test/v1",
			modelName:    "acme-chat",
			wantAPI:      model.APIOpenAICompatible,
			wantProvider: "openai-compatible",
			wantPath:     "/chat/completions",
			wantText:     "chat-ok",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var cfg Config
			if tt.persistedEndpoint != "" {
				var endpoint ProviderEndpointConfig
				var modelRecord Config
				if err := json.Unmarshal([]byte(tt.persistedEndpoint), &endpoint); err != nil {
					t.Fatalf("json.Unmarshal(endpoint) error = %v", err)
				}
				if err := json.Unmarshal([]byte(tt.persistedModel), &modelRecord); err != nil {
					t.Fatalf("json.Unmarshal(model) error = %v", err)
				}
				if endpoint.API != "" || modelRecord.API != "" {
					t.Fatalf("persisted records carried API %q/%q, want omitted default", endpoint.API, modelRecord.API)
				}
				cfg = MergeConfigProviderEndpoint(modelRecord, endpoint)
			} else {
				assembled, err := AssembleConnect(context.Background(), ConnectRequest{
					Provider: tt.provider,
					BaseURL:  tt.baseURL,
					APIKey:   "secret",
					Models:   []ModelSelection{{Name: tt.modelName}},
				}, ConnectOptions{})
				if err != nil {
					t.Fatalf("AssembleConnect() error = %v", err)
				}
				if len(assembled) != 1 {
					t.Fatalf("AssembleConnect() = %#v, want one", assembled)
				}
				endpointRaw, err := json.Marshal(SanitizePersistedProviderEndpoint(ProviderEndpointFromConfig(assembled[0])))
				if err != nil {
					t.Fatalf("json.Marshal(endpoint) error = %v", err)
				}
				modelRaw, err := json.Marshal(SanitizePersistedConfig(assembled[0]))
				if err != nil {
					t.Fatalf("json.Marshal(model) error = %v", err)
				}
				var endpoint ProviderEndpointConfig
				var modelRecord Config
				if err := json.Unmarshal(endpointRaw, &endpoint); err != nil {
					t.Fatalf("json.Unmarshal(endpoint) error = %v", err)
				}
				if err := json.Unmarshal(modelRaw, &modelRecord); err != nil {
					t.Fatalf("json.Unmarshal(model) error = %v", err)
				}
				if endpoint.API != "" || modelRecord.API != "" {
					t.Fatalf("sanitized persistence kept API %q/%q; json endpoint=%s model=%s", endpoint.API, modelRecord.API, endpointRaw, modelRaw)
				}
				cfg = MergeConfigProviderEndpoint(modelRecord, endpoint)
			}
			if cfg.Provider != tt.wantProvider || cfg.API != tt.wantAPI {
				t.Fatalf("rehydrated identity = provider:%q api:%q, want %q %q", cfg.Provider, cfg.API, tt.wantProvider, tt.wantAPI)
			}

			var gotPath string
			cfg.HTTPClient = &http.Client{Transport: modelconfigRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				gotPath = r.URL.Path
				if r.Method != http.MethodPost {
					return modelconfigHTTPResponse(r, http.StatusMethodNotAllowed, ""), nil
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/responses"):
					return modelconfigHTTPResponse(r, http.StatusOK, responsesBody), nil
				case strings.HasSuffix(r.URL.Path, "/chat/completions"):
					return modelconfigHTTPResponse(r, http.StatusOK, chatBody), nil
				default:
					return modelconfigHTTPResponse(r, http.StatusNotFound, `{"error":"unexpected path"}`), nil
				}
			})}

			resolved, err := BuildModel(cfg, 0, 0)
			if err != nil {
				t.Fatalf("BuildModel() error = %v", err)
			}
			var text string
			for event, genErr := range resolved.Model.Generate(context.Background(), &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
				Stream:   false,
			}) {
				if genErr != nil {
					t.Fatalf("Generate() error = %v path=%q", genErr, gotPath)
				}
				if event != nil && event.Response != nil {
					text = event.Response.Message.TextContent()
				}
			}
			if !strings.HasSuffix(gotPath, tt.wantPath) {
				t.Fatalf("POST path = %q, want suffix %q", gotPath, tt.wantPath)
			}
			if text != tt.wantText {
				t.Fatalf("Generate() text = %q, want %q from %s", text, tt.wantText, tt.wantPath)
			}
		})
	}
}

func TestCurrentOpenAIModelsDispatchReasoningAndToolsThroughResponses(t *testing.T) {
	t.Parallel()

	for _, provider := range []string{"openai", "codex"} {
		for _, name := range []string{"gpt-6-sol", "gpt-6-luna"} {
			t.Run(provider+"/"+name, func(t *testing.T) {
				t.Parallel()
				configs, err := AssembleConnect(context.Background(), ConnectRequest{
					Provider: provider,
					APIKey:   "test-key",
					Models:   []ModelSelection{{Name: name}},
				}, ConnectOptions{Authenticate: func(context.Context, AuthenticateRequest) error { return nil }})
				if err != nil {
					t.Fatal(err)
				}
				cfg := configs[0]
				var body struct {
					Model     string                        `json:"model"`
					Reasoning struct{ Effort string }       `json:"reasoning"`
					Tools     []struct{ Type, Name string } `json:"tools"`
				}
				var path string
				cfg.HTTPClient = &http.Client{Transport: modelconfigRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					path = r.URL.Path
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						return nil, err
					}
					response := modelconfigHTTPResponse(r, http.StatusOK, `data: {"type":"response.completed","response":{"status":"completed","output":[{"id":"msg_1","type":"message","content":[{"type":"output_text","text":"ok"}]}]}}`+"\n\n")
					response.Header.Set("Content-Type", "text/event-stream")
					return response, nil
				})}
				resolved, err := BuildModel(cfg, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				if resolved.Model.Name() != name {
					t.Fatalf("built model = %s, want %s", resolved.Model.Name(), name)
				}
				capabilities, declared := model.CapabilitiesOf(resolved.Model)
				if !declared || !capabilities.ImageInput {
					t.Fatalf("built model capabilities = %+v, declared=%v", capabilities, declared)
				}
				var text string
				for event, err := range resolved.Model.Generate(context.Background(), &model.Request{
					Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
					Reasoning: model.ReasoningConfig{Effort: resolved.ReasoningEffort},
					Tools: []model.ToolSpec{model.NewFunctionToolSpec("lookup", "look up a value", map[string]any{
						"type": "object", "properties": map[string]any{},
					})},
					Stream: true,
				}) {
					if err != nil {
						t.Fatal(err)
					}
					if event != nil && event.Response != nil {
						text = event.Response.Message.TextContent()
					}
				}
				wantPath := "/v1/responses"
				if provider == "codex" {
					wantPath = "/backend-api/codex/responses"
				}
				if path != wantPath || body.Model != name || body.Reasoning.Effort != "medium" ||
					len(body.Tools) != 1 || body.Tools[0].Type != "function" || body.Tools[0].Name != "lookup" || text != "ok" {
					t.Fatalf("Responses exchange: path=%q body=%+v text=%q", path, body, text)
				}
			})
		}
	}
}

func TestMaintainedSelectableModelsDoNotInheritVendorCatalogForResponsesCompatible(t *testing.T) {
	t.Parallel()

	models, err := MaintainedSelectableModels(context.Background(), "openai-responses-compatible", "https://proxy.example/v1")
	if err != nil {
		t.Fatalf("MaintainedSelectableModels(openai-responses-compatible) error = %v", err)
	}
	if selectableModelNamesContain(models, "gpt-4o-mini") || selectableModelNamesContain(models, "gpt-4o") {
		t.Fatalf("responses compatible models = %#v, want no OpenAI vendor catalog inheritance", models)
	}
}

func assertCompatibleConservativeDefaults(t *testing.T, template ProviderTemplate) {
	t.Helper()
	if template.DefaultContextWindowTokens != 262144 || template.DefaultMaxOutputTokens != 32768 {
		t.Fatalf("%s limits = context:%d output:%d, want 262144/32768", template.Label, template.DefaultContextWindowTokens, template.DefaultMaxOutputTokens)
	}
	if template.DefaultReasoningMode != "effort" || template.DefaultReasoningEffort != "medium" ||
		!slices.Equal(template.DefaultReasoningLevels, []string{"none", "minimal", "low", "medium", "high", "xhigh"}) {
		t.Fatalf("%s reasoning defaults = %#v", template.Label, template)
	}
	if template.AuthType != model.AuthAPIKey || template.DefaultBaseURL != "https://api.openai.com/v1" {
		t.Fatalf("%s auth/endpoint = %#v", template.Label, template)
	}
}
