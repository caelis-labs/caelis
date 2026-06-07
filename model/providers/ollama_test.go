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

func TestNewOllamaUsesDefaults(t *testing.T) {
	p := NewOllama(OllamaConfig{Model: "qwen2.5:7b"})
	if p.Name() != "qwen2.5:7b" {
		t.Fatalf("Name() = %q, want qwen2.5:7b", p.Name())
	}
	if p.provider != "ollama" {
		t.Fatalf("provider = %q, want ollama", p.provider)
	}
	if p.baseURL != defaultOllamaBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultOllamaBaseURL)
	}
}

func TestOllamaNonStreamRequestAndStructuredOutput(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path = %q, want /api/chat", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"qwen3.5:4b","message":{"role":"assistant","thinking":"step","content":"{\"outcome\":\"allow\"}"},"done":true,"prompt_eval_count":11,"eval_count":6}`)
	}))
	defer server.Close()

	p := NewOllama(OllamaConfig{Model: "qwen3.5:4b", BaseURL: server.URL + "/v1", HTTPClient: server.Client(), MaxOutputTok: 512})
	var text, reasoning string
	var usage *model.Usage
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: []model.Part{{Text: "system"}}},
			{Role: model.RoleUser, Content: []model.Part{{Text: "review"}}},
		},
		Output: &model.OutputSpec{
			Mode:            model.OutputModeSchema,
			JSONSchema:      map[string]any{"type": "object", "properties": map[string]any{"outcome": map[string]any{"type": "string"}}},
			MaxOutputTokens: 64,
		},
		Reasoning: model.ReasoningConfig{Effort: "medium"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
		reasoning += evt.ReasoningDelta
		if evt.Usage != nil {
			usage = evt.Usage
		}
	}
	if text != `{"outcome":"allow"}` || reasoning != "step" {
		t.Fatalf("text/reasoning = %q/%q, want JSON text and reasoning", text, reasoning)
	}
	if usage == nil || usage.PromptTokens != 11 || usage.CompletionTokens != 6 || usage.TotalTokens != 17 {
		t.Fatalf("usage = %+v, want ollama usage", usage)
	}
	if payload["stream"] != false {
		t.Fatalf("stream = %#v, want false", payload["stream"])
	}
	if payload["think"] != true {
		t.Fatalf("think = %#v, want true from reasoning effort", payload["think"])
	}
	options := payload["options"].(map[string]any)
	if options["num_predict"] != float64(64) {
		t.Fatalf("options = %#v, want output max tokens", options)
	}
	format := payload["format"].(map[string]any)
	if format["type"] != "object" {
		t.Fatalf("format = %#v, want schema object", format)
	}
}

func TestOllamaStreamMapsNDJSONEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprint(w, "{\"model\":\"qwen2.5:7b\",\"message\":{\"role\":\"assistant\",\"thinking\":\"plan \",\"content\":\"Hello\"},\"done\":false}\n")
		_, _ = fmt.Fprint(w, "{\"model\":\"qwen2.5:7b\",\"message\":{\"role\":\"assistant\",\"content\":\" World\"},\"done\":false}\n")
		_, _ = fmt.Fprint(w, "{\"model\":\"qwen2.5:7b\",\"message\":{\"role\":\"assistant\"},\"done\":true,\"prompt_eval_count\":7,\"eval_count\":5}\n")
	}))
	defer server.Close()

	p := NewOllama(OllamaConfig{Model: "qwen2.5:7b", BaseURL: server.URL, HTTPClient: server.Client()})
	var text, reasoning string
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
		if evt.Usage != nil {
			usage = evt.Usage
		}
	}
	if text != "Hello World" || reasoning != "plan " {
		t.Fatalf("text/reasoning = %q/%q, want streamed content", text, reasoning)
	}
	if usage == nil || usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v, want final usage", usage)
	}
}

func TestOllamaStreamMapsToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = fmt.Fprint(w, "{\"model\":\"qwen\",\"message\":{\"role\":\"assistant\",\"tool_calls\":[{\"function\":{\"name\":\"lookup\",\"arguments\":{\"q\":\"weather\"}}}]},\"done\":false}\n")
		_, _ = fmt.Fprint(w, "{\"model\":\"qwen\",\"message\":{\"role\":\"assistant\"},\"done\":true}\n")
	}))
	defer server.Close()

	p := NewOllama(OllamaConfig{Model: "qwen", BaseURL: server.URL, HTTPClient: server.Client()})
	var call *model.ToolCallDelta
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "use tool"}}}},
		Metadata: map[string]any{"stream": true},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if evt.ToolCall != nil {
			call = evt.ToolCall
		}
	}
	if call == nil || call.Name != "lookup" || call.Args["q"] != "weather" {
		t.Fatalf("tool call = %#v, want lookup weather", call)
	}
}

func TestDiscoverOllamaModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Fatalf("path = %q, want /api/tags", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"models":[{"name":"qwen2.5:7b"},{"model":"llama3.1:8b"}]}`)
	}))
	defer server.Close()

	models, err := DiscoverOllamaModels(context.Background(), OllamaConfig{BaseURL: server.URL + "/v1", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("DiscoverOllamaModels() error = %v", err)
	}
	names := []string{models[0].Name, models[1].Name}
	if strings.Join(names, ",") != "llama3.1:8b,qwen2.5:7b" {
		t.Fatalf("models = %#v, want sorted ollama models", models)
	}
}

func TestConfiguredFactoryResolvesOllama(t *testing.T) {
	factory, err := NewConfiguredFactory([]ModelConfig{{Provider: "ollama", Model: "qwen2.5:7b"}})
	if err != nil {
		t.Fatalf("NewConfiguredFactory() error = %v", err)
	}
	llm, err := factory.New(model.Ref{ModelID: "ollama/qwen2.5:7b"})
	if err != nil {
		t.Fatalf("factory.New() error = %v", err)
	}
	if llm.Name() != "qwen2.5:7b" {
		t.Fatalf("Name() = %q, want qwen2.5:7b", llm.Name())
	}
}
