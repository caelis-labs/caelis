package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
)

// CallControllerSpawn executes a Host-resolved Spawn tool under the exact live
// controller Turn's Runtime claim. It shares native Spawn's lifecycle, placement,
// approval and Task owner; neither observed Session state nor a caller's context
// can mint an execution claim. The Host must authorize the caller before entry.
func (r *Runtime) CallControllerSpawn(ctx context.Context, ref session.SessionRef, epoch string, base spawn.Tool, call tool.Call) (tool.Result, error) {
	active := r.activeRunForSession(ref)
	if active.handle == nil || active.session.Controller.Kind != session.ControllerKindACP || epoch == "" || active.session.Controller.EpochID != epoch {
		return tool.Result{}, fmt.Errorf("runtime: no matching live external controller Turn")
	}
	current, err := r.sessions.Session(ctx, ref)
	if err != nil {
		return tool.Result{}, err
	}
	if current.Controller.EpochID != epoch {
		return tool.Result{}, fmt.Errorf("runtime: controller was replaced")
	}
	workCtx, cancel := context.WithCancel(active.handle.ctx)
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	defer cancel()
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	select {
	case <-active.handle.done:
		return tool.Result{}, fmt.Errorf("runtime: controller Turn finished")
	default:
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
