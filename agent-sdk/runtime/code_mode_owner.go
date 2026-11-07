package runtime

import (
	"context"
	"errors"

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
			wrapped[index] = codeModeProducerTool{base: wrapped[index], scope: taskScopeFromContext(ctx)}
		}
		index++
	}
	return wrapped
}

type codeModeProducerTool struct {
	base  tool.Tool
	scope *taskContinuationScope
}

func (t codeModeProducerTool) Definition() tool.Definition { return t.base.Definition() }

func (t codeModeProducerTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t.scope == nil {
		return tool.Result{}, errors.New("code mode requires the Runtime Run producer owner")
	}
	// This is only an in-memory join registration, never an operation identity,
	// recovery record, or execution authority. Invocation still owns tool identity.
	id := "code-mode-producer:" + uuid.NewString()
	work, err := t.scope.register(ctx, id, t.scope.currentGeneration())
	if err != nil {
		return tool.Result{}, err
	}
	defer t.scope.complete(id, work, nil)
	return t.base.Call(work.ctx, call)
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
