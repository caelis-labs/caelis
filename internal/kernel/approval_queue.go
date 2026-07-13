package kernel

import (
	"context"
	"fmt"
	"strings"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// pendingApproval belongs to one live Turn request. The Turn owns its queue and
// exposes exactly one active head at a time. Its done channel is closed only
// when the owning Turn abandons the request; it is never used to inject a
// zero-value fallback decision into a waiter.
type pendingApproval struct {
	id                eventstream.ApprovalRequestID
	decisions         chan ApprovalDecision
	done              chan struct{}
	activated         chan struct{}
	request           *agent.ApprovalRequest
	publishOnActivate bool
}

// openPendingApproval registers one approval without publishing an ACP prompt.
// It exists for internal callers and tests that need a live resolver without a
// Surface-visible prompt; production approval routing uses enqueueApproval.
func (h *turnHandle) openPendingApproval(req *agent.ApprovalRequest) (*pendingApproval, error) {
	return h.enqueueApproval(req, false)
}

// enqueueApproval registers an exact waiter before making a request eligible
// for resolution. When publishOnActivate is true, this Control-owned queue
// publishes the standard ACP permission only after the request reaches its
// single active head.
func (h *turnHandle) enqueueApproval(req *agent.ApprovalRequest, publishOnActivate bool) (*pendingApproval, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cancelled || h.closed || h.finished || h.finishing {
		return nil, &Error{
			Kind:        KindConflict,
			Code:        CodeApprovalNotPending,
			UserVisible: true,
			Message:     "gateway: turn is not accepting approval requests",
		}
	}
	requestID := approvalRequestIDForRuntimeRequest(req)
	if requestID == "" {
		h.approvalRequestSeq++
		prefix := strings.TrimSpace(h.handleID)
		if prefix == "" {
			prefix = "turn"
		}
		requestID = eventstream.ApprovalRequestID(fmt.Sprintf("%s-approval-%06d", prefix, h.approvalRequestSeq))
	}
	if _, exists := h.pendingApprovals[requestID]; exists {
		return nil, &Error{
			Kind:        KindConflict,
			Code:        CodeApprovalNotPending,
			UserVisible: true,
			Message:     "gateway: approval request is already pending",
			Detail:      string(requestID),
		}
	}
	if state := strings.TrimSpace(h.settledApprovals[requestID]); state != "" {
		return nil, &Error{
			Kind:        KindConflict,
			Code:        CodeApprovalNotPending,
			UserVisible: true,
			Message:     "gateway: approval request is no longer pending",
			Detail:      string(requestID) + ": " + state,
		}
	}
	pending := &pendingApproval{
		id:                requestID,
		decisions:         make(chan ApprovalDecision, 1),
		done:              make(chan struct{}),
		activated:         make(chan struct{}),
		request:           req,
		publishOnActivate: publishOnActivate,
	}
	h.pendingApprovals[requestID] = pending
	h.approvalQueue = append(h.approvalQueue, pending)
	h.activateNextApprovalLocked()
	return pending, nil
}

func approvalRequestIDForRuntimeRequest(req *agent.ApprovalRequest) eventstream.ApprovalRequestID {
	if req == nil {
		return ""
	}
	return eventstream.ApprovalRequestID(strings.TrimSpace(req.PauseTokenID))
}

func (h *turnHandle) submitApproval(ctx context.Context, decision ApprovalDecision) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	requestID := eventstream.ApprovalRequestID(strings.TrimSpace(string(decision.RequestID)))
	if requestID == "" {
		return &Error{
			Kind:        KindValidation,
			Code:        CodeInvalidRequest,
			UserVisible: true,
			Message:     "gateway: approval request id is required",
		}
	}
	h.mu.Lock()
	pending := h.pendingApprovals[requestID]
	if pending == nil {
		err := h.approvalNotPendingErrorLocked(requestID)
		h.mu.Unlock()
		return err
	}
	err := h.resolvePendingApprovalLocked(pending, decision)
	h.mu.Unlock()
	return err
}

// resolvePendingApproval resolves one active request from a Control-owned
// resolver such as Guardian/auto-review. User submissions take the same path
// through submitApproval.
func (h *turnHandle) resolvePendingApproval(pending *pendingApproval, decision ApprovalDecision) error {
	if h == nil || pending == nil {
		return &Error{
			Kind:        KindConflict,
			Code:        CodeApprovalNotPending,
			UserVisible: true,
			Message:     "gateway: approval request is not pending",
		}
	}
	h.mu.Lock()
	err := h.resolvePendingApprovalLocked(pending, decision)
	h.mu.Unlock()
	return err
}

