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
	Hooks    agent.Hooks
	Evidence agent.HookContext
	mu       sync.Mutex
	early    *model.Message
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
