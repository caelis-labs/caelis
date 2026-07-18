package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestOpenAICodexToolLoopPreservesEncryptedReasoning(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var bodies []map[string]any
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("originator"); got != "caelis" {
			t.Errorf("originator = %q, want caelis", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer access-token" {
			t.Errorf("Authorization = %q, want injected bearer", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "account-1" {
			t.Errorf("ChatGPT-Account-Id = %q, want account-1", got)
		}
		if got := r.Header.Get("session-id"); got != "caelis-session-1" {
			t.Errorf("session-id = %q, want caelis-session-1", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if requestCount.Add(1) == 1 {
			writeOpenAICodexSSE(t, w,
				map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "rs_1", "type": "reasoning", "summary": []any{}}},
				map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": "rs_1", "output_index": 0, "summary_index": 0, "delta": "checking"},
				map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"id": "rs_1", "type": "reasoning", "encrypted_content": "encrypted-state", "summary": []any{map[string]any{"type": "summary_text", "text": "checking"}}}},
				map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": ""}},
				map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_1", "output_index": 1, "delta": "{\"query\":"},
				map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_1", "output_index": 1, "delta": "\"weather\"}"},
				map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{\"query\":\"weather\"}"}},
				map[string]any{"type": "response.completed", "response": map[string]any{
					"model": "gpt-5.4", "status": "completed",
					"output": []any{
						map[string]any{"id": "rs_1", "type": "reasoning", "encrypted_content": "encrypted-state", "summary": []any{map[string]any{"type": "summary_text", "text": "checking"}}},
						map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{\"query\":\"weather\"}"},
					},
					"usage": map[string]any{"input_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 5}, "output_tokens": 8, "output_tokens_details": map[string]any{"reasoning_tokens": 3}, "total_tokens": 28},
				}},
			)
			return
		}
		writeOpenAICodexSSE(t, w,
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "msg_2", "type": "message", "content": []any{}}},
			map[string]any{"type": "response.output_text.delta", "item_id": "msg_2", "output_index": 0, "delta": "sunny"},
			map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"id": "msg_2", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "sunny"}}}},
			map[string]any{"type": "response.completed", "response": map[string]any{
				"model":  "gpt-5.4",
				"status": "completed",
				"output": []any{
					map[string]any{"id": "msg_2", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "sunny"}}},
				},
				"usage": map[string]any{"input_tokens": 24, "output_tokens": 2, "total_tokens": 26},
			}},
		)
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = openAICodexTestAuthTransport{base: client.Transport}
	factory := NewFactory()
	if err := factory.Register(Config{
		Alias:      "codex/gpt-5.4",
		Provider:   "openai-codex",
		API:        APIOpenAICodex,
		Model:      "gpt-5.4",
		BaseURL:    server.URL,
		HTTPClient: client,
		Auth:       AuthConfig{Type: AuthOAuthToken},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	llm, err := factory.NewByAlias("codex/gpt-5.4")
	if err != nil {
		t.Fatalf("NewByAlias() error = %v", err)
	}

	user := model.NewMessage(
		model.RoleUser,
		model.NewTextPart("check weather"),
		model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: "aW1n"}, "image/png", "pixel.png"),
	)
	toolSpec := model.NewFunctionToolSpec("lookup", "lookup weather", map[string]any{
		"type":       "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}},
		"required":   []string{"query"},
	})
	first, reasoningDelta, toolDelta, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Instructions: []model.Part{model.NewTextPart("use tools")},
		Messages:     []model.Message{user},
		Tools:        []model.ToolSpec{toolSpec},
		Reasoning:    model.ReasoningConfig{Effort: "high"},
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	if reasoningDelta != "checking" || toolDelta != "{\"query\":\"weather\"}" {
		t.Fatalf("deltas = reasoning %q tool %q", reasoningDelta, toolDelta)
	}
	if first.FinishReason != model.FinishReasonToolCalls {
		t.Fatalf("FinishReason = %q, want tool_calls", first.FinishReason)
	}
	if first.Usage != (model.Usage{PromptTokens: 20, CachedInputTokens: 5, CompletionTokens: 8, ReasoningTokens: 3, TotalTokens: 28}) {
		t.Fatalf("Usage = %+v", first.Usage)
	}
	reasoning := first.Message.ReasoningParts()
	if len(reasoning) != 1 || reasoning[0].Replay == nil || reasoning[0].Replay.Token != "encrypted-state" || reasoning[0].Replay.Kind != openAICodexReplayKind {
		t.Fatalf("reasoning = %#v, want encrypted replay", reasoning)
	}
	calls := first.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Args != "{\"query\":\"weather\"}" {
		t.Fatalf("tool calls = %#v", calls)
	}

	toolResult := model.MessageFromToolResponse(&model.ToolResponse{ID: "call_1", Name: "lookup", Result: map[string]any{"weather": "sunny"}})
	second, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{user, first.Message, toolResult},
		Tools:    []model.ToolSpec{toolSpec},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if second.Message.TextContent() != "sunny" || second.FinishReason != model.FinishReasonStop {
		t.Fatalf("second response = %+v", second)
	}

	mu.Lock()
	gotBodies := append([]map[string]any(nil), bodies...)
	mu.Unlock()
	if len(gotBodies) != 2 {
		t.Fatalf("request bodies = %d, want 2", len(gotBodies))
	}
	assertOpenAICodexFirstRequest(t, gotBodies[0])
	assertOpenAICodexReplayRequest(t, gotBodies[1])
}

