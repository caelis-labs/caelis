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

func TestNewMiniMaxUsesAnthropicCompatibleDefaults(t *testing.T) {
	p := NewMiniMax(MiniMaxConfig{Model: "MiniMax-M2", Token: "token"})
	if p.Name() != "MiniMax-M2" {
		t.Fatalf("Name() = %q, want MiniMax-M2", p.Name())
	}
	if p.provider != "minimax" {
		t.Fatalf("provider = %q, want minimax", p.provider)
	}
	if p.baseURL != defaultMiniMaxBaseURL {
		t.Fatalf("baseURL = %q, want %q", p.baseURL, defaultMiniMaxBaseURL)
	}
	if p.maxOutputTokens != defaultMiniMaxMaxOutputTokens {
		t.Fatalf("maxOutputTokens = %d, want %d", p.maxOutputTokens, defaultMiniMaxMaxOutputTokens)
	}
}

func TestMiniMaxRequestPayloadAndHeaders(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-minimax-api-key"); got != "token" {
			t.Fatalf("x-minimax-api-key = %q, want token", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("anthropic-version header is empty")
		}
		if got := r.Header.Get("x-extra"); got != "1" {
			t.Fatalf("x-extra = %q, want configured header", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"model\":\"MiniMax-M2\",\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	p := NewMiniMax(MiniMaxConfig{
		Model:      "MiniMax-M2",
		Token:      "token",
		BaseURL:    server.URL + "/v1",
		HTTPClient: server.Client(),
		Headers:    map[string]string{"x-extra": "1"},
	})
	for _, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{
			{Role: model.RoleSystem, Content: []model.Part{{Text: "system instruction"}}},
			{Role: model.RoleUser, Content: []model.Part{{Text: "hello"}}},
			{Role: model.RoleAssistant, Content: []model.Part{
				{Reasoning: &model.Reasoning{
					Text:       "prior reasoning",
					Visibility: model.ReasoningVisibilityVisible,
					Replay:     &model.ReplayMeta{Provider: "minimax", Kind: anthropicReplayKindThinkingSignature, Token: "sig-prev"},
				}},
				{Text: "assistant text"},
				{ToolUse: &model.ToolUse{CallID: "call-prev", Name: "echo", Args: map[string]any{"text": "x"}}},
			}},
			{Role: model.RoleTool, Content: []model.Part{{ToolResult: &model.ToolResult{CallID: "call-prev", Content: `{"ok":true}`}}}},
		},
		Tools: []model.ToolSpec{{
			Name:        "lookup",
			Description: "lookup things",
			Schema: model.Schema{
				Type: "object",
				Properties: map[string]model.Schema{
					"query": {Type: "string"},
				},
				Required: []string{"query"},
			},
		}},
		Output:    &model.OutputSpec{MaxOutputTokens: 1234},
		Reasoning: model.ReasoningConfig{Effort: "low"},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}

	if payload["model"] != "MiniMax-M2" {
		t.Fatalf("model = %#v, want MiniMax-M2", payload["model"])
	}
	if payload["max_tokens"] != float64(1234) {
		t.Fatalf("max_tokens = %#v, want 1234", payload["max_tokens"])
	}
	thinking := payload["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(1024) {
		t.Fatalf("thinking = %#v, want enabled/1024", thinking)
	}
	system := payload["system"].([]any)
	if system[0].(map[string]any)["text"] != "system instruction" {
		t.Fatalf("system = %#v, want system text", system)
	}
	messages := payload["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %#v, want system filtered and tool result as user message", messages)
	}
	assistant := messages[1].(map[string]any)
	assistantContent := assistant["content"].([]any)
	if assistantContent[0].(map[string]any)["type"] != "thinking" || assistantContent[0].(map[string]any)["signature"] != "sig-prev" {
		t.Fatalf("assistant content = %#v, want thinking replay first", assistantContent)
	}
	if assistantContent[2].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("assistant content = %#v, want tool_use", assistantContent)
	}
	toolMessage := messages[2].(map[string]any)
	toolContent := toolMessage["content"].([]any)
	if toolContent[0].(map[string]any)["type"] != "tool_result" || toolContent[0].(map[string]any)["tool_use_id"] != "call-prev" {
		t.Fatalf("tool message = %#v, want tool_result", toolMessage)
	}
	tools := payload["tools"].([]any)
	if tools[0].(map[string]any)["name"] != "lookup" {
		t.Fatalf("tools = %#v, want lookup declaration", tools)
	}
}

func TestMiniMaxStreamMapsAnthropicEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"MiniMax-M2.5\",\"content\":[],\"stop_reason\":\"\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"MiniMax streaming \"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"should stream.\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"Plan.\",\"signature\":\"sig-start\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\" Continue.\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig-final\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":2,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_1\",\"name\":\"lookup\",\"input\":{\"q\":\"weather\"}}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":12,\"cache_read_input_tokens\":4}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	p := NewMiniMax(MiniMaxConfig{Model: "MiniMax-M2.5", Token: "token", BaseURL: server.URL, HTTPClient: server.Client()})
	var text, reasoning string
	var toolCall *model.ToolCallDelta
	var usage *model.Usage
	var finish string
	var replayToken string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hello"}}}},
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
		if evt.Metadata != nil {
			if token, _ := evt.Metadata["replay_token"].(string); token != "" {
				replayToken = token
			}
		}
	}
	if text != "MiniMax streaming should stream." {
		t.Fatalf("text = %q, want streamed text including start block", text)
	}
	if reasoning != "Plan. Continue." {
		t.Fatalf("reasoning = %q, want thinking stream", reasoning)
	}
	if replayToken != "sig-final" {
		t.Fatalf("replay token = %q, want sig-final", replayToken)
	}
	if toolCall == nil || toolCall.CallID != "call_1" || toolCall.Name != "lookup" || toolCall.Args["q"] != "weather" {
		t.Fatalf("toolCall = %#v, want lookup weather", toolCall)
	}
	if usage == nil || usage.PromptTokens != 11 || usage.CachedInputTokens != 4 || usage.CompletionTokens != 12 || usage.TotalTokens != 23 {
		t.Fatalf("usage = %+v, want anthropic-compatible usage", usage)
	}
	if finish != "tool_use" {
		t.Fatalf("finish = %q, want tool_use", finish)
	}
}

func TestConfiguredFactoryResolvesMiniMax(t *testing.T) {
	factory, err := NewConfiguredFactory([]ModelConfig{{Provider: "minimax", Model: "MiniMax-M2", Token: "token"}})
	if err != nil {
		t.Fatalf("NewConfiguredFactory() error = %v", err)
	}
	llm, err := factory.New(model.Ref{ModelID: "minimax/MiniMax-M2"})
	if err != nil {
		t.Fatalf("factory.New() error = %v", err)
	}
	if !strings.EqualFold(llm.Name(), "MiniMax-M2") {
		t.Fatalf("Name() = %q, want MiniMax-M2", llm.Name())
	}
}
