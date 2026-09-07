package chat

import (
	"context"
	"errors"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type cancellationProbeModel struct {
	calls   int
	cancel  context.CancelFunc
	yielded int
}

func (*cancellationProbeModel) Name() string { return "cancellation-probe" }
func (m *cancellationProbeModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	return func(yield func(*model.StreamEvent, error) bool) {
		if m.cancel != nil {
			m.cancel()
		}
		for range 3 {
			m.yielded++
			if !yield(model.StreamEventFromResponse(&model.Response{Message: model.NewTextMessage(model.RoleAssistant, "late success"), TurnComplete: true}), nil) {
				return
			}
		}
	}
}

func TestChatRunRejectsProviderSuccessAfterCancellation(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(map[bool]string{true: "before_request", false: "during_stream"}[before], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			llm := &cancellationProbeModel{}
			if before {
				cancel()
			} else {
				llm.cancel = cancel
			}
			a, err := New("main", llm, "")
			if err != nil {
				t.Fatal(err)
			}
			var runErr error
			for event, err := range a.Run(agent.NewContext(agent.ContextSpec{Context: ctx})) {
				if event != nil && session.EventTypeOf(event) == session.EventTypeAssistant {
					t.Errorf("cancelled request emitted assistant: %#v", event)
				}
				if err != nil {
					runErr = err
				}
			}
			if !errors.Is(runErr, context.Canceled) {
				t.Errorf("error = %v, want cancellation", runErr)
			}
			if before && llm.calls != 0 {
				t.Errorf("provider calls = %d, want zero", llm.calls)
			}
			if !before && llm.yielded != 1 {
				t.Errorf("stream consumed %d events, want stop at first cancelled yield", llm.yielded)
			}
		})
	}
}
