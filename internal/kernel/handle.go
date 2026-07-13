package kernel

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

type turnHandleConfig struct {
	handleID                string
	runID                   string
	turnID                  string
	activeKind              ActiveTurnKind
	participantID           string
	sessionRef              session.SessionRef
	createdAt               time.Time
	cancel                  func() bool
	allowPendingSubmissions bool
	prepareSubmission       func(context.Context, SubmitRequest) (SubmitRequest, error)
	persistApproval         func(*agent.ApprovalRequest, eventstream.ApprovalRequestID) (*session.Event, error)
	settleApproval          func(*agent.ApprovalRequest, eventstream.ApprovalRequestID, string) (*session.Event, error)
	approvals               *approvalCoordinator
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

	mu                      sync.Mutex
	events                  []eventstream.Envelope
	eventsCh                chan eventstream.Envelope
	eventsCond              *sync.Cond
	liveQueue               []eventstream.Envelope
	eventsStarted           bool
	eventsClosed            bool
	closed                  bool
	finishing               bool
	finished                bool
	failed                  bool
	cancelled               bool
	runner                  agent.Runner
	pendingSubmissions      []SubmitRequest
	allowPendingSubmissions bool
	prepareSubmission       func(context.Context, SubmitRequest) (SubmitRequest, error)
	persistApproval         func(*agent.ApprovalRequest, eventstream.ApprovalRequestID) (*session.Event, error)
	settleApproval          func(*agent.ApprovalRequest, eventstream.ApprovalRequestID, string) (*session.Event, error)
	approvals               *approvalCoordinator
	finishHooks             []func()

	approvalReviewSeq uint64
	acpCursorSeq      uint64
}

func newTurnHandle(cfg turnHandleConfig) *turnHandle {
	h := &turnHandle{
		handleID:                cfg.handleID,
		runID:                   cfg.runID,
		turnID:                  cfg.turnID,
		activeKind:              cfg.activeKind,
		participantID:           strings.TrimSpace(cfg.participantID),
		sessionRef:              cfg.sessionRef,
		createdAt:               cfg.createdAt,
		cancelFn:                cfg.cancel,
		allowPendingSubmissions: cfg.allowPendingSubmissions,
		prepareSubmission:       cfg.prepareSubmission,
		persistApproval:         cfg.persistApproval,
		settleApproval:          cfg.settleApproval,
		approvals:               cfg.approvals,
		eventsCh:                make(chan eventstream.Envelope, 32),
	}
	if h.approvals == nil {
		h.approvals = newApprovalCoordinator(cfg.sessionRef)
	}
	h.eventsCond = sync.NewCond(&h.mu)
	return h
}

func (h *turnHandle) HandleID() string               { return h.handleID }
func (h *turnHandle) RunID() string                  { return h.runID }
func (h *turnHandle) TurnID() string                 { return h.turnID }
func (h *turnHandle) ActiveKind() ActiveTurnKind     { return h.activeKind }
func (h *turnHandle) ParticipantID() string          { return h.participantID }
func (h *turnHandle) SessionRef() session.SessionRef { return h.sessionRef }
func (h *turnHandle) CreatedAt() time.Time           { return h.createdAt }
func (h *turnHandle) ACPEvents() <-chan eventstream.Envelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.eventsStarted && !h.eventsClosed {
		h.eventsStarted = true
		go h.dispatchEvents()
	}
	return h.eventsCh
}

func (h *turnHandle) eventsAfter(cursor string) ([]eventstream.Envelope, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	start, err := startEventstreamIndexAfterCursor(h.events, cursor)
	if err != nil {
		return nil, "", err
	}
	if start == 0 {
		out := eventstream.CloneEnvelopes(h.events)
		return out, lastEventstreamCursor(out), nil
	}
	out := eventstream.CloneEnvelopes(h.events[start:])
	return out, lastEventstreamCursor(out), nil
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

	h.mu.Lock()
	runner := h.runner
	cancelled := h.cancelled
	if err := ctx.Err(); err != nil {
		h.mu.Unlock()
		return err
	}
	if cancelled {
		h.mu.Unlock()
		return context.Canceled
	}
	if runner == nil && h.allowPendingSubmissions && !h.finished {
		h.pendingSubmissions = append(h.pendingSubmissions, cloneSubmitRequest(req))
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	if runner == nil {
		return &Error{
			Kind:        KindUnsupported,
			Code:        CodeSubmissionUnsupported,
			UserVisible: true,
			Message:     "gateway: submission is not available for this handle",
		}
	}
	return runner.Submit(runnerSubmissionFromSubmitRequest(req))
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
	if h.finished {
		h.closeEventsLocked()
	}
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
	}
}

