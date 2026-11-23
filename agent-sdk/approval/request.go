package approval

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Request is one runtime-owned approval request emitted before one
// sensitive tool execution continues.
type Request struct {
	// Authorization binds offered choices to exact Policy-owned scopes.
	Authorization *session.ApprovalAuthorization `json:"authorization,omitempty"`
	Origin        *Origin                        `json:"origin,omitempty"`
	SessionRef    session.SessionRef             `json:"session_ref"`
	Session       session.Session                `json:"session"`
	RunID         string                         `json:"run_id,omitempty"`
	TurnID        string                         `json:"turn_id,omitempty"`
	// ApprovalRequestID identifies the SDK-owned durable request when this approval
	// was created by Runtime durable approval handling. Product Control may use
	// it as the request correlation value without adding it to ACP wire payloads.
	ApprovalRequestID string `json:"approval_request_id,omitempty"`
	// OperationTaskID associates the approval with a Job in SessionRef. It is
	// separate from Origin.TaskID, which identifies the requesting Agent.
	OperationTaskID string `json:"operation_task_id,omitempty"`
	// OnAdmission acknowledges registration in the approval scheduler. Task
	// producers may yield only after it succeeds. The supplied context carries
	// the original approval deadline; observing a Task never renews it.
	// Requesters which omit the acknowledgment retain synchronous observation.
	OnAdmission func(context.Context) error `json:"-"`
	Tool        tool.Definition             `json:"tool"`
	// Execution and Parent are detached Runtime occurrence evidence.
	Execution tool.InvocationContext `json:"-"`
	Parent    *tool.ParentCall       `json:"-"`
	Call      tool.Call              `json:"call"`
	// ProducingModel is the process-local fallback reviewer from the request
	// that emitted Call. It is absent for approvals without a producing model.
	ProducingModel model.LLM `json:"-"`
	// ModelStep identifies the model-emitted tool-call batch. Concurrent batches
	// also carry a process-local admission barrier; neither is part of the ACP
	// permission payload.
	ModelStep *tool.ModelStepRef        `json:"model_step,omitempty"`
	Approval  *session.ProtocolApproval `json:"approval,omitempty"`
	Metadata  map[string]any            `json:"metadata,omitempty"`
}

// Response is one resolved approval outcome.
type Response struct {
	Outcome    string `json:"outcome,omitempty"`
	OptionID   string `json:"option_id,omitempty"`
	Approved   bool   `json:"approved,omitempty"`
	Reason     string `json:"reason,omitempty"`
	ReviewText string `json:"review_text,omitempty"`
}

// Requester bridges runtime approval decisions to an interactive client
// such as ACP request_permission.
type Requester interface {
	RequestApproval(context.Context, Request) (Response, error)
}

// ResolveRequest resolves one durable approval request.
type ResolveRequest struct {
	SessionRef session.SessionRef `json:"session_ref"`
	RequestID  string             `json:"request_id"`
	Decision   Response           `json:"decision"`
}

// Resolver resolves one durable approval pause. Resolution can wake a
// matching live waiter, but does not make a non-live run resumable.
type Resolver interface {
	ResolveApproval(context.Context, ResolveRequest) error
}
