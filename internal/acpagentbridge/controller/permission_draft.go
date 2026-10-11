package controller

import (
	"context"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	v2 "github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
)

type draftPermissionHandler func(context.Context, v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error)
type draftPermissionSetupKey struct{}

// The setup context passes the callback alongside the existing injected client
// starter. It carries no principal or grant authority; each invocation checks
// the same bound endpoint and uses its existing Runtime requester.
func withDraftPermissionHandler(ctx context.Context, handler draftPermissionHandler) context.Context {
	return context.WithValue(ctx, draftPermissionSetupKey{}, handler)
}

func (r *controllerRun) draftPermissionHandler(ctx context.Context, req v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
	if r == nil {
		return v2.RequestPermissionResponse{}, errACPPermissionSessionMismatchEndpoint
	}
	r.mu.Lock()
	remote, active, mode, requester, agent := r.remoteSessionID, session.CloneSession(r.turnSession), r.turnMode, r.approvalRequester, r.agent
	r.mu.Unlock()
	return dispatchBoundDraftPermission(ctx, remote, active, agent, mode, requester, req)
}

func (r *participantRun) draftPermissionHandler(ctx context.Context, req v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
	if r == nil {
		return v2.RequestPermissionResponse{}, errACPPermissionSessionMismatchEndpoint
	}
	r.mu.Lock()
	remote, active, mode, requester, agent := r.remoteSessionID, session.CloneSession(r.turnSession), r.turnMode, r.approvalRequester, r.agent
	r.mu.Unlock()
	return dispatchBoundDraftPermission(ctx, remote, active, agent, mode, requester, req)
}

func dispatchBoundDraftPermission(ctx context.Context, boundID string, active session.Session, agent, mode string, requester controller.ApprovalRequester, req v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
	if boundID == "" || boundID != string(req.SessionId) {
		return v2.RequestPermissionResponse{}, errACPPermissionSessionMismatchEndpoint
	}
	if requester == nil {
		return acppermission.ResponseToDraft(req, controller.ApprovalResponse{})
	}
	inbound, _ := acpsdk.InboundInfoFromContext(ctx)
	permission, err := acppermission.RequestFromDraft(req, inbound.RequestID)
	if err != nil {
		return v2.RequestPermissionResponse{}, err
	}
	response, err := requester.RequestControllerApproval(ctx, controller.ApprovalRequest{EndpointSessionID: boundID, SessionRef: active.SessionRef, Session: active, Agent: agent, Mode: mode, Approval: permission})
	if err != nil {
		return v2.RequestPermissionResponse{}, err
	}
	return acppermission.ResponseToDraft(req, response)
}
