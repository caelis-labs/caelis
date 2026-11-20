package tool

import "context"

// NamedTool adapts a static definition and a call function. A nil Invoke
// returns a zero result and nil error.
type NamedTool struct {
	Def    Definition
	Invoke func(context.Context, Call) (Result, error)
}

func (t NamedTool) Definition() Definition {
	return CloneDefinition(t.Def)
}

func (t NamedTool) Call(ctx context.Context, call Call) (Result, error) {
	if t.Invoke == nil {
		return Result{}, nil
	}
	return CloneResult(t.Invoke(ctx, CloneCall(call)))
}
