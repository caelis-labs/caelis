package kernel

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

type turnHandleConfig struct {
	ctx                     context.Context
	handleID                string
	runID                   string
	turnID                  string
	activeKind              ActiveTurnKind
	participantID           string
	sessionRef              session.SessionRef
	createdAt               time.Time
	cancel                  func() bool
	allowPendingSubmissions bool
	waitForRunnerSubmission bool
	prepareSubmission       func(context.Context, SubmitRequest) (SubmitRequest, error)
	persistApproval         func(*agent.ApprovalRequest, eventstream.ApprovalRequestID) (*session.Event, error)
	settleApproval          func(*agent.ApprovalRequest, eventstream.ApprovalRequestID, string) (*session.Event, error)
	approvals               *approvalCoordinator
	observer                TurnEventObserver
}

type turnHandle struct {
	handleID      string
	runID         string
	turnID        string
	activeKind    ActiveTurnKind
	participantID string
	sessionRef    session.SessionRef
	createdAt     time.Time
	cancelFn      func() bool
	ctx           context.Context
	observer      TurnEventObserver
	observerMu    sync.Mutex

	mu                      sync.Mutex
	closed                  bool
	finishing               bool
	finished                bool
	failed                  bool
	cancelled               bool
	failureReason           string
	producerTerminal        *eventstream.Envelope
	runner                  agent.Runner
	pendingSubmissions      []SubmitRequest
	allowPendingSubmissions bool
	waitForRunnerSubmission bool
	runnerReady             chan struct{}
	runnerReadyOnce         sync.Once
	prepareSubmission       func(context.Context, SubmitRequest) (SubmitRequest, error)
	persistApproval         func(*agent.ApprovalRequest, eventstream.ApprovalRequestID) (*session.Event, error)
	settleApproval          func(*agent.ApprovalRequest, eventstream.ApprovalRequestID, string) (*session.Event, error)
	approvals               *approvalCoordinator
	finishHooks             []func()
	done                    chan struct{}
	doneOnce                sync.Once

	approvalReviewSeq uint64
	acpCursorSeq      uint64
}

func newTurnHandle(cfg turnHandleConfig) *turnHandle {
	if cfg.ctx == nil {
		cfg.ctx = context.Background()
	}
	h := &turnHandle{
		handleID:                cfg.handleID,
		runID:                   cfg.runID,
		turnID:                  cfg.turnID,
		activeKind:              cfg.activeKind,
		participantID:           strings.TrimSpace(cfg.participantID),
		sessionRef:              cfg.sessionRef,
		createdAt:               cfg.createdAt,
		cancelFn:                cfg.cancel,
		ctx:                     cfg.ctx,
		observer:                cfg.observer,
		allowPendingSubmissions: cfg.allowPendingSubmissions,
		waitForRunnerSubmission: cfg.waitForRunnerSubmission,
		prepareSubmission:       cfg.prepareSubmission,
		persistApproval:         cfg.persistApproval,
		settleApproval:          cfg.settleApproval,
		approvals:               cfg.approvals,
		runnerReady:             make(chan struct{}),
		done:                    make(chan struct{}),
	}
	if h.approvals == nil {
		h.approvals = newApprovalCoordinator(cfg.sessionRef)
	}
	return h
}

