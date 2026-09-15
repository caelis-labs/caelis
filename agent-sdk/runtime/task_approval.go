package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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
}

type approvedToolCall func(context.Context, tool.Call) (tool.Result, error)

// taskApprovalProducer is implemented by trusted Runtime producers which own
// durable submission, cancellation and observation. ParallelSafe and a tool
// name alone do not grant this capability. Another producer can reuse policy
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
	if journal, ok := wrapped.(journaledTool); ok {
		producerTool = journal.base
		execute = journal.callTaskContinuation
	}
	producer, ok := producerTool.(taskApprovalProducer)
	if !ok {
		return tool.Result{}, false, nil
	}
	result, err := producer.submitApproval(ctx, call, approval, execute)
	return result, true, err
}

// A yielded invocation has already returned its sole canonical result. The
// continuation persists the execution receipt as journal data, never as a
// second result for that call ID. Journaling still begins only after approval.
func (t journaledTool) callTaskContinuation(ctx context.Context, call tool.Call) (tool.Result, error) {
	result, callErr := t.Call(ctx, call)
	value, ok := result.Metadata[tool.MetadataExecutionJournal]
	if !ok {
		return result, callErr
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return result, errors.Join(callErr, err)
	}
	var journal session.ExecutionJournalEntry
	if err := json.Unmarshal(raw, &journal); err != nil {
		return result, errors.Join(callErr, err)
	}
	record := journal.ToolExecution
	if record == nil {
		return result, errors.Join(callErr, errors.New("task execution receipt is missing"))
	}
	receiptCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), approvalResolutionRecoveryTimeout)
	defer cancel()
	_, err = t.sessions.AppendEvent(receiptCtx, session.AppendEventRequest{
		SessionRef: t.sessionRef, MutationGuard: session.RuntimeMutationGuard(ctx),
		Event: &session.Event{
			IdempotencyKey: "tool-execution:" + record.Identity + ":" + fmt.Sprint(record.Revision),
			Type:           session.EventTypeLifecycle, Visibility: session.VisibilityJournal,
			Time: record.UpdatedAt, Journal: &journal,
			Actor:     session.ActorRef{Kind: session.ActorKindTool, ID: call.ID, Name: call.Name},
			Lifecycle: &session.EventLifecycle{Status: string(record.Status), Reason: record.Reason},
		},
	})
	return result, errors.Join(callErr, err)
}
