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
// owner. Chat's bounded foreground cancellation drain is not producer quiescence;
// executeKernelTurn already joins this owner before Runner completion/fence release.
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

type codeModeProducerIdentityKey struct{}
type codeModeProducerErrorKey struct{}

type codeModeProducerTool struct {
	base  tool.Tool
	scope *taskContinuationScope
	owner context.Context
}

func (t codeModeProducerTool) Definition() tool.Definition { return t.base.Definition() }

func (t codeModeProducerTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	if t.scope == nil {
		return tool.Result{}, errors.New("code mode requires the Runtime Run producer owner")
	}
	// This is only an in-memory join registration, never an operation identity,
	// recovery record, or execution authority. Invocation still owns tool identity.
	id := "code-mode-producer:" + uuid.NewString()
	work, err := t.scope.register(t.owner, id, t.scope.currentGeneration())
	if err != nil {
		return tool.Result{}, err
	}
	t.scope.mu.Lock()
	work.stopAtFinal = true
	closed := t.scope.codeModeClosed
	t.scope.mu.Unlock()
	if closed {
		t.scope.complete(id, work, nil)
		return tool.Result{}, errCodeModeComplete
	}
	var done <-chan struct{}
	var producerError func() error
	var errorMu sync.Mutex
	var durabilityErr error
	ctx = context.WithValue(work.ctx, codeModeProducerErrorKey{}, func(err error) {
		errorMu.Lock()
		durabilityErr = errors.Join(durabilityErr, err)
		errorMu.Unlock()
	})
	completionError := func() error {
		errorMu.Lock()
		defer errorMu.Unlock()
		return durabilityErr
	}
	ctx = context.WithValue(context.WithValue(ctx, codeModeProducerIdentityKey{}, id), codeModeProducerLifetimeKey{}, func(ch <-chan struct{}, completionError func() error) { done = ch; producerError = completionError })
	result, callErr := t.base.Call(ctx, call)
	if done == nil {
		t.scope.complete(id, work, completionError())
	} else {
		go func() { <-done; t.scope.complete(id, work, errors.Join(producerError(), completionError())) }()
	}
	return result, callErr
}

// The policy owner receives source-read semantics from the concrete Runtime
// binding, never from a display name or model/remote metadata. Journal is the
// sole wrapper between policy and that binding at this point.
func codeModeReadPathArgument(one tool.Tool) string {
	if journal, ok := one.(journaledTool); ok {
		one = journal.base
	}
	if _, ok := one.(runtimeCodeModeTool); ok {
		return "file"
	}
	return ""
}

func (t codeModeProducerTool) RuntimeTaskResultSource(toolbinding.Token) bool {
	return toolbinding.IsTaskResultSource(t.base)
}

// A trusted child completion closes only this Run's script dispatch. Existing
// in-flight receipts finish under their original producer; no new owner exists.
func (s *taskContinuationScope) completeCodeMode(ctx context.Context) {
	current, _ := ctx.Value(codeModeProducerIdentityKey{}).(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codeModeClosed = true
	for id, work := range s.active {
		if work.stopAtFinal && id != current {
			work.cancel(errCodeModeComplete)
		}
	}
}
