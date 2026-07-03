package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/ports/model"
	"google.golang.org/genai"
)

func jsonArgs(v map[string]any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func TestListModelsRequiresRegistration(t *testing.T) {
	factory := NewFactory()
	if got := factory.ListModels(); len(got) != 0 {
		t.Fatalf("expected empty model list, got %v", got)
	}
	if _, err := factory.NewByAlias("deepseek/deepseek-v4-flash"); err == nil {
		t.Fatalf("expected unknown alias error without registration")
	}

	cfg := Config{
		Alias:               "deepseek/deepseek-v4-flash",
		Provider:            "deepseek",
		API:                 APIDeepSeek,
		Model:               "deepseek-v4-flash",
		BaseURL:             "https://api.deepseek.com/v1",
		ContextWindowTokens: 64000,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "secret",
		},
	}
	if err := factory.Register(cfg); err != nil {
		t.Fatalf("register provider config: %v", err)
	}
	list := factory.ListModels()
	if len(list) != 1 || list[0] != cfg.Alias {
		t.Fatalf("unexpected list models: %v", list)
	}
}

func TestFactoryRequiresTokenFromConfig(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-token-should-be-ignored")

	factory := NewFactory()
	cfg := Config{
		Alias:    "openai/gpt-4o-mini",
		Provider: "openai",
		API:      APIOpenAI,
		Model:    "gpt-4o-mini",
		BaseURL:  "https://api.openai.com/v1",
		Auth: AuthConfig{
			Type:     AuthAPIKey,
			TokenEnv: "OPENAI_API_KEY",
		},
	}
	if err := factory.Register(cfg); err != nil {
		t.Fatalf("register provider config: %v", err)
	}
	_, err := factory.NewByAlias(cfg.Alias)
	if err == nil {
		t.Fatalf("expected missing token error")
	}
	if !strings.Contains(err.Error(), "auth token is empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGeminiStream_DoesNotApplyRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/test-model:streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"!\"}]}}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":2,\"totalTokenCount\":3}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
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
	if finalText != "hello!" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

func TestAnthropicSDKStream_DoesNotApplyRequestTimeout(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"stop_reason\":\"\",\"stop_sequence\":\"\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(150 * time.Millisecond)
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"hello\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"!\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":\"\"},\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	llm := newAnthropic(Config{
		Provider:   "anthropic",
		API:        APIAnthropic,
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    50 * time.Millisecond,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "sk-anthropic",
		},
	}, "sk-anthropic")

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
		t.Fatalf("expected no stream error, got %v", gotErr)
	}
	if finalText != "hello!" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

func TestGeminiStream_PreservesDetailedUsageAcrossChunks(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/test-model:streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"hello\"}]}}],\"usageMetadata\":{\"promptTokenCount\":11,\"cachedContentTokenCount\":7,\"candidatesTokenCount\":1,\"thoughtsTokenCount\":5,\"totalTokenCount\":17}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: {\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":2,\"totalTokenCount\":18}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var usage model.Usage
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("expected no stream error, got %v", err)
		}
		if resp != nil && resp.Response != nil && resp.TurnComplete {
			usage = resp.Usage
		}
	}

	if usage.PromptTokens != 11 ||
		usage.CachedInputTokens != 7 ||
		usage.CompletionTokens != 2 ||
		usage.ReasoningTokens != 5 ||
		usage.TotalTokens != 18 {
		t.Fatalf("usage = %#v, want Gemini cached/thoughts preserved with latest totals", usage)
	}
}

func TestGeminiStream_EmitsReasoningChunks(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/test-model:streamGenerateContent") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"think-1\",\"thought\":true},{\"text\":\"hello\"}]}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"think-2\",\"thought\":true},{\"text\":\"!\"}]}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	var (
		reasoningChunks []string
		finalReasoning  string
		finalText       string
	)
	for resp, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   true,
		Reasoning: model.ReasoningConfig{
			Effort: "high",
		},
	}) {
		if err != nil {
			t.Fatalf("expected no stream error, got %v", err)
		}
		if resp == nil {
			continue
		}
		if resp.PartDelta != nil && resp.PartDelta.Kind == model.PartKindReasoning && strings.TrimSpace(resp.PartDelta.TextDelta) != "" {
			reasoningChunks = append(reasoningChunks, strings.TrimSpace(resp.PartDelta.TextDelta))
		}
		if resp.Response != nil && resp.TurnComplete {
			finalReasoning = strings.TrimSpace(resp.Response.Message.ReasoningText())
			finalText = strings.TrimSpace(resp.Response.Message.TextContent())
		}
	}
	if strings.Join(reasoningChunks, "|") != "think-1|think-2" {
		t.Fatalf("unexpected reasoning chunks: %v", reasoningChunks)
	}
	if finalReasoning != "think-1\nthink-2" {
		t.Fatalf("unexpected final reasoning %q", finalReasoning)
	}
	if finalText != "hello\n!" {
		t.Fatalf("unexpected final text %q", finalText)
	}
}

func TestGeminiRequest_IncludesMaxOutputTokens(t *testing.T) {
	var gotMax float64
	var gotThinkingLevel string
	var gotIncludeThoughts bool
	var gotThinkingBudget any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/test-model:generateContent") {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if cfg, ok := payload["generationConfig"].(map[string]any); ok {
			gotMax, _ = cfg["maxOutputTokens"].(float64)
			if thinking, ok := cfg["thinkingConfig"].(map[string]any); ok {
				gotThinkingLevel, _ = thinking["thinkingLevel"].(string)
				gotIncludeThoughts, _ = thinking["includeThoughts"].(bool)
				gotThinkingBudget = thinking["thinkingBudget"]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"thoughtsTokenCount":7,"totalTokenCount":2}}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:     "gemini",
		Model:        "test-model",
		BaseURL:      server.URL,
		HTTPClient:   server.Client(),
		Timeout:      2 * time.Second,
		MaxOutputTok: 3072,
	}, "token")

	var gotErr error
	var usage model.Usage
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
		Reasoning: model.ReasoningConfig{
			Effort: "high",
		},
	}) {
		if err != nil {
			gotErr = err
			continue
		}
		if event != nil && event.Response != nil {
			usage = event.Usage
		}
	}
	if gotErr != nil {
		t.Fatalf("expected no generate error, got %v", gotErr)
	}
	if gotMax != 3072 {
		t.Fatalf("expected generationConfig.maxOutputTokens=3072, got %v", gotMax)
	}
	if gotThinkingLevel != "HIGH" {
		t.Fatalf("expected generationConfig.thinkingConfig.thinkingLevel=HIGH, got %q", gotThinkingLevel)
	}
	if !gotIncludeThoughts {
		t.Fatalf("expected generationConfig.thinkingConfig.includeThoughts=true")
	}
	if gotThinkingBudget != nil {
		t.Fatalf("expected thinkingBudget omitted, got %v", gotThinkingBudget)
	}
	if usage.ReasoningTokens != 7 {
		t.Fatalf("usage.ReasoningTokens = %d, want Gemini thoughtsTokenCount", usage.ReasoningTokens)
	}
}

