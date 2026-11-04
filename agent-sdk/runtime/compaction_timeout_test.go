package runtime

import (
	"context"
	"iter"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestAsyncCompactionGenerationBeyond30Seconds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := newAsyncHarness(t, "")
		run := h.run(t)
		next := h.startCandidate(t)
		time.Sleep(40 * time.Second)
		select {
		case <-next.scope.job.done:
			t.Fatalf("candidate prematurely ended: %v", next.scope.job.err)
		default:
		}
		h.completeCandidate(t, next.scope)
		h.llm.responses <- asyncTextResponse("done", 500)
		if err := run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
	})
}

type recoveringSummaryModel struct{ calls int }

func (*recoveringSummaryModel) Name() string { return "summary-fixture" }
func (m *recoveringSummaryModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		m.calls++
		if m.calls <= 30 {
			yield(nil, errorcode.New(errorcode.Unavailable, "offline"))
			return
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) != 600*time.Second {
			panic("summary attempt did not receive fresh 600-second budget")
		}
		timer := time.NewTimer(40 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, asyncFixtureSummary), TurnComplete: true}), nil)
	}
}

func TestCompactionForegroundOfflineWaitingHasNoTotalDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := &Runtime{}
		inner := &recoveringSummaryModel{}
		llm := model.WithRetry(inner, model.RetryConfig{})
		started := time.Now()
		c := newCodexStyleCompactor(CompactionConfig{Enabled: true}, nil).(compact.ForceEngine)
		result, err := c.Force(r.compactionRequestContext(t.Context()), compact.Request{Model: llm, Events: []*session.Event{userTextEvent("retain original source")}}, "fixture")
		if err != nil || !result.Compacted || inner.calls != 31 || time.Since(started) <= 600*time.Second {
			t.Fatalf("calls=%d elapsed=%s compacted=%t err=%v", inner.calls, time.Since(started), result.Compacted, err)
		}
	})
}
