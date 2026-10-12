package modelconfig

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

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
			name:         "custom Responses",
			provider:     "custom-responses",
			modelName:    "gpt-4o-mini",
			baseURL:      "https://models.example.test/v1",
			wantAPI:      model.APIOpenAIResponses,
			wantProvider: "custom",
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
			provider:     "custom-responses",
			baseURL:      "https://models.example.test/v1",
			modelName:    "acme-responses",
			wantAPI:      model.APIOpenAIResponses,
			wantProvider: "custom",
			wantPath:     "/responses",
			wantText:     "responses-ok",
		},
		{
			name:         "chat compatible label",
			provider:     "custom-chat",
			baseURL:      "https://models.example.test/v1",
			modelName:    "acme-chat",
			wantAPI:      model.APIOpenAICompatible,
			wantProvider: "custom",
			wantPath:     "/chat/completions",
			wantText:     "chat-ok",
		},
		{
			name:         "chat compatible durable provider",
			provider:     "custom-chat",
			baseURL:      "https://models.example.test/v1",
			modelName:    "acme-chat",
			wantAPI:      model.APIOpenAICompatible,
			wantProvider: "custom",
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
				if endpoint.API != tt.wantAPI || modelRecord.API != "" {
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

func TestSelectableModelsDoNotInheritVendorCatalogForResponsesCompatible(t *testing.T) {
	t.Parallel()

	models, err := SelectableModels(context.Background(), ConnectRequest{Provider: "openai-responses-compatible", BaseURL: "https://proxy.example/v1"})
	if err != nil {
		t.Fatalf("SelectableModels(openai-responses-compatible) error = %v", err)
	}
	if selectableModelNamesContain(models, "gpt-4o-mini") || selectableModelNamesContain(models, "gpt-4o") {
		t.Fatalf("responses compatible models = %#v, want no OpenAI vendor catalog inheritance", models)
	}
}