func TestGeminiRequest_Pre3UsesThinkingBudget(t *testing.T) {
	var gotThinkingLevel string
	var gotThinkingBudget float64
	var gotIncludeThoughts bool
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1beta/models/gemini-2.5-flash:generateContent") {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if cfg, ok := payload["generationConfig"].(map[string]any); ok {
			if thinking, ok := cfg["thinkingConfig"].(map[string]any); ok {
				gotThinkingLevel, _ = thinking["thinkingLevel"].(string)
				gotThinkingBudget, _ = thinking["thinkingBudget"].(float64)
				gotIncludeThoughts, _ = thinking["includeThoughts"].(bool)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "gemini-2.5-flash",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:    false,
		Reasoning: model.ReasoningConfig{Effort: "high"},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if gotThinkingLevel != "" {
		t.Fatalf("expected thinkingLevel omitted for pre-3 model, got %q", gotThinkingLevel)
	}
	if gotThinkingBudget != 8192 {
		t.Fatalf("expected thinkingBudget=8192 for high effort, got %v", gotThinkingBudget)
	}
	if !gotIncludeThoughts {
		t.Fatalf("expected includeThoughts=true for enabled reasoning")
	}
}

func TestGeminiRequest_Pre3DisableReasoningUsesZeroBudget(t *testing.T) {
	var gotThinkingBudget float64
	var gotIncludeThoughts bool
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if cfg, ok := payload["generationConfig"].(map[string]any); ok {
			if thinking, ok := cfg["thinkingConfig"].(map[string]any); ok {
				gotThinkingBudget, _ = thinking["thinkingBudget"].(float64)
				gotIncludeThoughts, _ = thinking["includeThoughts"].(bool)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "gemini-2.5-pro",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
		Reasoning: model.ReasoningConfig{
			Effort: "none",
		},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if gotThinkingBudget != 0 {
		t.Fatalf("expected thinkingBudget=0 when reasoning disabled, got %v", gotThinkingBudget)
	}
	if gotIncludeThoughts {
		t.Fatalf("expected includeThoughts=false when reasoning disabled")
	}
}

func TestGeminiRequest_BaseURLWithVersionPath(t *testing.T) {
	var gotPath string
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL + "/v1beta",
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:   false,
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if gotPath != "/v1beta/models/test-model:generateContent" {
		t.Fatalf("unexpected request path %q", gotPath)
	}
}

func TestGeminiRequest_XHighEffortFallsBackToHighLevel(t *testing.T) {
	var gotThinkingLevel string
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if cfg, ok := payload["generationConfig"].(map[string]any); ok {
			if thinking, ok := cfg["thinkingConfig"].(map[string]any); ok {
				gotThinkingLevel, _ = thinking["thinkingLevel"].(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]}}]}`)
	}))
	defer server.Close()

	llm := newGemini(Config{
		Provider:   "gemini",
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
	}, "token")

	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hi")},
		Stream:    false,
		Reasoning: model.ReasoningConfig{Effort: "xhigh"},
	}) {
		if err != nil {
			t.Fatalf("expected no generate error, got %v", err)
		}
	}

	if gotThinkingLevel != "HIGH" {
		t.Fatalf("expected xhigh fallback to HIGH, got %q", gotThinkingLevel)
	}
}

func TestFromToOpenAIMessage(t *testing.T) {
	llm := newOpenAICompat(Config{
		Provider: "openai-compatible",
		Model:    "gpt-4o-mini",
		BaseURL:  "https://api.openai.com/v1",
		Timeout:  time.Second,
	}, "token")
	in := model.MessageFromAssistantParts("", "thinking...", []model.ToolCall{{
		ID:   "c1",
		Name: "echo",
		Args: jsonArgs(map[string]any{"text": "hello"}),
	}})
	raw := llm.fromKernelMessage(in)
	if raw.ReasoningContent != nil {
		t.Fatalf("did not expect reasoning_content in generic openai-compatible request")
	}
	back, err := toKernelMessage(openAICompatMsg{
		Role:       raw.Role,
		Content:    raw.Content,
		ToolCallID: raw.ToolCallID,
		ToolCalls:  raw.ToolCalls,
		ReasoningContent: func() string {
			if raw.ReasoningContent == nil {
				return ""
			}
			return *raw.ReasoningContent
		}(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(back.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(back.ToolCalls()))
	}
	if back.ToolCalls()[0].Name != "echo" {
		t.Fatalf("unexpected tool name %q", back.ToolCalls()[0].Name)
	}
	if back.ReasoningText() != "" {
		t.Fatalf("expected no reasoning in generic openai-compatible roundtrip, got %q", back.ReasoningText())
	}
}

func TestToKernelMessage_OpenAICompatKeepsRawToolArgsOnDecodeFailure(t *testing.T) {
	msg, err := toKernelMessage(openAICompatMsg{
		Role: "assistant",
		ToolCalls: []openAICompatToolCall{
			{
				ID:   "c1",
				Type: "function",
				Function: openAICompatCallFunction{
					Name:      "WRITE",
					Arguments: `{"path":`,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("expected no hard parse error, got %v", err)
	}
	if len(msg.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls()))
	}
	if got := strings.TrimSpace(msg.ToolCalls()[0].Args); got == "" {
		t.Fatalf("expected raw args kept, got %#v", msg.ToolCalls()[0])
	}
}

func TestDeepSeekUsesAnthropicCompatibleEndpointAndBearerAuth(t *testing.T) {
	var payload map[string]any
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer deepseek-token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		if got := r.Header.Get("x-api-key"); got != "" {
			t.Fatalf("x-api-key = %q, want empty for DeepSeek Anthropic-compatible auth", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		if got := payload["max_tokens"]; got != float64(deepSeekDefaultMaxTokens) {
			t.Fatalf("max_tokens = %#v, want DeepSeek default %d", got, deepSeekDefaultMaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-pro","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":42,"output_tokens":8,"cache_read_input_tokens":31,"output_tokens_details":{"thinking_tokens":3}}}`)
	}))
	defer server.Close()

	llm := newDeepSeek(Config{
		Provider:   "deepseek",
		Model:      "deepseek-v4-pro",
		BaseURL:    server.URL + "/anthropic",
		HTTPClient: server.Client(),
		Timeout:    time.Second,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")

	var final *model.Response
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil {
			final = event.Response
		}
	}
	if payload["model"] != "deepseek-v4-pro" {
		t.Fatalf("model = %#v, want deepseek-v4-pro", payload["model"])
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.Provider != "deepseek" {
		t.Fatalf("provider = %q, want deepseek", final.Provider)
	}
	if got := final.Message.TextContent(); got != "ok" {
		t.Fatalf("text = %q, want ok", got)
	}
	if final.Usage.PromptTokens != 42 || final.Usage.CachedInputTokens != 31 || final.Usage.CompletionTokens != 8 || final.Usage.ReasoningTokens != 3 || final.Usage.TotalTokens != 81 {
		t.Fatalf("usage = %+v, want DeepSeek Anthropic usage propagated", final.Usage)
	}
}

func TestDeepSeekThinkingConfigMapsEmptyMaxAndNone(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    "deepseek-v4-pro",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")
	typed, ok := llm.(*anthropicSDKLLM)
	if !ok {
		t.Fatalf("newDeepSeek() = %T, want *anthropicSDKLLM", llm)
	}

	params, err := typed.buildRequest(&model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	})
	if err != nil {
		t.Fatalf("buildRequest(default) error = %v", err)
	}
	if params.Thinking.OfEnabled != nil || params.Thinking.OfDisabled != nil {
		t.Fatalf("empty thinking = %#v, want no provider-level default", params.Thinking)
	}

	params, err = typed.buildRequest(&model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Reasoning: model.ReasoningConfig{Effort: "max"},
		Output:    &model.OutputSpec{MaxOutputTokens: 393216},
	})
	if err != nil {
		t.Fatalf("buildRequest(max) error = %v", err)
	}
	if params.MaxTokens != 393216 {
		t.Fatalf("MaxTokens = %d, want explicit DeepSeek max output", params.MaxTokens)
	}
	if params.Thinking.OfEnabled == nil || params.Thinking.OfEnabled.BudgetTokens != 16384 {
		t.Fatalf("max thinking = %#v, want enabled max budget", params.Thinking)
	}

	params, err = typed.buildRequest(&model.Request{
		Messages:  []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Reasoning: model.ReasoningConfig{Effort: "none"},
	})
	if err != nil {
		t.Fatalf("buildRequest(none) error = %v", err)
	}
	if params.Thinking.OfDisabled == nil {
		t.Fatalf("none thinking = %#v, want disabled", params.Thinking)
	}
}