func TestOpenAICodexTokenOnlyReasoningAndPrematureEOF(t *testing.T) {
	t.Parallel()

	accumulator := newOpenAICodexAccumulator()
	accumulator.applyItem(openAICodexOutputItem{ID: "rs_1", Type: "reasoning", EncryptedContent: "opaque"}, 0)
	accumulator.applyItem(openAICodexOutputItem{ID: "msg_1", Type: "message", Content: []openAICodexOutputContent{{Type: "output_text", Text: "done"}}}, 1)
	message, err := accumulator.message()
	if err != nil {
		t.Fatalf("message() error = %v", err)
	}
	reasoning := message.ReasoningParts()
	if len(reasoning) != 1 || reasoning[0].Visibility != model.ReasoningVisibilityTokenOnly || reasoning[0].Replay == nil || reasoning[0].Replay.Token != "opaque" {
		t.Fatalf("reasoning = %#v, want token-only replay", reasoning)
	}
	_, inputs, err := openAICodexInputs(nil, []model.Message{message})
	if err != nil {
		t.Fatalf("openAICodexInputs() error = %v", err)
	}
	if len(inputs) != 2 {
		t.Fatalf("len(inputs) = %d, want reasoning + assistant", len(inputs))
	}
	replay, ok := inputs[0].(openAICodexReasoningInput)
	if !ok || replay.EncryptedContent != "opaque" || len(replay.Summary) != 0 {
		t.Fatalf("replay input = %#v", inputs[0])
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeOpenAICodexSSE(t, w, map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "delta": "partial"})
	}))
	defer server.Close()
	llm := newOpenAICodex(Config{Provider: "openai-codex", Model: "gpt-test", BaseURL: server.URL, HTTPClient: server.Client()})
	_, _, _, err = collectOpenAICodexTestResponse(llm, &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")}, Stream: true})
	if err == nil || !strings.Contains(err.Error(), "ended before a terminal response") {
		t.Fatalf("Generate() error = %v, want premature EOF", err)
	}
}

func TestOpenAICodexOutputTextPreservesURLCitations(t *testing.T) {
	t.Parallel()

	accumulator := newOpenAICodexAccumulator()
	accumulator.applyItem(openAICodexOutputItem{
		ID:   "msg-cited",
		Type: "message",
		Content: []openAICodexOutputContent{{
			Type: "output_text",
			Text: "OpenAI docs",
			Annotations: []openAICodexURLAnnotation{{
				Type:       "url_citation",
				URL:        "https://developers.openai.com/api/docs/guides/tools-web-search",
				Title:      "Web search",
				StartIndex: 0,
				EndIndex:   len("OpenAI docs"),
			}},
		}},
	}, 0)
	message, err := accumulator.message()
	if err != nil {
		t.Fatalf("message() error = %v", err)
	}
	citations := message.TextContentCitations()
	if len(citations) != 1 || citations[0].Sources[0].URL != "https://developers.openai.com/api/docs/guides/tools-web-search" {
		t.Fatalf("citations = %#v", citations)
	}
}