func (h *turnHandle) HandleID() string               { return h.handleID }
func (h *turnHandle) RunID() string                  { return h.runID }
func (h *turnHandle) TurnID() string                 { return h.turnID }
func (h *turnHandle) ActiveKind() ActiveTurnKind     { return h.activeKind }
func (h *turnHandle) ParticipantID() string          { return h.participantID }
func (h *turnHandle) SessionRef() session.SessionRef { return h.sessionRef }
func (h *turnHandle) CreatedAt() time.Time           { return h.createdAt }
func (h *turnHandle) WaitCompletion(ctx context.Context) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-h.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *turnHandle) Submit(ctx context.Context, req SubmitRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateSubmitRequest(req); err != nil {
		return err
	}
	if req.Kind == SubmissionKindApproval && req.Approval != nil {
		return h.submitApproval(ctx, *req.Approval)
	}
	if h.prepareSubmission != nil {
		prepared, err := h.prepareSubmission(ctx, req)
		if err != nil {
			return err
		}
		req = prepared
	}

	for {
		h.mu.Lock()
		runner := h.runner
		cancelled := h.cancelled
		finished := h.finished || h.finishing
		waitForRunner := h.waitForRunnerSubmission && runner == nil && !cancelled && !finished
		ready := h.runnerReady
		if err := ctx.Err(); err != nil {
			h.mu.Unlock()
			return err
		}
		if cancelled {
			h.mu.Unlock()
			return context.Canceled
		}
		if runner == nil && h.allowPendingSubmissions && !waitForRunner && !finished {
			h.pendingSubmissions = append(h.pendingSubmissions, cloneSubmitRequest(req))
			h.mu.Unlock()
			return nil
		}
		h.mu.Unlock()
		if runner != nil {
			submission := runnerSubmissionFromSubmitRequest(req)
			if contextual, ok := runner.(agent.ContextSubmissionRunner); ok {
				return contextual.SubmitContext(ctx, submission)
			}
			return runner.Submit(submission)
		}
		if waitForRunner {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-ready:
				continue
			}
		}
		return &Error{
			Kind:        KindUnsupported,
			Code:        CodeSubmissionUnsupported,
			UserVisible: true,
			Message:     "gateway: submission is not available for this handle",
		}
	}
}

func (h *turnHandle) Cancel() agent.CancelResult {
	h.mu.Lock()
	if h.cancelled {
		h.mu.Unlock()
		return agent.CancelResult{Status: agent.CancelStatusAlreadyCancelled}
	}
	h.cancelled = true
	cancelFn := h.cancelFn
	runner := h.runner
	h.runnerReadyOnce.Do(func() { close(h.runnerReady) })
	h.mu.Unlock()
	h.approvals.abandonOwner(h, "cancelled")

	if cancelFn != nil {
		cancelFn()
	}
	result := agent.CancelResult{Status: agent.CancelStatusCancelled}
	if runner != nil {
		if runnerResult := runner.Cancel(); runnerResult.Err != nil {
			result.Err = runnerResult.Err
		}
	}
	return result
}

func (h *turnHandle) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	h.mu.Unlock()
	h.approvals.abandonOwner(h, "closed")
	return nil
}

func (h *turnHandle) isTerminal() bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancelled || h.closed || h.finished || h.finishing
}

func (h *turnHandle) setRunner(runner agent.Runner) {
	h.mu.Lock()
	cancelled := h.cancelled
	h.runner = runner
	pending := slices.Clone(h.pendingSubmissions)
	h.pendingSubmissions = nil
	h.runnerReadyOnce.Do(func() { close(h.runnerReady) })
	h.mu.Unlock()
	if cancelled && runner != nil {
		runner.Cancel()
		return
	}
	if runner == nil {
		return
	}
	for _, req := range pending {
		if err := runner.Submit(runnerSubmissionFromSubmitRequest(req)); err != nil {
			h.publishError(err)
		}
	}
}

func (h *turnHandle) onFinish(fn func()) {
	if fn == nil {
		return
	}
	h.mu.Lock()
	if h.finished || h.finishing {
		h.mu.Unlock()
		fn()
		return
	}
	h.finishHooks = append(h.finishHooks, fn)
	h.mu.Unlock()
}

func (h *turnHandle) didFail() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.failed
}

func cloneSubmitRequest(req SubmitRequest) SubmitRequest {
	out := SubmitRequest{
		Kind:         req.Kind,
		Text:         req.Text,
		DisplayText:  req.DisplayText,
		ContentParts: append([]model.ContentPart(nil), req.ContentParts...),
		Metadata:     cloneMap(req.Metadata),
		Actor:        session.CloneActorRef(req.Actor),
	}
	if req.Approval != nil {
		approval := *req.Approval
		out.Approval = &approval
	}
	return out
}

func runnerSubmissionFromSubmitRequest(req SubmitRequest) agent.Submission {
	return agent.Submission{
		Kind:         req.Kind,
		Text:         req.Text,
		DisplayInput: strings.TrimSpace(req.DisplayText),
		ContentParts: append([]model.ContentPart(nil), req.ContentParts...),
		Metadata:     cloneMap(req.Metadata),
		Actor:        session.CloneActorRef(req.Actor),
	}
}