func TestDeepSeekThinkingContentWithoutUsageDetailsDoesNotFabricateReasoningTokens(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-pro","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"visible reasoning","signature":"sig_1"},{"type":"text","text":"ok"}],"usage":{"input_tokens":42,"output_tokens":8}}`)
	}))
	defer server.Close()

	llm := newDeepSeek(Config{
		Provider:   "deepseek",
		Model:      "deepseek-v4-pro",
		BaseURL:    server.URL + "/anthropic",
		HTTPClient: server.Client(),
		Timeout:    time.Second,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")

	var final *model.Response
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil {
			final = event.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if strings.TrimSpace(final.Message.ReasoningText()) == "" {
		t.Fatalf("reasoning text = %q, want visible thinking content", final.Message.ReasoningText())
	}
	if final.Usage.ReasoningTokens != 0 {
		t.Fatalf("ReasoningTokens = %d, want 0 without provider token details", final.Usage.ReasoningTokens)
	}
}

func TestDeepSeekProviderExecutedWebSearchPOC(t *testing.T) {
	var calls int
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/anthropic/v1/messages" {
			http.NotFound(w, r)
			return
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		tools, _ := payload["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tools len = %d, want DeepSeek web_search tool: %#v", len(tools), payload["tools"])
		}
		webSearch, _ := tools[0].(map[string]any)
		if got := webSearch["type"]; got != anthropicWebSearchTool20260209 {
			t.Fatalf("web_search type = %#v, want %s", got, anthropicWebSearchTool20260209)
		}
		if got := webSearch["name"]; got != anthropicWebSearchToolName {
			t.Fatalf("web_search name = %#v, want %s", got, anthropicWebSearchToolName)
		}
		if got := webSearch["max_uses"]; got != float64(2) {
			t.Fatalf("web_search max_uses = %#v, want 2", got)
		}

		switch calls {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"deepseek-v4-pro","stop_reason":"end_turn","content":[{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"DeepSeek API docs"}},{"type":"web_search_tool_result","tool_use_id":"srv_1","content":[{"type":"web_search_result","title":"DeepSeek API Docs","url":"https://api-docs.deepseek.com/","encrypted_content":"encrypted","page_age":"2026-06-23"}]},{"type":"text","text":"search answer"}],"usage":{"input_tokens":10,"output_tokens":5}}`)
		case 2:
			messages, _ := payload["messages"].([]any)
			if len(messages) != 3 {
				t.Fatalf("messages len = %d, want prior assistant server-tool history replayed", len(messages))
			}
			assistantMsg, _ := messages[1].(map[string]any)
			content, _ := assistantMsg["content"].([]any)
			if len(content) != 3 {
				t.Fatalf("assistant content len = %d, want server_tool_use + web_search_tool_result + text: %#v", len(content), assistantMsg["content"])
			}
			serverUse, _ := content[0].(map[string]any)
			if got := serverUse["type"]; got != anthropicReplayKindServerToolUse {
				t.Fatalf("assistant content[0].type = %#v, want server_tool_use", got)
			}
			result, _ := content[1].(map[string]any)
			if got := result["type"]; got != anthropicReplayKindWebSearch {
				t.Fatalf("assistant content[1].type = %#v, want web_search_tool_result", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"msg_2","type":"message","role":"assistant","model":"deepseek-v4-pro","stop_reason":"end_turn","content":[{"type":"text","text":"continued"}],"usage":{"input_tokens":20,"output_tokens":3}}`)
		default:
			t.Fatalf("unexpected request call %d", calls)
		}
	}))
	defer server.Close()

	llm := newDeepSeek(Config{
		Provider:   "deepseek",
		Model:      "deepseek-v4-pro",
		BaseURL:    server.URL + "/anthropic",
		HTTPClient: server.Client(),
		Timeout:    time.Second,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")
	req := &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "use web search")},
		Tools: []model.ToolSpec{
			model.NewProviderExecutedToolSpec("deepseek", anthropicWebSearchToolName, map[string]json.RawMessage{
				"max_uses": json.RawMessage(`2`),
			}),
		},
	}

	providerTools, ok := llm.(interface{ UsesProviderExecutedTools(*model.Request) bool })
	if !ok {
		t.Fatalf("newDeepSeek() = %T, want provider-executed tool detection", llm)
	}
	if !providerTools.UsesProviderExecutedTools(req) {
		t.Fatal("UsesProviderExecutedTools() = false, want explicit DeepSeek web_search visible to retry policy")
	}

	var first *model.Response
	for event, err := range llm.Generate(context.Background(), req) {
		if err != nil {
			t.Fatalf("Generate() first call error = %v", err)
		}
		if event != nil && event.Response != nil {
			first = event.Response
		}
	}
	if first == nil {
		t.Fatal("expected first response")
	}
	if got := first.Message.TextContent(); got != "search answer" {
		t.Fatalf("first text = %q, want search answer", got)
	}
	if calls := first.Message.ToolCalls(); len(calls) != 0 {
		t.Fatalf("tool calls = %+v, want no client-side tool calls for provider-executed web_search", calls)
	}
	reasoningParts := first.Message.ReasoningParts()
	if len(reasoningParts) != 2 {
		t.Fatalf("reasoning replay parts len = %d, want server tool use and result", len(reasoningParts))
	}
	if reasoningParts[0].Replay == nil || reasoningParts[0].Replay.Kind != anthropicReplayKindServerToolUse {
		t.Fatalf("first replay part = %+v, want server_tool_use", reasoningParts[0])
	}
	if reasoningParts[1].Replay == nil || reasoningParts[1].Replay.Kind != anthropicReplayKindWebSearch {
		t.Fatalf("second replay part = %+v, want web_search_tool_result", reasoningParts[1])
	}

	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "use web search"),
			first.Message,
			model.NewTextMessage(model.RoleUser, "continue from previous search"),
		},
		Tools: req.Tools,
	}) {
		if err != nil {
			t.Fatalf("Generate() replay call error = %v", err)
		}
		if event != nil && event.Response != nil && event.Response.Message.TextContent() != "continued" {
			t.Fatalf("replay text = %q, want continued", event.Response.Message.TextContent())
		}
	}
	if calls != 2 {
		t.Fatalf("server calls = %d, want 2", calls)
	}
}

func TestDeepSeekLiveProviderExecutedWebSearch(t *testing.T) {
	if os.Getenv("CAELIS_REAL_DEEPSEEK_WEB_SEARCH") != "1" {
		t.Skip("set CAELIS_REAL_DEEPSEEK_WEB_SEARCH=1 with DeepSeek Anthropic-compatible credentials to run")
	}
	token := strings.TrimSpace(os.Getenv("ANTHROPIC_AUTH_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	}
	if token == "" {
		t.Fatal("ANTHROPIC_AUTH_TOKEN or DEEPSEEK_API_KEY is required")
	}
	baseURL := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL"))
	if baseURL == "" {
		baseURL = deepSeekDefaultAnthropicBaseURL
	}
	modelName := strings.TrimSpace(os.Getenv("ANTHROPIC_MODEL"))
	if modelName == "" {
		modelName = "deepseek-v4-pro"
	}

	llm := newDeepSeek(Config{
		Provider: "deepseek",
		Model:    modelName,
		BaseURL:  baseURL,
		Timeout:  90 * time.Second,
		Auth: AuthConfig{
			Type:  AuthBearerToken,
			Token: token,
		},
	}, token)
	searcher, ok := llm.(model.WebSearcher)
	if !ok {
		t.Fatalf("newDeepSeek() = %T, want WebSearcher", llm)
	}

	resp, err := searcher.SearchWeb(context.Background(), model.WebSearchRequest{
		Query:      "current official DeepSeek API documentation homepage URL",
		MaxResults: 2,
	})
	if err != nil {
		t.Fatalf("SearchWeb() live DeepSeek web_search error = %v", err)
	}
	if strings.TrimSpace(resp.Answer) == "" {
		t.Fatal("expected live answer")
	}
	if len(resp.Results) == 0 {
		t.Fatalf("expected live search results, answer=%q", resp.Answer)
	}
	t.Logf("live DeepSeek SearchWeb ok: answer=%q results=%+v", resp.Answer, resp.Results)
}

func TestOpenAICompatProviderSpecificStructuredOutputStrategy(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"outcome": map[string]any{"type": "string"},
		},
		"required": []any{"outcome"},
	}
	tests := []struct {
		name string
		llm  *openAICompatLLM
		want string
	}{
		{
			name: "mimo",
			llm: newMimo(Config{
				Provider: "xiaomi",
				Model:    "mimo-v2-pro",
			}, "token").(*mimoLLM).openAICompatLLM,
			want: "json_object",
		},
		{
			name: "volcengine",
			llm: newVolcengine(Config{
				Provider: "volcengine",
				Model:    "doubao-seed",
			}, "token").(*openAICompatLLM),
			want: "json_object",
		},
		{
			name: "openai-compatible",
			llm: newOpenAICompat(Config{
				Provider: "openai-compatible",
				Model:    "gpt-compatible",
			}, "token"),
			want: "json_schema",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			payload := openAICompatRequest{}
			applyOpenAICompatOutput(&payload, &model.OutputSpec{
				Mode:       model.OutputModeSchema,
				JSONSchema: schema,
			}, tc.llm.options.StructuredOutput)
			if payload.ResponseFormat == nil {
				t.Fatal("ResponseFormat = nil")
			}
			if payload.ResponseFormat.Type != tc.want {
				t.Fatalf("ResponseFormat.Type = %q, want %q", payload.ResponseFormat.Type, tc.want)
			}
			if tc.want == "json_object" && payload.ResponseFormat.JSONSchema != nil {
				t.Fatalf("JSONSchema = %#v, want nil for json_object strategy", payload.ResponseFormat.JSONSchema)
			}
		})
	}
}

func TestOpenAICompatProviderSpecificStrictToolStrategy(t *testing.T) {
	closedTool := model.ToolDefinition{
		Name:        "lookup",
		Description: "lookup closed schema",
		Strict:      true,
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []any{"query"},
		},
	}
	tests := []struct {
		name       string
		llm        *openAICompatLLM
		wantStrict bool
	}{
		{
			name: "mimo",
			llm: newMimo(Config{
				Provider: "xiaomi",
				Model:    "mimo-v2-pro",
			}, "token").(*mimoLLM).openAICompatLLM,
			wantStrict: false,
		},
		{
			name: "volcengine",
			llm: newVolcengine(Config{
				Provider: "volcengine",
				Model:    "doubao-seed",
			}, "token").(*openAICompatLLM),
			wantStrict: false,
		},
		{
			name: "openai-compatible",
			llm: newOpenAICompat(Config{
				Provider: "openai-compatible",
				Model:    "gpt-compatible",
			}, "token"),
			wantStrict: false,
		},
		{
			name: "openai",
			llm: newOpenAICompat(Config{
				Provider: "openai",
				API:      APIOpenAI,
				Model:    "gpt-5",
			}, "token"),
			wantStrict: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tools := fromKernelTools([]model.ToolDefinition{closedTool}, tc.llm.options.StrictFunctionTools)
			if len(tools) != 1 {
				t.Fatalf("tools len = %d, want 1", len(tools))
			}
			if tools[0].Function.Strict != tc.wantStrict {
				t.Fatalf("Function.Strict = %v, want %v", tools[0].Function.Strict, tc.wantStrict)
			}
		})
	}
}

func TestCodeFreeDoesNotApplyReasoningPayload(t *testing.T) {
	llm := newCodeFree(Config{
		Provider: "codefree",
		Model:    "GLM-5.1",
		BaseURL:  "https://www.srdcloud.cn",
		Timeout:  time.Second,
	}).(*codeFreeLLM)
	if llm.options.ApplyReasoning != nil {
		t.Fatal("CodeFree ApplyReasoning is configured, want nil")
	}
}

func TestMimoProviderUsesThinkingPayload(t *testing.T) {
	llm := newMimo(Config{
		Provider: "xiaomi",
		Model:    "mimo",
		BaseURL:  "https://api.xiaomimimo.com/v1",
		Timeout:  time.Second,
	}, "token").(*mimoLLM).openAICompatLLM
	payload := openAICompatRequest{
		Model: "mimo",
		Messages: llm.fromKernelMessages(nil, []model.Message{
			model.NewTextMessage(model.RoleUser, "hello"),
		}),
	}
	llm.options.ApplyReasoning(&payload, model.ReasoningConfig{Effort: "high"})
	if payload.Thinking == nil || payload.Thinking.Type != "enabled" {
		t.Fatalf("expected mimo thinking payload, got %#v", payload.Thinking)
	}
	if payload.Reasoning != nil || payload.ReasoningEffort != "" {
		t.Fatalf("did not expect openai reasoning fields for mimo payload")
	}
}

func TestMimoRequest_DoesNotIncludeWebSearchByDefaultForSupportedModels(t *testing.T) {
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
		_, _ = fmt.Fprint(w, `{"model":"mimo-v2.5-pro","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newMimo(Config{
		Provider:   "xiaomi",
		Model:      "mimo-v2.5-pro",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    time.Second,
	}, "token").(*mimoLLM)

	if llm.UsesProviderExecutedTools(&model.Request{}) {
		t.Fatal("UsesProviderExecutedTools() = true, want no implicit MiMo web_search")
	}
	for _, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "latest news")},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}

	if _, ok := payload["tools"]; ok {
		t.Fatalf("payload tools = %#v, want no implicit MiMo web_search", payload["tools"])
	}
}