func TestOpenAICodexErrorsAreClassified(t *testing.T) {
	t.Parallel()

	t.Run("http auth is terminal", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			http.Error(w, `{"error":"expired"}`, http.StatusUnauthorized)
		}))
		defer server.Close()
		llm := newOpenAICodex(Config{Provider: "openai-codex", Model: "gpt-test", BaseURL: server.URL, HTTPClient: server.Client()})
		_, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")}})
		if !errorcode.Is(err, errorcode.Unauthenticated) || model.IsRetryableLLMError(err) {
			t.Fatalf("error = %v code=%q retryable=%v", err, errorcode.CodeOf(err), model.IsRetryableLLMError(err))
		}
		if requests.Load() != 1 {
			t.Fatalf("requests = %d, want 1", requests.Load())
		}
	})

	t.Run("stream context overflow", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeOpenAICodexSSE(t, w, map[string]any{"type": "response.failed", "response": map[string]any{"error": map[string]any{"code": "context_length_exceeded", "message": "context is too long"}}})
		}))
		defer server.Close()
		llm := newOpenAICodex(Config{Provider: "openai-codex", Model: "gpt-test", BaseURL: server.URL, HTTPClient: server.Client()})
		_, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")}})
		if !model.IsContextOverflow(err) {
			t.Fatalf("error = %v, want context overflow", err)
		}
	})
}

