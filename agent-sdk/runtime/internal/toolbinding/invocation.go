package toolbinding

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"reflect"

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
	call   tool.Call
	result *tool.Result
}

// WithPrepared admits only the exact occurrence prepared by a Runtime caller.
func WithPrepared(ctx context.Context, call tool.Call, result ...*tool.Result) context.Context {
	p := &preparedInvocation{call: tool.CloneCall(call)}
	if len(result) > 0 && result[0] != nil {
		r, _ := tool.CloneResult(*result[0], nil)
		p.result = &r
	}
	return context.WithValue(ctx, preparedKey{}, p)
}

func PreparedResult(ctx context.Context) *tool.Result {
	p, _ := ctx.Value(preparedKey{}).(*preparedInvocation)
	if p == nil || p.result == nil {
		return nil
	}
	r, _ := tool.CloneResult(*p.result, nil)
	return &r
}

func PrepareEffective(ctx context.Context, candidate tool.Tool, call tool.Call) (tool.Call, *tool.Result, error) {
	call = Prepare(candidate, call)
	return PrepareArguments(ctx, candidate, call)
}

// PrepareArguments transforms an occurrence already allocated by its Runtime owner.
func PrepareArguments(ctx context.Context, candidate tool.Tool, call tool.Call) (tool.Call, *tool.Result, error) {
	if candidate == nil {
		return call, nil, nil
	}
	return hookrun.PrepareTool(ctx, candidate.Definition(), call)
}

// IsPrepared excludes caller-authored identity and changed effective arguments.
func IsPrepared(ctx context.Context, call tool.Call) bool {
	prepared, ok := ctx.Value(preparedKey{}).(*preparedInvocation)
	if !ok {
		return false
	}
	original := prepared.call
	return ok && original.Execution == call.Execution && original.ID == call.ID && original.Name == call.Name && string(original.Input) == string(call.Input) &&
		reflect.DeepEqual(original.Parent, call.Parent) && reflect.DeepEqual(original.TargetBinding, call.TargetBinding) && reflect.DeepEqual(original.ModelCall, call.ModelCall) && reflect.DeepEqual(original.ProposedCall, call.ProposedCall)
}
