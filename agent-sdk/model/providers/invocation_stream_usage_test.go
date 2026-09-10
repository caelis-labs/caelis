package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

// Each body delivers complete provider frames before its read fails. This
// exercises the real decoder, including cancellation after already-known usage.
type measuredStreamBody struct {
	*strings.Reader
	after   func()
	failure error
}

func (b *measuredStreamBody) Read(p []byte) (int, error) {
	if b.Len() > 0 {
		return b.Reader.Read(p)
	}
	if b.after != nil {
		b.after()
		b.after = nil
	}
	return 0, b.failure
}
func (*measuredStreamBody) Close() error { return nil }

func TestProviderInvocationsPreserveCumulativeUsageOnReadFailureAndCancel(t *testing.T) {
	constructors := map[string]func(Config) model.LLM{
		"openai-compatible":           func(c Config) model.LLM { return newOpenAICompat(c, "test") },
		"openai":                      func(c Config) model.LLM { c.API = APIOpenAI; return newOpenAIResponses(c, "test") },
		"openai-responses-compatible": func(c Config) model.LLM { c.API = APIOpenAIResponses; return newOpenAIResponses(c, "test") },
		"anthropic":                   func(c Config) model.LLM { return newAnthropic(c, "test") },
		"gemini":                      func(c Config) model.LLM { return newGemini(c, "test") },
		"ollama":                      func(c Config) model.LLM { return newOllama(c, "test") },
		"codex":                       func(c Config) model.LLM { return newOpenAICodex(c) },
		"xai":                         func(c Config) model.LLM { return newXAIResponses(c) },
	}
	for provider, newLLM := range constructors {
		for _, mode := range []string{"failure", "cancel", "zero", "cumulative"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				values := []int{12}
				if mode == "zero" {
					values = []int{0}
				}
				if mode == "cumulative" {
					values = []int{7, 12}
				}
				var frames strings.Builder
				for i, n := range values {
					switch provider {
					case "openai-compatible":
						fmt.Fprintf(&frames, "data: {\"choices\":[],\"usage\":{\"total_tokens\":%d}}\n\n", n)
					case "codex", "xai", "openai", "openai-responses-compatible":
						fmt.Fprintf(&frames, "data: {\"type\":\"response.in_progress\",\"response\":{\"usage\":{\"input_tokens\":%d,\"output_tokens\":0,\"total_tokens\":%d}}}\n\n", n, n)
					case "gemini":
						fmt.Fprintf(&frames, "data: {\"usageMetadata\":{\"promptTokenCount\":%d,\"totalTokenCount\":%d}}\n\n", n, n)
					case "ollama":
						fmt.Fprintf(&frames, "{\"message\":{\"role\":\"assistant\",\"content\":\"\"},\"prompt_eval_count\":%d,\"eval_count\":0,\"done\":false}\n", n)
					case "anthropic":
						if i == 0 {
							fmt.Fprintf(&frames, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"probe\",\"usage\":{\"input_tokens\":%d,\"output_tokens\":0}}}\n\n", n)
						} else {
							fmt.Fprintf(&frames, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{},\"usage\":{\"output_tokens\":%d}}\n\n", n-values[0])
						}
					}
				}
				failure := errors.New("transport failed after measurement")
				calls := 0
				client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					body := &measuredStreamBody{Reader: strings.NewReader(frames.String()), failure: failure}
					if mode == "cancel" {
						body.after = cancel
						body.failure = context.Canceled
					}
					return &http.Response{StatusCode: 200, Request: req, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
				})}
				llm := newLLM(Config{Provider: provider, Model: "probe", BaseURL: "https://provider.invalid", HTTPClient: client, Timeout: time.Second})
				var receipts []model.Invocation
				ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) { receipts = append(receipts, in) })
				var gotErr error
				for event, err := range model.Generate(ctx, llm, &model.Request{Stream: true, Messages: []model.Message{model.NewTextMessage(model.RoleUser, "hi")}}) {
					if event != nil && event.Response != nil && event.TurnComplete {
						t.Error("read failure became successful final")
					}
					if err != nil {
						gotErr = err
					}
				}
				if calls != 1 || len(receipts) != 1 || gotErr == nil {
					t.Fatalf("calls=%d receipts=%#v err=%v", calls, receipts, gotErr)
				}
				want := values[len(values)-1]
				outcome := "failed"
				if mode == "cancel" {
					outcome = "cancelled"
				}
				if in := receipts[0]; !in.Usage.IsReported() || in.Usage.TotalTokens != want || in.Outcome != outcome {
					t.Fatalf("receipt=%#v want reported %d %s", in, want, outcome)
				}
			})
		}
	}
}

func TestOpenAIInvocationKeepsUsageBeforeDecoderFailure(t *testing.T) {
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "single", true: "retry"}[retry], func(t *testing.T) {
			calls := 0
			server := newProviderTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\ndata: {invalid}\n\n")
			}))
			defer server.Close()
			var llm model.LLM = newOpenAICompat(Config{Provider: "openai-compatible", Model: "probe", BaseURL: server.URL, HTTPClient: server.Client()}, "")
			if retry {
				llm = model.WithRetry(llm, model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
			}
			var receipts []model.Invocation
			ctx := model.WithInvocationObserver(context.Background(), func(in model.Invocation) { receipts = append(receipts, in) })
			var failure error
			for event, err := range model.Generate(ctx, llm, &model.Request{Stream: true}) {
				if event != nil && event.Response != nil && event.TurnComplete {
					t.Error("failed decoder emitted success")
				}
				if err != nil {
					failure = err
				}
			}
			want := 1
			if retry {
				want = 2
			}
			if failure == nil || calls != want || len(receipts) != want {
				t.Fatalf("calls=%d receipts=%#v err=%v", calls, receipts, failure)
			}
			for _, in := range receipts {
				if !in.Usage.IsReported() || in.Usage.TotalTokens != 12 || !strings.EqualFold(in.Outcome, "failed") {
					t.Errorf("lost known usage: %#v", in)
				}
			}
		})
	}
}
