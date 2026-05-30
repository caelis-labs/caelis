package gemini

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

func TestProviderStreamSendsGenerateContentRequestAndParsesToolCall(t *testing.T) {
	var captured generateContentRequest
	var acceptHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
			t.Fatalf("path = %q, want streamGenerateContent endpoint", r.URL.Path)
		}
		if got := r.URL.Query().Get("alt"); got != "sse" {
			t.Fatalf("alt = %q, want sse", got)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "gemini-token" {
			t.Fatalf("x-goog-api-key = %q, want token", got)
		}
		acceptHeader = r.Header.Get("Accept")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{
				"finishReason":"STOP",
				"content":{
					"role":"model",
					"parts":[
						{"text":"checking","thought":true},
						{"text":"I will run it."},
						{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"printf hello"}},"thoughtSignature":"c2lnLWNhbGwtMQ=="}
					]
				}
			}],
			"usageMetadata":{
				"promptTokenCount":9,
				"cachedContentTokenCount":2,
				"candidatesTokenCount":4,
				"thoughtsTokenCount":3,
				"totalTokenCount":16
			}
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{
		BaseURL:         server.URL + "/v1beta",
		APIKey:          "gemini-token",
		Model:           "gemini-2.5-flash",
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
		Reasoning: model.ReasoningConfig{Effort: "high"},
		Output: &model.OutputSpec{
			Mode:       model.OutputSchema,
			JSONSchema: map[string]any{"type": "object"},
		},
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
	if got := event.Response.Message.TextContent(); got != "I will run it." {
		t.Fatalf("assistant text = %q", got)
	}
	if event.Response.Usage == nil || event.Response.Usage.InputTokens != 9 ||
		event.Response.Usage.CachedInputTokens != 2 ||
		event.Response.Usage.OutputTokens != 4 ||
		event.Response.Usage.ReasoningTokens != 3 ||
		event.Response.Usage.TotalTokens != 16 {
		t.Fatalf("usage = %#v, want parsed Gemini usage", event.Response.Usage)
	}
	calls := event.Response.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Name != "run_command" ||
		string(calls[0].Input) != `{"command":"printf hello"}` ||
		calls[0].Replay == nil || calls[0].Replay.Kind != replayKindThoughtSignature ||
		calls[0].Replay.Token != "b64:c2lnLWNhbGwtMQ==" {
		t.Fatalf("tool calls = %#v", calls)
	}

	if captured.SystemInstruction == nil || captured.SystemInstruction.Parts[0].Text != "You are concise.\n\nProject policy." {
		t.Fatalf("systemInstruction = %#v", captured.SystemInstruction)
	}
	if acceptHeader != streamAcceptValue {
		t.Fatalf("accept = %q, want %q", acceptHeader, streamAcceptValue)
	}
	if len(captured.Contents) != 1 || captured.Contents[0].Role != "user" || len(captured.Contents[0].Parts) != 2 {
		t.Fatalf("contents = %#v", captured.Contents)
	}
	if captured.Contents[0].Parts[1].InlineData == nil || captured.Contents[0].Parts[1].InlineData.MIMEType != "image/png" {
		t.Fatalf("image part = %#v", captured.Contents[0].Parts[1])
	}
	if captured.GenerationConfig == nil || captured.GenerationConfig.MaxOutputTokens != 2048 ||
		captured.GenerationConfig.ResponseMIME != "application/json" ||
		captured.GenerationConfig.ResponseSchema["type"] != "object" ||
		captured.GenerationConfig.ThinkingConfig == nil ||
		captured.GenerationConfig.ThinkingConfig.ThinkingBudget == nil ||
		*captured.GenerationConfig.ThinkingConfig.ThinkingBudget != 8192 {
		t.Fatalf("generationConfig = %#v", captured.GenerationConfig)
	}
	if len(captured.Tools) != 1 || len(captured.Tools[0].FunctionDeclarations) != 1 ||
		captured.Tools[0].FunctionDeclarations[0].Name != "run_command" {
		t.Fatalf("tools = %#v", captured.Tools)
	}
}

