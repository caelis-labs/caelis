package runtime

import (
	"context"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type modelRequestAuthorityKey struct{}

// Retain a request snapshot while background generation or synchronous recovery
// uses its model and validator. Idle handoff retains this only until it obtains
// an independent snapshot from the resolver; it does not reuse Turn admission.
type modelRequestAuthority struct {
	epoch         string
	admit         func(context.Context, agent.ModelRequestAdmission) error
	validate      func(context.Context) (func(), error)
	deferredTools tool.Source
	resolveIdle   agent.ModelRequestResolver
	// Runtime guards protect synchronous commits even without a parent guard.
	// They cannot establish the embedding authority required for speculation.
	canSpeculate bool
	mu           sync.Mutex
	refs         int
	release      func()
}

// Capture the embedding owner's current request validator and retain its
// provider/configuration snapshot through generation and adoption.
func (r *Runtime) captureRequestAuthority(parent *modelRequestAuthority) *modelRequestAuthority {
	if parent == nil {
		return nil
	}
	snapshot := agent.ModelRequestSnapshot{Revision: parent.epoch, Validate: parent.validate}
	return &modelRequestAuthority{epoch: snapshot.Revision, validate: snapshot.Validate,
		admit: parent.admit, deferredTools: parent.deferredTools, resolveIdle: parent.resolveIdle, canSpeculate: parent.validate != nil, refs: 1, release: parent.retain()}
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
