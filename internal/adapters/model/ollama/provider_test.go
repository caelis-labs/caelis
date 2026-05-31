package ollama

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
)

func TestProviderStreamSendsChatRequestAndParsesToolCall(t *testing.T) {
	var captured chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Fatalf("path = %q, want /api/chat", r.URL.Path)
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
			"model":"llama3.1",
			"message":{
				"role":"assistant",
				"content":"tool needed",
				"thinking":"short reasoning",
				"tool_calls":[{
					"function":{"name":"run_command","arguments":{"command":"printf hello"}}
				}]
			},
			"done":true,
			"prompt_eval_count":8,
			"eval_count":5
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{
		BaseURL:         server.URL + "/v1",
		Model:           "llama3.1",
		MaxOutputTokens: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Instructions: []string{"You are concise."},
		Messages: []model.Message{{
			Role: model.RoleUser,
			Parts: []model.Part{
				model.NewTextPart("run it"),
				{
					Kind: model.PartMedia,
					Media: &model.MediaPart{
						Modality: model.MediaImage,
						Source:   model.MediaSource{Kind: model.MediaInline, Data: "base64-image"},
					},
				},
			},
		}},
		Tools: []model.ToolSpec{model.NewFunctionToolSpec("run_command", "run shell", map[string]any{
			"type": "object",
		})},
		Output: &model.OutputSpec{Mode: model.OutputJSON},
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
	if got := event.Response.Message.TextContent(); got != "tool needed" {
		t.Fatalf("assistant text = %q, want tool needed", got)
	}
	if len(event.Response.Message.Parts) < 2 ||
		event.Response.Message.Parts[1].Kind != model.PartReasoning ||
		event.Response.Message.Parts[1].Reasoning == nil ||
		event.Response.Message.Parts[1].Reasoning.VisibleText != "short reasoning" {
		t.Fatalf("assistant parts = %#v, want reasoning part", event.Response.Message.Parts)
	}
	calls := event.Response.Message.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "run_command" || string(calls[0].Input) != `{"command":"printf hello"}` {
		t.Fatalf("tool calls = %#v", calls)
	}
	if event.Response.Usage == nil || event.Response.Usage.InputTokens != 8 || event.Response.Usage.OutputTokens != 5 || event.Response.Usage.TotalTokens != 13 {
		t.Fatalf("usage = %#v, want parsed usage", event.Response.Usage)
	}

	if captured.Model != "llama3.1" || captured.Stream {
		t.Fatalf("captured model/stream = %q/%v, want llama3.1/false", captured.Model, captured.Stream)
	}
	if len(captured.Messages) != 2 || captured.Messages[0].Role != "system" || captured.Messages[1].Role != "user" {
		t.Fatalf("captured messages = %#v", captured.Messages)
	}
	if captured.Messages[1].Content != "run it" || len(captured.Messages[1].Images) != 1 || captured.Messages[1].Images[0] != "base64-image" {
		t.Fatalf("captured user message = %#v", captured.Messages[1])
	}
	if len(captured.Tools) != 1 || captured.Tools[0].Function.Name != "run_command" {
		t.Fatalf("captured tools = %#v", captured.Tools)
	}
	if captured.Format != "json" {
		t.Fatalf("format = %#v, want json", captured.Format)
	}
	if captured.Think == nil || *captured.Think {
		t.Fatalf("think = %#v, want false for JSON output", captured.Think)
	}
	if captured.Options == nil || captured.Options.NumPredict != 128 {
		t.Fatalf("options = %#v, want num_predict 128", captured.Options)
	}
}

func TestChatToolsPassProviderToolPayloads(t *testing.T) {
	tools := chatTools([]model.ToolSpec{
		model.NewFunctionToolSpec("run_command", "run shell", map[string]any{"type": "object"}),
		model.NewProviderDefinedToolSpec("native_tool", map[string]json.RawMessage{
			"ollama": json.RawMessage(`{"type":"native_tool","native_tool":{"name":"search"}}`),
		}),
	})
	body, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded) != 2 || decoded[0]["type"] != "function" || decoded[1]["type"] != "native_tool" {
		t.Fatalf("tools = %#v, want function and provider payload", decoded)
	}
}

