package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

func TestOpenAIResponsesFactoryRoutesOfficialAndGenericToResponses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		alias   string
		api     APIType
		wantAPI string
	}{
		{name: "official openai", alias: "openai/gpt-test", api: APIOpenAI, wantAPI: "openai"},
		{name: "generic openai_responses", alias: "acme/gpt-test", api: APIOpenAIResponses, wantAPI: "openai_responses"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gotPath string
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", "ok"))
			}))
			defer server.Close()

			factory := NewFactory()
			if err := factory.Register(Config{
				Alias:      tc.alias,
				Provider:   "openai",
				API:        tc.api,
				Model:      "gpt-test",
				BaseURL:    server.URL,
				HTTPClient: server.Client(),
				Auth:       AuthConfig{Type: AuthAPIKey, Token: "sk-test"},
			}); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			llm, err := factory.NewByAlias(tc.alias)
			if err != nil {
				t.Fatalf("NewByAlias() error = %v", err)
			}
			caps, ok := model.CapabilitiesOf(llm)
			if !ok || !caps.ToolCalls || !caps.Streaming || !caps.StructuredOutput || !caps.ParallelToolCalls || !caps.ReasoningContinuation || caps.HostedTools {
				t.Fatalf("CapabilitiesOf() = %#v ok=%v, want tools/stream/schema/reasoning and no hosted tools", caps, ok)
			}
			if _, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
			}); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if gotPath != "/responses" {
				t.Fatalf("path = %q, want /responses for api %q", gotPath, tc.wantAPI)
			}
		})
	}
}

func TestOpenAIResponsesCompatibleFactoryStaysChatCompletions(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"gpt-test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	factory := NewFactory()
	if err := factory.Register(Config{
		Alias:      "compat/gpt-test",
		Provider:   "openai-compatible",
		API:        APIOpenAICompatible,
		Model:      "gpt-test",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Auth:       AuthConfig{Type: AuthAPIKey, Token: "sk-test"},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	llm, err := factory.NewByAlias("compat/gpt-test")
	if err != nil {
		t.Fatalf("NewByAlias() error = %v", err)
	}
	if _, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
	}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("path = %q, want /chat/completions", gotPath)
	}
}

func TestOpenAIResponsesAuthHeadersHaveNoOAuthOrCodexGrokIdentity(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-secret-must-not-leak")
	t.Setenv("OPENAI_ACCESS_TOKEN", "env-oauth-must-not-leak")

	type captured struct {
		header http.Header
		body   map[string]any
	}
	for _, tc := range []struct {
		name          string
		auth          AuthConfig
		headers       map[string]string
		wantAuth      string
		wantCustomKey string
		wantCustomVal string
		wantAuthEmpty bool
	}{
		{
			name:     "api key bearer",
			auth:     AuthConfig{Type: AuthAPIKey, Token: "sk-live"},
			wantAuth: "Bearer sk-live",
		},
		{
			name:          "auth none",
			auth:          AuthConfig{Type: AuthNone},
			wantAuthEmpty: true,
		},
		{
			name:          "custom header key",
			auth:          AuthConfig{Type: AuthAPIKey, Token: "tenant-key", HeaderKey: "X-Api-Key"},
			wantAuthEmpty: true,
			wantCustomKey: "X-Api-Key",
			wantCustomVal: "tenant-key",
		},
		{
			name:          "custom prefix",
			auth:          AuthConfig{Type: AuthAPIKey, Token: "tenant-key", HeaderKey: "X-Auth", Prefix: "Token"},
			wantAuthEmpty: true,
			wantCustomKey: "X-Auth",
			wantCustomVal: "Token tenant-key",
		},
		{
			name:          "safe configured header preserved",
			auth:          AuthConfig{Type: AuthAPIKey, Token: "sk-live"},
			headers:       map[string]string{"X-Custom-Safe": "preserved"},
			wantAuth:      "Bearer sk-live",
			wantCustomKey: "X-Custom-Safe",
			wantCustomVal: "preserved",
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			var got captured
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got.header = r.Header.Clone()
				if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", "ok"))
			}))
			defer server.Close()

			llm := newOpenAIResponses(Config{
				Provider:   "openai",
				API:        APIOpenAIResponses,
				Model:      "gpt-test",
				BaseURL:    server.URL,
				HTTPClient: server.Client(),
				Headers:    tc.headers,
				Auth:       tc.auth,
			}, strings.TrimSpace(tc.auth.Token))
			if _, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
			}); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			assertNoCodexOrGrokIdentityHeaders(t, got.header)
			if got.header.Get("Content-Type") != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got.header.Get("Content-Type"))
			}
			if !strings.HasPrefix(got.header.Get("User-Agent"), "caelis/") {
				t.Fatalf("User-Agent = %q, want caelis attribution", got.header.Get("User-Agent"))
			}
			if tc.wantAuthEmpty {
				if got := got.header.Get("Authorization"); got != "" {
					t.Fatalf("Authorization = %q, want empty", got)
				}
			} else if got := got.header.Get("Authorization"); got != tc.wantAuth {
				t.Fatalf("Authorization = %q, want %q", got, tc.wantAuth)
			}
			if tc.wantCustomKey != "" && got.header.Get(tc.wantCustomKey) != tc.wantCustomVal {
				t.Fatalf("%s = %q, want %q", tc.wantCustomKey, got.header.Get(tc.wantCustomKey), tc.wantCustomVal)
			}
			if strings.Contains(fmt.Sprintf("%v", got.header), "env-secret") || strings.Contains(fmt.Sprintf("%v", got.header), "env-oauth") {
				t.Fatalf("request headers leaked env credentials: %v", got.header)
			}
		})
	}
}