func (h *turnHandle) resolvePendingApprovalLocked(pending *pendingApproval, decision ApprovalDecision) error {
	if h.pendingApprovals[pending.id] != pending {
		return h.approvalNotPendingErrorLocked(pending.id)
	}
	if h.activeApproval != pending {
		return h.approvalNotActiveErrorLocked(pending.id)
	}
	decision.RequestID = pending.id
	// The per-request channel is freshly allocated with one slot. Sending before
	// advancing the queue cannot block and preserves the resolver-before-next-
	// prompt order without ever waking a different waiter.
	pending.decisions <- decision
	h.removePendingApprovalLocked(pending, "resolved")
	h.activateNextApprovalLocked()
	return nil
}

func (h *turnHandle) releasePendingApproval(pending *pendingApproval, state string) {
	if h == nil || pending == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pendingApprovals[pending.id] != pending {
		return
	}
	h.removePendingApprovalLocked(pending, state)
	close(pending.done)
	h.activateNextApprovalLocked()
}

func (h *turnHandle) clearPendingApprovalsLocked(state string) {
	for id, pending := range h.pendingApprovals {
		delete(h.pendingApprovals, id)
		h.settledApprovals[id] = strings.TrimSpace(state)
		close(pending.done)
	}
	clear(h.approvalQueue)
	h.approvalQueue = nil
	h.activeApproval = nil
}

func (h *turnHandle) removePendingApprovalLocked(pending *pendingApproval, state string) {
	if pending == nil {
		return
	}
	delete(h.pendingApprovals, pending.id)
	h.settledApprovals[pending.id] = strings.TrimSpace(state)
	for index, queued := range h.approvalQueue {
		if queued != pending {
			continue
		}
		copy(h.approvalQueue[index:], h.approvalQueue[index+1:])
		h.approvalQueue[len(h.approvalQueue)-1] = nil
		h.approvalQueue = h.approvalQueue[:len(h.approvalQueue)-1]
		break
	}
	if h.activeApproval == pending {
		h.activeApproval = nil
	}
}

func (h *turnHandle) activateNextApprovalLocked() {
	if h.activeApproval != nil {
		return
	}
	for len(h.approvalQueue) > 0 {
		pending := h.approvalQueue[0]
		if pending == nil || h.pendingApprovals[pending.id] != pending {
			h.approvalQueue = h.approvalQueue[1:]
			continue
		}
		h.activeApproval = pending
		close(pending.activated)
		if pending.publishOnActivate {
			h.publishApprovalPayloadLocked(pending.request, canonicalApprovalPayload(pending.request), pending.id)
		}
		return
	}
}

func (h *turnHandle) approvalNotPendingErrorLocked(requestID eventstream.ApprovalRequestID) error {
	detail := "unknown approval request"
	if state := strings.TrimSpace(h.settledApprovals[requestID]); state != "" {
		detail = "approval request is no longer pending: " + state
	}
	return &Error{
		Kind:        KindConflict,
		Code:        CodeApprovalNotPending,
		UserVisible: true,
		Message:     "gateway: approval request is not pending",
		Detail:      string(requestID) + ": " + detail,
	}
}

func (h *turnHandle) approvalNotActiveErrorLocked(requestID eventstream.ApprovalRequestID) error {
	activeID := ""
	if h.activeApproval != nil {
		activeID = string(h.activeApproval.id)
	}
	return &Error{
		Kind:        KindConflict,
		Code:        CodeApprovalNotActive,
		UserVisible: true,
		Message:     "gateway: approval request is not the active queue head",
		Detail:      string(requestID) + ": active approval request is " + activeID,
	}
}

func (h *turnHandle) publishApproval(req *agent.ApprovalRequest) (*pendingApproval, error) {
	return h.enqueueApproval(req, true)
}

func (h *turnHandle) publishApprovalPayloadLocked(req *agent.ApprovalRequest, payload *ApprovalPayload, requestID eventstream.ApprovalRequestID) {
	h.publishEnvelopesLocked(h.approvalEventEnvelopes(req, payload, EventKindApprovalRequested, nil, nil, requestID), "")
}
