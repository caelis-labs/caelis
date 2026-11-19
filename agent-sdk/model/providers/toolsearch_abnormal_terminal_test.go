package providers

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// A complete-looking JSON prefix is not a selection until the provider's
// terminal reason says generation finished normally. The list covers every
// finish reason in the pinned genai SDK, plus an unrecognized future reason.
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