func TestOpenAIResponsesGenerateRespectsStreamAcceptAndBody(t *testing.T) {
	t.Parallel()

	for _, stream := range []bool{true, false} {
		stream := stream
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			t.Parallel()
			var accept string
			var body map[string]any
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					t.Errorf("path = %q, want /responses", r.URL.Path)
				}
				accept = r.Header.Get("Accept")
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					writeOpenAICodexSSE(t, w,
						map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "output_index": 0, "delta": "hello"},
						map[string]any{"type": "response.completed", "response": openAIResponsesCompletedJSON("gpt-test", "hello")},
					)
					return
				}
				writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", "hello"))
			}))
			defer server.Close()

			llm := newOpenAIResponsesTestLLM(server, APIOpenAIResponses)
			response, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
				Stream:   stream,
			})
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if response.Message.TextContent() != "hello" {
				t.Fatalf("text = %q, want hello", response.Message.TextContent())
			}
			if body["stream"] != stream {
				t.Fatalf("body stream = %#v, want %v", body["stream"], stream)
			}
			if stream {
				if accept != "text/event-stream" {
					t.Fatalf("Accept = %q, want text/event-stream", accept)
				}
				return
			}
			if accept != "application/json" {
				t.Fatalf("Accept = %q, want application/json", accept)
			}
		})
	}
}

func TestOpenAIResponsesRequestIsStatelessStoreFalseFullHistory(t *testing.T) {
	t.Parallel()

	var body map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", "ok"))
	}))
	defer server.Close()

	llm := newOpenAIResponses(Config{
		Provider:     "openai",
		API:          APIOpenAIResponses,
		Model:        "gpt-test",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		MaxOutputTok: 64,
	}, "sk-test")
	user := model.NewTextMessage(model.RoleUser, "first")
	prior := model.NewTextMessage(model.RoleAssistant, "earlier")
	if _, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Instructions: []model.Part{model.NewTextPart("be brief")},
		Messages:     []model.Message{user, prior, model.NewTextMessage(model.RoleUser, "again")},
	}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if body["store"] != false {
		t.Fatalf("store = %#v, want false", body["store"])
	}
	if _, ok := body["previous_response_id"]; ok {
		t.Fatalf("previous_response_id leaked into stateless request: %#v", body)
	}
	if body["prompt_cache_key"] != nil {
		t.Fatalf("prompt_cache_key = %#v, want omitted on standard route", body["prompt_cache_key"])
	}
	if !reflect.DeepEqual(body["include"], []any{"reasoning.encrypted_content"}) {
		t.Fatalf("include = %#v, want reasoning.encrypted_content", body["include"])
	}
	if body["instructions"] != "be brief" {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
	input := responsesInputMaps(t, body)
	if len(input) != 3 {
		t.Fatalf("input = %#v, want full history of 3 items", input)
	}
}

func TestOpenAIResponsesMaxOutputTokensAndReasoningEffort(t *testing.T) {
	t.Parallel()

	var bodies []map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		bodies = append(bodies, body)
		writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", "ok"))
	}))
	defer server.Close()

	official := newOpenAIResponses(Config{
		Provider: "openai", API: APIOpenAI, Model: "gpt-test",
		BaseURL: server.URL, HTTPClient: server.Client(), MaxOutputTok: 32768,
	}, "sk-test")
	generic := newOpenAIResponses(Config{
		Provider: "openai", API: APIOpenAIResponses, Model: "gpt-test",
		BaseURL: server.URL, HTTPClient: server.Client(), MaxOutputTok: 32768,
	}, "sk-test")
	hello := []model.Message{model.NewTextMessage(model.RoleUser, "hi")}
	if _, _, _, err := collectOpenAICodexTestResponse(official, &model.Request{
		Messages:  hello,
		Reasoning: model.ReasoningConfig{Effort: "high"},
		Output:    &model.OutputSpec{MaxOutputTokens: 128},
	}); err != nil {
		t.Fatalf("official Generate() error = %v", err)
	}
	if _, _, _, err := collectOpenAICodexTestResponse(generic, &model.Request{
		Messages:  hello,
		Reasoning: model.ReasoningConfig{Effort: "high"},
	}); err != nil {
		t.Fatalf("generic Generate() error = %v", err)
	}

	if len(bodies) != 2 {
		t.Fatalf("captured %d bodies, want 2", len(bodies))
	}
	if bodies[0]["max_output_tokens"] != float64(128) {
		t.Fatalf("override max_output_tokens = %#v, want 128", bodies[0]["max_output_tokens"])
	}
	if bodies[1]["max_output_tokens"] != float64(32768) {
		t.Fatalf("default max_output_tokens = %#v, want 32768", bodies[1]["max_output_tokens"])
	}
	officialReasoning, _ := bodies[0]["reasoning"].(map[string]any)
	if officialReasoning["effort"] != "high" || officialReasoning["summary"] != "auto" {
		t.Fatalf("official reasoning = %#v, want effort high summary auto", officialReasoning)
	}
	genericReasoning, _ := bodies[1]["reasoning"].(map[string]any)
	if genericReasoning["effort"] != "high" {
		t.Fatalf("generic reasoning = %#v, want effort high", genericReasoning)
	}
	if _, ok := genericReasoning["summary"]; ok {
		t.Fatalf("generic reasoning unexpectedly sets summary: %#v", genericReasoning)
	}
}

