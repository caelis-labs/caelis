package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/google/uuid"
)

type codeModeProducerErrorKey struct{}

// codeModeProducerTool retains the complete wrapper chain under the Run's
// continuation owner. executeKernelTurn joins it before releasing the fence.
type codeModeProducerTool struct {
	base  tool.Tool
	scope *taskContinuationScope
	owner context.Context
}

func (codeModeProducerTool) RuntimeNativeSchedule(toolbinding.Token, context.Context, tool.Call) toolbinding.NativeSchedule {
	return toolbinding.NativeSchedule{Ordered: true}
}

func (t codeModeProducerTool) Definition() tool.Definition { return t.base.Definition() }

func (t codeModeProducerTool) RuntimePrepareInvocation(_ toolbinding.Token, call tool.Call) tool.InvocationContext {
	return toolbinding.Allocate(t.base, call)
}
func (t codeModeProducerTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	return t.RuntimeCall(toolbinding.Token{}, ctx, call, toolbinding.Dispatch{})
}

func (t codeModeProducerTool) RuntimeCall(_ toolbinding.Token, ctx context.Context, call tool.Call, dispatch toolbinding.Dispatch) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if t.scope == nil {
		return tool.Result{}, errors.New("code mode requires the Runtime Run producer owner")
	}
	if !toolbinding.IsPrepared(ctx, call) {
		return tool.Result{}, errors.New("code mode requires the exact prepared invocation")
	}
	// This is only an in-memory join registration, never an operation identity,
	// recovery record, or execution authority. Invocation still owns tool identity.
	id := "code-mode-producer:" + uuid.NewString()
	work, err := t.scope.register(t.owner, id, t.scope.currentGeneration(), taskContinuationCodeModeScript)
	if err != nil {
		return tool.Result{}, err
	}
	var done <-chan struct{}
	var producerError func() error
	var errorMu sync.Mutex
	var durabilityErr error
	// Retain the producer lifetime while carrying only the validated private
	// invocation credential. Caller-authored Execution never supplies it.
	ctx = context.WithValue(toolbinding.RebasePrepared(work.ctx, ctx), codeModeProducerErrorKey{}, func(err error) {
		errorMu.Lock()
		durabilityErr = errors.Join(durabilityErr, err)
		errorMu.Unlock()
	})
	completionError := func() error {
		errorMu.Lock()
		defer errorMu.Unlock()
		return durabilityErr
	}
	ctx = context.WithValue(ctx, codeModeProducerLifetimeKey{}, func(ch <-chan struct{}, completionError func() error) { done = ch; producerError = completionError })
	result, callErr := toolbinding.Call(ctx, t.base, call, dispatch)
	if done == nil {
		t.scope.complete(id, work, completionError())
	} else {
		go func() { <-done; t.scope.complete(id, work, errors.Join(producerError(), completionError())) }()
	}
	return result, callErr
}

func (t codeModeProducerTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.base)
}
