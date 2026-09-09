package subagent

import (
	"context"
	"fmt"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

// logChildError writes only to the injected owner-private diagnostics sink.
// Error details may contain peer paths or response data and must never be
// copied into Task output. Do not add prompts, launch environment or stderr.
func (r *Runner) logChildError(ctx context.Context, run *childRun, phase string, err error) {
	if r == nil || r.diagnostics == nil || run == nil || err == nil {
		return
	}
	run.mu.RLock()
	attrs := []any{
		"component", "acp_subagent", "phase", phase,
		"task_id", run.anchor.TaskID, "session_id", run.anchor.SessionID,
		"parent_session_id", run.spawn.SessionRef.SessionID,
		"parent_call_id", run.spawn.ParentCallID, "agent", run.agentName,
		"state", string(run.state), "running", run.running,
		"cancel_requested", run.cancelRequested,
	}
	activityID, slot := run.spawn.ActivityID, run.slot
	run.mu.RUnlock()
	if slot != nil {
		slot.mu.Lock()
		if slot.run == run {
			activityID = slot.activityID
		}
		slot.mu.Unlock()
	}
	attrs = append(attrs, "activity_id", activityID, "error_chain", diagnosticErrorChain(err),
		"connection_error", client.IsConnectionError(err))
	if code, ok := client.ErrorCode(err); ok {
		attrs = append(attrs, "rpc_code", code)
	}
	if state, ok := acpsdk.RequestSubmissionStateOf(err); ok {
		attrs = append(attrs, "submission_state", state.String())
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.diagnostics.WarnContext(context.WithoutCancel(ctx), "ACP child lifecycle failure", attrs...)
}

// Bound both joined-error traversal and message size. The public errorcode
// summary intentionally hides its cause, so retain each unwrap node privately.
func diagnosticErrorChain(err error) []map[string]string {
	const maxNodes = 8
	var chain []map[string]string
	pending := []error{err}
	for len(pending) != 0 && len(chain) < maxNodes {
		current := pending[0]
		pending = pending[1:]
		if current == nil {
			continue
		}
		chain = append(chain, map[string]string{
			"type": fmt.Sprintf("%T", current), "message": truncateMiddleUTF8(current.Error(), 4096),
		})
		switch wrapped := current.(type) { //nolint:errorlint // Traverse each immediate unwrap node; errors.As would skip wrapper diagnostics.
		case interface{ Unwrap() []error }:
			children := wrapped.Unwrap()
			pending = append(pending, children[:min(len(children), maxNodes-len(chain))]...)
		case interface{ Unwrap() error }:
			pending = append(pending, wrapped.Unwrap())
		}
	}
	return chain
}
