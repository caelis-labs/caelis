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

func TestNewAnthropicUsesDefaults(t *testing.T) {
	p := NewAnthropic(AnthropicConfig{Model: "claude-sonnet-4-5", Token: "sk-test"})
	if p.Name() != "claude-sonnet-4-5" {
		t.Fatalf("Name() = %q, want claude-sonnet-4-5", p.Name())
	}
	if p.provider != "anthropic" {
		t.Fatalf("provider = %q, want anthropic", p.provider)
	}
	if p.baseURL != defaultAnthropicBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultAnthropicBaseURL)
	}
}

func TestAnthropicNonStreamMapsRequestAndResponseBlocks(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-test" {
			t.Fatalf("x-api-key = %q, want sk-test", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("anthropic-version header is empty")
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","stop_reason":"tool_use","content":[{"type":"thinking","thinking":"I'll call the tool.","signature":"sig-final"},{"type":"text","text":"Let me check."},{"type":"tool_use","id":"call_2","name":"lookup","input":{"q":"weather"}}],"usage":{"input_tokens":11,"output_tokens":7,"cache_read_input_tokens":4}}`)
	}))
	defer server.Close()

	p := NewAnthropic(AnthropicConfig{
		Model:      "claude-sonnet-4-5",
		Token:      "sk-test",
		BaseURL:    server.URL + "/v1",
		HTTPClient: server.Client(),
	})
	var text, reasoning string
	var toolCall *model.ToolCallDelta
	var usage *model.Usage
	var finish string
	var replayToken string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: []model.Part{{Text: "system instruction"}}},
			{Role: model.RoleUser, Content: []model.Part{{Text: "hello"}}},
			{Role: model.RoleAssistant, Content: []model.Part{
				{Reasoning: &model.Reasoning{Text: "prior reasoning", Visibility: model.ReasoningVisibilityVisible, Replay: &model.ReplayMeta{Provider: "anthropic", Kind: anthropicReplayKindThinkingSignature, Token: "sig-prev"}}},
				{Text: "Working."},
				{ToolUse: &model.ToolUse{CallID: "call-prev", Name: "echo", ArgJSON: `{"text":"x"}`}},
			}},
			{Role: model.RoleTool, Content: []model.Part{{ToolResult: &model.ToolResult{CallID: "call-prev", Content: `{"echo":"x"}`}}}},
		},
		Tools:     []model.ToolSpec{{Name: "lookup", Description: "Look up weather.", Schema: model.Schema{Type: "object", Properties: map[string]model.Schema{"q": {Type: "string"}}, Required: []string{"q"}}}},
		Reasoning: model.ReasoningConfig{Effort: "high"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		text += evt.TextDelta
		reasoning += evt.ReasoningDelta
		if evt.ToolCall != nil {
			toolCall = evt.ToolCall
		}
		if evt.Usage != nil {
			usage = evt.Usage
		}
		if evt.FinishReason != "" {
			finish = evt.FinishReason
		}
		if token, _ := evt.Metadata["replay_token"].(string); token != "" {
			replayToken = token
		}
	}

	if text != "Let me check." || reasoning != "I'll call the tool." {
		t.Fatalf("text/reasoning = %q/%q, want response blocks", text, reasoning)
	}
	if toolCall == nil || toolCall.CallID != "call_2" || toolCall.Name != "lookup" || toolCall.Args["q"] != "weather" {
		t.Fatalf("toolCall = %#v, want lookup weather", toolCall)
	}
	if finish != "tool_use" {
		t.Fatalf("finish = %q, want tool_use", finish)
	}
	if usage == nil || usage.PromptTokens != 11 || usage.CachedInputTokens != 4 || usage.CompletionTokens != 7 || usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v, want anthropic usage", usage)
	}
	if replayToken != "sig-final" {
		t.Fatalf("replay token = %q, want sig-final", replayToken)
	}
	system := payload["system"].([]any)
	if system[0].(map[string]any)["text"] != "system instruction" {
		t.Fatalf("system = %#v, want text block", system)
	}
	messages := payload["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v, want user/assistant/tool-result", messages)
	}
	assistantContent := messages[1].(map[string]any)["content"].([]any)
	thinking := assistantContent[0].(map[string]any)
	if thinking["type"] != "thinking" || thinking["signature"] != "sig-prev" || thinking["thinking"] != "prior reasoning" {
		t.Fatalf("assistant content = %#v, want thinking replay", assistantContent)
	}
	if tools := payload["tools"].([]any); tools[0].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tools = %#v, want lookup", tools)
	}
}

func TestAnthropicStreamMapsThinkingSignatureAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"I should think first. \"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig-stream\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"Hello \"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"world\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"cache_read_input_tokens\":4}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	p := NewAnthropic(AnthropicConfig{Model: "claude-sonnet-4-5", Token: "sk-test", BaseURL: server.URL, HTTPClient: server.Client()})
	var text, reasoning string
	var usage *model.Usage
	var replayToken string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hello"}}}},
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
		if token, _ := evt.Metadata["replay_token"].(string); token != "" {
			replayToken = token
		}
	}
	if text != "Hello world" || reasoning != "I should think first. " {
		t.Fatalf("text/reasoning = %q/%q, want streamed blocks", text, reasoning)
	}
	if replayToken != "sig-stream" {
		t.Fatalf("replay token = %q, want sig-stream", replayToken)
	}
	if usage == nil || usage.TotalTokens != 18 {
		t.Fatalf("usage = %+v, want total 18", usage)
	}
}

func TestDiscoverAnthropicModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-test" {
			t.Fatalf("x-api-key = %q, want sk-test", got)
		}
		_, _ = fmt.Fprint(w, `{"data":[{"id":"claude-sonnet-4-5","context_window":200000,"max_output_tokens":8192,"capabilities":["tools","vision"]}]}`)
	}))
	defer server.Close()

	models, err := DiscoverAnthropicModels(context.Background(), AnthropicConfig{BaseURL: server.URL + "/v1", Token: "sk-test", HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("DiscoverAnthropicModels() error = %v", err)
	}
	if len(models) != 1 || models[0].Name != "claude-sonnet-4-5" || models[0].ContextWindowTokens != 200000 {
		t.Fatalf("models = %#v, want claude metadata", models)
	}
}

func TestConfiguredFactoryResolvesAnthropic(t *testing.T) {
	factory, err := NewConfiguredFactory([]ModelConfig{{Provider: "anthropic", Model: "claude-sonnet-4-5", Token: "sk-test"}})
	if err != nil {
		t.Fatalf("NewConfiguredFactory() error = %v", err)
	}
	llm, err := factory.New(model.Ref{ModelID: "anthropic/claude-sonnet-4-5"})
	if err != nil {
		t.Fatalf("factory.New() error = %v", err)
	}
	if !strings.EqualFold(llm.Name(), "claude-sonnet-4-5") {
		t.Fatalf("Name() = %q, want claude-sonnet-4-5", llm.Name())
	}
}
