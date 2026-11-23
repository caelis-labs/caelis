package runtime

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type taskInvocationStartKey struct{}

type taskInvocationStart struct {
	time       time.Time
	generation uint64
}

type taskApproval struct {
	owner      context.Context
	request    approval.Request
	resolve    func(context.Context, approval.Request) (approval.Response, error)
	started    time.Time
	generation uint64
	admission  *codeModeAdmissionBinding
}

type approvedToolCall func(context.Context, tool.Call, string) (tool.Result, error)

// taskApprovalProducer is implemented by trusted Runtime producers which own
// durable submission, cancellation and observation. A tool name alone does not
// grant this capability. Another producer can reuse policy
// admission without inheriting command-specific persistence or execution.
type taskApprovalProducer interface {
	submitApproval(context.Context, tool.Call, taskApproval, approvedToolCall) (tool.Result, error)
}

func submitTaskApproval(ctx context.Context, call tool.Call, approval taskApproval, target tool.Tool, execute approvedToolCall) (tool.Result, bool, error) {
	if taskScopeFromContext(approval.owner) == nil {
		return tool.Result{}, false, nil
	}
	producer, ok := target.(taskApprovalProducer)
	if !ok {
		return tool.Result{}, false, nil
	}
	binding, err := codeModeAdmissionFor(ctx, call)
	if err != nil {
		return tool.Result{}, true, err
	}
	approval.admission = binding
	result, err := producer.submitApproval(ctx, call, approval, execute)
	return result, true, err
}
