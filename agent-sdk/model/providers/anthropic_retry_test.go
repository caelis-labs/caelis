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

func TestAnthropicActualRequestsRespectInvocationAdmissionAndAccounting(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			calls, started, receipts := 0, 0, 0
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": []string{"application/json"}, "Retry-After-Ms": []string{"1"}}, Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"overloaded_error","message":"fixture"}}`))}, nil
			})
			llm := newAnthropic(Config{Provider: "anthropic", Model: "claude-sonnet-4-6", HTTPClient: &http.Client{Transport: transport}}, "fixture")
			ctx := model.WithInvocationAdmission(t.Context(), func(context.Context, *model.Request) error {
				if started >= 2 {
					return context.Canceled
				}
				return nil
			})
			ctx = model.WithInvocationStart(ctx, func(context.Context, model.Invocation, *model.Request) error { started++; return nil })
			ctx = model.WithInvocationObserver(ctx, func(in model.Invocation) {
				receipts++
				if in.Outcome != "failed" || in.StartedAt.IsZero() || in.FinishedAt.IsZero() {
					t.Errorf("attempt receipt=%+v", in)
				}
			})
			wrapped := model.WithRetry(llm, model.RetryConfig{MaxRetries: 5, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
			var finalErr error
			for _, err := range model.Generate(ctx, wrapped, &model.Request{Messages: []model.Message{model.NewTextMessage(model.RoleUser, "source")}, Stream: stream}) {
				if err != nil {
					finalErr = err
				}
			}
			if !errors.Is(finalErr, context.Canceled) || calls != 2 || started != 2 || receipts != 2 {
				t.Fatalf("HTTP=%d starts=%d receipts=%d error=%v", calls, started, receipts, finalErr)
			}
		})
	}
}
