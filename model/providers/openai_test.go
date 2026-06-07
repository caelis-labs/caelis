package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

// mockOpenAIServer creates a test HTTP server that mimics OpenAI streaming.
func mockOpenAIServer(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

func TestOpenAIProvider_TextStreaming(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"Hello"},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":" world"},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
		fmt.Fprintf(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{
		BaseURL: srv.URL,
		Token:   "test",
		Model:   "test-model",
	})

	var text string
	var usage *model.Usage
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		text += evt.TextDelta
		if evt.Usage != nil {
			usage = evt.Usage
		}
	}

	if text != "Hello world" {
		t.Errorf("got %q, want %q", text, "Hello world")
	}
	if usage == nil || usage.TotalTokens != 15 {
		t.Errorf("expected usage with 15 total tokens")
	}
}

func TestOpenAIProvider_UsageIncludesCachedAndReasoningTokens(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{},"finish_reason":"stop","index":0}],"usage":{"prompt_tokens":20,"completion_tokens":8,"prompt_tokens_details":{"cached_tokens":6},"completion_tokens_details":{"reasoning_tokens":3}}}`)
		fmt.Fprintf(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Token: "test", Model: "test-model"})

	var usage *model.Usage
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if evt.Usage != nil {
			usage = evt.Usage
		}
	}
	if usage == nil {
		t.Fatal("usage = nil, want usage")
	}
	if usage.PromptTokens != 20 || usage.CachedInputTokens != 6 || usage.CompletionTokens != 8 || usage.ReasoningTokens != 3 || usage.TotalTokens != 28 {
		t.Fatalf("usage = %+v, want cached/reasoning tokens with derived total", *usage)
	}
}

func TestOpenAIProvider_MultilineSSE(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[\n")
		fmt.Fprintf(w, "data: {\"delta\":{\"content\":\"Hello\"},\"index\":0}\n")
		fmt.Fprintf(w, "data: ]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\",\"index\":0}]}\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Token: "test", Model: "test-model"})

	var text string
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		text += evt.TextDelta
	}
	if text != "Hello" {
		t.Fatalf("text = %q, want Hello", text)
	}
}

func TestOpenAIProvider_FirstEventTimeout(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{
		BaseURL:                 srv.URL,
		Token:                   "test",
		Model:                   "test-model",
		StreamFirstEventTimeout: 20 * time.Millisecond,
	})

	var gotErr error
	for _, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
	}) {
		if err != nil {
			gotErr = err
			break
		}
	}
	if !errors.Is(gotErr, errStreamFirstEventTimeout) {
		t.Fatalf("Generate error = %v, want first event timeout", gotErr)
	}
}

func TestOpenAIProvider_ToolCallStreaming(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Text first.
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"Let me check."},"index":0}]}`)
		// Tool call deltas.
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"READ"}}]},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/tmp\"}"}}]},"index":0}]}`)
		// Finish with tool_calls.
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}`)
		fmt.Fprintf(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Token: "test", Model: "test"})

	var text string
	var toolCalls []*model.ToolCallDelta
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "read file"}}}},
	}) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if evt.TextDelta != "" {
			text += evt.TextDelta
		}
		if evt.ToolCall != nil {
			toolCalls = append(toolCalls, evt.ToolCall)
		}
	}

	if text != "Let me check." {
		t.Errorf("text: got %q", text)
	}
	if len(toolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(toolCalls))
	}
	if toolCalls[0].CallID != "call-1" {
		t.Errorf("call id: got %q", toolCalls[0].CallID)
	}
	if toolCalls[0].Name != "READ" {
		t.Errorf("name: got %q", toolCalls[0].Name)
	}
	if toolCalls[0].Args == nil || toolCalls[0].Args["path"] != "/tmp" {
		t.Errorf("args: got %v", toolCalls[0].Args)
	}
}