func TestOpenAIResponsesRejectsReasoningBudgetsAndHostedTools(t *testing.T) {
	t.Parallel()

	llm := newOpenAIResponses(Config{Provider: "openai", API: APIOpenAIResponses, Model: "gpt-test"}, "sk-test")
	_, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Reasoning: model.ReasoningConfig{BudgetTokens: 8192},
	})
	if !errorcode.Is(err, errorcode.Unsupported) || err == nil || !strings.Contains(err.Error(), "reasoning token budgets are unsupported") {
		t.Fatalf("budget error = %v, want unsupported budgets", err)
	}

	_, _, _, err = collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "search")},
		Tools:    []model.ToolSpec{model.NewProviderExecutedToolSpec("openai", "web_search", nil)},
	})
	if !errorcode.Is(err, errorcode.Unsupported) || err == nil || !strings.Contains(err.Error(), "only function tools are supported") {
		t.Fatalf("hosted tool error = %v, want function-only scope", err)
	}
}

func TestOpenAIResponsesTextFormatJSONAndJSONSchema(t *testing.T) {
	t.Parallel()

	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"answer": map[string]any{"type": "string"}},
		"required":             []any{"answer"},
	}
	for _, tc := range []struct {
		name   string
		output *model.OutputSpec
		check  func(*testing.T, map[string]any)
	}{
		{
			name:   "json object",
			output: &model.OutputSpec{Mode: model.OutputModeJSON},
			check: func(t *testing.T, body map[string]any) {
				t.Helper()
				format := responsesTextFormat(t, body)
				if format["type"] != "json_object" {
					t.Fatalf("format = %#v, want json_object", format)
				}
			},
		},
		{
			name:   "json schema",
			output: &model.OutputSpec{Mode: model.OutputModeSchema, JSONSchema: schema},
			check: func(t *testing.T, body map[string]any) {
				t.Helper()
				format := responsesTextFormat(t, body)
				if format["type"] != "json_schema" || format["name"] != "caelis_output" {
					t.Fatalf("format = %#v, want json_schema caelis_output", format)
				}
				if _, ok := format["schema"].(map[string]any); !ok {
					t.Fatalf("schema missing: %#v", format)
				}
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body map[string]any
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", `{"answer":"ok"}`))
			}))
			defer server.Close()
			llm := newOpenAIResponsesTestLLM(server, APIOpenAIResponses)
			if _, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
				Output:   tc.output,
			}); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			tc.check(t, body)
		})
	}

	llm := newOpenAIResponses(Config{Provider: "openai", API: APIOpenAIResponses, Model: "gpt-test"}, "sk-test")
	_, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Output:   &model.OutputSpec{Mode: model.OutputModeSchema},
	})
	var outputErr *model.OutputSpecError
	if !errors.As(err, &outputErr) || outputErr.Mode != model.OutputModeSchema {
		t.Fatalf("empty schema error = %v, want OutputSpecError", err)
	}
}

func TestOpenAIResponsesFunctionToolsFlattenedWithExplicitStrictBit(t *testing.T) {
	t.Parallel()

	spec := model.NewFunctionToolSpec("lookup", "lookup weather", map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"query": map[string]any{"type": "string"}},
		"required":             []string{"query"},
	})
	spec.Function.Strict = true

	for _, tc := range []struct {
		name       string
		api        APIType
		wantStrict bool
	}{
		{name: "official preserves strict", api: APIOpenAI, wantStrict: true},
		{name: "generic conservative false", api: APIOpenAIResponses, wantStrict: false},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var body map[string]any
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", "ok"))
			}))
			defer server.Close()
			llm := newOpenAIResponsesTestLLM(server, tc.api)
			if _, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
				Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
				Tools:    []model.ToolSpec{spec},
			}); err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			tools, _ := body["tools"].([]any)
			if len(tools) != 1 {
				t.Fatalf("tools = %#v, want one flattened function", body["tools"])
			}
			tool, _ := tools[0].(map[string]any)
			if tool["type"] != "function" || tool["name"] != "lookup" {
				t.Fatalf("tool = %#v, want flattened function lookup", tool)
			}
			if _, nested := tool["function"]; nested {
				t.Fatalf("tool uses chat-completions nested function shape: %#v", tool)
			}
			if _, ok := tool["strict"]; !ok {
				t.Fatalf("strict bit omitted: %#v", tool)
			}
			if tool["strict"] != tc.wantStrict {
				t.Fatalf("strict = %#v, want %v", tool["strict"], tc.wantStrict)
			}
			if body["tool_choice"] != "auto" {
				t.Fatalf("tool_choice = %#v, want auto", body["tool_choice"])
			}
		})
	}
}