func TestOpenAICodexFactoryRequiresInjectedOAuthClient(t *testing.T) {
	t.Parallel()

	factory := NewFactory()
	if err := factory.Register(Config{Alias: "codex", API: APIOpenAICodex, Model: "gpt-test"}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if _, err := factory.NewByAlias("codex"); err == nil || !strings.Contains(err.Error(), "authenticated http client") {
		t.Fatalf("NewByAlias() error = %v", err)
	}
}

func TestOpenAICodexRequestAffinityBoundsLongSessionID(t *testing.T) {
	t.Parallel()

	if got := openAICodexRequestAffinity("caelis-session-1"); got != "caelis-session-1" {
		t.Fatalf("short key = %q, want unchanged session affinity", got)
	}

	longAffinity := strings.Repeat("s", 28) + "-approval-review-" + strings.Repeat("a", 64)
	if len(longAffinity) != 109 {
		t.Fatalf("test Guardian affinity length = %d, want reported failure length 109", len(longAffinity))
	}
	got := openAICodexRequestAffinity(longAffinity)
	if len(got) != openAICodexRequestAffinityMaxLength {
		t.Fatalf("long key length = %d, want %d", len(got), openAICodexRequestAffinityMaxLength)
	}
	if got != openAICodexRequestAffinity(longAffinity) {
		t.Fatal("long key is not stable")
	}
	if got == openAICodexRequestAffinity(longAffinity+"other-session") {
		t.Fatal("distinct long affinities share a prompt cache key")
	}
}

func TestOpenAICodexLongSessionAffinityIsBoundedOnWire(t *testing.T) {
	t.Parallel()

	longAffinity := strings.Repeat("s", 28) + "-approval-review-" + strings.Repeat("a", 64)
	wantAffinity := openAICodexRequestAffinity(longAffinity)
	var gotHeader string
	var gotPromptCache string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("session-id")
		var body struct {
			PromptCache string `json:"prompt_cache_key"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		gotPromptCache = body.PromptCache
		w.Header().Set("Content-Type", "text/event-stream")
		writeOpenAICodexSSE(t, w, map[string]any{
			"type": "response.completed",
			"response": map[string]any{
				"model": "gpt-test", "status": "completed", "output": []any{},
			},
		})
	}))
	defer server.Close()

	llm := newOpenAICodex(Config{
		Provider:   "openai-codex",
		Model:      "gpt-test",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	ctx := model.WithProviderRequestMetadata(context.Background(), model.ProviderRequestMetadata{SessionAffinity: longAffinity})
	for _, err := range llm.Generate(ctx, &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "review")}}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	if gotPromptCache != wantAffinity {
		t.Fatalf("prompt_cache_key = %q, want bounded affinity %q", gotPromptCache, wantAffinity)
	}
	if gotHeader != wantAffinity {
		t.Fatalf("session-id = %q, want bounded affinity %q", gotHeader, wantAffinity)
	}
}

type openAICodexTestAuthTransport struct {
	base http.RoundTripper
}

func (t openAICodexTestAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	clone.Header.Set("Authorization", "Bearer access-token")
	clone.Header.Set("ChatGPT-Account-Id", "account-1")
	return t.base.RoundTrip(clone)
}

func writeOpenAICodexSSE(t *testing.T, w io.Writer, events ...map[string]any) {
	t.Helper()
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal SSE event: %v", err)
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			t.Fatalf("write SSE event: %v", err)
		}
	}
}

func collectOpenAICodexTestResponse(llm model.LLM, req *model.Request) (*model.Response, string, string, error) {
	var response *model.Response
	var reasoning strings.Builder
	var toolInput strings.Builder
	ctx := model.WithProviderRequestMetadata(context.Background(), model.ProviderRequestMetadata{SessionAffinity: "caelis-session-1"})
	for event, err := range llm.Generate(ctx, req) {
		if err != nil {
			return nil, reasoning.String(), toolInput.String(), err
		}
		if event == nil {
			continue
		}
		if event.PartDelta != nil {
			switch event.PartDelta.Kind {
			case model.PartKindReasoning:
				reasoning.WriteString(event.PartDelta.TextDelta)
			case model.PartKindToolUse:
				toolInput.WriteString(event.PartDelta.InputDelta)
			}
		}
		if event.Response != nil && event.TurnComplete {
			copy := *event.Response
			response = &copy
		}
	}
	if response == nil {
		return nil, reasoning.String(), toolInput.String(), errors.New("missing final response")
	}
	return response, reasoning.String(), toolInput.String(), nil
}

func assertOpenAICodexFirstRequest(t *testing.T, body map[string]any) {
	t.Helper()
	if body["model"] != "gpt-5.4" || body["stream"] != true || body["store"] != false {
		t.Fatalf("request basics = %#v", body)
	}
	if _, ok := body["max_output_tokens"]; ok {
		t.Fatalf("request unexpectedly includes max_output_tokens: %#v", body)
	}
	if !reflect.DeepEqual(body["include"], []any{"reasoning.encrypted_content"}) {
		t.Fatalf("include = %#v", body["include"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "lookup" {
		t.Fatalf("flattened tool = %#v", tool)
	}
	if _, nested := tool["function"]; nested {
		t.Fatalf("tool uses chat-completions shape: %#v", tool)
	}
	input, _ := body["input"].([]any)
	if body["instructions"] != "use tools" {
		t.Fatalf("instructions = %#v, want top-level Codex instructions", body["instructions"])
	}
	if body["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v, want auto", body["tool_choice"])
	}
	if body["prompt_cache_key"] != "caelis-session-1" {
		t.Fatalf("prompt_cache_key = %#v, want session affinity", body["prompt_cache_key"])
	}
	if len(input) != 1 {
		t.Fatalf("input = %#v, want user only", input)
	}
	user, _ := input[0].(map[string]any)
	content, _ := user["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("user content = %#v", content)
	}
	image, _ := content[1].(map[string]any)
	if image["type"] != "input_image" || image["image_url"] != "data:image/png;base64,aW1n" {
		t.Fatalf("image input = %#v", image)
	}
}

func assertOpenAICodexReplayRequest(t *testing.T, body map[string]any) {
	t.Helper()
	input, _ := body["input"].([]any)
	types := make([]string, 0, len(input))
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if value, _ := item["type"].(string); value != "" {
			types = append(types, value)
		} else if role, _ := item["role"].(string); role != "" {
			types = append(types, role)
		}
	}
	if !reflect.DeepEqual(types, []string{"user", "reasoning", "function_call", "function_call_output"}) {
		t.Fatalf("input order = %#v, want user/reasoning/call/output", types)
	}
	reasoning, _ := input[1].(map[string]any)
	if reasoning["encrypted_content"] != "encrypted-state" {
		t.Fatalf("reasoning replay = %#v", reasoning)
	}
	call, _ := input[2].(map[string]any)
	output, _ := input[3].(map[string]any)
	if call["call_id"] != "call_1" || output["call_id"] != "call_1" {
		t.Fatalf("tool replay = call %#v output %#v", call, output)
	}
}
