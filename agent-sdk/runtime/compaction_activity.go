package runtime

import (
	"context"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type compactionStartObserverKey struct{}
type compactionRetryObserverKey struct{}

func notifyCompactionRetry(ctx context.Context, reset model.AttemptReset) {
	if observer, ok := ctx.Value(compactionRetryObserverKey{}).(func(model.AttemptReset)); ok {
		observer(reset)
	}
}

func withCompactionStartObserver(ctx context.Context, observer func()) context.Context {
	if observer == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, compactionStartObserverKey{}, observer)
}

func notifyCompactionStarted(ctx context.Context) {
	if ctx == nil {
		return
	}
	observer, _ := ctx.Value(compactionStartObserverKey{}).(func())
	if observer != nil {
		observer()
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
	ctx = context.WithValue(ctx, compactionRetryObserverKey{}, func(reset model.AttemptReset) {
		sink.publishEvent(normalizeEvent(activeSession, turnID, session.NewModelAttemptReset(reset)))
	})
	var once sync.Once
	return withCompactionStartObserver(ctx, func() {
		once.Do(func() {
			event := buildCompactActivityEvent(activeSession, turnID, r.now())
			sink.publishEvent(normalizeEvent(activeSession, turnID, event))
		})
	})
}