func TestMimoRequest_DisabledProviderExecutedWebSearchSpecOmitsTool(t *testing.T) {
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
		_, _ = fmt.Fprint(w, `{"model":"mimo-v2.5-pro","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newMimo(Config{
		Provider:   "xiaomi",
		Model:      "mimo-v2.5-pro",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    time.Second,
	}, "token").(*mimoLLM)
	req := &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Tools: []model.ToolSpec{
			model.NewProviderExecutedToolSpec("xiaomi", mimoProviderWebSearchWireType, map[string]json.RawMessage{
				"disabled": json.RawMessage(`true`),
			}),
		},
	}

	if llm.UsesProviderExecutedTools(req) {
		t.Fatal("UsesProviderExecutedTools() = true, want disabled MiMo web_search hidden from retry policy")
	}
	for _, err := range llm.Generate(context.Background(), req) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}
	if _, ok := payload["tools"]; ok {
		t.Fatalf("payload tools = %#v, want omitted when MiMo web_search is disabled", payload["tools"])
	}
}

func TestMimoRequest_ProviderExecutedWebSearchCombinesWithFunctionToolsAndDetails(t *testing.T) {
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
		_, _ = fmt.Fprint(w, `{"model":"custom-mimo","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	llm := newMimo(Config{
		Provider:   "xiaomi",
		Model:      "custom-mimo",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    time.Second,
	}, "token").(*mimoLLM)
	req := &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "weather")},
		Tools: []model.ToolSpec{
			model.NewFunctionToolSpec("lookup", "Look up local data.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
				},
			}),
			model.NewProviderExecutedToolSpec("xiaomi", mimoProviderWebSearchWireType, map[string]json.RawMessage{
				"type":          json.RawMessage(`"web_search"`),
				"max_keyword":   json.RawMessage(`3`),
				"force_search":  json.RawMessage(`true`),
				"limit":         json.RawMessage(`1`),
				"user_location": json.RawMessage(`{"type":"approximate","country":"China","region":"Hubei","city":"Wuhan"}`),
			}),
		},
	}

	if !llm.UsesProviderExecutedTools(req) {
		t.Fatal("UsesProviderExecutedTools() = false, want explicit MiMo web_search visible to retry policy")
	}
	for _, err := range llm.Generate(context.Background(), req) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
	}

	tools, _ := payload["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools len = %d, want function plus web_search: %#v", len(tools), payload["tools"])
	}
	functionTool, _ := tools[0].(map[string]any)
	if got := functionTool["type"]; got != "function" {
		t.Fatalf("first tool type = %#v, want function", got)
	}
	webSearch, _ := tools[1].(map[string]any)
	if got := webSearch["type"]; got != "web_search" {
		t.Fatalf("second tool type = %#v, want web_search", got)
	}
	if got := webSearch["max_keyword"]; got != float64(3) {
		t.Fatalf("max_keyword = %#v, want 3", got)
	}
	if got := webSearch["force_search"]; got != true {
		t.Fatalf("force_search = %#v, want true", got)
	}
	if got := webSearch["limit"]; got != float64(1) {
		t.Fatalf("limit = %#v, want 1", got)
	}
	userLocation, _ := webSearch["user_location"].(map[string]any)
	if got := userLocation["city"]; got != "Wuhan" {
		t.Fatalf("user_location.city = %#v, want Wuhan", got)
	}
}

