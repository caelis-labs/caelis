package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestProviderConformanceMatrixStreamingSemantics(t *testing.T) {
	type conformanceCase struct {
		name         string
		build        func(*providerTestServer) model.LLM
		requestOK    func(string) bool
		handler      http.HandlerFunc
		wantProvider string
		wantModel    string
		wantText     string
		wantReason   string
		wantUsage    model.Usage
	}

	cases := []conformanceCase{
		{
			name: "openai-compatible",
			build: func(server *providerTestServer) model.LLM {
				return newOpenAICompat(Config{
					Provider:   "openai-compatible",
					Model:      "test-model",
					BaseURL:    server.URL,
					HTTPClient: server.Client(),
					Timeout:    2 * time.Second,
				}, "token")
			},
			requestOK: func(raw string) bool {
				return strings.Contains(raw, `"response_format"`) &&
					strings.Contains(raw, `"json_schema"`) &&
					strings.Contains(raw, `"lookup"`) &&
					strings.Contains(raw, `data:image/png;base64,aW1n`)
			},
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, `data: {"model":"test-model","choices":[{"delta":{"role":"assistant","reasoning_content":"think "},"finish_reason":null}]}`+"\n\n")
				_, _ = fmt.Fprint(w, `data: {"model":"test-model","choices":[{"delta":{"content":"answer"},"finish_reason":null}]}`+"\n\n")
				_, _ = fmt.Fprint(w, `data: {"model":"test-model","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
				_, _ = fmt.Fprint(w, `data: {"model":"test-model","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"completion_tokens_details":{"reasoning_tokens":3}}}`+"\n\n")
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			},
			wantProvider: "openai-compatible",
			wantModel:    "test-model",
			wantText:     "answer",
			wantReason:   "think ",
			wantUsage: model.Usage{
				PromptTokens:     11,
				CompletionTokens: 7,
				ReasoningTokens:  3,
				TotalTokens:      18,
			},
		},
		{
			name: "openrouter",
			build: func(server *providerTestServer) model.LLM {
				return newOpenRouter(Config{
					Provider:   "openrouter",
					API:        APIOpenRouter,
					Model:      "openrouter/openai/test-model",
					BaseURL:    server.URL,
					HTTPClient: server.Client(),
					Timeout:    2 * time.Second,
					OpenRouter: OpenRouterConfig{
						Models:     []string{"openrouter/anthropic/fallback-model"},
						Route:      "fallback",
						Transforms: []string{"middle-out"},
						Provider:   map[string]any{"allow_fallbacks": true},
						Plugins:    []map[string]any{{"id": "web"}},
					},
				}, "token")
			},
			requestOK: func(raw string) bool {
				return strings.Contains(raw, `"model":"openai/test-model"`) &&
					strings.Contains(raw, `"models":["anthropic/fallback-model"]`) &&
					strings.Contains(raw, `"route":"fallback"`) &&
					strings.Contains(raw, `"transforms":["middle-out"]`) &&
					strings.Contains(raw, `"allow_fallbacks":true`) &&
					strings.Contains(raw, `"plugins":[{"id":"web"}]`) &&
					strings.Contains(raw, `"response_format"`) &&
					strings.Contains(raw, `"lookup"`) &&
					strings.Contains(raw, `data:image/png;base64,aW1n`)
			},
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, `data: {"model":"openai/test-model","choices":[{"delta":{"role":"assistant","reasoning":"think "},"finish_reason":null}]}`+"\n\n")
				_, _ = fmt.Fprint(w, `data: {"model":"openai/test-model","choices":[{"delta":{"content":"answer"},"finish_reason":null}]}`+"\n\n")
				_, _ = fmt.Fprint(w, `data: {"model":"openai/test-model","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
				_, _ = fmt.Fprint(w, `data: {"model":"openai/test-model","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"completion_tokens_details":{"reasoning_tokens":3}}}`+"\n\n")
				_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			},
			wantProvider: "openrouter",
			wantModel:    "openrouter/openai/test-model",
			wantText:     "answer",
			wantReason:   "think ",
			wantUsage: model.Usage{
				PromptTokens:     11,
				CompletionTokens: 7,
				ReasoningTokens:  3,
				TotalTokens:      18,
			},
		},
		{
			name: "gemini",
			build: func(server *providerTestServer) model.LLM {
				return newGemini(Config{
					Provider:   "gemini",
					Model:      "gemini-2.5-flash",
					BaseURL:    server.URL,
					HTTPClient: server.Client(),
					Timeout:    2 * time.Second,
				}, "token")
			},
			requestOK: func(raw string) bool {
				return strings.Contains(raw, `"responseMimeType"`) &&
					strings.Contains(raw, `"responseSchema"`) &&
					strings.Contains(raw, `"functionDeclarations"`) &&
					(strings.Contains(raw, `"inlineData"`) || strings.Contains(raw, `"inline_data"`)) &&
					strings.Contains(raw, "aW1n")
			},
			handler: func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Path, ":streamGenerateContent") {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"think ","thought":true},{"functionCall":{"id":"call_1","name":"lookup","args":{"query":"x"}}},{"text":"answer"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"thoughtsTokenCount":3,"totalTokenCount":18}}`+"\n\n")
			},
			wantProvider: "gemini",
			wantModel:    "gemini-2.5-flash",
			wantText:     "answer",
			wantReason:   "think ",
			wantUsage: model.Usage{
				PromptTokens:     11,
				CompletionTokens: 7,
				ReasoningTokens:  3,
				TotalTokens:      18,
			},
		},
		{
			name: "anthropic",
			build: func(server *providerTestServer) model.LLM {
				return newAnthropic(Config{
					Provider:   "anthropic",
					API:        APIAnthropic,
					Model:      "claude-test",
					BaseURL:    server.URL,
					HTTPClient: server.Client(),
					Timeout:    2 * time.Second,
					Auth: AuthConfig{
						Type:  AuthAPIKey,
						Token: "sk-anthropic",
					},
				}, "sk-anthropic")
			},
			requestOK: func(raw string) bool {
				return strings.Contains(raw, `"output_config"`) &&
					strings.Contains(raw, `"json_schema"`) &&
					strings.Contains(raw, `"lookup"`) &&
					strings.Contains(raw, `"type":"image"`) &&
					strings.Contains(raw, `"media_type":"image/png"`) &&
					strings.Contains(raw, "aW1n")
			},
			handler: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/messages" {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "event: message_start\n")
				_, _ = fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":"","stop_sequence":"","usage":{"input_tokens":11,"output_tokens":0}}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_start\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_delta\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think "}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_stop\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_start\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_delta\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_stop\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_start\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_delta\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"x\"}"}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: content_block_stop\n")
				_, _ = fmt.Fprint(w, `data: {"type":"content_block_stop","index":2}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: message_delta\n")
				_, _ = fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":""},"usage":{"input_tokens":11,"output_tokens":7,"output_tokens_details":{"thinking_tokens":3}}}`+"\n\n")
				_, _ = fmt.Fprint(w, "event: message_stop\n")
				_, _ = fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
			},
			wantProvider: "anthropic",
			wantModel:    "claude-test",
			wantText:     "answer",
			wantReason:   "think ",
			wantUsage: model.Usage{
				PromptTokens:     11,
				CompletionTokens: 7,
				ReasoningTokens:  3,
				TotalTokens:      18,
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			var rawRequest string
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Fatalf("decode provider request: %v", err)
				}
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatalf("marshal provider request: %v", err)
				}
				rawRequest = string(raw)
				tt.handler(w, r)
			}))
			defer server.Close()

			final, reasoningDelta, textDelta := collectConformanceStream(t, tt.build(server), conformanceRequest())
			if rawRequest == "" {
				t.Fatal("provider request was not captured")
			}
			if !tt.requestOK(rawRequest) {
				t.Fatalf("provider request did not satisfy conformance checks: %s", rawRequest)
			}
			if final == nil {
				t.Fatal("expected terminal response")
			}
			if final.Provider != tt.wantProvider {
				t.Fatalf("provider = %q, want %q", final.Provider, tt.wantProvider)
			}
			if final.Model != tt.wantModel {
				t.Fatalf("model = %q, want %q", final.Model, tt.wantModel)
			}
			if got := final.Message.TextContent(); got != tt.wantText {
				t.Fatalf("final text = %q, want %q", got, tt.wantText)
			}
			if got := final.Message.ReasoningText(); got != tt.wantReason {
				t.Fatalf("final reasoning = %q, want %q", got, tt.wantReason)
			}
			if len(final.Message.ToolCalls()) != 1 {
				t.Fatalf("tool calls = %+v, want one call", final.Message.ToolCalls())
			}
			call := final.Message.ToolCalls()[0]
			if call.Name != "lookup" || !strings.Contains(call.Args, `"query"`) {
				t.Fatalf("tool call = %+v, want lookup query call", call)
			}
			if final.Usage != tt.wantUsage {
				t.Fatalf("usage = %+v, want %+v", final.Usage, tt.wantUsage)
			}
			if reasoningDelta == "" {
				t.Fatal("expected at least one reasoning delta")
			}
			if textDelta == "" {
				t.Fatal("expected at least one text delta")
			}
		})
	}
}

