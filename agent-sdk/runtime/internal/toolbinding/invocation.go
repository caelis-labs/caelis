package toolbinding

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/internal/invocationctx"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/google/uuid"
)

type preparer interface {
	RuntimePrepareInvocation(Token, tool.Call) tool.InvocationContext
}

// Allocate assigns an occurrence before its call fact is committed.
func Allocate(candidate any, call tool.Call) tool.InvocationContext {
	if p, ok := candidate.(preparer); ok {
		return p.RuntimePrepareInvocation(Token{}, call)
	}
	return tool.InvocationContext{ItemID: uuid.NewString()}
}

type targetKey struct{}

// WithOccurrence supplies the Runtime-owned identity and replaces any parent
// relation and target inherited from an outer invocation.
func WithOccurrence(ctx context.Context, execution tool.InvocationContext, parent *tool.ParentCall, target *tool.Binding) context.Context {
	ctx = invocationctx.With(ctx, invocationctx.ExecutionKey{}, execution)
	if parent != nil {
		copy := *parent
		parent = &copy
	}
	ctx = invocationctx.With(ctx, invocationctx.ParentKey{}, parent)
	var binding tool.Binding
	if target != nil {
		binding = *target
	}
	return context.WithValue(ctx, targetKey{}, binding)
}

// Target returns a copy of the frozen MCP binding for Runtime admission and facts.
func Target(ctx context.Context) *tool.Binding {
	binding, _ := ctx.Value(targetKey{}).(tool.Binding)
	if binding == (tool.Binding{}) {
		return nil
	}
	return &binding
}

type preparedKey struct{}
type preparedInvocation struct {
	call      tool.Call
	execution tool.InvocationContext
	parent    *tool.ParentCall
	target    *tool.Binding
	result    *tool.Result
	err       error
}

// WithPrepared freezes the effective call and its occurrence for dispatch.
func WithPrepared(ctx context.Context, call tool.Call, result *tool.Result, err error) context.Context {
	p := &preparedInvocation{
		call: tool.CloneCall(call), execution: tool.InvocationFromContext(ctx),
		parent: tool.ParentFromContext(ctx), target: Target(ctx),
	}
	if result != nil {
		r, _ := tool.CloneResult(*result, nil)
		p.result, p.err = &r, err
	}
	return context.WithValue(ctx, preparedKey{}, p)
}

// RebasePrepared transfers the frozen occurrence to the producer's lifetime.
// The caller must validate the prepared call before transferring ownership.
func RebasePrepared(dst, src context.Context) context.Context {
	p := src.Value(preparedKey{}).(*preparedInvocation)
	return context.WithValue(WithOccurrence(dst, p.execution, p.parent, p.target), preparedKey{}, p)
}

// PreparedReply returns the synthetic reply, if preparation supplied one.
func PreparedReply(ctx context.Context) (tool.Result, error, bool) {
	p, _ := ctx.Value(preparedKey{}).(*preparedInvocation)
	if p == nil || p.result == nil {
		return tool.Result{}, nil, false
	}
	r, err := tool.CloneResult(*p.result, p.err)
	return r, err, true
}

// PrepareArguments transforms an occurrence already allocated by its Runtime owner.
func PrepareArguments(ctx context.Context, candidate tool.Tool, call tool.Call) (tool.Call, *tool.Result, error) {
	if candidate == nil {
		return call, nil, nil
	}
	return hookrun.PrepareTool(ctx, candidate.Definition(), call)
}

// IsPrepared excludes changed occurrence data and effective arguments.
func IsPrepared(ctx context.Context, call tool.Call) bool {
	prepared, ok := ctx.Value(preparedKey{}).(*preparedInvocation)
	if !ok {
		return false
	}
	original := prepared.call
	return prepared.execution == tool.InvocationFromContext(ctx) && original.ID == call.ID && original.Name == call.Name && string(original.Input) == string(call.Input) &&
		sameOptional(prepared.parent, tool.ParentFromContext(ctx)) && sameOptional(prepared.target, Target(ctx))
}

func sameOptional[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