func TestOpenAIResponsesSSEAndJSONAccumulateTextReasoningToolsUsage(t *testing.T) {
	t.Parallel()

	t.Run("sse plaintext reasoning and tool ids", func(t *testing.T) {
		t.Parallel()
		server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeOpenAICodexSSE(t, w,
				map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "rs_1", "type": "reasoning"}},
				map[string]any{"type": "response.reasoning_text.delta", "item_id": "rs_1", "output_index": 0, "delta": "checking"},
				map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{
					"id": "rs_1", "type": "reasoning",
					"content": []any{map[string]any{"type": "reasoning_text", "text": "checking"}},
				}},
				map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": ""}},
				map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_1", "output_index": 1, "delta": `{"query":"weather"}`},
				map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"query":"weather"}`}},
				map[string]any{"type": "response.completed", "response": map[string]any{
					"model": "gpt-test-build", "status": "completed",
					"output": []any{
						map[string]any{"id": "rs_1", "type": "reasoning", "content": []any{map[string]any{"type": "reasoning_text", "text": "checking"}}},
						map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"query":"weather"}`},
					},
					"usage": map[string]any{"input_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 5}, "output_tokens": 8, "output_tokens_details": map[string]any{"reasoning_tokens": 3}, "total_tokens": 28},
				}},
			)
		}))
		defer server.Close()
		llm := newOpenAIResponsesTestLLM(server, APIOpenAIResponses)
		response, reasoningDelta, toolDelta, err := collectOpenAICodexTestResponse(llm, &model.Request{
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "weather")},
			Tools:    []model.ToolSpec{model.NewFunctionToolSpec("lookup", "lookup weather", map[string]any{"type": "object"})},
			Stream:   true,
		})
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if reasoningDelta != "checking" || toolDelta != `{"query":"weather"}` {
			t.Fatalf("deltas = reasoning %q tool %q", reasoningDelta, toolDelta)
		}
		assertPlainReasoning(t, response.Message, "openai", "checking")
		calls := response.Message.ToolCalls()
		if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "lookup" {
			t.Fatalf("tool calls = %#v", calls)
		}
		if response.FinishReason != model.FinishReasonToolCalls {
			t.Fatalf("FinishReason = %q, want tool_calls", response.FinishReason)
		}
		if response.Model != "gpt-test-build" {
			t.Fatalf("Model = %q, want raw provider model", response.Model)
		}
		if response.Usage != (model.Usage{PromptTokens: 20, CachedInputTokens: 5, CompletionTokens: 8, ReasoningTokens: 3, TotalTokens: 28}) {
			t.Fatalf("Usage = %+v", response.Usage)
		}
	})

	t.Run("json encrypted and plaintext without deltas", func(t *testing.T) {
		t.Parallel()
		server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeOpenAIResponsesJSON(t, w, map[string]any{
				"model":  "gpt-test",
				"status": "completed",
				"output": []any{
					map[string]any{
						"id": "rs_1", "type": "reasoning", "encrypted_content": "enc-1",
						"content": []any{map[string]any{"type": "reasoning_text", "text": "plan"}},
					},
					map[string]any{"id": "msg_1", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "done"}}},
				},
				"usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6},
			})
		}))
		defer server.Close()
		llm := newOpenAIResponsesTestLLM(server, APIOpenAIResponses)
		response, reasoningDelta, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		})
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if reasoningDelta != "" {
			t.Fatalf("JSON path emitted reasoning delta %q", reasoningDelta)
		}
		if response.Message.TextContent() != "done" || response.FinishReason != model.FinishReasonStop {
			t.Fatalf("json response = %+v", response)
		}
		reasoning := response.Message.ReasoningParts()
		if len(reasoning) != 1 || reasoning[0].VisibleText == nil || *reasoning[0].VisibleText != "plan" {
			t.Fatalf("json reasoning text = %#v", reasoning)
		}
		if reasoning[0].Replay == nil || reasoning[0].Replay.Token != "enc-1" || reasoning[0].Replay.Kind != openAICodexReplayKind || reasoning[0].Replay.Provider != "openai" {
			t.Fatalf("json encrypted replay = %#v", reasoning[0].Replay)
		}
	})
}

