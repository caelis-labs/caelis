package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
)

func TestProviderStreamSendsChatCompletionRequestAndParsesToolCall(t *testing.T) {
	var captured chatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"chatcmpl-1",
			"model":"gpt-test",
			"created":1700000000,
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"tool_calls":[{
						"id":"call-1",
						"type":"function",
						"function":{"name":"run_command","arguments":"{\"command\":\"printf hello\"}"}
					}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{
				"prompt_tokens":10,
				"completion_tokens":4,
				"total_tokens":14,
				"prompt_tokens_details":{"cached_tokens":2},
				"completion_tokens_details":{"reasoning_tokens":1}
			}
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-token",
		Model:   "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Instructions: []string{"You are concise."},
		Messages: []model.Message{{
			Role:  model.RoleUser,
			Parts: []model.Part{model.NewTextPart("run it")},
		}},
		Tools: []model.ToolSpec{model.NewFunctionToolSpec("run_command", "run shell", map[string]any{
			"type": "object",
		})},
		Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if event.Type != model.StreamTurnDone || event.Response == nil {
		t.Fatalf("event = %#v, want final response", event)
	}
	calls := event.Response.Message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	if calls[0].Name != "run_command" || string(calls[0].Input) != `{"command":"printf hello"}` {
		t.Fatalf("tool call = %#v", calls[0])
	}
	if event.Response.Usage == nil || event.Response.Usage.TotalTokens != 14 || event.Response.Usage.CachedInputTokens != 2 {
		t.Fatalf("usage = %#v, want parsed usage", event.Response.Usage)
	}

	if captured.Model != "gpt-test" || captured.Stream {
		t.Fatalf("captured model/stream = %q/%v, want gpt-test/false", captured.Model, captured.Stream)
	}
	if len(captured.Messages) != 2 || captured.Messages[0].Role != "system" || captured.Messages[1].Role != "user" {
		t.Fatalf("captured messages = %#v", captured.Messages)
	}
	if got, ok := captured.Messages[1].Content.(string); !ok || got != "run it" {
		t.Fatalf("user content = %#v, want text string", captured.Messages[1].Content)
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Function.Name != "run_command" {
		t.Fatalf("captured tools = %#v", captured.Tools)
	}
}

func TestProviderStreamSendsToolResultMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		if len(captured.Messages) != 3 {
			t.Fatalf("messages = %d, want user, assistant tool call, tool result", len(captured.Messages))
		}
		if captured.Messages[1].Role != "assistant" || len(captured.Messages[1].ToolCalls) != 1 {
			t.Fatalf("assistant tool-call message = %#v", captured.Messages[1])
		}
		if captured.Messages[2].Role != "tool" || captured.Messages[2].ToolCallID != "call-1" {
			t.Fatalf("tool result message = %#v", captured.Messages[2])
		}
		if captured.Messages[2].Content != "hello" {
			t.Fatalf("tool result content = %#v, want hello", captured.Messages[2].Content)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"gpt-test",
			"choices":[{
				"message":{"role":"assistant","content":"done"},
				"finish_reason":"stop"
			}]
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL, Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{Messages: []model.Message{
		{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("run")}},
		{Role: model.RoleAssistant, Parts: []model.Part{{
			Kind: model.PartToolUse,
			ToolUse: &model.ToolCall{
				ID:    "call-1",
				Name:  "run_command",
				Input: json.RawMessage(`{"command":"printf hello"}`),
			},
		}}},
		{Role: model.RoleTool, Parts: []model.Part{{
			Kind: model.PartToolResult,
			ToolResult: &model.ToolResultPart{
				ToolCallID: "call-1",
				Name:       "run_command",
				Content:    []model.Part{model.NewTextPart("hello")},
			},
		}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got := event.Response.Message.TextContent(); got != "done" {
		t.Fatalf("assistant text = %q, want done", got)
	}
}

func TestProviderModelsListsRemoteModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "gpt-a" || !models[0].SupportsToolCalls {
		t.Fatalf("models = %#v", models)
	}
}

func TestProviderDeepSeekProfileSendsReasoningAndParsesReasoningContent(t *testing.T) {
	var captured chatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer deepseek-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"deepseek-v4-pro",
			"choices":[{
				"message":{
					"role":"assistant",
					"content":"answer",
					"reasoning_content":"thinking"
				},
				"finish_reason":"stop"
			}],
			"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{
		ID:              "deepseek",
		BaseURL:         server.URL,
		APIKey:          "deepseek-token",
		Model:           "deepseek-v4-pro",
		MaxOutputTokens: 128,
		Flavor:          FlavorDeepSeek,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("hi")}}},
		Reasoning: model.ReasoningConfig{
			Effort: "max",
		},
		Output: &model.OutputSpec{Mode: model.OutputJSON},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got := event.Response.Message.TextContent(); got != "answer" {
		t.Fatalf("assistant text = %q, want answer", got)
	}
	if len(event.Response.Message.Parts) < 2 ||
		event.Response.Message.Parts[1].Kind != model.PartReasoning ||
		event.Response.Message.Parts[1].Reasoning == nil ||
		event.Response.Message.Parts[1].Reasoning.VisibleText != "thinking" {
		t.Fatalf("assistant parts = %#v, want reasoning content", event.Response.Message.Parts)
	}
	if captured.Thinking == nil || captured.Thinking.Type != "enabled" || captured.ReasoningEffort != "max" {
		t.Fatalf("captured reasoning = thinking:%#v effort:%q", captured.Thinking, captured.ReasoningEffort)
	}
	if captured.MaxTokens != deepSeekThinkingMinTokens {
		t.Fatalf("max_tokens = %d, want DeepSeek thinking minimum", captured.MaxTokens)
	}
	if captured.ResponseFormat == nil || captured.ResponseFormat.Type != "json_object" {
		t.Fatalf("response_format = %#v, want json_object", captured.ResponseFormat)
	}
}

func TestProviderOpenRouterProfileSetsAttributionHeaders(t *testing.T) {
	var captured chatCompletionRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer openrouter-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("HTTP-Referer"); got != openRouterReferer {
			t.Fatalf("HTTP-Referer = %q, want Caelis attribution", got)
		}
		if got := r.Header.Get("X-Title"); got != "Caelis" {
			t.Fatalf("X-Title = %q, want Caelis", got)
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Fatal("User-Agent is empty")
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"openrouter/auto",
			"choices":[{"message":{"role":"assistant","content":"ok","reasoning":"route"},"finish_reason":"stop"}]
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{
		ID:      "openrouter",
		BaseURL: server.URL,
		APIKey:  "openrouter-token",
		Model:   "openrouter/openrouter/auto",
		Flavor:  FlavorOpenRouter,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("hi")}}},
		Output: &model.OutputSpec{
			Mode: model.OutputSchema,
			JSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"answer"},
				"properties": map[string]any{
					"answer": map[string]any{"type": "string"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got := event.Response.Message.TextContent(); got != "ok" {
		t.Fatalf("assistant text = %q, want ok", got)
	}
	if captured.Model != "openrouter/auto" {
		t.Fatalf("captured model = %q, want normalized openrouter/auto", captured.Model)
	}
	if captured.ResponseFormat == nil || captured.ResponseFormat.Type != "json_schema" ||
		captured.ResponseFormat.JSONSchema == nil || !captured.ResponseFormat.JSONSchema.Strict {
		t.Fatalf("response_format = %#v, want strict json_schema", captured.ResponseFormat)
	}
}

func TestProviderNormalizesInvalidToolArguments(t *testing.T) {
	message := coreMessageFromChat(chatMessage{ToolCalls: []chatToolCall{{
		ID: "call-1",
		Function: chatToolFunction{
			Name:      "broken",
			Arguments: "not-json",
		},
	}}})
	calls := message.ToolCalls()
	if len(calls) != 1 || !json.Valid(calls[0].Input) {
		t.Fatalf("tool input = %s, want valid canonical JSON", string(calls[0].Input))
	}
}
