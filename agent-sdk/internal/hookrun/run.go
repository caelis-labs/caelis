package hookrun

import (
	"context"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type runKey struct{}

// Run keeps only ephemeral phase configuration and pending early responses.
// Session, policy, producer lifetime and commits retain their existing owners.
type Run struct {
	Trace     agent.TraceOptions
	Hooks     agent.Hooks
	Evidence  agent.HookContext
	mu        sync.Mutex
	early     *model.Message
	requestID string
	assembly  uint64
	edits     map[string]bool
}

// RequestAssembly survives a Runtime context rebuild, but ends with its one
// semantic model step. It is ephemeral identity, never a request journal.
func (r *Run) RequestAssembly(newID func() string) (string, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requestID == "" {
		r.requestID = newID()
		r.edits = map[string]bool{}
	}
	r.assembly++
	return r.requestID, r.assembly
}
func (r *Run) RequestEdited(id, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requestID == id && r.edits[name]
}
func (r *Run) RecordRequestEdit(id, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requestID == id {
		r.edits[name] = true
	}
}
func (r *Run) FinishRequest(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requestID == id {
		r.requestID = ""
		r.assembly = 0
		r.edits = nil
	}
}

func WithRun(ctx context.Context, h agent.Hooks, e agent.HookContext) context.Context {
	return context.WithValue(ctx, runKey{}, &Run{Hooks: agent.CloneHooks(h), Evidence: e})
}
func FromContext(ctx context.Context) *Run {
	r, _ := ctx.Value(runKey{}).(*Run)
	if r == nil {
		return &Run{}
	}
	return r
}
func (r *Run) SetEarly(m *model.Message) {
	if m == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := model.CloneMessage(*m)
	r.early = &c
}
func (r *Run) HasEarly() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.early != nil }
func (r *Run) TakeEarly() *model.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.early
	r.early = nil
	return m
}

func CloneMessagePointer(m *model.Message) *model.Message {
	if m == nil {
		return nil
	}
	c := model.CloneMessage(*m)
	return &c
}
func CloneInput(in agent.InputDecision) agent.InputDecision {
	in.Message = model.CloneMessage(in.Message)
	in.Context = model.CloneParts(in.Context)
	in.Response = CloneMessagePointer(in.Response)
	return in
}
func CloneTurn(in agent.TurnStartDecision) agent.TurnStartDecision {
	in.Context = model.CloneParts(in.Context)
	in.Response = CloneMessagePointer(in.Response)
	return in
}

type requestKey struct{}

func WithRequest(ctx context.Context, e agent.HookContext) context.Context {
	return context.WithValue(ctx, requestKey{}, e)
}
func RequestEvidence(ctx context.Context) agent.HookContext {
	if e, ok := ctx.Value(requestKey{}).(agent.HookContext); ok {
		return e
	}
	return FromContext(ctx).Evidence
}
