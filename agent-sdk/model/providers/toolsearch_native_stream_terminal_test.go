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
)

func TestToolSearchNativeStreamsRequireProviderTerminal(t *testing.T) {
	const anthropicPrefix = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"test-model\",\"content\":[],\"usage\":{\"input_tokens\":11,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"{\\\"tools\\\":[\\\"docs__lookup\\\"]}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n"
	const ollamaPrefix = `{"model":"test-model","message":{"role":"assistant","content":"{\"tools\":[\"docs__lookup\"]}"},"done":false,"prompt_eval_count":11,"eval_count":7}` + "\n"
	const geminiPrefix = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"{\"tools\":[\"docs__lookup\"]}"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}` + "\n\n"
	for _, tc := range []struct {
		name, path, prefix, terminal string
		newModel                     func(*providerTestServer) model.LLM
	}{
		{"anthropic", "/v1/messages", anthropicPrefix, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", func(server *providerTestServer) model.LLM {
			return newAnthropic(Config{Provider: "anthropic", Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client(), Auth: AuthConfig{Type: AuthAPIKey, Token: "synthetic"}}, "synthetic")
		}},
		{"ollama", "/api/chat", ollamaPrefix, `{"model":"test-model","message":{"role":"assistant"},"done":true,"prompt_eval_count":11,"eval_count":7}` + "\n", func(server *providerTestServer) model.LLM {
			return newOllama(Config{Provider: "ollama", Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client()}, "")
		}},
		{"gemini", "/v1beta/models/test-model:streamGenerateContent", geminiPrefix, `data: {"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7,"totalTokenCount":18}}` + "\n\n", func(server *providerTestServer) model.LLM {
			return newGemini(Config{Provider: "gemini", Model: "test-model", BaseURL: server.URL, HTTPClient: server.Client()}, "synthetic")
		}},
	} {
		for _, complete := range []bool{false, true} {
			name := "premature EOF"
			if complete {
				name = "terminal event"
			}
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				requests := 0
				server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests++
					if r.URL.Path != tc.path {
						t.Errorf("path=%q want %q", r.URL.Path, tc.path)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, tc.prefix)
					if complete {
						_, _ = fmt.Fprint(w, tc.terminal)
					}
				}))
				defer server.Close()
				var receipts []model.Invocation
				ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) })
				reads := 0
				names, err := toolsearch.NewAgentRanker().Rank(ctx, "lookup", []tool.Definition{{Name: "docs__lookup", Description: "Lookup"}}, 1, toolsearch.SearchModel{
					Model: tc.newModel(server), ReadSchema: func(context.Context, string) (tool.Definition, error) {
						reads++
						return tool.Definition{}, fmt.Errorf("unexpected schema read")
					},
				})
				if complete && (err != nil || !reflect.DeepEqual(names, []string{"docs__lookup"})) {
					t.Fatalf("complete terminal: names=%v error=%v", names, err)
				}
				if !complete && (err == nil || len(names) != 0) {
					t.Fatalf("premature EOF published selection: names=%v error=%v", names, err)
				}
				wantOutcome := "failed"
				if complete {
					wantOutcome = "completed"
				}
				if requests != 1 || reads != 0 || len(receipts) != 1 || receipts[0].Outcome != wantOutcome || receipts[0].Usage.TotalTokens != 18 {
					t.Fatalf("requests=%d reads=%d receipts=%+v", requests, reads, receipts)
				}
			})
		}
	}
}
