package runtime

import (
	"context"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type compactionActivityKey struct{}

// compactionActivity reports transient start and retry observations for the
// same foreground compaction. The runner remains their publication owner.
type compactionActivity struct {
	started func()
	retry   func(model.AttemptReset)
}

func notifyCompactionRetry(ctx context.Context, reset model.AttemptReset) {
	if activity, ok := ctx.Value(compactionActivityKey{}).(compactionActivity); ok {
		activity.retry(reset)
	}
}

func notifyCompactionStarted(ctx context.Context) {
	if activity, ok := ctx.Value(compactionActivityKey{}).(compactionActivity); ok {
		activity.started()
	}
}

func (r *Runtime) withCompactActivity(
	ctx context.Context,
	activeSession session.Session,
	turnID string,
	sink *runner,
) context.Context {
	if sink == nil {
		return ctx
	}
	var once sync.Once
	return context.WithValue(ctx, compactionActivityKey{}, compactionActivity{
		started: func() {
			once.Do(func() {
				event := buildCompactActivityEvent(activeSession, turnID, r.now())
				sink.publishEvent(normalizeEvent(activeSession, turnID, event))
			})
		},
		retry: func(reset model.AttemptReset) {
			sink.publishEvent(normalizeEvent(activeSession, turnID, session.NewModelAttemptReset(reset)))
		},
	})
}
