package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

// controllerCallScope belongs to one Runner, including any controller
// reattachment within its Turn. Closing admission and joining accepted calls
// precedes the terminal Run journal and release of the Runner's fence.
type controllerCallScope struct {
	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.Mutex
	closed bool
	calls  sync.WaitGroup
}

func newControllerCallScope(ctx context.Context) *controllerCallScope {
	ctx, cancel := context.WithCancel(ctx)
	return &controllerCallScope{ctx: ctx, cancel: cancel}
}

func (s *controllerCallScope) admit(ctx context.Context) (context.Context, func(), error) {
	if s == nil {
		return nil, nil, fmt.Errorf("runtime: controller Turn has no tool execution owner")
	}
	s.mu.Lock()
	if s.closed || s.ctx.Err() != nil {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("runtime: controller Turn finished")
	}
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, nil, err
	}
	s.calls.Add(1)
	s.mu.Unlock()
	workCtx, cancel := context.WithCancel(s.ctx)
	stop := context.AfterFunc(ctx, cancel)
	return workCtx, func() {
		stop()
		cancel()
		s.calls.Done()
	}, nil
}

func (s *controllerCallScope) closeAndWait() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.cancel()
	s.calls.Wait()
}

// CallControllerSpawn executes a Host-resolved Spawn tool under the exact live
// controller Turn's Runtime claim. It shares native Spawn's lifecycle, placement,
// approval and Task owner; neither observed Session state nor a caller's context
// can mint an execution claim. Runner completion joins the call through durable
// settlement. The Host must authorize the caller before entry.
func (r *Runtime) CallControllerSpawn(ctx context.Context, ref session.SessionRef, epoch string, base spawn.Tool, call tool.Call) (tool.Result, error) {
	active := r.activeRunForSession(ref)
	if active.handle == nil || active.session.Controller.Kind != session.ControllerKindACP || epoch == "" || active.session.Controller.EpochID != epoch {
		return tool.Result{}, fmt.Errorf("runtime: no matching live external controller Turn")
	}
	workCtx, release, err := active.handle.controllerCalls.admit(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	defer release()
	current, err := r.sessions.Session(workCtx, ref)
	if err != nil {
		return tool.Result{}, err
	}
	if current.Controller.EpochID != epoch {
		return tool.Result{}, fmt.Errorf("runtime: controller was replaced")
	}
	state, err := r.sessions.SnapshotState(workCtx, ref)
	if err != nil {
		return tool.Result{}, err
	}
	spec := cloneAgentSpec(active.spec)
	spec.Tools = []tool.Tool{base}
	mode, _ := r.policyForName(workCtx, r.policyMode(spec))
	spec.Tools = r.wrapToolsForRuntime(current, ref, spec, runtimeToolContext{mode: mode, approvalMode: string(r.currentApprovalMode(state)), approvalRequester: active.approvalRequester, runID: active.handle.RunID(), turnID: active.turnID})
	wrapped := r.wrapTurnTools(workCtx, current, ref, state, spec, active.approvalRequester, active.handle.RunID(), active.turnID, nil)
	result, callErr := wrapped[0].Call(workCtx, call)
	return result, errors.Join(callErr, persistToolExecutionReceipt(workCtx, r.sessions, ref, call, result))
}
