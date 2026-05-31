package anthropic

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

func TestProviderStreamSendsMessagesRequestAndParsesToolUse(t *testing.T) {
	var captured messagesRequest
	var acceptHeader string
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
		acceptHeader = r.Header.Get("Accept")
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
	if !captured.Stream || acceptHeader != streamAcceptValue {
		t.Fatalf("stream request = %v accept = %q, want streaming request", captured.Stream, acceptHeader)
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

func TestProviderNormalizesQuotedToolArguments(t *testing.T) {
	provider, err := New(Config{Model: "claude-test", APIKey: "token"})
	if err != nil {
		t.Fatal(err)
	}
	response, err := provider.modelResponse("claude-test", messageResponse{
		Model:      "claude-test",
		StopReason: "tool_use",
		Content: []contentBlockResponse{{
			Type:  "tool_use",
			ID:    "call-1",
			Name:  "run_command",
			Input: json.RawMessage(`"{\"command\":\"echo hi\"}"`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	calls := response.Message.ToolCalls()
	if len(calls) != 1 || string(calls[0].Input) != `{"command":"echo hi"}` {
		t.Fatalf("tool input = %#v, want decoded argument object", calls)
	}
}

func TestProviderStreamParsesSSE(t *testing.T) {
	var captured messagesRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":11,"cache_read_input_tokens":3}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"checking"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"I will run it."}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_start\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-1","name":"run_command","input":{}}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\""}}` + "\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":":\"echo hi\"}"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}` + "\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte(`data: {"type":"message_stop"}` + "\n\n"))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL, APIKey: "anthropic-token", Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("run it")}}},
		Tools: []model.ToolSpec{model.NewFunctionToolSpec("run_command", "run shell", map[string]any{
			"type": "object",
		})},
		Stream: true,
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
	if strings.Join(deltas, "") != "checkingI will run it." {
		t.Fatalf("deltas = %#v, want reasoning and text deltas", deltas)
	}
	if final == nil {
		t.Fatal("final response = nil")
	}
	if got := final.Message.TextContent(); got != "I will run it." {
		t.Fatalf("assistant text = %q", got)
	}
	calls := final.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Name != "run_command" ||
		string(calls[0].Input) != `{"command":"echo hi"}` {
		t.Fatalf("tool calls = %#v, want streamed tool call", calls)
	}
	if final.Usage == nil || final.Usage.InputTokens != 11 ||
		final.Usage.CachedInputTokens != 3 || final.Usage.OutputTokens != 5 ||
		final.Usage.TotalTokens != 16 {
		t.Fatalf("usage = %#v, want streamed usage", final.Usage)
	}
	if final.Origin == nil || final.Origin.Model != "claude-test" || final.Origin.RawFinishReason != "tool_use" {
		t.Fatalf("origin = %#v, want streamed origin", final.Origin)
	}
	if len(final.Message.Parts) == 0 || final.Message.Parts[0].Reasoning == nil ||
		final.Message.Parts[0].Reasoning.Replay == nil ||
		final.Message.Parts[0].Reasoning.Replay.Token != "sig-1" {
		t.Fatalf("reasoning part = %#v, want streamed signature", final.Message.Parts)
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