func TestMimoUsagePropagatesPromptTokenDetailsCachedTokens(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"model":"mimo-v2-flash","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":64,"completion_tokens":9,"total_tokens":73,"prompt_tokens_details":{"cached_tokens":48,"audio_tokens":0}}}`)
	}))
	defer server.Close()

	llm := newMimo(Config{
		Provider:   "xiaomi",
		Model:      "mimo-v2-flash",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    time.Second,
	}, "token")

	var final *model.Response
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil {
			final = event.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.Usage.PromptTokens != 64 || final.Usage.CachedInputTokens != 48 || final.Usage.CompletionTokens != 9 || final.Usage.TotalTokens != 73 {
		t.Fatalf("usage = %+v, want MiMo cached usage propagated", final.Usage)
	}
}

func TestVolcengineCodingPlanReasoningDisabledSendsThinkingDisabled(t *testing.T) {
	llm := newVolcengineCodingPlan(Config{
		Provider: "volcengine",
		Model:    "doubao-seed-2.0-pro",
		BaseURL:  "https://ark.cn-beijing.volces.com/api/coding/v3",
		Timeout:  time.Second,
	}, "token").(*openAICompatLLM)
	payload := openAICompatRequest{
		Model: "doubao-seed-2.0-pro",
		Messages: llm.fromKernelMessages(nil, []model.Message{
			model.NewTextMessage(model.RoleUser, "hello"),
		}),
	}
	llm.options.ApplyReasoning(&payload, model.ReasoningConfig{Effort: "none"})
	if payload.Thinking == nil || payload.Thinking.Type != "disabled" {
		t.Fatalf("expected volcengine coding plan payload to disable thinking explicitly, got %#v", payload.Thinking)
	}
	if payload.Reasoning != nil || payload.ReasoningEffort != "" {
		t.Fatalf("did not expect openai reasoning fields for volcengine coding plan payload")
	}
}

func TestOpenAICompatEffortReasoningUsesOpenAIReasoningPayload(t *testing.T) {
	llm := newOpenAICompat(Config{
		Provider:      "openai-compatible",
		Model:         "gpt-5",
		BaseURL:       "https://example.com/v1",
		Timeout:       time.Second,
		ReasoningMode: "effort",
	}, "token")
	payload := openAICompatRequest{
		Model: "gpt-5",
		Messages: llm.fromKernelMessages(nil, []model.Message{
			model.NewTextMessage(model.RoleUser, "hello"),
		}),
	}
	llm.options.ApplyReasoning(&payload, model.ReasoningConfig{Effort: "high"})
	if payload.Reasoning == nil || payload.Reasoning.Effort != "high" {
		t.Fatalf("expected effort openai-compatible payload to carry reasoning effort, got %#v", payload.Reasoning)
	}
	if payload.ReasoningEffort != "high" {
		t.Fatalf("expected compatibility reasoning_effort=high, got %q", payload.ReasoningEffort)
	}
	if payload.Thinking != nil {
		t.Fatalf("did not expect thinking payload for effort openai-compatible request")
	}
}

func TestOpenAICompatMessageTransform_SkipsInvalidToolResponses(t *testing.T) {
	llm := newOpenAICompat(Config{
		Provider: "openai-compatible",
		Model:    "test-model",
		BaseURL:  "https://example.com/v1",
		Timeout:  time.Second,
	}, "token")
	messages := llm.fromKernelMessages(nil, []model.Message{
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:   "call_1",
			Name: "echo",
			Args: jsonArgs(map[string]any{"text": "x"}),
		}}, ""),
		model.MessageFromToolResponse(&model.ToolResponse{
			ID:     "",
			Name:   "echo",
			Result: map[string]any{"echo": "missing-id"},
		}),
		model.MessageFromToolResponse(&model.ToolResponse{
			ID:     "call_2",
			Name:   "echo",
			Result: map[string]any{"echo": "unmatched-id"},
		}),
		model.MessageFromToolResponse(&model.ToolResponse{
			ID:     "call_1",
			Name:   "echo",
			Result: map[string]any{"echo": "ok"},
		}),
		{
			Role: model.RoleTool,
		},
	})
	if len(messages) != 2 {
		t.Fatalf("expected 2 transformed messages, got %d", len(messages))
	}
	if messages[1].Role != string(model.RoleTool) {
		t.Fatalf("expected tool role at index 1, got %q", messages[1].Role)
	}
	if messages[1].ToolCallID != "call_1" {
		t.Fatalf("expected tool_call_id=call_1, got %q", messages[1].ToolCallID)
	}
}

func TestOpenAICompatMessageTransformPreservesTerminalLikeCommandPayload(t *testing.T) {
	const deniedPath = "/home/test/go/pkg/mod/cache/download/work.ctyun.cn/git/ctstack_cmp_v2/system/@v/v0.0.0.tmp"
	llm := newOpenAICompat(Config{
		Provider: "openai-compatible",
		Model:    "test-model",
		BaseURL:  "https://example.com/v1",
		Timeout:  time.Second,
	}, "token")
	messages := llm.fromKernelMessages(nil, []model.Message{
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:   "call_1",
			Name: "RUN_COMMAND",
			Args: jsonArgs(map[string]any{"command": "go build ./... 2>&1"}),
		}}, ""),
		{
			Role: model.RoleTool,
			Parts: []model.Part{model.NewToolResultJSONPart("call_1", "RUN_COMMAND", map[string]any{
				"stdout":    "go: writing stat cache: open " + deniedPath + ": read-only file system\n",
				"stderr":    "",
				"error":     "Sandbox permission denied. Use a writable workspace path or request elevated permissions.",
				"exit_code": 1,
			}, false)},
		},
	})
	if len(messages) != 2 {
		t.Fatalf("expected 2 transformed messages, got %d", len(messages))
	}
	if messages[1].Role != string(model.RoleTool) || messages[1].ToolCallID != "call_1" {
		t.Fatalf("unexpected tool message: %+v", messages[1])
	}
	content, _ := messages[1].Content.(string)
	if !strings.Contains(content, "stdout") ||
		!strings.Contains(content, "error") ||
		!strings.Contains(content, "Sandbox permission denied") ||
		!strings.Contains(content, "exit_code") ||
		!strings.Contains(content, deniedPath) {
		t.Fatalf("tool content = %q, want raw terminal payload plus concise sandbox error", content)
	}
}

func TestAnthropicMessageTransform(t *testing.T) {
	system := toAnthropicSystem([]model.Part{model.NewTextPart("sys")})
	msgs := toAnthropicMessages([]model.Message{
		model.NewTextMessage(model.RoleSystem, "sys"),
		model.NewTextMessage(model.RoleUser, "u"),
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:   "call1",
			Name: "echo",
			Args: jsonArgs(map[string]any{"text": "x"}),
		}}, ""),
	})
	if len(system) != 1 || system[0].Text != "sys" {
		t.Fatalf("unexpected system blocks: %+v", system)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected >= 2 messages, got %d", len(msgs))
	}
}

func TestAnthropicMessageTransformMergesConsecutiveToolResults(t *testing.T) {
	msgs := toAnthropicMessages([]model.Message{
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{
			{
				ID:   "call1",
				Name: "SEARCH",
				Args: jsonArgs(map[string]any{"pattern": "tests"}),
			},
			{
				ID:   "call2",
				Name: "READ",
				Args: jsonArgs(map[string]any{"path": "calculator/tests/test_basic.py"}),
			},
		}, ""),
		{
			Role: model.RoleTool,
			Parts: []model.Part{model.NewToolResultJSONPart("call1", "SEARCH", map[string]any{
				"matches": []any{"test_add"},
			}, false)},
		},
		{
			Role: model.RoleTool,
			Parts: []model.Part{model.NewToolResultJSONPart("call2", "READ", map[string]any{
				"content": "def test_add(): pass",
			}, false)},
		},
		model.NewTextMessage(model.RoleUser, "continue"),
	})
	if len(msgs) != 3 {
		t.Fatalf("len(messages) = %d, want assistant, merged tool results, user", len(msgs))
	}
	raw, err := json.Marshal(msgs[1])
	if err != nil {
		t.Fatalf("marshal merged tool result message: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode merged tool result message: %v", err)
	}
	content, _ := payload["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("merged tool result content len = %d, want 2: %s", len(content), raw)
	}
	ids := []string{
		fmt.Sprint(content[0].(map[string]any)["tool_use_id"]),
		fmt.Sprint(content[1].(map[string]any)["tool_use_id"]),
	}
	if strings.Join(ids, ",") != "call1,call2" {
		t.Fatalf("merged tool result ids = %v, want call1,call2", ids)
	}
}

func TestAnthropicSDKNonStream_NormalizesBaseURLAndMapsParts(t *testing.T) {
	var sawCustomTool bool
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "sk-anthropic" {
			t.Fatalf("expected x-api-key header, got %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Fatal("expected anthropic-version header")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request payload: %v", err)
		}
		system, _ := payload["system"].([]any)
		if len(system) != 1 {
			t.Fatalf("expected one system block, got %+v", payload["system"])
		}
		sys0, _ := system[0].(map[string]any)
		if sys0["text"] != "system instruction" {
			t.Fatalf("unexpected system block %+v", sys0)
		}
		messages, _ := payload["messages"].([]any)
		if len(messages) != 3 {
			t.Fatalf("expected 3 messages, got %+v", payload["messages"])
		}
		assistant, _ := messages[1].(map[string]any)
		assistantContent, _ := assistant["content"].([]any)
		if len(assistantContent) != 3 {
			t.Fatalf("expected assistant thinking/text/tool_use blocks, got %+v", assistantContent)
		}
		thinking, _ := assistantContent[0].(map[string]any)
		if thinking["type"] != "thinking" || thinking["signature"] != "sig-prev" || thinking["thinking"] != "prior reasoning" {
			t.Fatalf("unexpected thinking block %+v", thinking)
		}
		toolUse, _ := assistantContent[2].(map[string]any)
		if toolUse["type"] != "tool_use" || toolUse["id"] != "call-prev" || toolUse["name"] != "echo" {
			t.Fatalf("unexpected tool_use block %+v", toolUse)
		}
		toolMessage, _ := messages[2].(map[string]any)
		toolContent, _ := toolMessage["content"].([]any)
		if len(toolContent) != 1 {
			t.Fatalf("expected single tool_result block, got %+v", toolMessage)
		}
		toolResult, _ := toolContent[0].(map[string]any)
		if toolResult["type"] != "tool_result" || toolResult["tool_use_id"] != "call-prev" {
			t.Fatalf("unexpected tool_result block %+v", toolResult)
		}
		tools, _ := payload["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("expected one declared tool, got %+v", payload["tools"])
		}
		toolDecl, _ := tools[0].(map[string]any)
		if toolDecl["name"] == "lookup" {
			sawCustomTool = true
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"test-model","stop_reason":"tool_use","stop_sequence":"","content":[{"type":"thinking","thinking":"I'll call the tool.","signature":"sig-final"},{"type":"text","text":"Let me check."},{"type":"tool_use","id":"call_2","name":"lookup","input":{"q":"weather"}}],"usage":{"input_tokens":11,"output_tokens":7}}`)
	}))
	defer server.Close()

	llm := newAnthropic(Config{
		Provider:   "anthropic",
		API:        APIAnthropic,
		Model:      "test-model",
		BaseURL:    server.URL + "/v1",
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "sk-anthropic",
		},
	}, "sk-anthropic")

	priorReasoning := model.NewReasoningPart("prior reasoning", model.ReasoningVisibilityVisible)
	priorReasoning.Reasoning.Replay = &model.ReplayMeta{Provider: "anthropic", Kind: anthropicReplayKindThinkingSignature, Token: "sig-prev"}

	var final *model.Response
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Instructions: []model.Part{model.NewTextPart("system instruction")},
		Messages: []model.Message{
			model.NewTextMessage(model.RoleUser, "hello"),
			model.NewMessage(
				model.RoleAssistant,
				priorReasoning,
				model.NewTextPart("Working."),
				model.NewToolUsePart("call-prev", "echo", json.RawMessage(`{"text":"x"}`)),
			),
			model.MessageFromToolResponse(&model.ToolResponse{
				ID:     "call-prev",
				Name:   "echo",
				Result: map[string]any{"echo": "x"},
			}),
		},
		Tools: []model.ToolSpec{
			model.NewFunctionToolSpec("lookup", "Look up weather.", map[string]any{
				"type": "object",
				"properties": map[string]any{
					"q": map[string]any{"type": "string"},
				},
				"required": []string{"q"},
			}),
		},
	}) {
		if err != nil {
			t.Fatalf("generate failed: %v", err)
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if !sawCustomTool {
		t.Fatal("expected tool declaration in anthropic request")
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	if final.FinishReason != model.FinishReasonToolCalls {
		t.Fatalf("expected tool_calls finish reason, got %q", final.FinishReason)
	}
	if got := final.Message.TextContent(); got != "Let me check." {
		t.Fatalf("unexpected final text %q", got)
	}
	if got := final.Message.ReasoningText(); got != "I'll call the tool." {
		t.Fatalf("unexpected reasoning text %q", got)
	}
	reasoningParts := final.Message.ReasoningParts()
	if len(reasoningParts) != 1 || reasoningParts[0].Replay == nil || reasoningParts[0].Replay.Token != "sig-final" {
		t.Fatalf("expected thinking signature replay token, got %+v", reasoningParts)
	}
	toolCalls := final.Message.ToolCalls()
	if len(toolCalls) != 1 || toolCalls[0].Name != "lookup" || toolCalls[0].Args != `{"q":"weather"}` {
		t.Fatalf("unexpected tool calls %+v", toolCalls)
	}
}

func TestAnthropicSDKStream_MapsThinkingDeltasAndSignature(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("x-minimax-api-key"); got != "compat-token" {
			t.Fatalf("expected custom auth header, got %q", got)
		}
		if got := r.Header.Get("x-extra-header"); got != "1" {
			t.Fatalf("expected configured header, got %q", got)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"stop_reason\":\"\",\"stop_sequence\":\"\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"I should think first. \"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig-stream\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello world\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":\"\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":7,\"cache_creation_input_tokens\":0,\"cache_read_input_tokens\":4,\"output_tokens_details\":{\"thinking_tokens\":3},\"server_tool_use\":{\"web_fetch_requests\":0,\"web_search_requests\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	llm := newAnthropic(Config{
		Provider:   "anthropic-compatible",
		API:        APIAnthropicCompatible,
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
		Auth: AuthConfig{
			Type:      AuthAPIKey,
			Token:     "compat-token",
			HeaderKey: "x-minimax-api-key",
		},
		Headers: map[string]string{"x-extra-header": "1"},
	}, "compat-token")

	var (
		reasoningDelta string
		textDelta      string
		final          *model.Response
	)
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("generate failed: %v", err)
		}
		if event == nil {
			continue
		}
		if event.PartDelta != nil {
			switch event.PartDelta.Kind {
			case model.PartKindReasoning:
				reasoningDelta += event.PartDelta.TextDelta
			case model.PartKindText:
				textDelta += event.PartDelta.TextDelta
			}
		}
		if event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if reasoningDelta != "I should think first. " {
		t.Fatalf("unexpected reasoning delta %q", reasoningDelta)
	}
	if textDelta != "Hello world" {
		t.Fatalf("unexpected text delta %q", textDelta)
	}
	if final == nil {
		t.Fatal("expected final streamed response")
	}
	if final.FinishReason != model.FinishReasonStop {
		t.Fatalf("expected stop finish reason, got %q", final.FinishReason)
	}
	if got := final.Message.TextContent(); got != "Hello world" {
		t.Fatalf("unexpected final text %q", got)
	}
	if final.Usage.PromptTokens != 11 || final.Usage.CachedInputTokens != 4 || final.Usage.CompletionTokens != 7 || final.Usage.ReasoningTokens != 3 || final.Usage.TotalTokens != 22 {
		t.Fatalf("unexpected usage: %+v", final.Usage)
	}
	reasoningParts := final.Message.ReasoningParts()
	if len(reasoningParts) != 1 || reasoningParts[0].Replay == nil || reasoningParts[0].Replay.Token != "sig-stream" {
		t.Fatalf("expected streamed signature replay token, got %+v", reasoningParts)
	}
}