func TestProviderStreamsChatResponse(t *testing.T) {
	var captured chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = w.Write([]byte(`{"model":"llama3.1","message":{"role":"assistant","content":"hello "},"done":false}` + "\n"))
		_, _ = w.Write([]byte(`{"model":"llama3.1","message":{"role":"assistant","content":"world","thinking":"think"},"done":false}` + "\n"))
		_, _ = w.Write([]byte(`{"model":"llama3.1","message":{"role":"assistant","tool_calls":[{"function":{"name":"run_command","arguments":{"command":"echo hi"}}}]},"done":true,"prompt_eval_count":4,"eval_count":3}` + "\n"))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL, Model: "llama3.1"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("hi")}}},
		Tools:    []model.ToolSpec{model.NewFunctionToolSpec("run_command", "run shell", map[string]any{"type": "object"})},
		Stream:   true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	var deltas []string
	var final *model.Response
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Type == model.StreamPartDelta {
			deltas = append(deltas, event.Delta)
		}
		if event.Response != nil {
			final = event.Response
		}
	}
	if !captured.Stream {
		t.Fatalf("captured stream = false, want true")
	}
	if strings.Join(deltas, "") != "hello worldthink" {
		t.Fatalf("deltas = %#v, want streamed text and reasoning", deltas)
	}
	if final == nil {
		t.Fatal("final response = nil")
	}
	if got := final.Message.TextContent(); got != "hello world" {
		t.Fatalf("final text = %q, want hello world", got)
	}
	var reasoning string
	for _, part := range final.Message.Parts {
		if part.Kind == model.PartReasoning && part.Reasoning != nil {
			reasoning = part.Reasoning.VisibleText
		}
	}
	if reasoning != "think" {
		t.Fatalf("final parts = %#v, want reasoning", final.Message.Parts)
	}
	calls := final.Message.ToolCalls()
	if len(calls) != 1 || calls[0].Name != "run_command" || string(calls[0].Input) != `{"command":"echo hi"}` {
		t.Fatalf("tool calls = %#v, want streamed tool call", calls)
	}
	if final.Usage == nil || final.Usage.TotalTokens != 7 {
		t.Fatalf("usage = %#v, want streamed usage", final.Usage)
	}
}

func TestProviderStreamSendsToolResultMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured chatRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		if len(captured.Messages) != 3 {
			t.Fatalf("messages = %d, want user, assistant tool call, tool result", len(captured.Messages))
		}
		if captured.Messages[1].Role != "assistant" || len(captured.Messages[1].ToolCalls) != 1 {
			t.Fatalf("assistant tool-call message = %#v", captured.Messages[1])
		}
		if captured.Messages[2].Role != "tool" || captured.Messages[2].ToolName != "run_command" {
			t.Fatalf("tool result message = %#v", captured.Messages[2])
		}
		if captured.Messages[2].Content != "hello" {
			t.Fatalf("tool result content = %#v, want hello", captured.Messages[2].Content)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"model":"llama3.1",
			"message":{"role":"assistant","content":"done"},
			"done":true
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL, Model: "llama3.1"})
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
		if r.URL.Path != "/api/tags" {
			t.Fatalf("path = %q, want /api/tags", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"llama3.1"},{"name":"qwen2.5"}]}`))
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
	if len(models) != 2 || models[0].ID != "llama3.1" || models[0].Provider != "ollama" || !models[0].SupportsToolCalls {
		t.Fatalf("models = %#v", models)
	}
}

func TestProviderNormalizesInvalidToolArguments(t *testing.T) {
	tests := []struct {
		name string
		args string
		want string
	}{
		{name: "invalid", args: `not-json`, want: `{"raw":"not-json"}`},
		{name: "quoted", args: `"{\"command\":\"echo hi\"}"`, want: `{"command":"echo hi"}`},
		{name: "fenced", args: "```json\n{\"command\":\"echo hi\"}\n```", want: `{"command":"echo hi"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, err := coreMessageFromChat(chatMessage{ToolCalls: []toolCall{{
				Function: toolCallFunction{
					Name:      "run_command",
					Arguments: json.RawMessage(test.args),
				},
			}}})
			if err != nil {
				t.Fatal(err)
			}
			calls := message.ToolCalls()
			if len(calls) != 1 || !json.Valid(calls[0].Input) || string(calls[0].Input) != test.want {
				t.Fatalf("tool input = %s, want %s", string(calls[0].Input), test.want)
			}
		})
	}
}