func validateSubmitRequest(req SubmitRequest) error {
	switch req.Kind {
	case SubmissionKindConversation:
		if req.Approval != nil {
			return invalidSubmissionKind(req.Kind)
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
	h.publishApprovalEvent(req, payload, EventKindApprovalReview, nil, nil, "")
}

func (h *turnHandle) publishApprovalReviewPayloadWithUsage(req *agent.ApprovalRequest, payload *ApprovalPayload, usage *UsageSnapshot, invocation *session.EventInvocation) {
	h.publishApprovalEvent(req, payload, EventKindApprovalReview, usage, invocation, "")
}

func (h *turnHandle) publishApprovalEvent(req *agent.ApprovalRequest, payload *ApprovalPayload, kind EventKind, usage *UsageSnapshot, invocation *session.EventInvocation, requestID eventstream.ApprovalRequestID) {
	h.publishEnvelopes(h.approvalEventEnvelopes(req, payload, kind, usage, invocation, requestID), "")
}

func (h *turnHandle) approvalEventEnvelopes(req *agent.ApprovalRequest, payload *ApprovalPayload, kind EventKind, usage *UsageSnapshot, invocation *session.EventInvocation, requestID eventstream.ApprovalRequestID) []eventstream.Envelope {
	payload = cloneApprovalPayload(payload)
	base := eventstream.Envelope{
		SessionID:         h.sessionRef.SessionID,
		HandleID:          h.handleID,
		RunID:             h.runID,
		TurnID:            h.turnID,
		OccurredAt:        time.Now(),
		Scope:             eventstream.ScopeMain,
		ScopeID:           h.sessionRef.SessionID,
		Meta:              approvalEventMeta(req, invocation),
		ApprovalRequestID: requestID,
	}
	if origin := canonicalOriginFromApproval(req, h.sessionRef, h.turnID); origin != nil {
		base.Scope = eventstream.Scope(origin.Scope)
		base.ScopeID = firstNonEmpty(strings.TrimSpace(origin.ScopeID), base.ScopeID)
		base.Actor = strings.TrimSpace(origin.Actor)
		base.ParticipantID = strings.TrimSpace(origin.ParticipantID)
		if base.Scope == eventstream.ScopeSubagent {
			base.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryMirror}
			if parent := approvalParentToolRelation(req); parent != nil {
				base.ParentTool = parent
			}
		}
	}
	var out []eventstream.Envelope
	switch kind {
	case EventKindApprovalRequested:
		out = append(out, acpprojector.ProjectApprovalPayloadEnvelope(base, payload)...)
	case EventKindApprovalReview:
		if review := approvalReviewFromPayload(payload); review != nil {
			next := base
			next.Kind = eventstream.KindApprovalReview
			next.ApprovalReview = review
			out = append(out, next)
		}
	}
	if usage != nil {
		next := base
		next.Kind = eventstream.KindSessionUpdate
		next.Update = eventstream.UsageUpdateFromSnapshot(eventstream.UsageSnapshot{
			PromptTokens:        usage.PromptTokens,
			CachedInputTokens:   usage.CachedInputTokens,
			CompletionTokens:    usage.CompletionTokens,
			ReasoningTokens:     usage.ReasoningTokens,
			TotalTokens:         usage.TotalTokens,
			ContextWindowTokens: usage.ContextWindowTokens,
		}, base.Meta)
		out = append(out, next)
	}
	return out
}

func approvalEventMeta(req *agent.ApprovalRequest, invocation *session.EventInvocation) map[string]any {
	meta := canonicalApprovalEventMeta(req)
	if invocation == nil || (strings.TrimSpace(invocation.Provider) == "" && strings.TrimSpace(invocation.Model) == "") {
		return meta
	}
	invocationCopy := session.CloneEventInvocation(*invocation)
	return metautil.Merge(meta, map[string]any{
		metautil.Root: map[string]any{
			"invocation": map[string]any{
				"provider": strings.TrimSpace(invocationCopy.Provider),
				"model":    strings.TrimSpace(invocationCopy.Model),
			},
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
	return withCaelisRuntimeSection(nil, EventMetaRuntimeStream, map[string]any{
		EventMetaRuntimeStreamParentCallID: parentCallID,
		EventMetaRuntimeStreamParentTool:   parentTool,
		EventMetaRuntimeStreamParentTaskID: parentTaskID,
	})
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
	h.mu.Unlock()
	env := eventstream.Error(err)
	env.HandleID = h.handleID
	env.RunID = h.runID
	env.TurnID = h.turnID
	env.SessionID = h.sessionRef.SessionID
	h.publishEnvelope(env, "")
}

func (h *turnHandle) publishEnvelope(env eventstream.Envelope, bridgeSource string) {
	h.publishEnvelopes([]eventstream.Envelope{env}, bridgeSource)
}

func (h *turnHandle) publishEnvelopes(events []eventstream.Envelope, bridgeSource string) {
	if len(events) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.publishEnvelopesLocked(events, bridgeSource)
}

// publishEnvelopesLocked records and schedules ACP envelopes while h.mu is
// held. Approval activation uses it so waiter registration, queue activation,
// and prompt publication stay under the same Control owner.
func (h *turnHandle) publishEnvelopesLocked(events []eventstream.Envelope, bridgeSource string) {
	if len(events) == 0 {
		return
	}
	for _, env := range events {
		if env.Cursor == "" || (env.ProjectionID != "" && env.Cursor == env.ProjectionID) {
			env.Cursor = h.allocateEventCursorLocked()
		}
		env = h.enrichEnvelopeLocked(env, bridgeSource)
		h.events = append(h.events, eventstream.CloneEnvelope(env))
		if h.closed || h.eventsClosed {
			continue
		}
		h.liveQueue = append(h.liveQueue, env)
	}
	h.eventsCond.Broadcast()
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

func lastEventstreamCursor(events []eventstream.Envelope) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Cursor
}

func startEventstreamIndexAfterCursor(events []eventstream.Envelope, cursor string) (int, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0, nil
	}
	for i, env := range events {
		if env.Cursor == cursor {
			return i + 1, nil
		}
	}
	return 0, cursorNotFoundError(cursor)
}

func (h *turnHandle) finish() {
	h.mu.Lock()
	if h.finished || h.finishing {
		h.mu.Unlock()
		return
	}
	h.finishing = true
	hooks := append([]func(){}, h.finishHooks...)
	h.finishHooks = nil
	h.mu.Unlock()
	for _, hook := range hooks {
		hook()
	}
	h.mu.Lock()
	h.finishing = false
	h.finished = true
	h.closeEventsLocked()
	h.mu.Unlock()
	h.approvals.abandonOwner(h, "terminal")
}

func (h *turnHandle) closeEventsLocked() {
	if !h.eventsClosed && h.eventsStarted {
		h.eventsCond.Broadcast()
	}
}

func (h *turnHandle) dispatchEvents() {
	for {
		h.mu.Lock()
		for len(h.liveQueue) == 0 && !h.finished {
			h.eventsCond.Wait()
		}
		if len(h.liveQueue) == 0 && h.finished {
			if !h.eventsClosed {
				h.eventsClosed = true
				close(h.eventsCh)
			}
			h.mu.Unlock()
			return
		}
		env := h.liveQueue[0]
		copy(h.liveQueue, h.liveQueue[1:])
		h.liveQueue = h.liveQueue[:len(h.liveQueue)-1]
		h.mu.Unlock()
		h.eventsCh <- env
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
		metautil.Version: 1,
	}
	if strings.TrimSpace(bridgeSource) != "" {
		caelis["bridge"] = map[string]any{
			"source": strings.TrimSpace(bridgeSource),
		}
	}
	return metautil.Merge(meta, map[string]any{metautil.Root: caelis})
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