func TestDeepSeekAnthropicStreamWrapsMalformedToolInputBeforeSDKMarshal(t *testing.T) {
	const rawToolInput = "* 4 SEARCH - file content"

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/anthropic/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"deepseek-v4-flash\",\"content\":[],\"stop_reason\":\"\",\"stop_sequence\":\"\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"Now let me do SEARCH.\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call_bad\",\"name\":\"SEARCH\",\"input\":{}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", rawToolInput)
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":1}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\",\"stop_sequence\":\"\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":7}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	llm := newDeepSeek(Config{
		Provider:   "deepseek",
		Model:      "deepseek-v4-flash",
		BaseURL:    server.URL + "/anthropic",
		HTTPClient: server.Client(),
		Timeout:    time.Second,
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")

	var final *model.Response
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Tools: []model.ToolSpec{
			model.NewFunctionToolSpec("SEARCH", "Search file content.", map[string]any{"type": "object"}),
		},
		Stream: true,
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}
	if final == nil {
		t.Fatal("expected final response")
	}
	calls := final.Message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls len = %d, want 1: %+v", len(calls), calls)
	}
	if got := calls[0].Args; got != rawToolInput {
		t.Fatalf("tool call args = %q, want raw malformed input %q", got, rawToolInput)
	}
	if _, err := json.Marshal(final.Message); err != nil {
		t.Fatalf("json.Marshal(final.Message) error = %v", err)
	}
}