func TestOpenAIResponsesErrorsFailedIncompleteAndTruncatedEOF(t *testing.T) {
	t.Parallel()

	t.Run("sse truncated eof", func(t *testing.T) {
		t.Parallel()
		server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeOpenAICodexSSE(t, w, map[string]any{"type": "response.output_text.delta", "item_id": "msg_1", "delta": "partial"})
		}))
		defer server.Close()
		_, _, _, err := collectOpenAICodexTestResponse(newOpenAIResponsesTestLLM(server, APIOpenAIResponses), &model.Request{
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
			Stream:   true,
		})
		if err == nil || !strings.Contains(err.Error(), "ended before a terminal response") {
			t.Fatalf("error = %v, want truncated EOF", err)
		}
	})

	t.Run("sse failed invalid request", func(t *testing.T) {
		t.Parallel()
		server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeOpenAICodexSSE(t, w, map[string]any{
				"type": "response.failed",
				"response": map[string]any{"status": "failed", "error": map[string]any{
					"type": "invalid_request_error", "code": "invalid_value", "message": "bad field",
				}},
			})
		}))
		defer server.Close()
		_, _, _, err := collectOpenAICodexTestResponse(newOpenAIResponsesTestLLM(server, APIOpenAIResponses), &model.Request{
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
			Stream:   true,
		})
		if !errorcode.Is(err, errorcode.InvalidArgument) || model.IsRetryableLLMError(err) {
			t.Fatalf("error = %v code=%q retryable=%v", err, errorcode.CodeOf(err), model.IsRetryableLLMError(err))
		}
	})

	t.Run("sse incomplete max output tokens", func(t *testing.T) {
		t.Parallel()
		server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			writeOpenAICodexSSE(t, w, map[string]any{
				"type": "response.incomplete",
				"response": map[string]any{
					"model": "gpt-test", "status": "incomplete",
					"incomplete_details": map[string]any{"reason": "max_output_tokens"},
					"output":             []any{map[string]any{"id": "msg_1", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "cut"}}}},
				},
			})
		}))
		defer server.Close()
		response, _, _, err := collectOpenAICodexTestResponse(newOpenAIResponsesTestLLM(server, APIOpenAIResponses), &model.Request{
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
			Stream:   true,
		})
		if err != nil {
			t.Fatalf("Generate() error = %v, want incomplete success", err)
		}
		if response.FinishReason != model.FinishReasonLength || response.RawFinishReason != "max_output_tokens" {
			t.Fatalf("incomplete finish = %q/%q", response.FinishReason, response.RawFinishReason)
		}
	})

	t.Run("json failed", func(t *testing.T) {
		t.Parallel()
		server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeOpenAIResponsesJSON(t, w, map[string]any{
				"status": "failed",
				"error":  map[string]any{"type": "invalid_request_error", "message": "bad json request"},
			})
		}))
		defer server.Close()
		_, _, _, err := collectOpenAICodexTestResponse(newOpenAIResponsesTestLLM(server, APIOpenAIResponses), &model.Request{
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		})
		if !errorcode.Is(err, errorcode.InvalidArgument) {
			t.Fatalf("json failed error = %v, want invalid_argument", err)
		}
	})

	t.Run("json incomplete", func(t *testing.T) {
		t.Parallel()
		server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeOpenAIResponsesJSON(t, w, map[string]any{
				"model":              "gpt-test",
				"status":             "incomplete",
				"incomplete_details": map[string]any{"reason": "max_output_tokens"},
				"output":             []any{map[string]any{"id": "msg_1", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "cut"}}}},
			})
		}))
		defer server.Close()
		response, _, _, err := collectOpenAICodexTestResponse(newOpenAIResponsesTestLLM(server, APIOpenAIResponses), &model.Request{
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		})
		if err != nil {
			t.Fatalf("json incomplete error = %v", err)
		}
		if response.FinishReason != model.FinishReasonLength {
			t.Fatalf("json incomplete finish = %q", response.FinishReason)
		}
	})

	t.Run("http unauthorized", func(t *testing.T) {
		t.Parallel()
		server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"expired"}`, http.StatusUnauthorized)
		}))
		defer server.Close()
		_, _, _, err := collectOpenAICodexTestResponse(newOpenAIResponsesTestLLM(server, APIOpenAIResponses), &model.Request{
			Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		})
		if !errorcode.Is(err, errorcode.Unauthenticated) {
			t.Fatalf("http 401 error = %v, want unauthenticated", err)
		}
	})
}

func TestOpenAIResponsesStatelessMultiTurnRoundTripThroughMessageJSON(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var bodies []map[string]any
	var requestCount atomic.Int32
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "rs_1", "type": "reasoning"}},
				map[string]any{"type": "response.reasoning_text.delta", "item_id": "rs_1", "output_index": 0, "delta": "checking"},
				map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{
					"id": "rs_1", "type": "reasoning", "encrypted_content": "enc-state",
					"content": []any{map[string]any{"type": "reasoning_text", "text": "checking"}},
				}},
				map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup"}},
				map[string]any{"type": "response.function_call_arguments.delta", "item_id": "fc_1", "output_index": 1, "delta": `{"query":"weather"}`},
				map[string]any{"type": "response.completed", "response": map[string]any{
					"model": "gpt-test", "status": "completed",
					"output": []any{
						map[string]any{"id": "rs_1", "type": "reasoning", "encrypted_content": "enc-state", "content": []any{map[string]any{"type": "reasoning_text", "text": "checking"}}},
						map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"query":"weather"}`},
					},
				}},
			)
			return
		}
		writeOpenAICodexSSE(t, w,
			map[string]any{"type": "response.output_text.delta", "item_id": "msg_2", "output_index": 0, "delta": "sunny"},
			map[string]any{"type": "response.completed", "response": openAIResponsesCompletedJSON("gpt-test", "sunny")},
		)
	}))
	defer server.Close()

	llm := newOpenAIResponsesTestLLM(server, APIOpenAIResponses)
	tool := model.NewFunctionToolSpec("lookup", "lookup weather", map[string]any{"type": "object"})
	user := model.NewTextMessage(model.RoleUser, "check weather")
	first, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{user},
		Tools:    []model.ToolSpec{tool},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	assistant := roundTripMessageJSON(t, first.Message)
	reasoning := assistant.ReasoningParts()
	if len(reasoning) != 1 || reasoning[0].Replay == nil || reasoning[0].Replay.Token != "enc-state" || reasoning[0].Replay.Kind != openAICodexReplayKind {
		t.Fatalf("round-tripped encrypted reasoning = %#v", reasoning)
	}
	calls := assistant.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("round-tripped tool calls = %#v", calls)
	}
	toolResult := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart("call_1", "lookup", map[string]any{"weather": "sunny"}, false))
	second, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{user, assistant, roundTripMessageJSON(t, toolResult)},
		Tools:    []model.ToolSpec{tool},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if second.Message.TextContent() != "sunny" || second.FinishReason != model.FinishReasonStop {
		t.Fatalf("second response = %+v", second)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d, want 2", len(bodies))
	}
	if _, ok := bodies[1]["previous_response_id"]; ok {
		t.Fatalf("second request used previous_response_id: %#v", bodies[1])
	}
	input := responsesInputMaps(t, bodies[1])
	if !hasEncryptedReasoning(input, "enc-state") {
		t.Fatalf("second input omitted encrypted reasoning: %#v", input)
	}
	if !hasFunctionOutput(input, "call_1") {
		t.Fatalf("second input omitted function output: %#v", input)
	}
}

