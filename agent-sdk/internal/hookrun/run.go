package hookrun

import (
	"context"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

type runKey struct{}

// Run keeps ephemeral hook configuration, candidate identities and budgets.
// Session, policy, producer lifetime and commits retain their existing owners.
type Run struct {
	Trace     agent.TraceOptions
	Hooks     agent.Hooks
	Evidence  agent.HookContext
	mu        sync.Mutex
	early     *Early
	requestID string
	assembly  uint64
	edits     map[string]bool
	feedback  map[string]bool
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

// FeedbackAvailable and CommitFeedback share the Run's bounded feedback budget
// across Agent reconstruction. A candidate spends nothing until its canonical
// append (or original-ID full-payload readback) has been confirmed.
func (r *Run) FeedbackAvailable() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	limit := r.Hooks.CompletionLimit
	if limit == 0 {
		limit = 2
	}
	return len(r.feedback) < limit
}
func (r *Run) CommitFeedback(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.feedback == nil {
		r.feedback = map[string]bool{}
	}
	r.feedback[id] = true
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

// Early keeps the detached candidate origin for final output transformation.
type Early struct {
	Message  model.Message
	Evidence agent.HookContext
}

func (r *Run) SetEarly(m *model.Message, e agent.HookContext) {
	if m == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.early = &Early{Message: model.CloneMessage(*m), Evidence: e}
}
func (r *Run) HasEarly() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.early != nil }
func (r *Run) TakeEarly() *Early {
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