func TestAnthropicHeaderKeyDisablesEnvironmentDefaults(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-api-key")

	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-minimax-api-key"); got != "compat-token" {
			t.Fatalf("x-minimax-api-key = %q, want custom token", got)
		}
		if got := r.Header.Get("x-api-key"); got != "" {
			t.Fatalf("x-api-key = %q, want no Anthropic env default", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"msg_1","type":"message","role":"assistant","model":"test-model","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer server.Close()

	llm := newAnthropic(Config{
		Provider:   "anthropic-compatible",
		API:        APIAnthropicCompatible,
		Model:      "test-model",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    time.Second,
		Auth: AuthConfig{
			Type:      AuthAPIKey,
			Token:     "compat-token",
			HeaderKey: "x-minimax-api-key",
		},
	}, "compat-token")

	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
	}) {
		if err != nil {
			t.Fatalf("Generate() error = %v", err)
		}
		if event != nil && event.Response != nil && event.Response.Message.TextContent() != "ok" {
			t.Fatalf("response text = %q, want ok", event.Response.Message.TextContent())
		}
	}
}

func TestMiniMaxStream_EmitsStartBlockTextWithoutSmoothingAtProviderLayer(t *testing.T) {
	server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" && r.URL.Path != "/v1/messages" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_stream\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"MiniMax-M2.5\",\"content\":[],\"stop_reason\":\"\",\"stop_sequence\":\"\",\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"MiniMax streaming \"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"should feel much smoother in the terminal output.\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		_, _ = fmt.Fprint(w, "event: message_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":\"\"},\"usage\":{\"input_tokens\":11,\"output_tokens\":12}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()

	llm := newAnthropic(Config{
		Provider:   "minimax",
		API:        APIAnthropicCompatible,
		Model:      "MiniMax-M2.5",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Timeout:    2 * time.Second,
		Auth: AuthConfig{
			Type:      AuthAPIKey,
			Token:     "compat-token",
			HeaderKey: "x-minimax-api-key",
		},
	}, "compat-token")

	var (
		textChunks []string
		final      *model.Response
	)
	for event, err := range llm.Generate(context.Background(), &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hello")},
		Stream:   true,
	}) {
		if err != nil {
			t.Fatalf("generate failed: %v", err)
		}
		if event == nil {
			continue
		}
		if event.PartDelta != nil && event.PartDelta.Kind == model.PartKindText && event.PartDelta.TextDelta != "" {
			textChunks = append(textChunks, event.PartDelta.TextDelta)
		}
		if event.Response != nil && event.TurnComplete {
			final = event.Response
		}
	}

	if len(textChunks) != 2 {
		t.Fatalf("expected start block text plus one delta, got %v", textChunks)
	}
	if got := strings.Join(textChunks, ""); got != "MiniMax streaming should feel much smoother in the terminal output." {
		t.Fatalf("unexpected streamed text %q", got)
	}
	if final == nil {
		t.Fatal("expected final streamed response")
	}
	if got := final.Message.TextContent(); got != "MiniMax streaming should feel much smoother in the terminal output." {
		t.Fatalf("unexpected final text %q", got)
	}
}

func TestMiniMaxUsesAnthropicCompatibleConstructorDefaults(t *testing.T) {
	llm := newMiniMax(Config{
		Provider: "minimax",
		API:      APIMiniMax,
		Model:    "MiniMax-M2",
		Auth: AuthConfig{
			Type:  AuthBearerToken,
			Token: "compat-token",
		},
	}, "compat-token")
	typed, ok := llm.(*anthropicSDKLLM)
	if !ok {
		t.Fatalf("newAnthropic() = %T, want *anthropicSDKLLM", llm)
	}
	if typed.baseURL != miniMaxDefaultBaseURL {
		t.Fatalf("baseURL = %q, want %q", typed.baseURL, miniMaxDefaultBaseURL)
	}
	if typed.maxOutputTok != 4096 {
		t.Fatalf("maxOutputTok = %d, want 4096", typed.maxOutputTok)
	}
}

