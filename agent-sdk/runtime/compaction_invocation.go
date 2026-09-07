package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type compactionInvocations struct{ attempts []model.Invocation }

func (c *compactionInvocations) context(ctx context.Context) context.Context {
	return model.WithInvocationObserver(ctx, func(in model.Invocation) { c.attempts = append(c.attempts, in) })
}

// The caller already holds the Session write queue and its existing mutation
// guard. Completion accounting survives caller cancellation, with a bounded
// cleanup context, and advances the admitted revision before the checkpoint.
func (r *Runtime) persistCompactionInvocations(ctx context.Context, active *session.Session, turnID string, records *compactionInvocations) error {
	if len(records.attempts) == 0 {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, attempt := range records.attempts {
		event := normalizeEvent(*active, turnID, session.NewModelInvocationReceipt(attempt, "compaction"))
		_, err := r.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: active.SessionRef, ExpectedRevision: &active.Revision, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event})
		if err != nil {
			return fmt.Errorf("persist model invocation receipt: %w", err)
		}
		active.Revision++
	}
	return nil
}
