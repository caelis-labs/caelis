package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/model"
)

func TestNewGeminiUsesDefaults(t *testing.T) {
	p := NewGemini(GeminiConfig{Model: "gemini-2.5-flash", Token: "token"})
	if p.Name() != "gemini-2.5-flash" {
		t.Fatalf("Name() = %q, want gemini-2.5-flash", p.Name())
	}
	if p.provider != "gemini" {
		t.Fatalf("provider = %q, want gemini", p.provider)
	}
	if p.baseURL != defaultGeminiBaseURL || p.apiVersion != defaultGeminiAPIVersion {
		t.Fatalf("baseURL/version = %q/%q, want defaults", p.baseURL, p.apiVersion)
	}
}

func TestGeminiNonStreamRequestMapsThinkingSchemaAndThoughtSignature(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-3-pro:generateContent" {
			t.Fatalf("path = %q, want generateContent", r.URL.Path)
		}
		if got := r.URL.Query().Get("key"); got != "token" {
			t.Fatalf("key = %q, want token", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"thought","thought":true},{"text":"answer"},{"functionCall":{"id":"call_1","name":"lookup","args":{"q":"weather"}},"thoughtSignature":"c2lnLWNhbGw="}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"thoughtsTokenCount":3,"totalTokenCount":18}}`)
	}))
	defer server.Close()

	p := NewGemini(GeminiConfig{Model: "gemini-3-pro", Token: "token", BaseURL: server.URL + "/v1beta", HTTPClient: server.Client(), MaxOutputTok: 2048})
	var text, reasoning string
	var call *model.ToolCallDelta
	var usage *model.Usage
	var replayToken string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: []model.Part{{Text: "system"}}},
			{Role: model.RoleUser, Content: []model.Part{{Text: "hello"}}},
			{Role: model.RoleAssistant, Content: []model.Part{
				{Text: "prior answer"},
				{ToolUse: &model.ToolUse{CallID: "call-prev", Name: "lookup", ArgJSON: `{"q":"old"}`, ProviderMeta: map[string]any{"thought_signature": encodeGeminiThoughtSignature([]byte("sig-prev"))}}},
			}},
			{Role: model.RoleTool, Content: []model.Part{{ToolResult: &model.ToolResult{CallID: "call-prev", Content: `{"ok":true}`}}}},
		},
		Tools:     []model.ToolSpec{{Name: "lookup", Description: "lookup", Schema: model.Schema{Type: "object", Properties: map[string]model.Schema{"q": {Type: "string"}}, Required: []string{"q"}}}},
		Output:    &model.OutputSpec{Mode: model.OutputModeSchema, JSONSchema: map[string]any{"type": "object"}, MaxOutputTokens: 512},
		Reasoning: model.ReasoningConfig{Effort: "high"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
		reasoning += evt.ReasoningDelta
		if evt.ToolCall != nil {
			call = evt.ToolCall
		}
		if evt.Usage != nil {
			usage = evt.Usage
		}
		if token, _ := evt.Metadata["thought_signature"].(string); token != "" {
			replayToken = token
		}
	}
	if text != "answer" || reasoning != "thought" {
		t.Fatalf("text/reasoning = %q/%q, want answer/thought", text, reasoning)
	}
	if call == nil || call.CallID != "call_1" || call.Name != "lookup" || call.Args["q"] != "weather" {
		t.Fatalf("call = %#v, want lookup weather", call)
	}
	if replayToken == "" || decodeGeminiThoughtSignature(replayToken) == nil {
		t.Fatalf("replay token = %q, want encoded thought signature", replayToken)
	}
	if usage == nil || usage.PromptTokens != 11 || usage.CompletionTokens != 7 || usage.ReasoningTokens != 3 || usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v, want gemini usage", usage)
	}
	cfg := payload["generationConfig"].(map[string]any)
	if cfg["maxOutputTokens"] != float64(512) || cfg["responseMimeType"] != "application/json" {
		t.Fatalf("generationConfig = %#v, want max/schema JSON", cfg)
	}
	thinking := cfg["thinkingConfig"].(map[string]any)
	if thinking["thinkingLevel"] != "HIGH" || thinking["includeThoughts"] != true {
		t.Fatalf("thinkingConfig = %#v, want HIGH include", thinking)
	}
	if _, ok := thinking["thinkingBudget"]; ok {
		t.Fatalf("thinkingBudget present = %#v, want omitted for Gemini 3+", thinking["thinkingBudget"])
	}
	contents := payload["contents"].([]any)
	assistantParts := contents[1].(map[string]any)["parts"].([]any)
	functionCall := assistantParts[1].(map[string]any)["functionCall"].(map[string]any)
	if functionCall["name"] != "lookup" || assistantParts[1].(map[string]any)["thoughtSignature"] == "" {
		t.Fatalf("assistant function call = %#v, want thought signature", assistantParts[1])
	}
}

func TestGeminiPre3ThinkingBudgetAndDisable(t *testing.T) {
	tests := []struct {
		name         string
		reasoning    model.ReasoningConfig
		wantBudget   float64
		wantThoughts bool
	}{
		{name: "high", reasoning: model.ReasoningConfig{Effort: "high"}, wantBudget: 8192, wantThoughts: true},
		{name: "none", reasoning: model.ReasoningConfig{Effort: "none"}, wantBudget: 0, wantThoughts: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var payload map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
			}))
			defer server.Close()
			p := NewGemini(GeminiConfig{Model: "gemini-2.5-flash", Token: "token", BaseURL: server.URL, HTTPClient: server.Client()})
			for _, err := range p.Generate(context.Background(), model.Request{
				Messages:  []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
				Reasoning: tc.reasoning,
			}) {
				if err != nil {
					t.Fatalf("Generate() error = %v", err)
				}
			}
			thinking := payload["generationConfig"].(map[string]any)["thinkingConfig"].(map[string]any)
			if thinking["thinkingBudget"] != tc.wantBudget || thinking["includeThoughts"] != tc.wantThoughts {
				t.Fatalf("thinking = %#v, want budget=%v include=%v", thinking, tc.wantBudget, tc.wantThoughts)
			}
			if _, ok := thinking["thinkingLevel"]; ok {
				t.Fatalf("thinkingLevel present = %#v, want omitted for Gemini 2.x", thinking["thinkingLevel"])
			}
		})
	}
}

func TestGeminiStreamMapsReasoningTextUsageAndToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
			t.Fatalf("path = %q, want streamGenerateContent", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"think-1\",\"thought\":true},{\"text\":\"hello\"}]}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"call_1\",\"name\":\"lookup\",\"args\":{\"q\":\"weather\"}},\"thoughtSignature\":\"c2lnLTE=\"}]}}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":2,\"totalTokenCount\":8}}\n\n")
	}))
	defer server.Close()

	p := NewGemini(GeminiConfig{Model: "gemini-2.5-flash", Token: "token", BaseURL: server.URL, HTTPClient: server.Client()})
	var text, reasoning string
	var call *model.ToolCallDelta
	var usage *model.Usage
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
		Metadata: map[string]any{"stream": true},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
		reasoning += evt.ReasoningDelta
		if evt.ToolCall != nil {
			call = evt.ToolCall
		}
		if evt.Usage != nil {
			usage = evt.Usage
		}
	}
	if text != "hello" || reasoning != "think-1" {
		t.Fatalf("text/reasoning = %q/%q, want stream parts", text, reasoning)
	}
	if call == nil || call.Name != "lookup" || call.Args["q"] != "weather" {
		t.Fatalf("call = %#v, want lookup weather", call)
	}
	if usage == nil || usage.TotalTokens != 8 || usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v, want stream usage", usage)
	}
}

func TestDiscoverGeminiModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Fatalf("path = %q, want /v1beta/models", r.URL.Path)
		}
		if r.URL.Query().Get("key") != "token" {
			t.Fatalf("query = %q, want key token", r.URL.RawQuery)
		}
		_, _ = fmt.Fprint(w, `{"models":[{"name":"models/gemini-2.5-flash","inputTokenLimit":1048576,"outputTokenLimit":8192,"supportedGenerationMethods":["generateContent","streamGenerateContent"]}]}`)
	}))
	defer server.Close()

	models, err := DiscoverGeminiModels(context.Background(), GeminiConfig{BaseURL: server.URL + "/v1beta", Token: "token", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("DiscoverGeminiModels() error = %v", err)
	}
	if len(models) != 1 || models[0].Name != "gemini-2.5-flash" || models[0].ContextWindowTokens != 1048576 {
		t.Fatalf("models = %#v, want gemini metadata", models)
	}
}

func TestConfiguredFactoryResolvesGemini(t *testing.T) {
	factory, err := NewConfiguredFactory([]ModelConfig{{Provider: "gemini", Model: "gemini-2.5-flash", Token: "token"}})
	if err != nil {
		t.Fatalf("NewConfiguredFactory() error = %v", err)
	}
	llm, err := factory.New(model.Ref{ModelID: "gemini/gemini-2.5-flash"})
	if err != nil {
		t.Fatalf("factory.New() error = %v", err)
	}
	if !strings.EqualFold(llm.Name(), "gemini-2.5-flash") {
		t.Fatalf("Name() = %q, want gemini-2.5-flash", llm.Name())
	}
}