func TestDeepSeekUsesAnthropicCompatibleConstructorDefaults(t *testing.T) {
	llm := newDeepSeek(Config{
		Provider: "deepseek",
		API:      APIDeepSeek,
		Model:    "deepseek-v4-pro",
		Auth: AuthConfig{
			Type:  AuthAPIKey,
			Token: "deepseek-token",
		},
	}, "deepseek-token")
	typed, ok := llm.(*anthropicSDKLLM)
	if !ok {
		t.Fatalf("newDeepSeek() = %T, want *anthropicSDKLLM", llm)
	}
	if typed.baseURL != deepSeekDefaultAnthropicBaseURL {
		t.Fatalf("baseURL = %q, want %q", typed.baseURL, deepSeekDefaultAnthropicBaseURL)
	}
	if typed.maxOutputTok != deepSeekDefaultMaxTokens {
		t.Fatalf("maxOutputTok = %d, want %d", typed.maxOutputTok, deepSeekDefaultMaxTokens)
	}
	if typed.auth.Type != AuthBearerToken {
		t.Fatalf("auth type = %q, want bearer token", typed.auth.Type)
	}
}

func TestGeminiMessageTransform(t *testing.T) {
	system, msgs, err := toGeminiContents(nil, []model.Message{
		model.NewTextMessage(model.RoleSystem, "sys"),
		model.NewTextMessage(model.RoleUser, "u"),
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:               "call1",
			Name:             "echo",
			Args:             jsonArgs(map[string]any{"text": "x"}),
			ThoughtSignature: "sig-1",
		}}, ""),
	})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if system != "sys" {
		t.Fatalf("unexpected system text: %q", system)
	}
	if len(msgs) < 2 {
		t.Fatalf("expected >= 2 messages, got %d", len(msgs))
	}
	parts := msgs[len(msgs)-1].Parts
	if len(parts) == 0 || parts[0].FunctionCall == nil {
		t.Fatalf("expected function call part in last gemini message")
	}
	if string(parts[0].ThoughtSignature) != "sig-1" {
		t.Fatalf("expected thought signature propagated, got %q", string(parts[0].ThoughtSignature))
	}
}

func TestGeminiMessageTransform_SkipsToolCallWithoutThoughtSignature(t *testing.T) {
	_, msgs, err := toGeminiContents(nil, []model.Message{
		model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
			ID:   "call1",
			Name: "RUN_COMMAND",
			Args: jsonArgs(map[string]any{"command": "ls"}),
		}}, "tool planned"),
	})
	if err != nil {
		t.Fatalf("toGeminiContents: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].Parts) != 1 {
		t.Fatalf("expected only assistant text part, got %d", len(msgs[0].Parts))
	}
	if msgs[0].Parts[0].FunctionCall != nil {
		t.Fatalf("expected tool call without thought signature to be skipped")
	}
}

func TestGeminiResponseToMessage_PreservesThoughtSignature(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{
							ThoughtSignature: []byte("sig-call-1"),
							FunctionCall: &genai.FunctionCall{
								Name: "RUN_COMMAND",
								Args: map[string]any{"command": "ls"},
							},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls()))
	}
	if msg.ToolCalls()[0].ThoughtSignature == "sig-call-1" {
		t.Fatalf("expected thought signature to be encoded for lossless persistence, got raw %q", msg.ToolCalls()[0].ThoughtSignature)
	}
	if got := decodeGeminiThoughtSignature(msg.ToolCalls()[0].ThoughtSignature); string(got) != "sig-call-1" {
		t.Fatalf("expected decoded thought signature kept, got %q", string(got))
	}
}

func TestGeminiResponseToMessage_ExtractsReasoningText(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: "thought-1", Thought: true},
						{Text: "answer"},
						{Text: "thought-2", Thought: true},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.TextContent() != "answer" {
		t.Fatalf("unexpected answer text %q", msg.TextContent())
	}
	if msg.ReasoningText() != "thought-1\nthought-2" {
		t.Fatalf("unexpected reasoning text %q", msg.ReasoningText())
	}
}

func TestGeminiResponseToMessage_DoesNotClassifyAnswerTextAsReasoningByThoughtSignature(t *testing.T) {
	msg, _, err := geminiResponseToMessage(&genai.GenerateContentResponse{
		Candidates: []*genai.Candidate{
			{
				Content: &genai.Content{
					Parts: []*genai.Part{
						{Text: "thought-signature", ThoughtSignature: []byte("sig-thought")},
						{Text: "answer"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg.TextContent() != "thought-signature\nanswer" {
		t.Fatalf("unexpected answer text %q", msg.TextContent())
	}
	if msg.ReasoningText() != "" {
		t.Fatalf("unexpected reasoning text %q", msg.ReasoningText())
	}
}

func TestGeminiResponseDecode_PartLevelThoughtSignature(t *testing.T) {
	raw := []byte(`{
		"candidates":[
			{
				"content":{
					"parts":[
						{
							"functionCall":{"name":"RUN_COMMAND","args":{"command":"ls"}},
							"thoughtSignature":"c2lnLXBhcnQtMQ=="
						}
					]
				}
			}
		]
	}`)
	var out genai.GenerateContentResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	msg, _, err := geminiResponseToMessage(&out)
	if err != nil {
		t.Fatal(err)
	}
	if len(msg.ToolCalls()) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msg.ToolCalls()))
	}
	if got := decodeGeminiThoughtSignature(msg.ToolCalls()[0].ThoughtSignature); string(got) != "sig-part-1" {
		t.Fatalf("expected part-level thought signature, got %q", string(got))
	}
}

func TestDedupToolCalls_MergesLateThoughtSignature(t *testing.T) {
	calls := dedupToolCalls([]model.ToolCall{
		{
			ID:   "RUN_COMMAND",
			Name: "RUN_COMMAND",
			Args: jsonArgs(map[string]any{"command": "ls"}),
		},
		{
			ID:               "RUN_COMMAND",
			Name:             "RUN_COMMAND",
			Args:             jsonArgs(map[string]any{"command": "ls -la"}),
			ThoughtSignature: "sig-late-1",
		},
	})
	if len(calls) != 1 {
		t.Fatalf("expected 1 merged call, got %d", len(calls))
	}
	if calls[0].ThoughtSignature != "sig-late-1" {
		t.Fatalf("expected merged thought signature, got %q", calls[0].ThoughtSignature)
	}
	if strings.TrimSpace(calls[0].Args) != `{"command":"ls -la"}` {
		t.Fatalf("expected latest args merged, got %v", calls[0].Args)
	}
}

func TestGeminiThoughtSignature_BinaryRoundTrip(t *testing.T) {
	raw := []byte{0x00, 0x01, 0x02, 0xff, 0x20, 0x09}
	encoded := encodeGeminiThoughtSignature(raw)
	if encoded == "" || encoded == string(raw) {
		t.Fatalf("expected non-empty encoded signature, got %q", encoded)
	}
	decoded := decodeGeminiThoughtSignature(encoded)
	if !bytes.Equal(decoded, raw) {
		t.Fatalf("expected decoded signature to match raw bytes")
	}
	legacy := decodeGeminiThoughtSignature("sig-legacy-1")
	if string(legacy) != "sig-legacy-1" {
		t.Fatalf("expected legacy signature compatibility, got %q", string(legacy))
	}
}
