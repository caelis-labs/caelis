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
// guard. Accounting is append-only and independent of the compacted snapshot:
// overlapping approval and Task journals must not discard a completed attempt.
// The checkpoint separately validates its source before advancing a stale CAS.
func (r *Runtime) persistCompactionInvocations(ctx context.Context, active *session.Session, turnID string, records *compactionInvocations) error {
	if len(records.attempts) == 0 {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, attempt := range records.attempts {
		event := normalizeEvent(*active, turnID, session.NewModelInvocationReceipt(attempt, "compaction"))
		_, err := r.sessions.AppendEvent(cleanup, session.AppendEventRequest{SessionRef: active.SessionRef, MutationGuard: session.RuntimeMutationGuard(ctx), Event: event})
		if err != nil {
			return fmt.Errorf("persist model invocation receipt: %w", err)
		}
		active.Revision++
	}
	return nil
}
