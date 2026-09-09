package controller

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
)

var errACPPermissionSessionMismatchEndpoint = fmt.Errorf("ACP permission Session does not match bound endpoint")

func (r *controllerRun) permissionHandler(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
	if r == nil {
		return client.RequestPermissionResponse{}, errACPPermissionSessionMismatchEndpoint
	}
	r.mu.Lock()
	remoteID := r.remoteSessionID
	activeSession := session.CloneSession(r.turnSession)
	mode := strings.TrimSpace(r.turnMode)
	requester := r.approvalRequester
	agent := strings.TrimSpace(r.agent)
	r.mu.Unlock()
	return dispatchBoundEndpointPermission(ctx, remoteID, activeSession, agent, mode, requester, req)
}

func (r *participantRun) permissionHandler(ctx context.Context, req client.RequestPermissionRequest) (client.RequestPermissionResponse, error) {
	if r == nil {
		return client.RequestPermissionResponse{}, errACPPermissionSessionMismatchEndpoint
	}
	r.mu.Lock()
	remoteID := r.remoteSessionID
	activeSession := session.CloneSession(r.turnSession)
	mode := strings.TrimSpace(r.turnMode)
	requester := r.approvalRequester
	agent := strings.TrimSpace(r.agent)
	r.mu.Unlock()
	return dispatchBoundEndpointPermission(ctx, remoteID, activeSession, agent, mode, requester, req)
}

// dispatchBoundEndpointPermission fails closed when the local endpoint Session
// is unknown or the request does not name that exact Session. Activate and
// attach fail closed until the remote Session is bound. Reconnect already has
// that identity, so a matching request may proceed during resume. A bound
// request never proceeds with a blank EndpointSessionID.
func dispatchBoundEndpointPermission(
	ctx context.Context,
	boundID string,
	activeSession session.Session,
	agent string,
	mode string,
	requester controller.ApprovalRequester,
	req client.RequestPermissionRequest,
) (client.RequestPermissionResponse, error) {
	boundID = strings.TrimSpace(boundID)
	if boundID == "" || boundID != string(req.SessionId) {
		return client.RequestPermissionResponse{}, errACPPermissionSessionMismatchEndpoint
	}
	if requester != nil {
		approvalReq, err := translateApprovalRequest(activeSession, agent, mode, req)
		if err != nil {
			return client.RequestPermissionResponse{}, err
		}
		approvalReq.EndpointSessionID = boundID
		resp, err := requester.RequestControllerApproval(ctx, approvalReq)
		if err != nil {
			return client.RequestPermissionResponse{}, err
		}
		if selected, ok := acputil.SelectedOutcome(resp.Outcome, resp.OptionID); ok {
			return selected, nil
		}
	}
	return acputil.RejectOnce(), nil
}
