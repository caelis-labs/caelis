package model

import (
	"context"
	"errors"
	"iter"
	"testing"
	"testing/synctest"
	"time"
)

type consumerDeadlineLLM struct {
	final, reportContext bool
	calls                int
}

func (*consumerDeadlineLLM) Name() string { return "review-final" }
func (m *consumerDeadlineLLM) Generate(ctx context.Context, _ *Request) iter.Seq2[*StreamEvent, error] {
	return func(yield func(*StreamEvent, error) bool) {
		m.calls++
		response := &Response{TurnComplete: m.final, Usage: Usage{TotalTokens: 12}}
		event := StreamEventFromResponse(response)
		if !m.final {
			event = &StreamEvent{Type: StreamEventPartDelta, Response: response}
		}
		if !yield(event, nil) {
			return
		}
		if m.reportContext && ctx.Err() != nil {
			yield(nil, ctx.Err())
		}
	}
}

func TestRequestAttemptFinalResponseConsumerDeadline(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		final, reportContext, parentCancel bool
		want                               error
		outcome                            string
		calls                              int
	}{
		{"final child deadline on return", true, false, false, nil, "completed", 1},
		{"final child deadline reported by provider", true, true, false, nil, "completed", 1},
		{"unfinished child deadline", false, true, false, context.DeadlineExceeded, "failed", 2},
		{"final parent cancellation", true, true, true, context.Canceled, "cancelled", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ctx = WithRequestAttemptTimeout(ctx, time.Second)
				var receipts []Invocation
				ctx = WithInvocationObserver(ctx, func(in Invocation) { receipts = append(receipts, in) })
				inner := &consumerDeadlineLLM{final: tc.final, reportContext: tc.reportContext}
				llm := WithRetry(inner, RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
				var failure error
				for event, err := range Generate(ctx, llm, &Request{}) {
					if event != nil && event.Response != nil {
						if tc.parentCancel {
							cancel()
						} else {
							time.Sleep(2 * time.Second)
						}
					}
					if err != nil {
						failure = err
					}
				}
				if !errors.Is(failure, tc.want) || inner.calls != tc.calls || len(receipts) != tc.calls {
					t.Fatalf("error=%v calls=%d receipts=%+v want=%v/%d", failure, inner.calls, receipts, tc.want, tc.calls)
				}
				for _, receipt := range receipts {
					if receipt.Outcome != tc.outcome || receipt.Usage.TotalTokens != 12 {
						t.Fatalf("receipt=%+v want outcome=%s", receipt, tc.outcome)
					}
				}
			})
		})
	}
}
