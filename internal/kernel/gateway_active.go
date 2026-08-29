package kernel

import (
	"context"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// BlockSessionTurnAdmission temporarily prevents new main or participant
// Turns for one Session while Control reconciles an execution-affecting
// configuration mutation. Existing Turns remain visible so the caller can
// interrupt them and wait for their durable execution fence to be released.
func (g *Gateway) BlockSessionTurnAdmission(ref session.SessionRef) (func(), error) {
	if g == nil {
		return nil, &Error{Kind: KindInternal, Code: CodeInternal, UserVisible: true, Message: "gateway: gateway is not configured"}
	}
	ref = session.NormalizeSessionRef(ref)
	sessionID := strings.TrimSpace(ref.SessionID)
	if sessionID == "" {
		return nil, &Error{Kind: KindValidation, Code: CodeInvalidRequest, UserVisible: true, Message: "gateway: session id is required for a Turn admission block"}
	}
	g.mu.Lock()
	if g.quiescing {
		g.mu.Unlock()
		return nil, hostClosingError()
	}
	if g.turnAdmissionBlocks == nil {
		g.turnAdmissionBlocks = map[string]uint64{}
	}
	g.turnAdmissionBlocks[sessionID]++
	g.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			remaining := g.turnAdmissionBlocks[sessionID]
			if remaining <= 1 {
				delete(g.turnAdmissionBlocks, sessionID)
			} else {
				g.turnAdmissionBlocks[sessionID] = remaining - 1
			}
			g.mu.Unlock()
		})
	}, nil
}

func (g *Gateway) sessionTurnAdmissionBlockedLocked(sessionID string) bool {
	return g != nil && g.turnAdmissionBlocks[strings.TrimSpace(sessionID)] > 0
}

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

// WaitActiveTurnChange waits until the exact active Turn is released or
// replaced. Callers use it only after a proven-no-effect submission failure,
// so they can reselect between the next active Turn and a new idle Turn
// without spinning on a handle whose Runtime runner is already closed.
func (g *Gateway) WaitActiveTurnChange(ctx context.Context, expected ActiveTurnState) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(expected.SessionRef.SessionID)
	if sessionID == "" {
		return nil
	}
	for {
		g.mu.Lock()
		current := g.active[sessionID]
		if current == nil || current.HandleID() != expected.HandleID ||
			current.RunID() != expected.RunID || current.TurnID() != expected.TurnID {
			g.mu.Unlock()
			return nil
		}
		changed := g.activeChanged
		g.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// CancelActiveTurnAndWait cancels the exact active Turn when it is still
// present, then waits until its producer releases the durable execution fence.
// A Turn that was already cancelled is still waited out; callers that hold a
// Session admission block can therefore reconcile execution-affecting state
// without racing a draining producer or a replacement Turn.
func (g *Gateway) CancelActiveTurnAndWait(ctx context.Context, expected ActiveTurnState) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(expected.SessionRef.SessionID)
	if sessionID == "" {
		return &Error{
			Kind: KindValidation, Code: CodeInvalidRequest, UserVisible: true,
			Message: "gateway: session id is required to cancel an active Turn",
		}
	}
	g.mu.Lock()
	handle := g.active[sessionID]
	if handle == nil {
		g.mu.Unlock()
		return nil
	}
	if handle.HandleID() != expected.HandleID || handle.RunID() != expected.RunID ||
		handle.TurnID() != expected.TurnID || handle.ActiveKind() != expected.Kind ||
		handle.ParticipantID() != expected.ParticipantID {
		g.mu.Unlock()
		return &Error{
			Kind: KindConflict, Code: CodeActiveRunConflict, UserVisible: true,
			Message: "gateway: active run does not match cancellation target",
		}
	}
	g.mu.Unlock()

	handle.Cancel()
	return g.WaitActiveTurnChange(ctx, expected)
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
	for _, candidate := range []any{g.approvalApprover, g.approvalReviewer} {
		if releaser, ok := candidate.(interface{ ReleaseApprovalContext(session.SessionRef) }); ok {
			releaser.ReleaseApprovalContext(ref)
		}
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
	if handle != nil && !submissionMatchesHandle(req, handle) {
		g.mu.Unlock()
		return &Error{
			Kind: KindConflict, Code: CodeActiveRunConflict, UserVisible: true,
			Message: "gateway: active run does not match submission target",
		}
	}
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
		Actor:        session.CloneActorRef(req.Actor),
		Approval:     req.Approval,
	})
}

func submissionMatchesHandle(req SubmitActiveTurnRequest, handle *turnHandle) bool {
	if handle == nil {
		return false
	}
	if value := strings.TrimSpace(req.HandleID); value != "" && value != handle.HandleID() {
		return false
	}
	if value := strings.TrimSpace(req.RunID); value != "" && value != handle.RunID() {
		return false
	}
	if value := strings.TrimSpace(req.TurnID); value != "" && value != handle.TurnID() {
		return false
	}
	return true
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

// Quiesce permanently closes Turn admission, cancels every active producer,
// and waits until all producer goroutines have released their active handles.
func (g *Gateway) Quiesce(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		g.mu.Lock()
		g.quiescing = true
		handles := make([]*turnHandle, 0, len(g.active))
		for _, handle := range g.active {
			if handle != nil {
				handles = append(handles, handle)
			}
		}
		if len(handles) == 0 {
			g.mu.Unlock()
			return nil
		}
		changed := g.activeChanged
		g.mu.Unlock()

		for _, handle := range handles {
			handle.Cancel()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func hostClosingError() *Error {
	return &Error{
		Kind:        KindUnavailable,
		Code:        CodeHostClosing,
		Retryable:   true,
		UserVisible: true,
		Message:     "gateway: host is closing",
	}
}
