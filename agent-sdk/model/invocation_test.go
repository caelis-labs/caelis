package model

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGenerateAccountsEachRetryAttempt(t *testing.T) {
	final := StreamEventFromResponse(&Response{Message: NewTextMessage(RoleAssistant, "done"), TurnComplete: true, Usage: Usage{TotalTokens: 12}})
	partial := &StreamEvent{Type: StreamEventPartDelta, Response: &Response{Usage: Usage{PromptTokens: 7, CachedInputTokens: 3, TotalTokens: 7}}}
	inner := &retryTestLLM{events: [][]*StreamEvent{{partial}, {final}}, errs: []error{errors.New("provider unavailable"), nil}}
	llm := WithRetry(inner, RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	var receipts []Invocation
	ctx := WithInvocationObserver(context.Background(), func(in Invocation) { receipts = append(receipts, in) })
	var finalID string
	for event, err := range Generate(ctx, llm, &Request{}) {
		if err != nil {
			t.Fatal(err)
		}
		if event != nil && event.Response != nil && event.TurnComplete {
			finalID = event.InvocationID
		}
	}
	if inner.calls != 2 || len(receipts) != 2 {
		t.Fatalf("calls=%d receipts=%#v", inner.calls, receipts)
	}
	if receipts[0].ID == receipts[1].ID || finalID != receipts[1].ID {
		t.Fatalf("identities: %#v final=%q", receipts, finalID)
	}
	if receipts[0].Outcome != "failed" || receipts[0].Usage.TotalTokens != 7 || receipts[0].Usage.CachedInputTokens != 3 || receipts[1].Outcome != "completed" || receipts[1].Usage.TotalTokens != 12 {
		t.Fatalf("receipts=%#v", receipts)
	}
}

func TestGenerateDistinguishesUnknownAndReportedZero(t *testing.T) {
	for _, reported := range []bool{false, true} {
		inner := &retryTestLLM{events: [][]*StreamEvent{{StreamEventFromResponse(&Response{TurnComplete: true, Usage: Usage{Reported: reported}})}}}
		var receipts []Invocation
		ctx := WithInvocationObserver(context.Background(), func(in Invocation) { receipts = append(receipts, in) })
		for _, err := range Generate(ctx, inner, &Request{}) {
			if err != nil {
				t.Fatal(err)
			}
		}
		if len(receipts) != 1 || receipts[0].Usage.IsReported() != reported {
			t.Fatalf("reported=%v receipts=%#v", reported, receipts)
		}
	}
}

func TestNestedInvocationObserversReceiveEachAttemptOnce(t *testing.T) {
	outer, inner := 0, 0
	ctx := WithInvocationObserver(context.Background(), func(Invocation) { outer++ })
	ctx = WithInvocationObserver(ctx, func(Invocation) { inner++ })
	llm := &retryTestLLM{events: [][]*StreamEvent{{StreamEventFromResponse(&Response{TurnComplete: true})}}}
	for _, err := range Generate(ctx, llm, &Request{}) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if outer != 1 || inner != 1 {
		t.Fatalf("outer=%d inner=%d", outer, inner)
	}
}

func TestInvocationAdmissionStopsRetriesBeforeProviderAndReceipt(t *testing.T) {
	inner := &retryTestLLM{errs: []error{errors.New("provider unavailable"), nil}}
	llm := WithRetry(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	var admitted, receipts int
	exhausted := errors.New("budget exhausted")
	ctx := WithInvocationObserver(t.Context(), func(Invocation) { receipts++ })
	ctx = WithInvocationAdmission(ctx, func(context.Context, *Request) error {
		admitted++
		if admitted > 1 {
			return exhausted
		}
		return nil
	})
	var last error
	for _, err := range Generate(ctx, llm, &Request{}) {
		last = err
	}
	if !errors.Is(last, exhausted) || inner.calls != 1 || receipts != 1 {
		t.Fatalf("err=%v calls=%d receipts=%d", last, inner.calls, receipts)
	}
}

func TestToolAvailabilityPreservesPrefixAndClosesSelectionAcrossRetries(t *testing.T) {
	inner := &retryTestLLM{errs: []error{errors.New("provider unavailable"), nil}}
	llm := WithRetry(inner, RetryConfig{MaxRetries: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Nanosecond})
	req := &Request{Tools: ToolSpecsFromDefinitions([]ToolDefinition{{Name: "Read", Parameters: map[string]any{"type": "object"}}})}
	ctx := WithToolAvailability(t.Context(), func() bool { return false })
	// A nested scope cannot reopen an embedding's closed tool budget.
	ctx = WithToolAvailability(ctx, func() bool { return true })
	for _, err := range Generate(ctx, llm, req) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if inner.calls != 2 || len(req.Tools) != 1 || req.DisableTools {
		t.Fatalf("calls=%d original tools=%d", inner.calls, len(req.Tools))
	}
	for _, seen := range inner.seenReqs {
		if !seen.DisableTools || len(seen.Tools) != 1 {
			t.Fatal("retry reopened tools")
		}
	}
}
