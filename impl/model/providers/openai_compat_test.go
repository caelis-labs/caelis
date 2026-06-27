package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/model"
	porttool "github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestOpenAICompatStream_PropagatesSSEErrorsWithoutTurnComplete(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {invalid-json}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var (
		gotErr       error
		turnComplete bool
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			turnComplete = true
		}
	}
	if gotErr == nil {
		t.Fatalf("expected stream error, got nil")
	}
	if turnComplete {
		t.Fatalf("did not expect turn_complete on stream error")
	}
}

func TestOpenAICompatStreamFirstEventTimeoutRetriesBeforeEmission(t *testing.T) {
	t.Parallel()

	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			reader, writer := io.Pipe()
			t.Cleanup(func() {
				_ = writer.Close()
			})
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       reader,
				Request:    req,
			}, nil
		}
		body := strings.Join([]string{
			`data: {"model":"test-model","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	llm := model.WithRetry(newOpenAICompat(Config{
		Provider:                "openai-compatible",
		Model:                   "test-model",
		BaseURL:                 "http://provider.test",
		HTTPClient:              client,
		StreamFirstEventTimeout: 20 * time.Millisecond,
	}, "token"), model.RetryConfig{
		MaxRetries:          1,
		BaseDelay:           time.Nanosecond,
		MaxDelay:            time.Nanosecond,
		RateLimitMaxRetries: 1,
		RateLimitBaseDelay:  time.Nanosecond,
		RateLimitMaxDelay:   time.Nanosecond,
	})

	var (
		gotErr    error
		finalText string
	)
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			finalText = event.Response.Message.TextContent()
		}
	}
	if gotErr != nil {
		t.Fatalf("Generate() error = %v, want retry to recover", gotErr)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want stalled request plus retry", requests)
	}
	if finalText != "ok" {
		t.Fatalf("final text = %q, want ok", finalText)
	}
}

func TestOpenAICompatStream_DoesNotApplyRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    50 * time.Millisecond,
	}, "token")

	var (
		gotErr    error
		finalText string
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			finalText = resp.Response.Message.TextContent()
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no stream error, got %v", gotErr)
	}
	if finalText != "hello world" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

func TestOpenAICompatStream_IncludesUsageRequestOptionAndPropagatesUsage(t *testing.T) {
	var includeUsage bool
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		streamOptions, _ := payload["stream_options"].(map[string]any)
		includeUsage, _ = streamOptions["include_usage"].(bool)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18,\"prompt_tokens_details\":{\"cached_tokens\":9},\"completion_tokens_details\":{\"reasoning_tokens\":4}}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var (
		gotErr error
		usage  model.Usage
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			usage = resp.Usage
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no stream error, got %v", gotErr)
	}
	if !includeUsage {
		t.Fatal("expected stream_options.include_usage=true in request payload")
	}
	if usage.PromptTokens != 11 || usage.CachedInputTokens != 9 || usage.CompletionTokens != 7 || usage.ReasoningTokens != 4 || usage.TotalTokens != 18 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
}

func TestOpenAICompatNonStream_IncludesStructuredOutputRequest(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"{\"outcome\":\"allow\"}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:     "openai-compatible",
		Model:        "test-model",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		MaxOutputTok: 2048,
		Timeout:      2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "review")},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"outcome": map[string]any{"type": "string"},
				},
			},
			MaxOutputTokens: 64,
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	if got := payload["max_tokens"]; got != float64(64) {
		t.Fatalf("max_tokens = %v, want 64", got)
	}
	responseFormat, _ := payload["response_format"].(map[string]any)
	if got := responseFormat["type"]; got != "json_schema" {
		t.Fatalf("response_format.type = %v, want json_schema", got)
	}
	jsonSchema, _ := responseFormat["json_schema"].(map[string]any)
	if _, ok := jsonSchema["strict"]; ok {
		t.Fatalf("json_schema.strict is present for optional schema: %#v", jsonSchema["strict"])
	}
	schema, _ := jsonSchema["schema"].(map[string]any)
	if got := schema["type"]; got != "object" {
		t.Fatalf("json_schema.schema.type = %v, want object", got)
	}
}

func TestOpenAICompatNonStream_UsesStrictStructuredOutputOnlyForClosedRequiredSchema(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"{\"outcome\":\"allow\"}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "review")},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"outcome": map[string]any{"type": "string"},
				},
				"required": []any{"outcome"},
			},
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	responseFormat, _ := payload["response_format"].(map[string]any)
	jsonSchema, _ := responseFormat["json_schema"].(map[string]any)
	if got := jsonSchema["strict"]; got != true {
		t.Fatalf("json_schema.strict = %v, want true", got)
	}
}

func TestOpenAICompatNonStream_IncludesStrictFunctionToolOnlyWhenCompatible(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai",
		API:        APIOpenAI,
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "use tools")},
		Tools: []model.ToolSpec{
			{
				Kind: model.ToolSpecKindFunction,
				Function: &model.FunctionToolSpec{
					Name:        "closed",
					Description: "closed strict tool",
					Strict:      true,
					Parameters: map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
						},
						"required": []any{"path"},
					},
				},
			},
			{
				Kind: model.ToolSpecKindFunction,
				Function: &model.FunctionToolSpec{
					Name:        "optional",
					Description: "requested strict with nullable optional field",
					Strict:      true,
					Parameters: map[string]any{
						"type":                 "object",
						"additionalProperties": false,
						"properties": map[string]any{
							"path":  map[string]any{"type": "string"},
							"limit": map[string]any{"type": "integer"},
							"mode":  map[string]any{"type": "string", "enum": []string{"fast", "safe"}},
							"include": map[string]any{
								"anyOf": []any{
									map[string]any{"type": "string"},
									map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
								},
							},
						},
						"required": []any{"path"},
					},
				},
			},
			{
				Kind: model.ToolSpecKindFunction,
				Function: &model.FunctionToolSpec{
					Name:        "open",
					Description: "requested strict but schema is open",
					Strict:      true,
					Parameters: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"path": map[string]any{"type": "string"},
						},
						"required": []any{"path"},
					},
				},
			},
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}

	tools, _ := payload["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("tools len = %d, want 3: %#v", len(tools), payload["tools"])
	}
	closedFunction := tools[0].(map[string]any)["function"].(map[string]any)
	if got := closedFunction["strict"]; got != true {
		t.Fatalf("closed function strict = %#v, want true", got)
	}
	optionalFunction := tools[1].(map[string]any)["function"].(map[string]any)
	if got := optionalFunction["strict"]; got != true {
		t.Fatalf("optional function strict = %#v, want true", got)
	}
	optionalParams := optionalFunction["parameters"].(map[string]any)
	required, _ := optionalParams["required"].([]any)
	if got := strings.Join(stringSliceFromProviderAny(required), ","); got != "include,limit,mode,path" {
		t.Fatalf("optional required = %#v, want include,limit,mode,path", required)
	}
	optionalProps := optionalParams["properties"].(map[string]any)
	limitType, _ := optionalProps["limit"].(map[string]any)["type"].([]any)
	if got := strings.Join(stringSliceFromProviderAny(limitType), ","); got != "integer,null" {
		t.Fatalf("optional limit type = %#v, want integer,null", limitType)
	}
	modeEnum, _ := optionalProps["mode"].(map[string]any)["enum"].([]any)
	if len(modeEnum) != 3 || modeEnum[0] != "fast" || modeEnum[1] != "safe" || modeEnum[2] != nil {
		t.Fatalf("optional mode enum = %#v, want fast/safe/null", modeEnum)
	}
	includeAnyOf, _ := optionalProps["include"].(map[string]any)["anyOf"].([]any)
	if len(includeAnyOf) != 3 {
		t.Fatalf("optional include anyOf = %#v, want string/array/null", includeAnyOf)
	}
	includeNull, _ := includeAnyOf[2].(map[string]any)
	if got := includeNull["type"]; got != "null" {
		t.Fatalf("optional include null variant = %#v, want type null", includeNull)
	}
	openFunction := tools[2].(map[string]any)["function"].(map[string]any)
	if _, ok := openFunction["strict"]; ok {
		t.Fatalf("open function strict present for open schema: %#v", openFunction["strict"])
	}
}

func TestOpenAICompatNonStream_PropagatesFinishReason(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"truncated"},"finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var final *model.Response
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
		if resp != nil && resp.Response != nil {
			final = resp.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if !final.TurnComplete {
		t.Fatal("expected turn complete on terminal non-stream response")
	}
	if final.FinishReason != model.FinishReasonLength {
		t.Fatalf("expected finish reason length, got %q", final.FinishReason)
	}
}

func TestOpenAICompatStream_PropagatesTerminalFinishReason(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"length\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var final *model.Response
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("expected no stream error, got %v", err)
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			final = resp.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.Message.TextContent() != "hello world" {
		t.Fatalf("unexpected final text %q", final.Message.TextContent())
	}
	if final.FinishReason != model.FinishReasonLength {
		t.Fatalf("expected finish reason length, got %q", final.FinishReason)
	}
}

func TestOpenAICompatRequest_IncludesMaxTokens(t *testing.T) {
	var gotMax float64
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		got, _ := payload["max_tokens"].(float64)
		gotMax = got
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:     "openai-compatible",
		Model:        "test-model",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		Timeout:      2 * time.Second,
		MaxOutputTok: 2048,
	}, "token")

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no generate error, got %v", gotErr)
	}
	if gotMax != 2048 {
		t.Fatalf("expected max_tokens=2048, got %v", gotMax)
	}
}

func TestOpenRouterRequest_AppliesConfiguredHeaders(t *testing.T) {
	var gotReferer string
	var gotTitle string
	var gotUserAgent string
	var gotModel string
	var gotModels []any
	var gotRoute string
	var gotTransforms []any
	var gotProvider map[string]any
	var gotPlugins []any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		gotReferer = r.Header.Get("HTTP-Referer")
		gotTitle = r.Header.Get("X-Title")
		gotUserAgent = r.Header.Get("User-Agent")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		gotModel, _ = payload["model"].(string)
		gotModels, _ = payload["models"].([]any)
		gotRoute, _ = payload["route"].(string)
		gotTransforms, _ = payload["transforms"].([]any)
		gotProvider, _ = payload["provider"].(map[string]any)
		gotPlugins, _ = payload["plugins"].([]any)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok","reasoning":"thinking..."}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	llm := newOpenRouter(Config{
		Provider:   "openrouter",
		API:        APIOpenRouter,
		Model:      "openrouter/healer-alpha",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Headers: map[string]string{
			"HTTP-Referer": "https://example.com/app",
			"X-Title":      "caelis",
			"User-Agent":   "custom-client/9.9.9",
		},
		OpenRouter: OpenRouterConfig{
			Models:     []string{"openrouter/openai/gpt-4o-mini", "openrouter/anthropic/claude-sonnet-4"},
			Route:      "fallback",
			Transforms: []string{"middle-out"},
			Provider: map[string]any{
				"allow_fallbacks": true,
			},
			Plugins: []map[string]any{
				{"id": "web"},
			},
		},
		Timeout: 2 * time.Second,
	}, "token")

	var finalReasoning string
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			finalReasoning = resp.Response.Message.ReasoningText()
		}
	}
	if gotReferer != "https://example.com/app" || gotTitle != "caelis" {
		t.Fatalf("expected configured headers, got referer=%q title=%q", gotReferer, gotTitle)
	}
	if gotUserAgent != "custom-client/9.9.9" {
		t.Fatalf("expected configured User-Agent, got %q", gotUserAgent)
	}
	if gotModel != "openrouter/healer-alpha" {
		t.Fatalf("expected native openrouter model id preserved, got %q", gotModel)
	}
	if len(gotModels) != 2 {
		t.Fatalf("expected native openrouter models list, got %#v", gotModels)
	}
	if gotModels[0] != "openai/gpt-4o-mini" || gotModels[1] != "anthropic/claude-sonnet-4" {
		t.Fatalf("expected routed model ids normalized for request payload, got %#v", gotModels)
	}
	if gotRoute != "fallback" {
		t.Fatalf("expected native openrouter route, got %q", gotRoute)
	}
	if len(gotTransforms) != 1 || gotTransforms[0] != "middle-out" {
		t.Fatalf("expected native openrouter transforms, got %#v", gotTransforms)
	}
	if value, _ := gotProvider["allow_fallbacks"].(bool); !value {
		t.Fatalf("expected native openrouter provider preferences, got %#v", gotProvider)
	}
	if len(gotPlugins) != 1 {
		t.Fatalf("expected native openrouter plugins, got %#v", gotPlugins)
	}
	if finalReasoning != "thinking..." {
		t.Fatalf("expected native openrouter reasoning field, got %q", finalReasoning)
	}
}

func TestOpenRouterRequest_DoesNotForceStrictForOptionalStructuredOutput(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"openrouter/test","choices":[{"message":{"role":"assistant","content":"{\"outcome\":\"allow\"}"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newOpenRouter(Config{
		Provider:   "openrouter",
		API:        APIOpenRouter,
		Model:      "openrouter/test",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "review")},
		Output: &model.OutputSpec{
			Mode: model.OutputModeSchema,
			JSONSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"outcome":   map[string]any{"type": "string"},
					"rationale": map[string]any{"type": "string"},
				},
				"required": []any{"outcome"},
			},
		},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	responseFormat, _ := payload["response_format"].(map[string]any)
	jsonSchema, _ := responseFormat["json_schema"].(map[string]any)
	if _, ok := jsonSchema["strict"]; ok {
		t.Fatalf("json_schema.strict is present for optional OpenRouter schema: %#v", jsonSchema["strict"])
	}
}

func TestOpenRouterRequest_UsesStrictFunctionToolsFromRuntimeToolSpecs(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"openrouter/test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newOpenRouter(Config{
		Provider:   "openrouter",
		API:        APIOpenRouter,
		Model:      "openrouter/test",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "use tool")},
		Tools: porttool.ModelSpecs([]porttool.Tool{porttool.NamedTool{Def: porttool.Definition{
			Name:        "lookup",
			Description: "lookup closed schema",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer"},
				},
				"required": []any{"query"},
			},
		}}}),
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %#v, want one tool", payload["tools"])
	}
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if got := function["strict"]; got != true {
		t.Fatalf("function.strict = %#v, want true", got)
	}
	parameters := function["parameters"].(map[string]any)
	required, _ := parameters["required"].([]any)
	if got := strings.Join(stringSliceFromProviderAny(required), ","); got != "limit,query" {
		t.Fatalf("required = %#v, want limit,query", required)
	}
	properties := parameters["properties"].(map[string]any)
	limitType, _ := properties["limit"].(map[string]any)["type"].([]any)
	if got := strings.Join(stringSliceFromProviderAny(limitType), ","); got != "integer,null" {
		t.Fatalf("limit type = %#v, want integer,null", limitType)
	}
}

func TestOpenRouterStream_PropagatesTerminalFinishReason(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\"step 1\"},\"finish_reason\":null}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"model\":\"test-model\",\"choices\":[{\"delta\":{\"content\":\" done\"},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	llm := newOpenRouter(Config{
		Provider:   "openrouter",
		API:        APIOpenRouter,
		Model:      "openrouter/test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var final *model.Response
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("expected no stream error, got %v", err)
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			final = resp.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.FinishReason != model.FinishReasonToolCalls {
		t.Fatalf("expected tool_calls finish reason, got %q", final.FinishReason)
	}
}

func TestOpenAICompatNonStream_AppliesRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    50 * time.Millisecond,
	}, "token")

	var gotErr error
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			gotErr = err
		}
	}
	if gotErr == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !strings.Contains(strings.ToLower(gotErr.Error()), "context deadline exceeded") {
		t.Fatalf("expected context deadline exceeded, got %v", gotErr)
	}
}

func TestOpenAICompatNonStream_DefaultDoesNotApplyRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"test-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	llm := newOpenAICompat(Config{
		Provider:   "openai-compatible",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}, "token")

	var (
		gotErr    error
		finalText string
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			finalText = resp.Response.Message.TextContent()
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no timeout error, got %v", gotErr)
	}
	if finalText != "ok" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}