func TestOpenAIProvider_MultipleToolCalls(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Two parallel tool calls.
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"READ"}}]},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"LIST"}}]},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":\"/a\"}"}}]},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"path\":\"/b\"}"}}]},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}`)
		fmt.Fprintf(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Token: "test", Model: "test"})

	var toolCalls []*model.ToolCallDelta
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "go"}}}},
	}) {
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if evt.ToolCall != nil {
			toolCalls = append(toolCalls, evt.ToolCall)
		}
	}

	if len(toolCalls) != 2 {
		t.Fatalf("got %d tool calls, want 2", len(toolCalls))
	}
	if toolCalls[0].Name != "READ" || toolCalls[1].Name != "LIST" {
		t.Errorf("names: %q, %q", toolCalls[0].Name, toolCalls[1].Name)
	}
}

func TestOpenAIProvider_InvalidToolCallArgumentsReturnsError(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"READ","arguments":"{\"path\":"}}]},"index":0}]}`)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{},"finish_reason":"tool_calls","index":0}]}`)
		fmt.Fprintf(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Token: "test", Model: "test"})

	var gotErr bool
	var gotToolCall bool
	for evt, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "go"}}}},
	}) {
		if err != nil {
			gotErr = true
			continue
		}
		if evt.ToolCall != nil {
			gotToolCall = true
		}
	}
	if !gotErr {
		t.Fatal("expected error for invalid tool call arguments")
	}
	if gotToolCall {
		t.Fatal("invalid tool call arguments should not emit a tool call")
	}
}

func TestOpenAIProvider_APIError(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprintf(w, `{"error":{"message":"unauthorized"}}`)
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Token: "bad", Model: "test"})

	var gotErr bool
	for _, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
	}) {
		if err != nil {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected error for 401 response")
	}
}

func TestOpenAIProvider_MalformedSSE(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {invalid json\n\n")
		fmt.Fprintf(w, "data: [DONE]\n\n")
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Token: "test", Model: "test"})

	var gotErr bool
	for _, err := range p.Generate(context.Background(), model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "hi"}}}},
	}) {
		if err != nil {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected error for malformed SSE")
	}
}

func TestOpenAIProvider_BuildMessageWithToolCalls(t *testing.T) {
	p := NewOpenAI(OpenAIConfig{Model: "test"})
	msg := model.Message{
		Role: model.RoleAssistant,
		Content: []model.Part{
			{Text: "Let me check"},
			{ToolUse: &model.ToolUse{CallID: "c1", Name: "READ", Args: map[string]any{"path": "/tmp"}}},
		},
	}
	built := p.buildMessage(msg)
	if built["role"] != "assistant" {
		t.Errorf("role: %v", built["role"])
	}
	if built["content"] != "Let me check" {
		t.Errorf("content: %v", built["content"])
	}
	tc, ok := built["tool_calls"].([]map[string]any)
	if !ok || len(tc) != 1 {
		t.Fatalf("expected 1 tool call, got %v", built["tool_calls"])
	}
	if tc[0]["id"] != "c1" {
		t.Errorf("tool call id: %v", tc[0]["id"])
	}
}

func TestOpenAIProvider_BuildMessagePreservesRawToolArguments(t *testing.T) {
	p := NewOpenAI(OpenAIConfig{Model: "test"})
	msg := model.Message{
		Role: model.RoleAssistant,
		Content: []model.Part{
			{ToolUse: &model.ToolUse{
				CallID:  "c1",
				Name:    "READ",
				Args:    map[string]any{"path": "/decoded"},
				ArgJSON: `{"path":"\/raw","limit":1.0}`,
			}},
		},
	}

	built := p.buildMessage(msg)
	tc := built["tool_calls"].([]map[string]any)
	fn := tc[0]["function"].(map[string]any)
	if got := fn["arguments"]; got != `{"path":"\/raw","limit":1.0}` {
		t.Fatalf("arguments = %#v, want raw ArgJSON preserved", got)
	}
}

func TestOpenAIProvider_BuildMessageIncludesReasoningText(t *testing.T) {
	p := NewOpenAI(OpenAIConfig{Model: "test"})
	msg := model.Message{
		Role: model.RoleAssistant,
		Content: []model.Part{
			{Reasoning: &model.Reasoning{Text: "Think first.", Visibility: model.ReasoningVisibilityVisible}},
			{Text: "Final answer."},
		},
	}

	built := p.buildMessage(msg)
	if built["content"] != "Final answer." {
		t.Fatalf("content = %#v, want answer text only", built["content"])
	}
	if built["reasoning_content"] != "Think first." {
		t.Fatalf("reasoning_content = %#v, want reasoning preserved separately", built["reasoning_content"])
	}
}

func TestOpenAIProvider_BuildMessageToolResult(t *testing.T) {
	p := NewOpenAI(OpenAIConfig{Model: "test"})
	msg := model.Message{
		Role: model.RoleTool,
		Content: []model.Part{
			{ToolResult: &model.ToolResult{CallID: "c1", Content: "file contents"}},
		},
	}
	built := p.buildMessage(msg)
	if built["tool_call_id"] != "c1" {
		t.Errorf("tool_call_id: %v", built["tool_call_id"])
	}
	if built["content"] != "file contents" {
		t.Errorf("content: %v", built["content"])
	}
}

func TestOpenAIProvider_ContextCancellation(t *testing.T) {
	srv := mockOpenAIServer(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprintf(w, "data: %s\n\n", `{"choices":[{"delta":{"content":"hi"},"index":0}]}`)
		if flusher != nil {
			flusher.Flush()
		}
		// Block until context is cancelled.
		<-r.Context().Done()
	})
	defer srv.Close()

	p := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Token: "test", Model: "test"})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	var count int
	for _, err := range p.Generate(ctx, model.Request{
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.Part{{Text: "go"}}}},
	}) {
		if err != nil {
			break
		}
		count++
	}
	if count < 1 {
		t.Error("expected at least 1 chunk before cancellation")
	}
}

func TestOpenAIToolArgsMapCompatibility(t *testing.T) {
	m, err := toolArgsMap(`{"path":"/tmp","limit":10}`)
	if err != nil {
		t.Fatalf("toolArgsMap: %v", err)
	}
	if m["path"] != "/tmp" {
		t.Errorf("path: %v", m["path"])
	}
	empty, err := toolArgsMap("")
	if err != nil {
		t.Fatalf("toolArgsMap empty: %v", err)
	}
	if len(empty) != 0 {
		t.Error("empty string should return empty object")
	}
	if _, err := toolArgsMap("not json"); err == nil {
		t.Error("invalid json should return error")
	}
}
