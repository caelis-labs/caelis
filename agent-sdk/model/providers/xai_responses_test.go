package providers

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestXAIResponsesToolLoopPreservesEncryptedReasoning(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var bodies []map[string]any
	var requestCount atomic.Int32
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer xai-access" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("originator"); got != "" {
			t.Errorf("originator = %q, want empty", got)
		}
		if got := r.Header.Get("session-id"); got != "" {
			t.Errorf("session-id = %q, want empty", got)
		}
		if got := r.Header.Get("x-grok-model-override"); got != "grok-4.5" {
			t.Errorf("x-grok-model-override = %q, want grok-4.5", got)
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
				map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"id": "rs_1", "type": "reasoning", "encrypted_content": "xai-encrypted", "summary": []any{map[string]any{"type": "summary_text", "text": "checking"}}}},
				map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": ""}},
				map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_1", "output_index": 1, "delta": "{\"query\":\"weather\"}"},
				map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{\"query\":\"weather\"}"}},
				map[string]any{"type": "response.completed", "response": map[string]any{
					"model": "grok-4.5", "status": "completed",
					"output": []any{
						map[string]any{"id": "rs_1", "type": "reasoning", "encrypted_content": "xai-encrypted", "summary": []any{map[string]any{"type": "summary_text", "text": "checking"}}},
						map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": "{\"query\":\"weather\"}"},
					},
					"usage": map[string]any{"input_tokens": 20, "output_tokens": 8, "total_tokens": 28},
				}},
			)
			return
		}
		writeOpenAICodexSSE(t, w,
			map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "msg_2", "type": "message", "content": []any{}}},
			map[string]any{"type": "response.output_text.delta", "item_id": "msg_2", "output_index": 0, "delta": "sunny"},
			map[string]any{"type": "response.completed", "response": map[string]any{
				"model": "grok-4.5", "status": "completed",
				"output": []any{map[string]any{"id": "msg_2", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "sunny"}}}},
			}},
		)
	}))
	defer server.Close()

	client := server.Client()
	client.Transport = xAIResponsesTestAuthTransport{base: client.Transport}
	factory := NewFactory()
	if err := factory.Register(Config{
		Alias: "xai/grok-4.5", Provider: "xai", API: APIXAIResponses, Model: "grok-4.5",
		BaseURL: server.URL, HTTPClient: client, MaxOutputTok: 32768, ContextWindowTokens: 500000,
		Auth: AuthConfig{Type: AuthOAuthToken},
	}); err != nil {
		t.Fatal(err)
	}
	llm, err := factory.NewByAlias("xai/grok-4.5")
	if err != nil {
		t.Fatal(err)
	}
	capabilities, ok := model.CapabilitiesOf(llm)
	if !ok || !capabilities.ToolCalls || !capabilities.Streaming || !capabilities.ParallelToolCalls || !capabilities.ReasoningContinuation {
		t.Fatalf("CapabilitiesOf(xai/grok-4.5) = %#v, %v", capabilities, ok)
	}
	tool := model.NewFunctionToolSpec("lookup", "lookup weather", map[string]any{"type": "object"})
	first, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "check weather")},
		Tools:     []model.ToolSpec{tool},
		Reasoning: model.ReasoningConfig{Effort: "high"},
		Output:    &model.OutputSpec{Mode: model.OutputModeText, MaxOutputTokens: 128},
		Stream:    true,
	})
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	reasoning := first.Message.ReasoningParts()
	if len(reasoning) != 1 || reasoning[0].Replay == nil || reasoning[0].Replay.Provider != "xai" || reasoning[0].Replay.Token != "xai-encrypted" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
	calls := first.Message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %#v", calls)
	}
	second, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "check weather"),
			first.Message,
			model.NewMessage(model.RoleTool, model.NewToolResultJSONPart(calls[0].ID, calls[0].Name, map[string]any{"weather": "sunny"}, false)),
		},
		Tools:     []model.ToolSpec{tool},
		Reasoning: model.ReasoningConfig{Effort: "high"},
		Stream:    true,
	})
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if second.Message.TextContent() != "sunny" {
		t.Fatalf("second text = %q", second.Message.TextContent())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("request bodies = %d, want 2", len(bodies))
	}
	if bodies[0]["max_output_tokens"] != float64(128) {
		t.Fatalf("max_output_tokens = %#v", bodies[0]["max_output_tokens"])
	}
	if bodies[1]["max_output_tokens"] != float64(32768) {
		t.Fatalf("default max_output_tokens = %#v", bodies[1]["max_output_tokens"])
	}
	if !reflect.DeepEqual(bodies[0]["include"], []any{"reasoning.encrypted_content"}) {
		t.Fatalf("include = %#v", bodies[0]["include"])
	}
	reasoningConfig, _ := bodies[0]["reasoning"].(map[string]any)
	if reasoningConfig["effort"] != "high" || reasoningConfig["summary"] != "concise" {
		t.Fatalf("reasoning config = %#v", reasoningConfig)
	}
	input, _ := bodies[1]["input"].([]any)
	var replayed bool
	for _, raw := range input {
		entry, _ := raw.(map[string]any)
		if entry["type"] == "reasoning" && entry["encrypted_content"] == "xai-encrypted" {
			replayed = true
		}
	}
	if !replayed {
		t.Fatalf("second input omitted xAI encrypted reasoning: %#v", input)
	}
}

func TestXAIResponsesFactoryRequiresOAuthClient(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		auth AuthType
		want string
	}{
		{name: "wrong auth", auth: AuthAPIKey, want: "requires oauth authentication"},
		{name: "missing client", auth: AuthOAuthToken, want: "authenticated http client"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factory := NewFactory()
			if err := factory.Register(Config{
				Alias: "grok", API: APIXAIResponses, Model: "grok-4.5", Auth: AuthConfig{Type: tc.auth},
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := factory.NewByAlias("grok"); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NewByAlias() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestXAIResponsesNonReasoningRequestOmitsReasoningFields(t *testing.T) {
	t.Parallel()

	payload, err := xAIResponsesRequestFromModel(&model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	}, "grok-4.20-0309-non-reasoning", 32768)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["reasoning"]; ok {
		t.Fatalf("non-reasoning request contains reasoning: %s", raw)
	}
	if _, ok := body["include"]; ok {
		t.Fatalf("non-reasoning request contains include: %s", raw)
	}
}

type xAIResponsesTestAuthTransport struct {
	base http.RoundTripper
}

func (t xAIResponsesTestAuthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	clone.Header.Set("Authorization", "Bearer xai-access")
	return t.base.RoundTrip(clone)
}
