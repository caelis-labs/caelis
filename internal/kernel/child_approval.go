package kernel

import (
	"context"
	"errors"

	agent "github.com/caelis-labs/caelis/agent-sdk"
)

// RequestChildApproval uses the Session approval queue without acquiring a
// foreground Turn. The child producer owns the request lifetime; its caller
// supplies the same Session feed observer used by ordinary Turn approvals.
func (g *Gateway) RequestChildApproval(ctx context.Context, req agent.ApprovalRequest, observer TurnEventObserver) (agent.ApprovalResponse, error) {
	if !detachedApprovalRequest(&req) {
		return agent.ApprovalResponse{}, errors.New("gateway: child approval requires a child origin")
	}
	if req.TurnID == "" {
		req.TurnID = newParticipantTurnID()
	}
	handle := newTurnHandle(turnHandleConfig{
		ctx: ctx, sessionRef: req.SessionRef, createdAt: g.clock(),
		handleID: g.allocateID("child-approval"), runID: g.allocateID("child-approval-run"), turnID: req.TurnID,
		observer: observer, approvals: g.sessionApprovals(req.SessionRef),
		persistApproval: g.approvalPersister(req.SessionRef, req.TurnID),
		settleApproval:  g.approvalSettler(req.SessionRef, req.TurnID),
	})
	// This handle owns only queue publication, so it emits no parent Turn
	// lifecycle. resolveApprovalRequest releases its queue entry before return.
	return g.resolveApprovalRequest(ctx, ctx, handle, &req, nil)
}
