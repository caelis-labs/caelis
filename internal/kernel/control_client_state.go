package kernel

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// ControlClientRuntimeState returns the live handle and approval FIFO state
// used by reconnect bootstrap. Durable Session state is read separately so the
// bootstrap service can enforce one revision/boundary transaction.
func (g *Gateway) ControlClientRuntimeState(ctx context.Context, ref session.SessionRef) (controlclient.RuntimeState, error) {
	if g == nil {
		return controlclient.RuntimeState{}, errors.New("gateway: gateway is unavailable")
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return controlclient.RuntimeState{}, session.ErrInvalidSession
	}
	runState, err := g.runtime.RunState(ctx, ref)
	if err != nil && !errors.Is(err, session.ErrSessionNotFound) {
		return controlclient.RuntimeState{}, err
	}
	g.mu.Lock()
	handle := g.active[ref.SessionID]
	coordinator := g.approvals[ref.SessionID]
	g.mu.Unlock()
	if handle == nil {
		out := controlclient.RuntimeState{Run: controlclient.RunState{
			Status: strings.TrimSpace(string(runState.Status)), WaitingApproval: runState.WaitingApproval,
			RunID: strings.TrimSpace(runState.ActiveRunID),
		}}
		applyControlApprovalState(&out, coordinator, ref, "")
		return out, nil
	}
	return handle.controlClientRuntimeState(strings.TrimSpace(string(runState.Status)), runState.WaitingApproval), nil
}

func (h *turnHandle) controlClientRuntimeState(status string, waitingApproval bool) controlclient.RuntimeState {
	if h == nil {
		return controlclient.RuntimeState{}
	}
	h.mu.Lock()
	out := controlclient.RuntimeState{Run: controlclient.RunState{
		Status: status, WaitingApproval: waitingApproval,
		Active: !h.finished && !h.closed, Kind: string(h.activeKind),
		HandleID: h.handleID, RunID: h.runID, TurnID: h.turnID, StartedAt: h.createdAt,
	}}
	ref := h.sessionRef
	turnID := h.turnID
	coordinator := h.approvals
	h.mu.Unlock()
	applyControlApprovalState(&out, coordinator, ref, turnID)
	return out
}

func applyControlApprovalState(out *controlclient.RuntimeState, coordinator *approvalCoordinator, ref session.SessionRef, fallbackTurnID string) {
	if out == nil {
		return
	}
	active, queued := coordinator.snapshot()
	out.Approval.QueuedCount = queued
	out.Run.WaitingApproval = out.Run.WaitingApproval || active != nil
	if active != nil {
		payload := approval.PayloadFromRuntimeRequest(*active.request)
		origin := canonicalOriginFromApproval(active.request, ref, fallbackTurnID)
		item := &controlclient.ActiveApproval{
			RequestID: active.id, Scope: eventstream.ScopeMain, ScopeID: ref.SessionID,
			Permission: approval.ProtocolApprovalFromPayload(payload),
		}
		if origin != nil {
			item.Scope = eventstream.Scope(origin.Scope)
			item.ScopeID = firstNonEmpty(strings.TrimSpace(origin.ScopeID), item.ScopeID)
			item.ParticipantID = strings.TrimSpace(origin.ParticipantID)
			item.ParentTool = approvalParentToolRelation(active.request)
		}
		out.Approval.Active = item
	}
}