func TestOpenAIResponsesForeignEncryptedReplayIsDropped(t *testing.T) {
	t.Parallel()

	var body map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", "ok"))
	}))
	defer server.Close()

	foreign := model.NewMessage(model.RoleAssistant,
		func() model.Part {
			part := model.NewReasoningPart("secret plan", model.ReasoningVisibilityVisible)
			part.Reasoning.Replay = &model.ReplayMeta{Provider: "xai", Kind: openAICodexReplayKind, Token: "xai-encrypted"}
			return part
		}(),
		func() model.Part {
			part := model.NewReasoningPart("other", model.ReasoningVisibilityTokenOnly)
			part.Reasoning.Replay = &model.ReplayMeta{Provider: "openai", Kind: "gemini_thought_signature", Token: "gemini-token"}
			return part
		}(),
		model.NewTextPart("hello"),
	)
	llm := newOpenAIResponsesTestLLM(server, APIOpenAIResponses)
	if _, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi"), foreign},
	}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	input := responsesInputMaps(t, body)
	if hasEncryptedReasoning(input, "xai-encrypted") || hasEncryptedReasoning(input, "gemini-token") {
		t.Fatalf("foreign encrypted tokens were forwarded: %#v", input)
	}
	for _, item := range input {
		if item["type"] == "reasoning" {
			t.Fatalf("foreign reasoning replayed as valid token: %#v", item)
		}
	}
}

