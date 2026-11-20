package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/google/uuid"
)

// Keep the complete ordinary wrapper chain inside the existing Run continuation
// owner. Foreground cancellation does not end the producer; executeKernelTurn
// joins this owner before Runner completion and fence release.
func ownCodeModeProducers(ctx context.Context, concrete, wrapped []tool.Tool) []tool.Tool {
	index := 0
	for _, one := range concrete {
		if one == nil {
			continue
		}
		if _, ok := one.(runtimeCodeModeTool); ok {
			wrapped[index] = codeModeProducerTool{base: wrapped[index], scope: taskScopeFromContext(ctx), owner: ctx}
		}
		index++
	}
	return wrapped
}

type codeModeProducerErrorKey struct{}

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

// The policy owner receives source-read semantics from the concrete Runtime
// binding through its dispatch wrapper, never from a display name or
// model/remote metadata.
func codeModeReadPathArgument(one tool.Tool) string {
	if dispatch, ok := one.(dispatchTool); ok {
		one = dispatch.base
	}
	if _, ok := one.(runtimeCodeModeTool); ok {
		return "file"
	}
	return ""
}

func (t codeModeProducerTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.base)
}
