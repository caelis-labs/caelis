package kernel

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func (g *Gateway) ActiveCounts() (int, int) {
	if g == nil {
		return 0, 0
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.active), len(g.bindings)
}

func (g *Gateway) ActiveTurns() []ActiveTurnState {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]ActiveTurnState, 0, len(g.active))
	for sessionID, handle := range g.active {
		if handle == nil {
			continue
		}
		ref := handle.SessionRef()
		if strings.TrimSpace(ref.SessionID) == "" {
			ref.SessionID = strings.TrimSpace(sessionID)
		}
		out = append(out, ActiveTurnState{
			SessionRef:    ref,
			Kind:          handle.ActiveKind(),
			ParticipantID: handle.ParticipantID(),
			HandleID:      handle.HandleID(),
			RunID:         handle.RunID(),
			TurnID:        handle.TurnID(),
			StartedAt:     handle.CreatedAt(),
		})
	}
	return out
}

func (g *Gateway) ActiveTurn(sessionID string) (ActiveTurnState, bool) {
	if g == nil {
		return ActiveTurnState{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ActiveTurnState{}, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	handle := g.active[sessionID]
	if handle == nil {
		return ActiveTurnState{}, false
	}
	ref := handle.SessionRef()
	if strings.TrimSpace(ref.SessionID) == "" {
		ref.SessionID = sessionID
	}
	return ActiveTurnState{
		SessionRef:    ref,
		Kind:          handle.ActiveKind(),
		ParticipantID: handle.ParticipantID(),
		HandleID:      handle.HandleID(),
		RunID:         handle.RunID(),
		TurnID:        handle.TurnID(),
		StartedAt:     handle.CreatedAt(),
	}, true
}

// ApprovalTarget returns the exact Turn identity that owns one pending
// Session-scoped approval, including approvals whose detached child outlived
// the active parent Turn.
func (g *Gateway) ApprovalTarget(sessionID string, requestID eventstream.ApprovalRequestID) (ActiveTurnState, bool) {
	if g == nil {
		return ActiveTurnState{}, false
	}
	g.mu.Lock()
	coordinator := g.approvals[strings.TrimSpace(sessionID)]
	g.mu.Unlock()
	if coordinator == nil {
		return ActiveTurnState{}, false
	}
	return coordinator.target(requestID)
}

// CloseSessionApprovals releases every waiter owned by a semantically closed
// Session and removes its coordinator registry entry.
func (g *Gateway) CloseSessionApprovals(ref session.SessionRef, reason string) {
	if g == nil {
		return
	}
	sessionID := strings.TrimSpace(ref.SessionID)
	g.mu.Lock()
	coordinator := g.approvals[sessionID]
	delete(g.approvals, sessionID)
	g.mu.Unlock()
	if coordinator != nil {
		coordinator.clear(reason)
	}
}

func (g *Gateway) SubmitActiveTurn(ctx context.Context, req SubmitActiveTurnRequest) error {
	if g == nil {
		return &Error{
			Kind:        KindInternal,
			Code:        CodeInternal,
			UserVisible: true,
			Message:     "gateway: gateway is not configured",
		}
	}
	ref := session.NormalizeSessionRef(req.SessionRef)
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: session id is required for active turn submission",
		}
	}
	g.mu.Lock()
	handle := g.active[sessionID]
	coordinator := g.approvals[sessionID]
	g.mu.Unlock()
	if handle == nil {
		if req.Kind == SubmissionKindApproval && req.Approval != nil && coordinator != nil {
			return coordinator.submit(ctx, *req.Approval)
		}
		return &Error{
			Kind:        KindConflict,
			Code:        CodeNoActiveRun,
			UserVisible: true,
			Message:     "gateway: no active run is available for this session",
		}
	}
	return handle.Submit(ctx, SubmitRequest{
		Kind:         req.Kind,
		Text:         req.Text,
		DisplayText:  req.DisplayText,
		ContentParts: append([]model.ContentPart(nil), req.ContentParts...),
		Metadata:     cloneMap(req.Metadata),
		Approval:     req.Approval,
	})
}

func (g *Gateway) CancelActiveTurns() {
	if g == nil {
		return
	}
	g.mu.Lock()
	handles := make([]*turnHandle, 0, len(g.active))
	for _, handle := range g.active {
		if handle != nil {
			handles = append(handles, handle)
		}
	}
	g.mu.Unlock()
	for _, handle := range handles {
		handle.Cancel()
	}
}