func TestOpenAIResponsesPlainReasoningIsNotSummary(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var bodies []map[string]any
	var requestCount atomic.Int32
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
				map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "rs_1", "type": "reasoning"}},
				map[string]any{"type": "response.reasoning_text.delta", "item_id": "rs_1", "output_index": 0, "delta": "plain plan"},
				map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{
					"id": "rs_1", "type": "reasoning",
					"content": []any{map[string]any{"type": "reasoning_text", "text": "plain plan"}},
					"summary": []any{map[string]any{"type": "summary_text", "text": "should not win"}},
				}},
				map[string]any{"type": "response.completed", "response": map[string]any{
					"model": "gpt-test", "status": "completed",
					"output": []any{map[string]any{
						"id": "rs_1", "type": "reasoning",
						"content": []any{map[string]any{"type": "reasoning_text", "text": "plain plan"}},
						"summary": []any{map[string]any{"type": "summary_text", "text": "should not win"}},
					}, map[string]any{"id": "msg_1", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "ok"}}}},
				}},
			)
			return
		}
		writeOpenAICodexSSE(t, w, map[string]any{"type": "response.completed", "response": openAIResponsesCompletedJSON("gpt-test", "later")})
	}))
	defer server.Close()

	llm := newOpenAIResponsesTestLLM(server, APIOpenAIResponses)
	first, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	assertPlainReasoning(t, first.Message, "openai", "plain plan")
	assistant := roundTripMessageJSON(t, first.Message)
	unmarked := model.NewMessage(model.RoleAssistant, model.NewReasoningPart("display summary only", model.ReasoningVisibilityVisible), model.NewTextPart("visible"))
	if _, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "hi"),
			assistant,
			unmarked,
		},
		Stream: true,
	}); err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d, want 2", len(bodies))
	}
	input := responsesInputMaps(t, bodies[1])
	assertPlainReasoningInput(t, input, "rs_1", "plain plan")
	if reasoningHasText(input, "display summary only") || reasoningHasText(input, "should not win") {
		t.Fatalf("unmarked or losing summary text was replayed: %#v", input)
	}
}

func TestOpenAIResponsesSessionFileStoreRebuildsModelContext(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var bodies []map[string]any
	var requestCount atomic.Int32
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		if requestCount.Add(1) == 1 {
			writeOpenAIResponsesJSON(t, w, map[string]any{
				"model":  "gpt-test",
				"status": "completed",
				"output": []any{
					map[string]any{
						"id": "rs_1", "type": "reasoning",
						"content": []any{map[string]any{"type": "reasoning_text", "text": "checking"}},
					},
					map[string]any{"id": "fc_1", "type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"query":"weather"}`},
				},
			})
			return
		}
		writeOpenAIResponsesJSON(t, w, openAIResponsesCompletedJSON("gpt-test", "rebuilt"))
	}))
	defer server.Close()

	llm := newOpenAIResponsesTestLLM(server, APIOpenAIResponses)
	tool := model.NewFunctionToolSpec("lookup", "lookup weather", map[string]any{"type": "object"})
	user := model.NewTextMessage(model.RoleUser, "check weather")
	first, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: []model.Message{user},
		Tools:    []model.ToolSpec{tool},
	})
	if err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	assertPlainReasoning(t, first.Message, "openai", "checking")
	calls := first.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" {
		t.Fatalf("provider tool calls = %#v", calls)
	}
	toolResult := model.NewMessage(model.RoleTool, model.NewToolResultJSONPart(calls[0].ID, calls[0].Name, map[string]any{"weather": "sunny"}, false))

	store := sessionfile.NewStore(sessionfile.Config{
		RootDir:            t.TempDir(),
		SessionIDGenerator: func() string { return "sess-responses-roundtrip" },
	})
	active, err := store.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis",
		UserID:  "user-1",
		Workspace: session.WorkspaceRef{
			Key: "ws-responses-roundtrip",
			CWD: t.TempDir(),
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	appendStored := func(event *session.Event) {
		t.Helper()
		if _, err := store.AppendEvent(context.Background(), session.AppendEventRequest{
			SessionRef: active.SessionRef,
			Event:      event,
		}); err != nil {
			t.Fatalf("AppendEvent() error = %v", err)
		}
	}
	appendStored(&session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: ptrMessage(user)})
	appendStored(&session.Event{Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: ptrMessage(first.Message)})
	appendStored(&session.Event{Type: session.EventTypeToolResult, Visibility: session.VisibilityCanonical, Message: ptrMessage(toolResult)})

	loaded, err := store.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	var rebuilt []model.Message
	for _, event := range loaded.Events {
		msg, ok := session.ModelMessageOf(event)
		if !ok {
			continue
		}
		rebuilt = append(rebuilt, msg)
	}
	if len(rebuilt) != 3 {
		t.Fatalf("rebuilt messages = %d, want 3: %#v", len(rebuilt), rebuilt)
	}

	second, _, _, err := collectOpenAICodexTestResponse(llm, &model.Request{
		Messages: rebuilt,
		Tools:    []model.ToolSpec{tool},
	})
	if err != nil {
		t.Fatalf("second Generate() error = %v", err)
	}
	if second.Message.TextContent() != "rebuilt" {
		t.Fatalf("text = %q", second.Message.TextContent())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("bodies = %d, want 2", len(bodies))
	}
	if _, ok := bodies[1]["previous_response_id"]; ok {
		t.Fatalf("second request used previous_response_id: %#v", bodies[1])
	}
	input := responsesInputMaps(t, bodies[1])
	assertPlainReasoningInput(t, input, "rs_1", "checking")
	if !hasFunctionOutput(input, calls[0].ID) {
		t.Fatalf("rebuilt file-store context omitted function output: %#v", input)
	}
}

