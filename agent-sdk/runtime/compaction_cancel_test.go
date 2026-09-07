package runtime

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type cancelledCompactionModel struct {
	calls   int
	cancel  context.CancelFunc
	yielded int
}

func (*cancelledCompactionModel) Name() string { return "cancelled-compaction" }
func (m *cancelledCompactionModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.cancel != nil {
			m.cancel()
		}
		for range 3 {
			m.yielded++
			if !yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "A sufficiently long checkpoint from a cancelled request."), TurnComplete: true}), nil) {
				return
			}
		}
	}
}
func TestCompactionRejectsProviderSuccessAfterCancellation(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_request", false: "during_stream"}[before], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			llm := &cancelledCompactionModel{}
			if before {
				cancel()
			} else {
				llm.cancel = cancel
			}
			text, err := modelCompactMarkdownInContext(ctx, llm, &model.Request{})
			if !errors.Is(err, context.Canceled) || text != "" {
				t.Errorf("checkpoint = %q, error = %v, want empty/cancelled", text, err)
			}
			if before && llm.calls != 0 {
				t.Errorf("provider calls = %d, want zero", llm.calls)
			}
			if !before && llm.yielded != 1 {
				t.Errorf("stream consumed %d events, want one", llm.yielded)
			}
		})
	}
}
