package providers

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestGenerationContextOverflowClassification(t *testing.T) {
	const anthropicOverflow = `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 232005 tokens > 200000 maximum"}}`
	const anthropicLimit = `{"id":"msg_limit","type":"message","role":"assistant","model":"fixture","content":[{"type":"text","text":"incomplete"}],"stop_reason":"model_context_window_exceeded","usage":{"input_tokens":9,"output_tokens":4}}`
	const anthropicLimitStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_limit","type":"message","role":"assistant","model":"fixture","content":[],"usage":{"input_tokens":9,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"},"usage":{"output_tokens":4}}

event: message_stop
data: {"type":"message_stop"}

`
	const geminiOverflow = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The input token count (1191140) exceeds the maximum number of tokens allowed (1048576)."}}`
	const geminiQuota = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Too many tokens queued; retry later"}}`
	for _, tc := range []struct {
		name, body            string
		api                   model.APIType
		status                int
		stream, providerTools bool
		wantCode              errorcode.Code
		wantCalls             int32
	}{
		{name: "anthropic HTTP rejection", api: model.APIAnthropic, status: 400, body: anthropicOverflow, providerTools: true, wantCode: errorcode.ResourceExhausted, wantCalls: 1},
		{name: "anthropic SSE rejection", api: model.APIAnthropic, status: 200, stream: true, body: "event: error\ndata: " + anthropicOverflow + "\n\n", wantCode: errorcode.ResourceExhausted, wantCalls: 1},
		{name: "anthropic SSE with possible provider effect", api: model.APIAnthropic, status: 200, stream: true, body: "event: error\ndata: " + anthropicOverflow + "\n\n", providerTools: true, wantCode: errorcode.InvalidArgument, wantCalls: 1},
		{name: "anthropic terminal context window", api: model.APIAnthropic, status: 200, body: anthropicLimit, wantCode: errorcode.ResourceExhausted, wantCalls: 1},
		{name: "anthropic streamed terminal context window", api: model.APIAnthropic, status: 200, stream: true, body: anthropicLimitStream, wantCode: errorcode.ResourceExhausted, wantCalls: 1},
		{name: "gemini HTTP rejection", api: model.APIGemini, status: 400, body: geminiOverflow, providerTools: true, wantCode: errorcode.ResourceExhausted, wantCalls: 1},
		{name: "gemini SSE rejection", api: model.APIGemini, status: 200, stream: true, body: "data: " + geminiOverflow + "\n\n", wantCode: errorcode.ResourceExhausted, wantCalls: 1},
		{name: "gemini SSE with possible provider effect", api: model.APIGemini, status: 200, stream: true, body: "data: " + geminiOverflow + "\n\n", providerTools: true, wantCode: errorcode.InvalidArgument, wantCalls: 1},
		{name: "gemini image token limit", api: model.APIGemini, status: 400, body: `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Image too large for the image token limit"}}`, wantCode: errorcode.InvalidArgument, wantCalls: 1},
		{name: "gemini quota", api: model.APIGemini, status: 429, body: geminiQuota, wantCalls: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				contentType := "application/json"
				if tc.stream {
					contentType = "text/event-stream"
				}
				w.Header().Set("Content-Type", contentType)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			llm, err := New(Config{API: tc.api, Model: "fixture", BaseURL: server.URL, HTTPClient: server.Client(),
				Auth: AuthConfig{Token: "fixture"}, Retry: model.RetryConfig{MaxRetries: 1, RateLimitMaxRetries: 1, RateLimitBaseDelay: time.Nanosecond, RateLimitMaxDelay: time.Nanosecond}})
			if err != nil {
				t.Fatal(err)
			}
			request := &model.Request{Stream: tc.stream, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "fixture")}}
			if tc.providerTools {
				if tc.api == model.APIAnthropic {
					request.Tools = []model.ToolSpec{model.NewProviderExecutedToolSpec("anthropic", "web_search", nil)}
				} else {
					request.Tools = []model.ToolSpec{model.NewProviderExecutedToolSpec("gemini", "google_search", nil)}
				}
			}
			var failure error
			for event, err := range model.Generate(t.Context(), llm, request) {
				if event != nil && event.Response != nil {
					t.Fatalf("generation published a completed response: %+v", event.Response)
				}
				if err != nil {
					failure = err
				}
			}
			if failure == nil || calls.Load() != tc.wantCalls || model.IsContextOverflow(failure) != (tc.wantCode == errorcode.ResourceExhausted) {
				t.Fatalf("calls=%d error=%v, want %d calls with code %s", calls.Load(), failure, tc.wantCalls, tc.wantCode)
			}
			if tc.wantCode != "" && errorcode.CodeOf(failure) != tc.wantCode {
				t.Fatalf("code = %s, want %s", errorcode.CodeOf(failure), tc.wantCode)
			}
			if tc.status == http.StatusTooManyRequests && !model.IsBackpressureLLMError(failure) {
				t.Fatalf("quota failure lost backpressure classification: %v", failure)
			}
		})
	}
}
