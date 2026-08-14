package kernel

import (
	"context"
	"fmt"
	"strings"
	"sync"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// pendingApproval belongs to one Session-scoped Control approval plane. owner
// supplies origin and live Turn publication only; it does not own the FIFO.
type pendingApproval struct {
	id                eventstream.ApprovalRequestID
	decisions         chan ApprovalDecision
	done              chan struct{}
	activated         chan struct{}
	request           *agent.ApprovalRequest
	owner             *turnHandle
	detached          bool
	publishOnActivate bool
	parallelGroup     string
	parallelDone      <-chan struct{}
	activationErr     error
	persisted         bool
	settled           bool
}

// approvalCoordinator owns one Session's approval registry and FIFO across
// main, participant, Side ACP, and detached child lifetimes. Manual and legacy
// requests have one active head; same-model-step auto reviews may form one
// active cohort at that head.
type approvalCoordinator struct {
	sessionRef session.SessionRef

	mu      sync.Mutex
	pending map[eventstream.ApprovalRequestID]*pendingApproval
	settled map[eventstream.ApprovalRequestID]string
	queue   []*pendingApproval
	active  []*pendingApproval
	// activeParallelGroup is non-empty only while one same-model-step
	// auto-review cohort is active. Manual and ungrouped approvals remain
	// single-head FIFO entries.
	activeParallelGroup string
	activeParallelDone  <-chan struct{}
	activeParallelOwner *turnHandle
	requests            uint64
}

func newApprovalCoordinator(ref session.SessionRef) *approvalCoordinator {
	return &approvalCoordinator{
		sessionRef: session.NormalizeSessionRef(ref),
		pending:    map[eventstream.ApprovalRequestID]*pendingApproval{},
		settled:    map[eventstream.ApprovalRequestID]string{},
	}
}

func (c *approvalCoordinator) enqueue(owner *turnHandle, req *agent.ApprovalRequest, publishOnActivate bool) (*pendingApproval, error) {
	if c == nil {
		return nil, approvalUnavailableError()
	}
	detached := detachedApprovalRequest(req)
	if owner != nil && owner.isTerminal() && !detached {
		return nil, approvalUnavailableError()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	requestID := approvalRequestIDForRuntimeRequest(req)
	if requestID == "" {
		c.requests++
		prefix := strings.TrimSpace(c.sessionRef.SessionID)
		if prefix == "" {
			prefix = "session"
		}
		requestID = eventstream.ApprovalRequestID(fmt.Sprintf("%s-approval-%06d", prefix, c.requests))
	}
	if _, exists := c.pending[requestID]; exists {
		return nil, &Error{
			Kind: KindConflict, Code: CodeApprovalNotPending, UserVisible: true,
			Message: "gateway: approval request is already pending",
		}
	}
	if state := strings.TrimSpace(c.settled[requestID]); state != "" {
		return nil, &Error{
			Kind: KindConflict, Code: CodeApprovalNotPending, UserVisible: true,
			Message: "gateway: approval request is no longer pending",
			Detail:  string(requestID) + ": " + state,
		}
	}
	pending := &pendingApproval{
		id: requestID, decisions: make(chan ApprovalDecision, 1), done: make(chan struct{}),
		activated: make(chan struct{}), request: req, owner: owner, detached: detached,
		publishOnActivate: publishOnActivate,
	}
	if !publishOnActivate {
		pending.parallelGroup, pending.parallelDone = autoApprovalParallelGroup(req)
	}
	c.pending[requestID] = pending
	c.queue = append(c.queue, pending)
	if req != nil {
		req.ModelStep.MarkAdmissionComplete()
	}
	c.activateNextLocked()
	if pending.activationErr != nil {
		return nil, pending.activationErr
	}
	return pending, nil
}

func detachedApprovalRequest(req *agent.ApprovalRequest) bool {
	if req == nil {
		return false
	}
	origin := canonicalOriginFromApproval(req, req.SessionRef, req.TurnID)
	return origin != nil && origin.Scope == EventScopeSubagent
}

func approvalUnavailableError() error {
	return &Error{
		Kind: KindConflict, Code: CodeApprovalNotPending, UserVisible: true,
		Message: "gateway: session approval coordinator is unavailable",
	}
}

func approvalRequestIDForRuntimeRequest(req *agent.ApprovalRequest) eventstream.ApprovalRequestID {
	if req == nil {
		return ""
	}
	return eventstream.ApprovalRequestID(strings.TrimSpace(req.PauseTokenID))
}

func (c *approvalCoordinator) submit(ctx context.Context, decision ApprovalDecision) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	requestID := eventstream.ApprovalRequestID(strings.TrimSpace(string(decision.RequestID)))
	if requestID == "" {
		return &Error{
			Kind: KindValidation, Code: CodeInvalidRequest, UserVisible: true,
			Message: "gateway: approval request id is required",
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending[requestID]
	if pending == nil {
		return c.approvalNotPendingErrorLocked(requestID)
	}
	return c.resolveLocked(pending, decision)
}

func (c *approvalCoordinator) resolve(pending *pendingApproval, decision ApprovalDecision) error {
	if c == nil || pending == nil {
		return approvalUnavailableError()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolveLocked(pending, decision)
}

func (c *approvalCoordinator) resolveLocked(pending *pendingApproval, decision ApprovalDecision) error {
	if c.pending[pending.id] != pending {
		return c.approvalNotPendingErrorLocked(pending.id)
	}
	if !c.isActiveLocked(pending) {
		return c.approvalNotActiveErrorLocked(pending.id)
	}
	if err := c.settleLocked(pending, "resolved"); err != nil {
		return err
	}
	decision.RequestID = pending.id
	pending.decisions <- decision
	c.removeLocked(pending, "resolved")
	c.activateNextLocked()
	return nil
}

func (c *approvalCoordinator) release(pending *pendingApproval, state string) {
	if c == nil || pending == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending[pending.id] != pending {
		return
	}
	_ = c.settleLocked(pending, state)
	c.removeLocked(pending, state)
	close(pending.done)
	c.activateNextLocked()
}

func (c *approvalCoordinator) abandonOwner(owner *turnHandle, state string) {
	if c == nil || owner == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, pending := range append([]*pendingApproval(nil), c.queue...) {
		if pending == nil || pending.owner != owner || pending.detached || c.pending[pending.id] != pending {
			continue
		}
		_ = c.settleLocked(pending, state)
		c.removeLocked(pending, state)
		close(pending.done)
	}
	if c.activeParallelOwner == owner {
		c.activeParallelGroup = ""
		c.activeParallelDone = nil
		c.activeParallelOwner = nil
	}
	c.activateNextLocked()
}

func (c *approvalCoordinator) clear(state string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, pending := range c.pending {
		_ = c.settleLocked(pending, state)
		delete(c.pending, id)
		c.settled[id] = strings.TrimSpace(state)
		close(pending.done)
	}
	clear(c.queue)
	c.queue = nil
	c.active = nil
	c.activeParallelGroup = ""
	c.activeParallelDone = nil
	c.activeParallelOwner = nil
}

func (c *approvalCoordinator) removeLocked(pending *pendingApproval, state string) {
	if pending == nil {
		return
	}
	delete(c.pending, pending.id)
	c.settled[pending.id] = strings.TrimSpace(state)
	for index, queued := range c.queue {
		if queued != pending {
			continue
		}
		copy(c.queue[index:], c.queue[index+1:])
		c.queue[len(c.queue)-1] = nil
		c.queue = c.queue[:len(c.queue)-1]
		break
	}
	for index, active := range c.active {
		if active != pending {
			continue
		}
		copy(c.active[index:], c.active[index+1:])
		c.active[len(c.active)-1] = nil
		c.active = c.active[:len(c.active)-1]
		break
	}
}

func (c *approvalCoordinator) activateNextLocked() {
	for {
		if c.activeParallelGroup != "" {
			pending := c.nextPendingLocked(c.activeParallelGroup)
			if pending == nil {
				if len(c.active) > 0 || !channelClosed(c.activeParallelDone) {
					return
				}
				c.activeParallelGroup = ""
				c.activeParallelDone = nil
				c.activeParallelOwner = nil
				continue
			}
			if !c.activateLocked(pending) {
				continue
			}
			continue
		}
		if len(c.active) > 0 {
			return
		}
		pending := c.nextPendingLocked("")
		if pending == nil {
			return
		}
		if pending.parallelGroup != "" {
			c.activeParallelGroup = pending.parallelGroup
			c.activeParallelDone = pending.parallelDone
			c.activeParallelOwner = pending.owner
			c.watchParallelAdmissionLocked(pending.parallelGroup, pending.parallelDone)
		}
		if !c.activateLocked(pending) {
			continue
		}
		if c.activeParallelGroup == "" {
			return
		}
	}
}

func (c *approvalCoordinator) nextPendingLocked(group string) *pendingApproval {
	for _, candidate := range c.queue {
		if candidate == nil || c.pending[candidate.id] != candidate || c.isActiveLocked(candidate) {
			continue
		}
		if group != "" && candidate.parallelGroup != group {
			continue
		}
		return candidate
	}
	return nil
}

func (c *approvalCoordinator) activateLocked(pending *pendingApproval) bool {
	if pending == nil {
		return false
	}
	c.active = append(c.active, pending)
	if pending.publishOnActivate {
		owner := pending.owner
		if owner == nil || owner.persistApproval == nil {
			pending.activationErr = fmt.Errorf("gateway: durable approval persistence is unavailable")
			c.removeLocked(pending, "persistence_failed")
			close(pending.done)
			return false
		}
		event, err := owner.persistApproval(pending.request, pending.id)
		if err == nil {
			err = validatePersistedApprovalEvent(event)
		}
		if err != nil {
			pending.activationErr = err
			c.removeLocked(pending, "persistence_failed")
			close(pending.done)
			return false
		}
		owner.publishEnvelopes(projectSessionACPEvent(owner.sessionRef, event, owner.handleID, owner.runID, owner.turnID), "")
		pending.persisted = true
	}
	close(pending.activated)
	return true
}

func (c *approvalCoordinator) watchParallelAdmissionLocked(group string, done <-chan struct{}) {
	if strings.TrimSpace(group) == "" || done == nil {
		return
	}
	go func() {
		<-done
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.activeParallelGroup != group || c.activeParallelDone != done {
			return
		}
		c.activateNextLocked()
	}()
}

func channelClosed(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (c *approvalCoordinator) isActiveLocked(pending *pendingApproval) bool {
	if pending == nil {
		return false
	}
	for _, active := range c.active {
		if active == pending {
			return true
		}
	}
	return false
}

func autoApprovalParallelGroup(req *agent.ApprovalRequest) (string, <-chan struct{}) {
	if req == nil || req.ModelStep == nil {
		return "", nil
	}
	step := req.ModelStep
	done := step.AdmissionDone()
	stepID := strings.TrimSpace(step.ID)
	runID := strings.TrimSpace(req.RunID)
	turnID := strings.TrimSpace(req.TurnID)
	if done == nil || stepID == "" || runID == "" || turnID == "" || step.CallCount < 2 || step.Index < 0 || step.Index >= step.CallCount {
		return "", nil
	}
	return runID + "\x00" + turnID + "\x00" + stepID, done
}

func (c *approvalCoordinator) settleLocked(pending *pendingApproval, state string) error {
	if pending == nil || pending.settled || !pending.persisted || pending.owner == nil || pending.owner.settleApproval == nil {
		return nil
	}
	event, err := pending.owner.settleApproval(pending.request, pending.id, state)
	if err != nil {
		return err
	}
	if err := validatePersistedApprovalEvent(event); err != nil {
		return err
	}
	pending.settled = true
	pending.owner.publishEnvelopes(projectSessionACPEvent(
		pending.owner.sessionRef, event, pending.owner.handleID, pending.owner.runID, pending.owner.turnID,
	), "")
	return nil
}

func validatePersistedApprovalEvent(event *session.Event) error {
	if event == nil {
		return fmt.Errorf("gateway: durable approval persistence returned no event")
	}
	if strings.TrimSpace(event.ID) == "" || event.Seq == 0 {
		return fmt.Errorf("gateway: durable approval persistence returned an event without a durable position")
	}
	return nil
}

func (c *approvalCoordinator) approvalNotPendingErrorLocked(requestID eventstream.ApprovalRequestID) error {
	detail := "unknown approval request"
	if state := strings.TrimSpace(c.settled[requestID]); state != "" {
		detail = "approval request is no longer pending: " + state
	}
	return &Error{
		Kind: KindConflict, Code: CodeApprovalNotPending, UserVisible: true,
		Message: "gateway: approval request is not pending",
		Detail:  string(requestID) + ": " + detail,
	}
}

func (c *approvalCoordinator) approvalNotActiveErrorLocked(requestID eventstream.ApprovalRequestID) error {
	activeID := ""
	if len(c.active) > 0 {
		activeID = string(c.active[0].id)
	}
	return &Error{
		Kind: KindConflict, Code: CodeApprovalNotActive, UserVisible: true,
		Message: "gateway: approval request is not in the active queue head cohort",
		Detail:  string(requestID) + ": active approval request is " + activeID,
	}
}

func (c *approvalCoordinator) snapshot() (*pendingApproval, int) {
	if c == nil {
		return nil, 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var active *pendingApproval
	if len(c.active) > 0 {
		active = c.active[0]
	}
	queued := 0
	for _, pending := range c.queue {
		if pending != nil && c.pending[pending.id] == pending && !c.isActiveLocked(pending) {
			queued++
		}
	}
	return active, queued
}

func (c *approvalCoordinator) queueSnapshot() []*pendingApproval {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*pendingApproval(nil), c.queue...)
}

func (c *approvalCoordinator) target(requestID eventstream.ApprovalRequestID) (ActiveTurnState, bool) {
	if c == nil || strings.TrimSpace(string(requestID)) == "" {
		return ActiveTurnState{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.pending[requestID]
	if pending == nil || pending.owner == nil {
		return ActiveTurnState{}, false
	}
	owner := pending.owner
	return ActiveTurnState{
		SessionRef: owner.SessionRef(), Kind: owner.ActiveKind(), ParticipantID: owner.ParticipantID(),
		HandleID: owner.HandleID(), RunID: owner.RunID(), TurnID: owner.TurnID(), StartedAt: owner.CreatedAt(),
	}, true
}

func (h *turnHandle) openPendingApproval(req *agent.ApprovalRequest) (*pendingApproval, error) {
	return h.enqueueApproval(req, false)
}

func (h *turnHandle) enqueueApproval(req *agent.ApprovalRequest, publishOnActivate bool) (*pendingApproval, error) {
	if h == nil || h.approvals == nil {
		return nil, approvalUnavailableError()
	}
	return h.approvals.enqueue(h, req, publishOnActivate)
}

func (h *turnHandle) submitApproval(ctx context.Context, decision ApprovalDecision) error {
	if h == nil || h.approvals == nil {
		return approvalUnavailableError()
	}
	return h.approvals.submit(ctx, decision)
}

func (h *turnHandle) resolvePendingApproval(pending *pendingApproval, decision ApprovalDecision) error {
	if h == nil || h.approvals == nil {
		return approvalUnavailableError()
	}
	return h.approvals.resolve(pending, decision)
}

func (h *turnHandle) releasePendingApproval(pending *pendingApproval, state string) {
	if h != nil && h.approvals != nil {
		h.approvals.release(pending, state)
	}
}

func (h *turnHandle) publishApproval(req *agent.ApprovalRequest) (*pendingApproval, error) {
	return h.enqueueApproval(req, true)
}
