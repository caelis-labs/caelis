package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestNewDeepSeekUsesProfileDefaults(t *testing.T) {
	p := NewDeepSeek(OpenAICompatConfig{Model: "deepseek-v4-pro", Token: "token"})
	if p.Name() != "deepseek-v4-pro" {
		t.Fatalf("Name() = %q, want deepseek-v4-pro", p.Name())
	}
	if p.provider != "deepseek" {
		t.Fatalf("provider = %q, want deepseek", p.provider)
	}
	if p.baseURL != defaultDeepSeekBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultDeepSeekBaseURL)
	}
}

func TestNewOpenRouterUsesProfileDefaults(t *testing.T) {
	p := NewOpenRouter(OpenAICompatConfig{Model: "openrouter/openai/gpt-4o-mini", Token: "token"})
	if p.Name() != "openrouter/openai/gpt-4o-mini" {
		t.Fatalf("Name() = %q, want configured model", p.Name())
	}
	if p.provider != "openrouter" {
		t.Fatalf("provider = %q, want openrouter", p.provider)
	}
	if p.baseURL != defaultOpenRouterBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultOpenRouterBaseURL)
	}
}

func TestNewMimoUsesProfileDefaults(t *testing.T) {
	p := NewMimo(OpenAICompatConfig{Model: "mimo-v2-flash", Token: "token"})
	if p.Name() != "mimo-v2-flash" {
		t.Fatalf("Name() = %q, want configured model", p.Name())
	}
	if p.provider != "mimo" {
		t.Fatalf("provider = %q, want mimo", p.provider)
	}
	if p.baseURL != defaultMimoBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultMimoBaseURL)
	}
}

func TestNewVolcengineUsesProfileDefaults(t *testing.T) {
	p := NewVolcengine(OpenAICompatConfig{Model: "doubao-seed-2.0-pro", Token: "token"})
	if p.Name() != "doubao-seed-2.0-pro" {
		t.Fatalf("Name() = %q, want configured model", p.Name())
	}
	if p.provider != "volcengine" {
		t.Fatalf("provider = %q, want volcengine", p.provider)
	}
	if p.baseURL != defaultVolcengineBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultVolcengineBaseURL)
	}
}

func TestNewVolcengineCodingUsesProfileDefaults(t *testing.T) {
	p := NewVolcengineCoding(OpenAICompatConfig{Model: "doubao-seed-2.0-pro", Token: "token"})
	if p.provider != "volcengine-coding" {
		t.Fatalf("provider = %q, want volcengine-coding", p.provider)
	}
	if p.baseURL != defaultVolcengineCodingBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultVolcengineCodingBaseURL)
	}
}

func TestOpenAICompatibleProviderSendsConfiguredHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("X-Provider"); got != "deepseek" {
			t.Fatalf("X-Provider = %q, want configured header", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["model"] != "deepseek-v4-pro" {
			t.Fatalf("model = %#v, want deepseek-v4-pro", payload["model"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewDeepSeek(OpenAICompatConfig{
		Model:      "deepseek-v4-pro",
		Token:      "token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Headers:    map[string]string{"X-Provider": "deepseek"},
	})

	var text string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
	}
	if text != "ok" {
		t.Fatalf("text = %q, want ok", text)
	}
}

func TestDiscoverDeepSeekModelsUsesOpenAICompatibleDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"deepseek-v4-flash","context_length":1048576,"supported_parameters":["tools","reasoning"]}]}`)
	}))
	defer server.Close()

	models, err := DiscoverDeepSeekModels(context.Background(), OpenAICompatConfig{
		Token:      "token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("DiscoverDeepSeekModels() error = %v", err)
	}
	if len(models) != 1 || models[0].Name != "deepseek-v4-flash" || models[0].ContextWindowTokens != 1048576 {
		t.Fatalf("models = %#v, want deepseek-v4-flash with context", models)
	}
}

func TestDiscoverMimoAndVolcengineModelsUseOpenAICompatibleDefaults(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"profile-model","context_length":65536,"supported_parameters":["tools"]}]}`)
	}))
	defer server.Close()

	cfg := OpenAICompatConfig{Token: "token", BaseURL: server.URL, HTTPClient: server.Client()}
	for name, discover := range map[string]func(context.Context, OpenAICompatConfig) ([]RemoteModel, error){
		"mimo":              DiscoverMimoModels,
		"volcengine":        DiscoverVolcengineModels,
		"volcengine-coding": DiscoverVolcengineCodingModels,
	} {
		models, err := discover(context.Background(), cfg)
		if err != nil {
			t.Fatalf("%s discovery error = %v", name, err)
		}
		if len(models) != 1 || models[0].Name != "profile-model" || models[0].ContextWindowTokens != 65536 {
			t.Fatalf("%s models = %#v, want profile-model", name, models)
		}
	}
	if len(paths) != 3 || paths[0] != "/models" || paths[1] != "/models" || paths[2] != "/models" {
		t.Fatalf("paths = %#v, want three /models calls", paths)
	}
}

