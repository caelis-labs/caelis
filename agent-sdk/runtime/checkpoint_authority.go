package runtime

import (
	"context"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Retain a request snapshot while background generation or synchronous recovery
// uses its model and validator. Idle handoff retains this only until it obtains
// an independent snapshot from the resolver; it does not reuse Turn admission.
type modelRequestAuthority struct {
	epoch         string
	admit         func(context.Context, agent.ModelRequestAdmission) error
	validate      func(context.Context) (func(), error)
	deferredTools tool.Source
	resolveIdle   agent.ModelRequestResolver
	mu            sync.Mutex
	refs          int
	release       func()
}

func (a *modelRequestAuthority) retain() func() {
	if a == nil {
		return func() {}
	}
	a.mu.Lock()
	a.refs++
	a.mu.Unlock()
	return a.drop
}

func (a *modelRequestAuthority) drop() {
	a.mu.Lock()
	a.refs--
	last := a.refs == 0
	a.mu.Unlock()
	if last && a.release != nil {
		a.release()
	}
}

type checkpointValidationKey struct{}
type checkpointValidator = func(context.Context) (func(), error)