func newOpenAIResponsesTestLLM(server *providerTestServer, api APIType) *openAIResponsesLLM {
	return newOpenAIResponses(Config{
		Provider:   "openai",
		API:        api,
		Model:      "gpt-test",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Auth:       AuthConfig{Type: AuthAPIKey, Token: "sk-test"},
	}, "sk-test")
}

func writeOpenAIResponsesJSON(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encode JSON response: %v", err)
	}
}

func openAIResponsesCompletedJSON(modelName, text string) map[string]any {
	return map[string]any{
		"model":  modelName,
		"status": "completed",
		"output": []any{
			map[string]any{"id": "msg_1", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": text}}},
		},
	}
}

func assertNoCodexOrGrokIdentityHeaders(t *testing.T, header http.Header) {
	t.Helper()
	for _, name := range []string{
		"originator", "session-id", "session_id", "ChatGPT-Account-Id",
		"x-grok-conv-id", "x-grok-req-id", "x-grok-session-id", "x-grok-model-override",
		"x-grok-agent-id", "x-authenticateresponse", "X-XAI-Token-Auth",
	} {
		if got := header.Get(name); got != "" {
			t.Errorf("%s = %q, want empty on standard Responses", name, got)
		}
	}
}

func responsesInputMaps(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, _ := body["input"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("input item = %#v, want object", item)
		}
		out = append(out, entry)
	}
	return out
}

func responsesTextFormat(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	text, _ := body["text"].(map[string]any)
	format, _ := text["format"].(map[string]any)
	if format == nil {
		t.Fatalf("text.format missing: %#v", body["text"])
	}
	return format
}

func roundTripMessageJSON(t *testing.T, message model.Message) model.Message {
	t.Helper()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("json.Marshal(message) error = %v", err)
	}
	var out model.Message
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json.Unmarshal(message) error = %v", err)
	}
	return out
}

func ptrMessage(message model.Message) *model.Message {
	copy := model.CloneMessage(message)
	return &copy
}

func assertPlainReasoning(t *testing.T, message model.Message, provider, text string) {
	t.Helper()
	reasoning := message.ReasoningParts()
	if len(reasoning) != 1 || reasoning[0].VisibleText == nil || *reasoning[0].VisibleText != text {
		t.Fatalf("reasoning visible text = %#v, want %q", reasoning, text)
	}
	if reasoning[0].Replay == nil || reasoning[0].Replay.Provider != provider || reasoning[0].Replay.Kind != openAIResponsesReplayKindText || reasoning[0].Replay.Token != "" {
		t.Fatalf("plaintext replay = %#v, want provider %s kind %s empty token", reasoning[0].Replay, provider, openAIResponsesReplayKindText)
	}
}

func hasEncryptedReasoning(input []map[string]any, token string) bool {
	for _, item := range input {
		if item["type"] == "reasoning" && item["encrypted_content"] == token {
			return true
		}
	}
	return false
}

func assertPlainReasoningInput(t *testing.T, input []map[string]any, id, text string) {
	t.Helper()
	var found bool
	for _, item := range input {
		if item["type"] != "reasoning" {
			continue
		}
		if item["id"] != id {
			t.Fatalf("plain reasoning id = %#v, want provider-issued %q", item["id"], id)
		}
		summary, ok := item["summary"].([]any)
		if !ok {
			t.Fatalf("plain reasoning missing required summary: [] : %#v", item)
		}
		if len(summary) != 0 {
			t.Fatalf("plain reasoning summary = %#v, want empty array (raw text must not be relabeled as summary)", summary)
		}
		content, _ := item["content"].([]any)
		for _, raw := range content {
			part, _ := raw.(map[string]any)
			if part["type"] == "summary_text" {
				t.Fatalf("plain reasoning content relabeled as summary_text: %#v", item)
			}
			if part["type"] == "reasoning_text" && part["text"] == text {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("plain reasoning content %q missing: %#v", text, input)
	}
}

func hasPlainReasoningContent(input []map[string]any, text string) bool {
	for _, item := range input {
		if item["type"] != "reasoning" {
			continue
		}
		content, _ := item["content"].([]any)
		for _, raw := range content {
			part, _ := raw.(map[string]any)
			if part["type"] == "reasoning_text" && part["text"] == text {
				return true
			}
		}
	}
	return false
}

func hasFunctionOutput(input []map[string]any, callID string) bool {
	for _, item := range input {
		if item["type"] == "function_call_output" && item["call_id"] == callID {
			return true
		}
	}
	return false
}

func reasoningHasText(input []map[string]any, text string) bool {
	return hasPlainReasoningContent(input, text) || reasoningHasSummaryText(input, text)
}

func reasoningHasSummaryText(input []map[string]any, text string) bool {
	for _, item := range input {
		if item["type"] != "reasoning" {
			continue
		}
		summary, _ := item["summary"].([]any)
		for _, raw := range summary {
			part, _ := raw.(map[string]any)
			if part["type"] == "summary_text" && part["text"] == text {
				return true
			}
		}
	}
	return false
}