func validateSubmitRequest(req SubmitRequest) error {
	switch req.Kind {
	case SubmissionKindConversation:
		if req.Approval != nil {
			return invalidSubmissionKind(req.Kind)
		}
		return nil
	case SubmissionKindAgentCommunication:
		if req.Approval != nil {
			return invalidSubmissionKind(req.Kind)
		}
		if err := session.ValidateAgentCommunicationActor(req.Actor); err != nil {
			return invalidAgentCommunication(err)
		}
		return nil
	case SubmissionKindApproval:
		if req.Approval == nil {
			return invalidSubmissionKind(req.Kind)
		}
		return nil
	default:
		return invalidSubmissionKind(req.Kind)
	}
}

func invalidSubmissionKind(kind SubmissionKind) error {
	return &Error{
		Kind:        KindValidation,
		Code:        CodeInvalidRequest,
		UserVisible: true,
		Message:     "gateway: invalid submission kind",
		Detail:      string(kind),
	}
}

func (h *turnHandle) publishSessionEvent(event *session.Event) {
	h.publishSessionEventWithACPProjection(event, true)
}

func (h *turnHandle) publishSessionEventWithACPProjection(event *session.Event, projectACP bool) {
	if event == nil {
		return
	}
	if projectACP {
		h.publishEnvelopes(projectSessionACPEvent(h.sessionRef, event, h.handleID, h.runID, h.turnID), "")
	}
}

func (h *turnHandle) publishApprovalReviewPayload(req *agent.ApprovalRequest, payload *ApprovalPayload) {
	h.publishEnvelopes(h.approvalReviewEnvelopes(req, payload, nil), "")
}

func (h *turnHandle) publishApprovalReviewPayloadWithInvocation(req *agent.ApprovalRequest, payload *ApprovalPayload, invocation *session.EventInvocation) {
	h.publishEnvelopes(h.approvalReviewEnvelopes(req, payload, invocation), "")
}

func (h *turnHandle) approvalReviewEnvelopes(req *agent.ApprovalRequest, payload *ApprovalPayload, invocation *session.EventInvocation) []eventstream.Envelope {
	payload = cloneApprovalPayload(payload)
	base := eventstream.Envelope{
		SessionID:  h.sessionRef.SessionID,
		HandleID:   h.handleID,
		RunID:      h.runID,
		TurnID:     h.turnID,
		OccurredAt: time.Now(),
		Scope:      eventstream.ScopeMain,
		ScopeID:    h.sessionRef.SessionID,
		Meta:       approvalEventMeta(req, invocation),
	}
	if origin := canonicalOriginFromApproval(req, h.sessionRef, h.turnID); origin != nil {
		base.Scope = eventstream.Scope(origin.Scope)
		base.ScopeID = firstNonEmpty(strings.TrimSpace(origin.ScopeID), base.ScopeID)
		base.Actor = strings.TrimSpace(origin.Actor)
		base.ParticipantID = strings.TrimSpace(origin.ParticipantID)
		if base.Scope == eventstream.ScopeSubagent {
			if parent := approvalParentToolRelation(req); parent != nil {
				base.ParentTool = parent
			}
		}
	}
	// Scope describes who produced an event; it does not make that event
	// durable. Guardian review progress is a live observation. Its usage belongs
	// to durable Session accounting instead of the parent's context stream,
	// while reconnectable permission requests are projected from their stored
	// Session events by the approval coordinator.
	base.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	if review := approvalReviewFromPayload(payload); review != nil {
		next := base
		next.Kind = eventstream.KindApprovalReview
		next.ApprovalReview = review
		return []eventstream.Envelope{next}
	}
	return nil
}

func approvalEventMeta(req *agent.ApprovalRequest, invocation *session.EventInvocation) map[string]any {
	meta := canonicalApprovalEventMeta(req)
	if invocation == nil || (strings.TrimSpace(invocation.Provider) == "" && strings.TrimSpace(invocation.Model) == "") {
		return meta
	}
	invocationCopy := session.CloneEventInvocation(*invocation)
	return mergeKernelMeta(meta, map[string]any{
		"invocation": map[string]any{
			"provider": strings.TrimSpace(invocationCopy.Provider),
			"model":    strings.TrimSpace(invocationCopy.Model),
		},
	})
}

