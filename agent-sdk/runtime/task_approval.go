package runtime

import (
	"context"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

type taskInvocationStartKey struct{}

type taskInvocationStart struct {
	time       time.Time
	generation uint64
}

type taskApproval struct {
	owner      context.Context
	request    agent.ApprovalRequest
	resolve    func(context.Context, agent.ApprovalRequest) (agent.ApprovalResponse, error)
	started    time.Time
	generation uint64
	admission  *codeModeAdmissionBinding
}

type approvedToolCall func(context.Context, tool.Call) (tool.Result, error)

// taskApprovalProducer is implemented by trusted Runtime producers which own
// durable submission, cancellation and observation. A tool name alone does not
// grant this capability. Another producer can reuse policy
// admission without inheriting command-specific persistence or execution.
type taskApprovalProducer interface {
	submitApproval(context.Context, tool.Call, taskApproval, approvedToolCall) (tool.Result, error)
}

func submitTaskApproval(ctx context.Context, call tool.Call, approval taskApproval, wrapped tool.Tool) (tool.Result, bool, error) {
	if taskScopeFromContext(approval.owner) == nil {
		return tool.Result{}, false, nil
	}
	execute := wrapped.Call
	producerTool := wrapped
	if dispatch, ok := wrapped.(dispatchTool); ok {
		producerTool = dispatch.base
		execute = dispatch.Call
	}
	producer, ok := producerTool.(taskApprovalProducer)
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
