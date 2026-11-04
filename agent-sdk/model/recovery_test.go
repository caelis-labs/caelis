package model

import (
	"context"
	"errors"
	"iter"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func TestRecoverySurvivesOfflineBeyondRequestBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &retryTestLLM{}
		for range 30 {
			inner.errs = append(inner.errs, errorcode.New(errorcode.Unavailable, "fixture offline"))
		}
		inner.events = make([][]*StreamEvent, 31)
		inner.events[30] = []*StreamEvent{StreamEventFromResponse(&Response{TurnComplete: true})}
		llm := WithRetry(inner, RetryConfig{})
		started := time.Now()
		for _, err := range llm.Generate(t.Context(), &Request{}) {
			if err != nil {
				t.Fatalf("still-live request ended after %d calls: %v", inner.calls, err)
			}
		}
		if inner.calls != 31 || time.Since(started) < 600*time.Second {
			t.Fatalf("calls=%d offline=%s", inner.calls, time.Since(started))
		}
	})
}

type recoveryDeadlineLLM struct {
	calls     int
	deadlines []time.Duration
}

func (*recoveryDeadlineLLM) Name() string { return "deadline-fixture" }
func (m *recoveryDeadlineLLM) Generate(ctx context.Context, _ *Request) iter.Seq2[*StreamEvent, error] {
	return func(yield func(*StreamEvent, error) bool) {
		m.calls++
		deadline, ok := ctx.Deadline()
		if !ok {
			panic("actual attempt has no deadline")
		}
		m.deadlines = append(m.deadlines, time.Until(deadline))
		if m.calls == 1 {
			<-ctx.Done()
			// A transport may report cancellation after its child deadline.
			yield(nil, context.Canceled)
			return
		}
		yield(StreamEventFromResponse(&Response{TurnComplete: true}), nil)
	}
}

func TestRecoveryResetsChildDeadlineButPreservesParentDeadline(t *testing.T) {
	for _, parentBudget := range []time.Duration{0, 90 * time.Second} {
		synctest.Test(t, func(t *testing.T) {
			ctx := t.Context()
			if parentBudget > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, parentBudget)
				defer cancel()
			}
			inner := &recoveryDeadlineLLM{}
			var failure error
			for _, err := range WithRetry(inner, RetryConfig{}).Generate(ctx, &Request{}) {
				if err != nil {
					failure = err
				}
			}
			if parentBudget > 0 {
				if !errors.Is(failure, context.DeadlineExceeded) || inner.calls != 1 {
					t.Fatalf("parent deadline extended: calls=%d err=%v", inner.calls, failure)
				}
			} else if failure != nil || inner.calls != 2 || inner.deadlines[0] != 600*time.Second || inner.deadlines[1] != 600*time.Second {
				t.Fatalf("request deadline ended recovery: calls=%d deadlines=%v err=%v", inner.calls, inner.deadlines, failure)
			}
		})
	}
}

type recoveryAfterError struct{ delay time.Duration }

func (e recoveryAfterError) Error() string             { return "fixture backpressure" }
func (e recoveryAfterError) Retryable() bool           { return true }
func (e recoveryAfterError) RetryAfter() time.Duration { return e.delay }

func TestRecoveryRetryAfterAndCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		inner := &retryTestLLM{errs: []error{recoveryAfterError{2 * time.Minute}}}
		started := time.Now()
		var failure error
		var wait int64
		done := make(chan struct{})
		go func() {
			defer close(done)
			for event, err := range WithRetry(inner, RetryConfig{}).Generate(ctx, &Request{}) {
				if event != nil && event.AttemptReset != nil {
					wait = event.AttemptReset.RetryDelayMillis
				}
				if err != nil {
					failure = err
				}
			}
		}()
		synctest.Wait()
		if wait != 120000 || inner.calls != 1 {
			t.Fatalf("Retry-After not respected: delay=%d calls=%d", wait, inner.calls)
		}
		cancel()
		<-done
		if !errors.Is(failure, context.Canceled) || time.Since(started) != 0 {
			t.Fatalf("cancellation waited: elapsed=%s err=%v", time.Since(started), failure)
		}
	})
}

func TestRecoveryCappedJitterAndSuccessReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		inner := &retryTestLLM{}
		for range 25 {
			inner.errs = append(inner.errs, errorcode.New(errorcode.Unavailable, "offline"))
		}
		inner.events = make([][]*StreamEvent, 26)
		inner.events[25] = []*StreamEvent{StreamEventFromResponse(&Response{TurnComplete: true})}
		llm := WithRetry(inner, RetryConfig{})
		var delays []int64
		for event, err := range llm.Generate(t.Context(), &Request{}) {
			if err != nil {
				t.Fatal(err)
			}
			if event != nil && event.AttemptReset != nil {
				delays = append(delays, event.AttemptReset.RetryDelayMillis)
			}
		}
		for _, delay := range delays[6:] {
			if delay < 30000 || delay > 60000 {
				t.Fatalf("dense/cap-violating probe: %v", delays)
			}
		}
		inner.errs = append(inner.errs, nil, errorcode.New(errorcode.Unavailable, "new outage"), nil)
		inner.events = append(inner.events, nil, []*StreamEvent{StreamEventFromResponse(&Response{TurnComplete: true})})
		for event, err := range llm.Generate(t.Context(), &Request{}) {
			if err != nil {
				t.Fatal(err)
			}
			if event != nil && event.AttemptReset != nil && event.AttemptReset.Attempt != 1 {
				t.Fatal("previous outage retained retry count")
			}
		}
	})
}

func TestRecoveryRejectsPermanentStructuredErrors(t *testing.T) {
	for _, code := range []errorcode.Code{errorcode.Unauthenticated, errorcode.PermissionDenied, errorcode.InvalidArgument, errorcode.FailedPrecondition, errorcode.ResourceExhausted, errorcode.Interrupted} {
		t.Run(string(code), func(t *testing.T) {
			if IsRetryableLLMError(errorcode.New(code, "fixture terminal")) {
				t.Fatal("terminal failure classified as retryable")
			}
		})
	}
	if IsRetryableLLMError(context.Canceled) {
		t.Fatal("cancellation classified as retryable")
	}
}

func TestRecoveryDoesNotReplayUnknownProviderTool(t *testing.T) {
	failure := errorcode.New(errorcode.Unavailable, "response lost after dispatch")
	inner := &retryProviderToolUsageLLM{retryTestLLM: &retryTestLLM{errs: []error{failure}}, usesProviderTools: true}
	var got error
	for _, err := range WithRetry(inner, RetryConfig{}).Generate(t.Context(), &Request{}) {
		if err != nil {
			got = err
		}
	}
	if inner.calls != 1 || !errors.Is(got, failure) {
		t.Fatalf("unknown provider tool was replayed: calls=%d err=%v", inner.calls, got)
	}
}
