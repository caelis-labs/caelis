package providers

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
	"google.golang.org/genai"
)

// A complete-looking JSON prefix is not a selection until the provider's
// terminal reason says generation finished normally. The list covers every
// finish reason in the pinned genai SDK, plus an unrecognized future reason.
func TestToolSearchGeminiFinishReasons(t *testing.T) {
	reasons := []genai.FinishReason{
		genai.FinishReasonStop, genai.FinishReasonMaxTokens, genai.FinishReasonSafety,
		genai.FinishReasonRecitation, genai.FinishReasonLanguage, genai.FinishReasonOther,
		genai.FinishReasonBlocklist, genai.FinishReasonProhibitedContent,
		genai.FinishReasonSPII, genai.FinishReasonMalformedFunctionCall,
		genai.FinishReasonImageSafety, genai.FinishReasonUnexpectedToolCall,
		genai.FinishReasonTooManyToolCalls, genai.FinishReasonImageProhibitedContent,
		genai.FinishReasonNoImage, genai.FinishReasonImageRecitation,
		genai.FinishReasonImageOther, genai.FinishReasonContinuation,
		genai.FinishReasonUnspecified, genai.FinishReason("FUTURE_REASON"),
	}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			requests := 0
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"{\"tools\":[\"docs__lookup\"]}"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}`+"\n\n")
				_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"finishReason\":%q}],\"usageMetadata\":{\"promptTokenCount\":11,\"candidatesTokenCount\":7,\"totalTokenCount\":18}}\n\n", reason)
			}))
			defer server.Close()
			llm := newGemini(Config{Provider: "gemini", Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client()}, "synthetic")
			checkToolSearchTerminalSelection(t, llm, requestsCount(&requests), reason == genai.FinishReasonStop)
		})
	}
}

func TestToolSearchOllamaDoneReasons(t *testing.T) {
	for _, reason := range []string{"stop", "", "length", "future_reason"} {
		t.Run("reason="+reason, func(t *testing.T) {
			requests := 0
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.Header().Set("Content-Type", "application/x-ndjson")
				_, _ = fmt.Fprint(w, `{"model":"test-model","message":{"role":"assistant","content":"{\"tools\":[\"docs__lookup\"]}"},"done":false}`+"\n")
				_, _ = fmt.Fprintf(w, "{\"model\":\"test-model\",\"message\":{\"role\":\"assistant\"},\"done\":true,\"done_reason\":%q,\"prompt_eval_count\":11,\"eval_count\":7}\n", reason)
			}))
			defer server.Close()
			llm := newOllama(Config{Provider: "ollama", Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client()}, "")
			checkToolSearchTerminalSelection(t, llm, requestsCount(&requests), reason == "" || reason == "stop")
		})
	}
}

func requestsCount(requests *int) func() int { return func() int { return *requests } }

func checkToolSearchTerminalSelection(t *testing.T, llm model.LLM, requests func() int, success bool) {
	t.Helper()
	var receipts []model.Invocation
	ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) })
	reads := 0
	names, err := toolsearch.NewAgentRanker().Rank(ctx, "lookup", []tool.Definition{{Name: "docs__lookup", Description: "Lookup"}}, 1, toolsearch.SearchModel{
		Model: llm, ReadSchema: func(context.Context, string) (tool.Definition, error) {
			reads++
			return tool.Definition{}, fmt.Errorf("unexpected schema read")
		},
	})
	if success && (err != nil || !reflect.DeepEqual(names, []string{"docs__lookup"})) {
		t.Fatalf("normal terminal: names=%v err=%v", names, err)
	}
	if !success && (err == nil || len(names) != 0) {
		t.Fatalf("abnormal terminal published selection: names=%v err=%v", names, err)
	}
	wantOutcome := "failed"
	if success {
		wantOutcome = "completed"
	}
	if requests() != 1 || reads != 0 || len(receipts) != 1 || receipts[0].Outcome != wantOutcome || receipts[0].Usage.TotalTokens != 18 {
		t.Fatalf("requests=%d reads=%d receipts=%+v", requests(), reads, receipts)
	}
}

// Guardian and ordinary Agent calls use the same provider adapters. A blocked
// or truncated non-streaming response must retain usage but expose no final
// message, while normal stops still complete.
func TestNativeAbnormalTerminalsNonStreamingAgent(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		newModel   func(*providerTestServer) model.LLM
		success    bool
	}{
		{"gemini/stop", `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}`, func(s *providerTestServer) model.LLM {
			return newGemini(Config{Provider: "gemini", Model: "test-model", BaseURL: s.URL, HTTPClient: s.Client()}, "synthetic")
		}, true},
		{"gemini/recitation", `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"RECITATION"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}`, func(s *providerTestServer) model.LLM {
			return newGemini(Config{Provider: "gemini", Model: "test-model", BaseURL: s.URL, HTTPClient: s.Client()}, "synthetic")
		}, false},
		{"ollama/stop", `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop","prompt_eval_count":11,"eval_count":7}`, func(s *providerTestServer) model.LLM {
			return newOllama(Config{Provider: "ollama", Model: "test-model", BaseURL: s.URL, HTTPClient: s.Client()}, "")
		}, true},
		{"ollama/length", `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"length","prompt_eval_count":11,"eval_count":7}`, func(s *providerTestServer) model.LLM {
			return newOllama(Config{Provider: "ollama", Model: "test-model", BaseURL: s.URL, HTTPClient: s.Client()}, "")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tc.wire)
			}))
			defer server.Close()
			var receipts []model.Invocation
			ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) })
			var final *model.Response
			var generateErr error
			for event, err := range model.Generate(ctx, tc.newModel(server), &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "synthetic")}}) {
				if err != nil {
					generateErr = err
				}
				if event != nil && event.Response != nil {
					final = event.Response
				}
			}
			if tc.success && (generateErr != nil || final == nil || final.FinishReason != model.FinishReasonStop) {
				t.Fatalf("normal terminal err=%v final=%+v", generateErr, final)
			}
			if !tc.success && (generateErr == nil || final != nil) {
				t.Fatalf("abnormal terminal err=%v final=%+v", generateErr, final)
			}
			wantOutcome := "failed"
			if tc.success {
				wantOutcome = "completed"
			}
			if len(receipts) != 1 || receipts[0].Outcome != wantOutcome || receipts[0].Usage.TotalTokens != 18 {
				t.Fatalf("receipts=%+v", receipts)
			}
		})
	}
}
