package gatewayapp

import (
	"context"
	"io"
	"iter"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestProductionReasoningWrapperCountsHTTPAttempts(t *testing.T) {
	for _, mode := range []string{"success", "retry", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls atomic.Int32
			client := &http.Client{Transport: gatewayAppRoundTripFunc(func(*http.Request) (*http.Response, error) {
				call := calls.Add(1)

				data := "data: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"
				if mode == "retry" && call == 1 {
					data = "data: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\ndata: {invalid}\n\n"
				}
				body := io.NopCloser(strings.NewReader(data))
				if mode == "cancel" {
					body = &reasoningCancelBody{Reader: strings.NewReader("data: {\"choices\":[],\"usage\":{\"total_tokens\":12}}\n\n"), cancel: cancel}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			})}
			factory := providers.NewFactory()
			if err := factory.Register(providers.Config{Alias: "probe", Provider: "openai-compatible", Model: "probe", API: providers.APIOpenAICompatible, Auth: providers.AuthConfig{Type: providers.AuthNone}, BaseURL: "https://provider.invalid", HTTPClient: client, Retry: model.RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond}}); err != nil {
				t.Fatal(err)
			}
			inner, err := factory.NewByAlias("probe")
			if err != nil {
				t.Fatal(err)
			}
			llm := withSystemAgentReasoningEffort(kernel.ModelResolution{Model: inner, ReasoningEffort: "high"})
			var receipts []model.Invocation
			ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) { receipts = append(receipts, in) })
			for _, err := range model.Generate(ctx, llm, &model.Request{Stream: true}) {
				if mode != "cancel" && err != nil {
					t.Fatal(err)
				}
			}
			total := 0
			for _, in := range receipts {
				total += in.Usage.TotalTokens
			}
			wantCalls, wantTokens := 1, 12
			if mode == "retry" {
				wantCalls = 2
				wantTokens = 24
			}
			if int(calls.Load()) != wantCalls || len(receipts) != wantCalls || total != wantTokens {
				t.Fatalf("HTTP=%d receipts=%d tokens=%d; want %d/%d/%d", calls.Load(), len(receipts), total, wantCalls, wantCalls, wantTokens)
			}
		})
	}
}

type reasoningCancelBody struct {
	*strings.Reader
	cancel context.CancelFunc
}

func (b *reasoningCancelBody) Read(p []byte) (int, error) {
	if b.Len() > 0 {
		return b.Reader.Read(p)
	}
	b.cancel()
	return 0, context.Canceled
}
func (*reasoningCancelBody) Close() error { return nil }

type reasoningDirectProvider struct {
	calls  int
	effort string
}

func (*reasoningDirectProvider) Name() string { return "direct" }
func (m *reasoningDirectProvider) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if req != nil {
			m.effort = req.Reasoning.Effort
		}
		yield(model.StreamEventFromResponse(&model.Response{TurnComplete: true, Usage: model.Usage{TotalTokens: 12}}), nil)
	}
}
func TestReasoningWrapperTracksDirectProviderAndNilRequest(t *testing.T) {
	for _, effort := range []string{"", "high"} {
		for _, nilRequest := range []bool{false, true} {
			direct := &reasoningDirectProvider{}
			llm := withSystemAgentReasoningEffort(kernel.ModelResolution{Model: direct, ReasoningEffort: effort})
			var receipts []model.Invocation
			ctx := model.WithInvocationObserver(t.Context(), func(in model.Invocation) { receipts = append(receipts, in) })
			var req *model.Request
			if !nilRequest {
				req = &model.Request{}
			}
			for _, err := range model.Generate(ctx, llm, req) {
				if err != nil {
					t.Fatal(err)
				}
			}
			if direct.calls != 1 || len(receipts) != 1 || receipts[0].Usage.TotalTokens != 12 {
				t.Fatalf("effort=%q nil=%v calls=%d receipts=%#v", effort, nilRequest, direct.calls, receipts)
			}
			if !nilRequest && (direct.effort != effort || req.Reasoning.Effort != "") {
				t.Fatalf("effort forwarding mutated source request: %#v", req)
			}
		}
	}
}