func TestDiscoverOpenRouterModelsUsesOpenAICompatibleDefaults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("HTTP-Referer"); got != "https://example.com/app" {
			t.Fatalf("HTTP-Referer = %q, want configured referer", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"openai/gpt-4o-mini","context_length":128000,"top_provider":{"max_completion_tokens":16384},"supported_parameters":["tools","reasoning"]}]}`)
	}))
	defer server.Close()

	models, err := DiscoverOpenRouterModels(context.Background(), OpenAICompatConfig{
		Token:      "token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Headers:    map[string]string{"HTTP-Referer": "https://example.com/app"},
	})
	if err != nil {
		t.Fatalf("DiscoverOpenRouterModels() error = %v", err)
	}
	if len(models) != 1 || models[0].Name != "openai/gpt-4o-mini" || models[0].MaxOutputTokens != 16384 {
		t.Fatalf("models = %#v, want OpenRouter model with max output", models)
	}
}

func TestDeepSeekReasoningPayload(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewDeepSeek(OpenAICompatConfig{
		Model:        "deepseek-v4-pro",
		Token:        "token",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		MaxOutputTok: 8192,
	})

	for _, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: model.RoleAssistant, Content: []model.Part{{Text: "plain assistant"}}},
			{Role: model.RoleUser, Content: []model.Part{{Text: "continue"}}},
		},
		Reasoning: model.ReasoningConfig{Effort: "medium"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}

	thinking, _ := payload["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %#v, want enabled", payload["thinking"])
	}
	if payload["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", payload["reasoning_effort"])
	}
	if payload["max_tokens"] != float64(thinkingModeMinTokens) {
		t.Fatalf("max_tokens = %#v, want %d", payload["max_tokens"], thinkingModeMinTokens)
	}
	messages := payload["messages"].([]any)
	first := messages[0].(map[string]any)
	if got, ok := first["reasoning_content"].(string); !ok || got != "" {
		t.Fatalf("assistant reasoning_content = %#v, want explicit empty string", first["reasoning_content"])
	}
}

func TestDeepSeekReasoningDisabledClearsReasoningContent(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewDeepSeek(OpenAICompatConfig{
		Model:        "deepseek-v4-pro",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		MaxOutputTok: 400000,
	})

	for _, err := range p.Generate(context.Background(), model.Request{
		Messages:  []model.Message{{Role: model.RoleAssistant, Content: []model.Part{{Text: "previous"}}}},
		Reasoning: model.ReasoningConfig{Effort: "none"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	thinking, _ := payload["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", payload["thinking"])
	}
	if _, ok := payload["reasoning_effort"]; ok {
		t.Fatalf("reasoning_effort present = %#v, want omitted", payload["reasoning_effort"])
	}
	if payload["max_tokens"] != float64(deepSeekMaxTokens) {
		t.Fatalf("max_tokens = %#v, want cap %d", payload["max_tokens"], deepSeekMaxTokens)
	}
	first := payload["messages"].([]any)[0].(map[string]any)
	if _, ok := first["reasoning_content"]; ok {
		t.Fatalf("reasoning_content present = %#v, want omitted", first["reasoning_content"])
	}
}

func TestOpenRouterNativePayloadAndHeaders(t *testing.T) {
	var gotReferer string
	var gotTitle string
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning\":\"think\",\"content\":\"ok\"},\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	p := NewOpenRouter(OpenAICompatConfig{
		Model:      "openrouter/openai/gpt-4o-mini",
		Token:      "token",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Headers:    map[string]string{"X-Title": "custom-caelis"},
		OpenRouter: OpenRouterConfig{
			Models:     []string{"openrouter/openai/gpt-4o-mini", "anthropic/claude-sonnet-4"},
			Route:      "fallback",
			Transforms: []string{"middle-out"},
			Provider:   map[string]any{"allow_fallbacks": true},
			Plugins:    []map[string]any{{"id": "web"}},
		},
	})

	var text, reasoning string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: model.RoleAssistant, Content: []model.Part{
				{Reasoning: &model.Reasoning{Text: "prior", Visibility: model.ReasoningVisibilityVisible}},
				{ToolUse: &model.ToolUse{CallID: "call_1", Name: "lookup", ArgJSON: `{"q":"x"}`}},
			}},
			{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}},
		},
		Reasoning: model.ReasoningConfig{Effort: "high"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
		reasoning += evt.ReasoningDelta
	}

	if gotReferer != openRouterDefaultReferer {
		t.Fatalf("HTTP-Referer = %q, want default attribution", gotReferer)
	}
	if gotTitle != "custom-caelis" {
		t.Fatalf("X-Title = %q, want configured override", gotTitle)
	}
	if payload["model"] != "openai/gpt-4o-mini" {
		t.Fatalf("model = %#v, want normalized OpenRouter model id", payload["model"])
	}
	models := payload["models"].([]any)
	if len(models) != 2 || models[0] != "openai/gpt-4o-mini" || models[1] != "anthropic/claude-sonnet-4" {
		t.Fatalf("models = %#v, want normalized routed model ids", models)
	}
	if payload["route"] != "fallback" {
		t.Fatalf("route = %#v, want fallback", payload["route"])
	}
	transforms := payload["transforms"].([]any)
	if len(transforms) != 1 || transforms[0] != "middle-out" {
		t.Fatalf("transforms = %#v, want middle-out", transforms)
	}
	provider := payload["provider"].(map[string]any)
	if provider["allow_fallbacks"] != true {
		t.Fatalf("provider = %#v, want allow_fallbacks", provider)
	}
	if plugins := payload["plugins"].([]any); len(plugins) != 1 {
		t.Fatalf("plugins = %#v, want one plugin", plugins)
	}
	if payload["reasoning_effort"] != "high" {
		t.Fatalf("reasoning_effort = %#v, want high", payload["reasoning_effort"])
	}
	if reasoningBlock := payload["reasoning"].(map[string]any); reasoningBlock["effort"] != "high" {
		t.Fatalf("reasoning = %#v, want effort high", reasoningBlock)
	}
	first := payload["messages"].([]any)[0].(map[string]any)
	if first["reasoning"] != "prior" {
		t.Fatalf("assistant reasoning = %#v, want OpenRouter reasoning field", first["reasoning"])
	}
	if _, ok := first["reasoning_content"]; ok {
		t.Fatalf("reasoning_content present = %#v, want OpenRouter reasoning field", first["reasoning_content"])
	}
	if text != "ok" || reasoning != "think" {
		t.Fatalf("stream deltas text=%q reasoning=%q, want ok/think", text, reasoning)
	}
}

func TestMimoThinkingPayloadAndJSONObjectOutput(t *testing.T) {
	payload := captureProviderPayload(t, NewMimo, OpenAICompatConfig{
		Model: "mimo-v2-flash",
	}, model.Request{
		Messages: []model.Message{{Role: model.RoleAssistant, Content: []model.Part{
			{Reasoning: &model.Reasoning{Text: "prior", Visibility: model.ReasoningVisibilityVisible}},
			{ToolUse: &model.ToolUse{CallID: "call_1", Name: "lookup", ArgJSON: `{"q":"x"}`}},
		}}},
		Reasoning: model.ReasoningConfig{Effort: "high"},
		Output: &model.OutputSpec{
			Mode:       model.OutputModeSchema,
			JSONSchema: map[string]any{"type": "object"},
		},
	})

	thinking := payload["thinking"].(map[string]any)
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking = %#v, want enabled", thinking)
	}
	if _, ok := payload["reasoning"]; ok {
		t.Fatalf("reasoning present = %#v, want omitted for Mimo", payload["reasoning"])
	}
	if _, ok := payload["reasoning_effort"]; ok {
		t.Fatalf("reasoning_effort present = %#v, want omitted for Mimo", payload["reasoning_effort"])
	}
	responseFormat := payload["response_format"].(map[string]any)
	if responseFormat["type"] != "json_object" {
		t.Fatalf("response_format = %#v, want json_object", responseFormat)
	}
	msg := payload["messages"].([]any)[0].(map[string]any)
	if msg["reasoning_content"] != "prior" {
		t.Fatalf("reasoning_content = %#v, want prior", msg["reasoning_content"])
	}
}

func TestMimoThinkingDisabled(t *testing.T) {
	payload := captureProviderPayload(t, NewMimo, OpenAICompatConfig{Model: "mimo-v2-flash"}, model.Request{
		Messages:  []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
		Reasoning: model.ReasoningConfig{Effort: "none"},
	})
	thinking := payload["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", thinking)
	}
}

func TestVolcengineThinkingPayload(t *testing.T) {
	for name, newProvider := range map[string]func(OpenAICompatConfig) *OpenAIProvider{
		"volcengine":        NewVolcengine,
		"volcengine-coding": NewVolcengineCoding,
	} {
		t.Run(name+"/auto", func(t *testing.T) {
			payload := captureProviderPayload(t, newProvider, OpenAICompatConfig{Model: "doubao-seed-2.0-pro"}, model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
			})
			thinking := payload["thinking"].(map[string]any)
			if thinking["type"] != "auto" {
				t.Fatalf("thinking = %#v, want auto", thinking)
			}
		})
		t.Run(name+"/disabled", func(t *testing.T) {
			payload := captureProviderPayload(t, newProvider, OpenAICompatConfig{Model: "doubao-seed-2.0-pro"}, model.Request{
				Messages:  []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
				Reasoning: model.ReasoningConfig{Effort: "none"},
			})
			thinking := payload["thinking"].(map[string]any)
			if thinking["type"] != "disabled" {
				t.Fatalf("thinking = %#v, want disabled", thinking)
			}
		})
	}
}

func TestOpenAICompatibleStructuredOutputUsesJSONSchema(t *testing.T) {
	payload := captureProviderPayload(t, NewOpenAICompatible, OpenAICompatConfig{
		Provider: "openai-compatible",
		Model:    "compat-model",
	}, model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "json"}}}},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"outcome": map[string]any{"type": "string"},
				},
			},
			MaxOutputTokens: 1234,
		},
	})

	responseFormat := payload["response_format"].(map[string]any)
	if responseFormat["type"] != "json_schema" {
		t.Fatalf("response_format.type = %#v, want json_schema", responseFormat["type"])
	}
	jsonSchema := responseFormat["json_schema"].(map[string]any)
	if jsonSchema["name"] != "caelis_output" {
		t.Fatalf("json_schema.name = %#v, want caelis_output", jsonSchema["name"])
	}
	if schema := jsonSchema["schema"].(map[string]any); schema["type"] != "object" {
		t.Fatalf("json_schema.schema = %#v, want object schema", schema)
	}
	if payload["max_tokens"] != float64(1234) {
		t.Fatalf("max_tokens = %#v, want 1234", payload["max_tokens"])
	}
}

func TestDeepSeekStructuredOutputUsesJSONObjectStrategy(t *testing.T) {
	payload := captureProviderPayload(t, NewDeepSeek, OpenAICompatConfig{
		Model: "deepseek-v4-pro",
	}, model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "json"}}}},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type": "object",
			},
			MaxOutputTokens: 64000,
		},
		Reasoning: model.ReasoningConfig{Effort: "none"},
	})

	responseFormat := payload["response_format"].(map[string]any)
	if responseFormat["type"] != "json_object" {
		t.Fatalf("response_format.type = %#v, want json_object", responseFormat["type"])
	}
	if _, ok := responseFormat["json_schema"]; ok {
		t.Fatalf("json_schema present = %#v, want omitted for DeepSeek json_object strategy", responseFormat["json_schema"])
	}
	if payload["max_tokens"] != float64(64000) {
		t.Fatalf("max_tokens = %#v, want output max tokens respected", payload["max_tokens"])
	}
}

func captureProviderPayload(t *testing.T, newProvider func(OpenAICompatConfig) *OpenAIProvider, cfg OpenAICompatConfig, req model.Request) map[string]any {
	t.Helper()
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	cfg.BaseURL = server.URL
	cfg.HTTPClient = server.Client()
	p := newProvider(cfg)
	for _, err := range p.Generate(context.Background(), req) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	return payload
}
