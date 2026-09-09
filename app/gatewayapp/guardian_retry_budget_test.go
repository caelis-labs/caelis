package gatewayapp

import (
	"context"
	"errors"
	"iter"
	"testing"
	"testing/synctest"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type guardianRetryBudgetModel struct {
	*approvalReviewerFakeModel
	remaining []time.Duration
}

func (m *guardianRetryBudgetModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		deadline, ok := ctx.Deadline()
		if !ok {
			yield(nil, errors.New("missing approval deadline"))
			return
		}
		m.remaining = append(m.remaining, time.Until(deadline))
		delay := 170 * time.Second
		if len(m.remaining) > 1 {
			delay = 20 * time.Second
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			yield(nil, ctx.Err())
			return
		}
		for e, err := range m.approvalReviewerFakeModel.Generate(ctx, req) {
			if !yield(e, err) {
				return
			}
		}
	}
}

func TestGuardianFormatRetryGetsFreshTimeoutAndPreservesCallerDeadline(t *testing.T) {
	for _, callerLimit := range []time.Duration{0, 175 * time.Second} {
		t.Run(callerLimit.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				if callerLimit > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, callerLimit)
					defer cancel()
				}
				service, active := newApprovalReviewerTestSession(t, ctx)
				llm := &guardianRetryBudgetModel{approvalReviewerFakeModel: &approvalReviewerFakeModel{responses: []string{`{"outcome":`, `{"option_id":"allow_once"}`}}}
				reviewer := newGuardianApprovalApprover(service)
				result, err := reviewer.Decide(ctx, approvalReviewerTestRequest(active, llm, "inspect", nil))
				if len(llm.remaining) != 2 {
					t.Fatalf("attempts=%d error=%v", len(llm.remaining), err)
				}
				wantFirst, wantSecond := 3*time.Minute, 3*time.Minute
				if callerLimit > 0 {
					wantFirst = callerLimit
					wantSecond = 5 * time.Second
				}
				if llm.remaining[0] != wantFirst || llm.remaining[1] != wantSecond {
					t.Fatalf("attempt budgets=%v want %v/%v", llm.remaining, wantFirst, wantSecond)
				}
				if callerLimit > 0 {
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("caller deadline ignored: %v", err)
					}
				} else if err != nil || !result.Approved {
					t.Fatalf("fresh retry failed: %+v %v", result, err)
				}
			})
		})
	}
}