func TestProviderStreamParsesSSE(t *testing.T) {
	var captured generateContentRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
			t.Fatalf("path = %q, want streamGenerateContent endpoint", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"modelVersion":"gemini-2.5-flash","candidates":[{"content":{"role":"model","parts":[{"text":"checking","thought":true}]}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"I will run it."}]}}]}` + "\n\n"))
		_, _ = w.Write([]byte(`data: {"candidates":[{"finishReason":"STOP","content":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"run_command","args":{"command":"printf hello"}},"thoughtSignature":"c2lnLWNhbGwtMQ=="}]}}],"usageMetadata":{"promptTokenCount":9,"cachedContentTokenCount":2,"candidatesTokenCount":4,"thoughtsTokenCount":3,"totalTokenCount":16}}` + "\n\n"))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL + "/v1beta", Model: "gemini-2.5-flash"})
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
	if len(captured.Contents) != 1 || captured.Contents[0].Role != "user" {
		t.Fatalf("captured request = %#v", captured)
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
	if final.Usage == nil || final.Usage.InputTokens != 9 ||
		final.Usage.CachedInputTokens != 2 ||
		final.Usage.OutputTokens != 4 ||
		final.Usage.ReasoningTokens != 3 ||
		final.Usage.TotalTokens != 16 {
		t.Fatalf("usage = %#v, want streamed usage", final.Usage)
	}
	calls := final.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call-1" || calls[0].Name != "run_command" ||
		string(calls[0].Input) != `{"command":"printf hello"}` ||
		calls[0].Replay == nil || calls[0].Replay.Token != "b64:c2lnLWNhbGwtMQ==" {
		t.Fatalf("tool calls = %#v, want streamed tool call", calls)
	}
	if final.Origin == nil || final.Origin.Model != "gemini-2.5-flash" ||
		final.Origin.RawFinishReason != "STOP" {
		t.Fatalf("origin = %#v, want streamed origin", final.Origin)
	}
}

func TestProviderStreamSendsFunctionResponseWithThoughtSignature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var captured generateContentRequest
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		if len(captured.Contents) != 3 {
			t.Fatalf("contents = %d, want user, model function call, function response", len(captured.Contents))
		}
		callPart := captured.Contents[1].Parts[0]
		if captured.Contents[1].Role != "model" || callPart.FunctionCall == nil ||
			callPart.FunctionCall.ID != "call-1" || callPart.ThoughtSignature != "c2lnLWNhbGwtMQ==" {
			t.Fatalf("model function call part = %#v", callPart)
		}
		responsePart := captured.Contents[2].Parts[0]
		if captured.Contents[2].Role != "user" || responsePart.FunctionResponse == nil ||
			responsePart.FunctionResponse.ID != "call-1" ||
			responsePart.FunctionResponse.Name != "run_command" ||
			responsePart.FunctionResponse.Response["output"] != "hello" {
			t.Fatalf("function response part = %#v", responsePart)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"modelVersion":"gemini-2.5-flash",
			"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]}}]
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL + "/v1beta", Model: "gemini-2.5-flash"})
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
				Replay: &model.ReplayMeta{
					Provider: "gemini",
					Kind:     replayKindThoughtSignature,
					Token:    "b64:c2lnLWNhbGwtMQ==",
				},
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
		if r.URL.Path != "/v1beta/models" {
			t.Fatalf("path = %q, want /v1beta/models", r.URL.Path)
		}
		if got := r.Header.Get("x-goog-api-key"); got != "gemini-token" {
			t.Fatalf("x-goog-api-key = %q, want token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"models":[{
				"name":"models/gemini-2.5-flash",
				"displayName":"Gemini 2.5 Flash",
				"inputTokenLimit":1048576,
				"outputTokenLimit":65536,
				"supportedGenerationMethods":["generateContent"]
			}]
		}`))
	}))
	defer server.Close()

	provider, err := New(Config{BaseURL: server.URL + "/v1beta", APIKey: "gemini-token"})
	if err != nil {
		t.Fatal(err)
	}
	models, err := provider.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "gemini-2.5-flash" ||
		models[0].ContextWindowTokens != 1048576 ||
		models[0].MaxOutputTokens != 65536 ||
		!models[0].SupportsToolCalls || !models[0].SupportsJSON {
		t.Fatalf("models = %#v", models)
	}
}
