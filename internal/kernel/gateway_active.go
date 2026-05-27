package kernel

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
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
			SessionRef: ref,
			Kind:       handle.ActiveKind(),
			HandleID:   handle.HandleID(),
			RunID:      handle.RunID(),
			TurnID:     handle.TurnID(),
			StartedAt:  handle.CreatedAt(),
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
		SessionRef: ref,
		Kind:       handle.ActiveKind(),
		HandleID:   handle.HandleID(),
		RunID:      handle.RunID(),
		TurnID:     handle.TurnID(),
		StartedAt:  handle.CreatedAt(),
	}, true
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
	g.mu.Unlock()
	if handle == nil {
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
