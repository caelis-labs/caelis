package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
)

func TestProviderStreamSendsMessagesRequestAndParsesToolUse(t *testing.T) {
	var captured messagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "anthropic-token" {
			t.Fatalf("x-api-key = %q, want token", got)
		}
		if got := r.Header.Get("anthropic-version"); got != defaultAPIVersion {
			t.Fatalf("anthropic-version = %q, want default", got)
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
			"id":"msg_1",
			"type":"message",
			"role":"assistant",
			"model":"claude-test",
			"stop_reason":"tool_use",
			"content":[
				{"type":"thinking","thinking":"checking","signature":"sig-1"},
				{"type":"text","text":"I will run it."},
				{"type":"tool_use","id":"call-1","name":"run_command","input":{"command":"printf hello"}}
			],
			"usage":{
				"input_tokens":11,
				"cache_read_input_tokens":3,
				"output_tokens":5
			}
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{
		BaseURL:         server.URL + "/v1",
		APIKey:          "anthropic-token",
		Model:           "claude-test",
		MaxOutputTokens: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Instructions: []string{"You are concise."},
		Messages: []model.Message{{
			Role: model.RoleSystem,
			Parts: []model.Part{
				model.NewTextPart("Project policy."),
			},
		}, {
			Role: model.RoleUser,
			Parts: []model.Part{
				model.NewTextPart("run it"),
				{
					Kind: model.PartMedia,
					Media: &model.MediaPart{
						Modality: model.MediaImage,
						MimeType: "image/png",
						Source:   model.MediaSource{Kind: model.MediaInline, Data: "base64-image"},
					},
				},
			},
		}},
		Tools: []model.ToolSpec{model.NewFunctionToolSpec("run_command", "run shell", map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
		})},
		Reasoning: model.ReasoningConfig{Effort: "medium"},
		Stream:    true,
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
	if got := event.Response.Message.TextContent(); got != "I will run it." {
		t.Fatalf("assistant text = %q", got)
	}
	calls := event.Response.Message.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "run_command" || string(calls[0].Input) != `{"command":"printf hello"}` {
		t.Fatalf("tool calls = %#v", calls)
	}
	if event.Response.Usage == nil || event.Response.Usage.InputTokens != 11 || event.Response.Usage.CachedInputTokens != 3 || event.Response.Usage.TotalTokens != 16 {
		t.Fatalf("usage = %#v, want parsed token usage", event.Response.Usage)
	}
	if len(event.Response.Message.Parts) == 0 || event.Response.Message.Parts[0].Reasoning == nil ||
		event.Response.Message.Parts[0].Reasoning.Replay == nil ||
		event.Response.Message.Parts[0].Reasoning.Replay.Kind != replayKindThinkingSignature {
		t.Fatalf("reasoning part = %#v, want replay signature", event.Response.Message.Parts)
	}

	if captured.Model != "claude-test" || captured.MaxTokens != 2048 {
		t.Fatalf("captured model/max = %q/%d", captured.Model, captured.MaxTokens)
	}
	if captured.System != "You are concise.\n\nProject policy." {
		t.Fatalf("system = %q", captured.System)
	}
	if len(captured.Messages) != 1 || captured.Messages[0].Role != "user" || len(captured.Messages[0].Content) != 2 {
		t.Fatalf("messages = %#v", captured.Messages)
	}
	if captured.Messages[0].Content[1].Source == nil || captured.Messages[0].Content[1].Source.Type != "base64" {
		t.Fatalf("image block = %#v", captured.Messages[0].Content[1])
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Name != "run_command" {
		t.Fatalf("tools = %#v", captured.Tools)
	}
	if captured.Thinking == nil || captured.Thinking.BudgetTokens != 4096 {
		t.Fatalf("thinking = %#v, want medium budget", captured.Thinking)
	}
}

func TestProviderStreamSendsToolResultMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured messagesRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		if len(captured.Messages) != 3 {
			t.Fatalf("messages = %d, want user, assistant tool call, tool result", len(captured.Messages))
		}
		if captured.Messages[1].Role != "assistant" || len(captured.Messages[1].Content) != 1 ||
			captured.Messages[1].Content[0].Type != "tool_use" {
			t.Fatalf("assistant tool-use message = %#v", captured.Messages[1])
		}
		if captured.Messages[2].Role != "user" || len(captured.Messages[2].Content) != 1 {
			t.Fatalf("tool result message = %#v", captured.Messages[2])
		}
		result := captured.Messages[2].Content[0]
		if result.Type != "tool_result" || result.ToolUseID != "call-1" || len(result.Content) != 1 ||
			result.Content[0].Text != "hello" {
			t.Fatalf("tool result block = %#v", result)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_2",
			"type":"message",
			"role":"assistant",
			"model":"claude-test",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"done"}]
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL, Model: "claude-test"})
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
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-a","display_name":"Claude A"},{"id":"claude-b"}]}`))
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
	if len(models) != 2 || models[0].ID != "claude-a" || models[0].Name != "Claude A" || !models[0].SupportsToolCalls {
		t.Fatalf("models = %#v", models)
	}
}

func TestProviderAuthorizationHeaderUsesBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer minimax-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id":"msg_3",
			"type":"message",
			"role":"assistant",
			"model":"MiniMax-M1",
			"stop_reason":"end_turn",
			"content":[{"type":"text","text":"minimax pong"}]
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{
		ID:         "minimax",
		BaseURL:    server.URL,
		APIKey:     "minimax-token",
		AuthHeader: "Authorization",
		Model:      "MiniMax-M1",
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("ping")}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	event, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if got := event.Response.Message.TextContent(); got != "minimax pong" {
		t.Fatalf("assistant text = %q, want minimax pong", got)
	}
}