func approvalReviewFromPayload(payload *ApprovalPayload) *eventstream.ApprovalReview {
	if payload == nil {
		return nil
	}
	return &eventstream.ApprovalReview{
		ToolCallID:    strings.TrimSpace(payload.ToolCallID),
		ToolName:      strings.TrimSpace(payload.ToolName),
		RawInput:      cloneMap(payload.RawInput),
		Status:        strings.TrimSpace(string(payload.ReviewStatus)),
		Text:          strings.TrimSpace(payload.ReviewText),
		Risk:          strings.TrimSpace(payload.Risk),
		Authorization: strings.TrimSpace(payload.Authorization),
	}
}

func canonicalApprovalEventMeta(req *agent.ApprovalRequest) map[string]any {
	if req == nil || len(req.Metadata) == 0 {
		return nil
	}
	parentCallID := metadataString(req.Metadata, "parent_call_id")
	parentTool := firstNonEmpty(metadataString(req.Metadata, "parent_tool"), metadataString(req.Metadata, "parent_tool_name"))
	parentTaskID := metadataString(req.Metadata, "parent_task_id")
	if parentCallID == "" && parentTool == "" && parentTaskID == "" {
		return nil
	}
	return approvalRelationMeta(parentCallID, parentTool, parentTaskID)
}

func approvalParentToolRelation(req *agent.ApprovalRequest) *eventstream.ParentToolRelation {
	if req == nil {
		return nil
	}
	toolCallID := metadataString(req.Metadata, "parent_call_id")
	if toolCallID == "" {
		return nil
	}
	return &eventstream.ParentToolRelation{
		ToolCallID: toolCallID,
		ToolName:   firstNonEmpty(metadataString(req.Metadata, "parent_tool"), metadataString(req.Metadata, "parent_tool_name")),
	}
}

func (h *turnHandle) nextApprovalReviewID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.approvalReviewSeq++
	return fmt.Sprintf("%s-approval-review-%d", h.handleID, h.approvalReviewSeq)
}

func (h *turnHandle) publishError(err error) {
	if err == nil {
		return
	}
	h.mu.Lock()
	h.failed = true
	h.failureReason = strings.TrimSpace(display.UserVisibleError(err))
	h.mu.Unlock()
	env := eventstream.Error(err)
	env.Error = display.UserVisibleError(err)
	env.HandleID = h.handleID
	env.RunID = h.runID
	env.TurnID = h.turnID
	env.SessionID = h.sessionRef.SessionID
	if h.activeKind == ActiveTurnKindParticipant {
		env.Scope = eventstream.ScopeParticipant
		env.ScopeID = firstNonEmpty(h.turnID, h.participantID)
		env.ParticipantID = h.participantID
		env.Actor = h.participantID
	}
	h.publishEnvelope(env, "")
}

func (h *turnHandle) publishEnvelope(env eventstream.Envelope, bridgeSource string) {
	h.publishEnvelopes([]eventstream.Envelope{env}, bridgeSource)
}

func (h *turnHandle) publishEnvelopes(events []eventstream.Envelope, bridgeSource string) {
	if len(events) == 0 {
		return
	}
	h.observerMu.Lock()
	defer h.observerMu.Unlock()
	for _, env := range events {
		h.mu.Lock()
		if env.Cursor == "" || (env.ProjectionID != "" && env.Cursor == env.ProjectionID) {
			env.Cursor = h.allocateEventCursorLocked()
		}
		env = h.enrichEnvelopeLocked(env, bridgeSource)
		if isMainFeedTerminal(env) {
			if h.producerTerminal == nil {
				clone := eventstream.CloneEnvelope(env)
				h.producerTerminal = &clone
			}
			h.mu.Unlock()
			continue
		}
		observer := h.observer
		ctx := h.ctx
		h.mu.Unlock()
		if isSubagentTaskObservation(env) || observer == nil {
			continue
		}
		if err := observer.ObserveTurnEvent(ctx, eventstream.CloneEnvelope(env)); err != nil {
			h.mu.Lock()
			h.failed = true
			h.failureReason = strings.TrimSpace(err.Error())
			cancelFn := h.cancelFn
			h.mu.Unlock()
			if cancelFn != nil {
				cancelFn()
			}
		}
	}
}