func TestProviderConformanceMatrixProviderExecutedToolUsage(t *testing.T) {
	type providerToolUsage interface {
		UsesProviderExecutedTools(*model.Request) bool
	}

	enabledSpec := func(provider, name string) []model.ToolSpec {
		return []model.ToolSpec{model.NewProviderExecutedToolSpec(provider, name, nil)}
	}
	disabledSpec := func(provider, name string) []model.ToolSpec {
		return []model.ToolSpec{model.NewProviderExecutedToolSpec(provider, name, map[string]json.RawMessage{
			"disabled": json.RawMessage(`true`),
		})}
	}

	cases := []struct {
		name      string
		llm       model.LLM
		enabled   []model.ToolSpec
		disabled  []model.ToolSpec
		wantUsage bool
	}{
		{
			name:      "gemini-google-search",
			llm:       newGemini(Config{Provider: "gemini", Model: "gemini-2.5-flash"}, "token"),
			enabled:   enabledSpec("gemini", geminiGoogleSearchToolName),
			disabled:  disabledSpec("gemini", geminiGoogleSearchToolName),
			wantUsage: true,
		},
		{
			name:      "anthropic-web-search",
			llm:       newAnthropic(Config{Provider: "anthropic", Model: "claude-test"}, "token"),
			enabled:   enabledSpec("anthropic", anthropicWebSearchToolName),
			disabled:  disabledSpec("anthropic", anthropicWebSearchToolName),
			wantUsage: true,
		},
		{
			name:      "deepseek-web-search",
			llm:       newDeepSeek(Config{Provider: "deepseek", Model: "deepseek-v4-pro"}, "token"),
			enabled:   enabledSpec("deepseek", anthropicWebSearchToolName),
			disabled:  disabledSpec("deepseek", anthropicWebSearchToolName),
			wantUsage: true,
		},
		{
			name:      "mimo-native-web-search",
			llm:       newMimo(Config{Provider: "mimo", Model: "mimo-test", BaseURL: "https://api.xiaomimimo.com/v1"}, "token"),
			enabled:   enabledSpec("xiaomi", mimoProviderWebSearchWireType),
			disabled:  disabledSpec("xiaomi", mimoProviderWebSearchWireType),
			wantUsage: true,
		},
		{
			name:      "mimo-token-plan-suppresses-web-search",
			llm:       newMimo(Config{Provider: "mimo", Model: "mimo-test", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1"}, "token"),
			enabled:   enabledSpec("xiaomi", mimoProviderWebSearchWireType),
			disabled:  disabledSpec("xiaomi", mimoProviderWebSearchWireType),
			wantUsage: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			usage, ok := tt.llm.(providerToolUsage)
			if !ok {
				t.Fatalf("%T does not expose provider-executed tool usage contract", tt.llm)
			}
			if got := usage.UsesProviderExecutedTools(&model.Request{}); got {
				t.Fatal("UsesProviderExecutedTools(empty) = true, want explicit tool opt-in")
			}
			if got := usage.UsesProviderExecutedTools(&model.Request{Tools: tt.enabled}); got != tt.wantUsage {
				t.Fatalf("UsesProviderExecutedTools(enabled) = %v, want %v", got, tt.wantUsage)
			}
			if got := usage.UsesProviderExecutedTools(&model.Request{Tools: tt.disabled}); got {
				t.Fatal("UsesProviderExecutedTools(disabled) = true, want false")
			}
		})
	}
}

func conformanceRequest() *model.Request {
	return &model.Request{
		Instructions: []model.Part{model.NewTextPart("system instruction")},
		Messages: []model.Message{
			model.NewMessage(
				model.RoleUser,
				model.NewTextPart("hi"),
				model.NewMediaPart(model.MediaModalityImage, model.MediaSource{
					Kind: model.MediaSourceInline,
					Data: "aW1n",
				}, "image/png", "pixel.png"),
			),
		},
		Tools: []model.ToolSpec{
			model.NewFunctionToolSpec("lookup", "Look up local data.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
				"required": []string{"query"},
			}),
		},
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
		Reasoning: model.ReasoningConfig{Effort: "high"},
		Stream:    true,
	}
}

func collectConformanceStream(t *testing.T, llm model.LLM, req *model.Request) (*model.Response, string, string) {
	t.Helper()
	var final *model.Response
	var reasoningDelta strings.Builder
	var textDelta strings.Builder
	for event, err := range llm.Generate(context.Background(), req) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event == nil {
			continue
		}
		if event.PartDelta != nil {
			switch event.PartDelta.Kind {
			case model.PartKindReasoning:
				reasoningDelta.WriteString(event.PartDelta.TextDelta)
			case model.PartKindText:
				textDelta.WriteString(event.PartDelta.TextDelta)
			}
		}
		if event.Response != nil && event.TurnComplete {
			resp := *event.Response
			final = &resp
		}
	}
	return final, reasoningDelta.String(), textDelta.String()
}
