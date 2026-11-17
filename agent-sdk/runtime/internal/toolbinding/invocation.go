package toolbinding

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/google/uuid"
)

type preparer interface {
	RuntimePrepareInvocation(Token, tool.Call) tool.Call
}

// Prepare freezes a tool occurrence before its call fact is committed.
func Prepare(candidate any, call tool.Call) tool.Call {
	if p, ok := candidate.(preparer); ok {
		return p.RuntimePrepareInvocation(Token{}, call)
	}
	call.Execution = tool.InvocationContext{ItemID: uuid.NewString()}
	return call
}

type preparedKey struct{}
type preparedInvocation struct {
	call tool.Call
}

// WithPrepared admits only the exact occurrence prepared by a Runtime caller.
func WithPrepared(ctx context.Context, call tool.Call) context.Context {
	return context.WithValue(ctx, preparedKey{}, &preparedInvocation{call: call})
}

// IsPrepared excludes caller-authored identity and changed effective arguments.
func IsPrepared(ctx context.Context, call tool.Call) bool {
	prepared, ok := ctx.Value(preparedKey{}).(*preparedInvocation)
	if !ok {
		return false
	}
	original := prepared.call
	return ok && original.Execution == call.Execution && original.ID == call.ID && original.Name == call.Name && string(original.Input) == string(call.Input)
}
