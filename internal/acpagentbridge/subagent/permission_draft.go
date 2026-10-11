package subagent

import (
	"context"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	v2 "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/control/acppermission"
)

// DraftPermissionBridge optionally extends the retained direct permission bridge
// with exact draft requests. A stable-only bridge cannot receive a fabricated
// tool invocation and explicitly cancels draft permission instead.
type DraftPermissionBridge interface {
	RequestDraftPermission(context.Context, DraftPermissionRequest) (v2.RequestPermissionResponse, error)
}

type DraftPermissionRequest struct {
	Spawn   subagent.SpawnContext
	Agent   delegation.Agent
	AgentID string
	Request v2.RequestPermissionRequest
}

func (r *Runner) boundDraftPermissionHandler(run *childRun, spawn subagent.SpawnContext, cfg AgentConfig, agentID string) func(context.Context, v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
	return func(ctx context.Context, req v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
		if run == nil {
			return v2.RequestPermissionResponse{}, errACPPermissionSessionMismatchChild
		}
		run.mu.Lock()
		bound, observationOnly := run.anchor.SessionID, run.observationOnly
		run.mu.Unlock()
		if bound == "" || bound != string(req.SessionId) {
			return v2.RequestPermissionResponse{}, errACPPermissionSessionMismatchChild
		}
		if observationOnly {
			return acppermission.ResponseToDraft(req, agent.ApprovalResponse{})
		}
		if r.permissionBridge != nil {
			if bridge, ok := r.permissionBridge.(DraftPermissionBridge); ok {
				return bridge.RequestDraftPermission(ctx, DraftPermissionRequest{Spawn: spawn, Agent: delegation.Agent{Name: cfg.Name, Description: cfg.Description}, AgentID: agentID, Request: req})
			}
			return acppermission.ResponseToDraft(req, agent.ApprovalResponse{})
		}
		if r.permissionHandler != nil || spawn.ApprovalRequester == nil {
			return acppermission.ResponseToDraft(req, agent.ApprovalResponse{})
		}
		inbound, _ := acpsdk.InboundInfoFromContext(ctx)
		permission, err := acppermission.RequestFromDraft(req, inbound.RequestID)
		if err != nil {
			return v2.RequestPermissionResponse{}, err
		}
		endpoint := agent.ApprovalEndpointExternalACP
		if cfg.BuiltinRuntime {
			endpoint = agent.ApprovalEndpointBuiltin
		}
		response, err := spawn.ApprovalRequester.RequestSubagentApproval(ctx, subagent.ApprovalRequest{
			Origin:     agent.ApprovalOrigin{Role: agent.ApprovalRoleSubagent, Endpoint: endpoint, SessionID: bound, ParentSessionID: spawn.SessionRef.SessionID, TaskID: spawn.TaskID, ParentCallID: spawn.ParentCallID, ToolCallID: permission.ToolCall.ID, Agent: agentID},
			SessionRef: session.NormalizeSessionRef(spawn.SessionRef), Session: session.CloneSession(spawn.Session), TaskID: spawn.TaskID, ParentCallID: spawn.ParentCallID, Agent: cfg.Name, Mode: spawn.Mode, Approval: permission,
		})
		if err != nil {
			return v2.RequestPermissionResponse{}, err
		}
		return acppermission.ResponseToDraft(req, response)
	}
}