func (h *turnHandle) publishACP(env eventstream.Envelope, bridgeSource string) {
	h.publishEnvelope(env, bridgeSource)
}

func (h *turnHandle) allocateEventCursorLocked() string {
	h.acpCursorSeq++
	prefix := strings.TrimSpace(h.handleID)
	if prefix == "" {
		prefix = "acp"
	}
	return fmt.Sprintf("%s-acp-%06d", prefix, h.acpCursorSeq)
}

func (h *turnHandle) finish() {
	h.mu.Lock()
	if h.finished || h.finishing {
		h.mu.Unlock()
		return
	}
	h.finishing = true
	h.runnerReadyOnce.Do(func() { close(h.runnerReady) })
	hooks := append([]func(){}, h.finishHooks...)
	h.finishHooks = nil
	h.mu.Unlock()
	for _, hook := range hooks {
		hook()
	}
	h.publishFinalTerminal()
	h.mu.Lock()
	h.finishing = false
	h.finished = true
	h.mu.Unlock()
	h.doneOnce.Do(func() { close(h.done) })
	h.approvals.abandonOwner(h, "terminal")
}

func (h *turnHandle) publishFinalTerminal() {
	h.observerMu.Lock()
	defer h.observerMu.Unlock()
	h.mu.Lock()
	terminal := eventstream.Envelope{}
	switch {
	case h.cancelled:
		terminal = eventstream.TurnCancelled(h.handleID, h.runID, h.turnID, h.failureReason, time.Now())
	case h.failed:
		terminal = eventstream.TurnFailed(h.handleID, h.runID, h.turnID, h.failureReason, time.Now())
	case h.producerTerminal != nil:
		terminal = eventstream.CloneEnvelope(*h.producerTerminal)
	default:
		terminal = eventstream.TurnCompleted(h.handleID, h.runID, h.turnID, time.Now())
	}
	if terminal.Cursor == "" || (terminal.ProjectionID != "" && terminal.Cursor == terminal.ProjectionID) {
		terminal.Cursor = h.allocateEventCursorLocked()
	}
	terminal = h.enrichEnvelopeLocked(terminal, "")
	observer := h.observer
	ctx := h.ctx
	h.mu.Unlock()
	if observer != nil {
		_ = observer.ObserveTurnEvent(ctx, terminal)
	}
}

func isMainFeedTerminal(env eventstream.Envelope) bool {
	return (env.Scope == "" || env.Scope == eventstream.ScopeMain) && eventstream.IsTurnTerminalLifecycle(env)
}

func isSubagentTaskObservation(env eventstream.Envelope) bool {
	if env.Scope != eventstream.ScopeSubagent || strings.TrimSpace(string(env.ApprovalRequestID)) != "" {
		return false
	}
	switch env.Kind {
	case eventstream.KindRequestPermission, eventstream.KindApprovalReview, eventstream.KindParticipant:
		return false
	default:
		return true
	}
}

func (h *turnHandle) enrichEnvelopeLocked(env eventstream.Envelope, bridgeSource string) eventstream.Envelope {
	env.SessionID = strings.TrimSpace(h.sessionRef.SessionID)
	env.HandleID = strings.TrimSpace(h.handleID)
	env.RunID = strings.TrimSpace(h.runID)
	env.TurnID = strings.TrimSpace(h.turnID)
	if env.OccurredAt.IsZero() {
		env.OccurredAt = time.Now()
	}
	if env.Scope == "" {
		env.Scope = eventstream.ScopeMain
	}
	if strings.TrimSpace(env.ScopeID) == "" {
		env.ScopeID = strings.TrimSpace(h.sessionRef.SessionID)
	}
	env.Meta = mergeCaelisBridgeMeta(env.Meta, bridgeSource)
	if env.Permission != nil {
		env.Permission.SessionID = strings.TrimSpace(h.sessionRef.SessionID)
	}
	return env
}

func mergeCaelisBridgeMeta(meta map[string]any, bridgeSource string) map[string]any {
	caelis := map[string]any{
		"version": 1,
	}
	if strings.TrimSpace(bridgeSource) != "" {
		caelis["bridge"] = map[string]any{
			"source": strings.TrimSpace(bridgeSource),
		}
	}
	return mergeKernelMeta(meta, caelis)
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
