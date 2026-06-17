package kernel

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	acpprojector "github.com/OnslaughtSnail/caelis/protocol/acp/projector"
)

type turnHandleConfig struct {
	handleID                string
	runID                   string
	turnID                  string
	activeKind              ActiveTurnKind
	sessionRef              session.SessionRef
	createdAt               time.Time
	cancel                  func() bool
	allowPendingSubmissions bool
}

type turnHandle struct {
	handleID   string
	runID      string
	turnID     string
	activeKind ActiveTurnKind
	sessionRef session.SessionRef
	createdAt  time.Time
	cancelFn   func() bool

	mu                      sync.Mutex
	events                  []EventEnvelope
	eventsCh                chan EventEnvelope
	acpEventsCh             chan eventstream.Envelope
	eventsCond              *sync.Cond
	liveQueue               []EventEnvelope
	acpLiveQueue            []eventstream.Envelope
	eventsStarted           bool
	acpEventsStarted        bool
	eventsClosed            bool
	acpEventsClosed         bool
	closed                  bool
	finished                bool
	cancelled               bool
	runner                  agent.Runner
	pendingSubmissions      []SubmitRequest
	allowPendingSubmissions bool
	pendingApprovalCh       chan ApprovalDecision

	approvalReviewSeq uint64
}

func newTurnHandle(cfg turnHandleConfig) *turnHandle {
	h := &turnHandle{
		handleID:                cfg.handleID,
		runID:                   cfg.runID,
		turnID:                  cfg.turnID,
		activeKind:              cfg.activeKind,
		sessionRef:              cfg.sessionRef,
		createdAt:               cfg.createdAt,
		cancelFn:                cfg.cancel,
		allowPendingSubmissions: cfg.allowPendingSubmissions,
		eventsCh:                make(chan EventEnvelope, 32),
		acpEventsCh:             make(chan eventstream.Envelope, 32),
	}
	h.eventsCond = sync.NewCond(&h.mu)
	return h
}

func (h *turnHandle) HandleID() string               { return h.handleID }
func (h *turnHandle) RunID() string                  { return h.runID }
func (h *turnHandle) TurnID() string                 { return h.turnID }
func (h *turnHandle) ActiveKind() ActiveTurnKind     { return h.activeKind }
func (h *turnHandle) SessionRef() session.SessionRef { return h.sessionRef }
func (h *turnHandle) CreatedAt() time.Time           { return h.createdAt }
func (h *turnHandle) Events() <-chan EventEnvelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.eventsStarted && !h.eventsClosed {
		h.eventsStarted = true
		go h.dispatchLiveEvents()
	}
	return h.eventsCh
}

func (h *turnHandle) ACPEvents() <-chan eventstream.Envelope {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.acpEventsStarted && !h.acpEventsClosed {
		h.acpEventsStarted = true
		go h.dispatchACPEvents()
	}
	return h.acpEventsCh
}

func (h *turnHandle) EventsAfter(cursor string) ([]EventEnvelope, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	start, err := startIndexAfterCursor(h.events, cursor)
	if err != nil {
		return nil, "", err
	}
	if start == 0 {
		out := slices.Clone(h.events)
		return out, lastCursor(out), nil
	}
	out := slices.Clone(h.events[start:])
	return out, lastCursor(out), nil
}

func (h *turnHandle) Submit(ctx context.Context, req SubmitRequest) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateSubmitRequest(req); err != nil {
		return err
	}
	if req.Kind == SubmissionKindApproval && req.Approval != nil {
		h.mu.Lock()
		wait := h.pendingApprovalCh
		h.pendingApprovalCh = nil
		h.mu.Unlock()
		if wait == nil {
			return &Error{
				Kind:        KindApproval,
				Code:        CodeApprovalNotPending,
				UserVisible: true,
				Message:     "gateway: no approval is pending",
			}
		}
		select {
		case wait <- *req.Approval:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
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
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	h.closed = true
	if h.finished {
		h.closeEventsLocked()
	}
	return nil
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
			h.publish(EventEnvelope{
				Event: Event{
					Kind:       EventKindLifecycle,
					HandleID:   h.handleID,
					RunID:      h.runID,
					TurnID:     h.turnID,
					SessionRef: h.sessionRef,
				},
				Err: EventError(err),
			})
		}
	}
}

func cloneSubmitRequest(req SubmitRequest) SubmitRequest {
	out := SubmitRequest{
		Kind:         req.Kind,
		Text:         req.Text,
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

func (h *turnHandle) setPendingApproval() <-chan ApprovalDecision {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan ApprovalDecision, 1)
	h.pendingApprovalCh = ch
	return ch
}

func (h *turnHandle) publishSessionEvent(event *session.Event) {
	h.publishSessionEventWithACPProjection(event, true)
}

func (h *turnHandle) publishSessionEventWithACPProjection(event *session.Event, projectACP bool) {
	if event == nil {
		return
	}
	env := EventEnvelope{
		Cursor: event.ID,
		Event: Event{
			Kind:        sessionEventKind(event),
			HandleID:    h.handleID,
			RunID:       h.runID,
			TurnID:      h.turnID,
			OccurredAt:  event.Time,
			SessionRef:  h.sessionRef,
			Origin:      canonicalOriginFromSessionEvent(h.sessionRef, event),
			Meta:        canonicalEventMeta(event),
			Invocation:  canonicalInvocationPayload(event),
			Protocol:    canonicalProtocolPayload(event),
			Usage:       usageSnapshotFromSessionEvent(event),
			Narrative:   canonicalNarrativePayload(event),
			ToolCall:    canonicalToolCallPayload(event),
			ToolResult:  canonicalToolResultPayload(event),
			Plan:        canonicalPlanPayload(event),
			Participant: canonicalParticipantPayload(event),
			Lifecycle:   canonicalLifecyclePayload(event),
		},
	}
	h.publishWithACPProjection(env, projectACP)
}

func (h *turnHandle) publishApproval(req *agent.ApprovalRequest) <-chan ApprovalDecision {
	wait := h.setPendingApproval()
	h.publishApprovalPayload(req, canonicalApprovalPayload(req))
	return wait
}

func (h *turnHandle) publishApprovalPayload(req *agent.ApprovalRequest, payload *ApprovalPayload) {
	h.publishApprovalEvent(req, payload, EventKindApprovalRequested, nil, nil)
}

func (h *turnHandle) publishApprovalReviewPayload(req *agent.ApprovalRequest, payload *ApprovalPayload) {
	h.publishApprovalEvent(req, payload, EventKindApprovalReview, nil, nil)
}

func (h *turnHandle) publishApprovalReviewPayloadWithUsage(req *agent.ApprovalRequest, payload *ApprovalPayload, usage *UsageSnapshot, invocation *session.EventInvocation) {
	h.publishApprovalEvent(req, payload, EventKindApprovalReview, usage, invocation)
}

func (h *turnHandle) publishApprovalEvent(req *agent.ApprovalRequest, payload *ApprovalPayload, kind EventKind, usage *UsageSnapshot, invocation *session.EventInvocation) {
	var eventUsage *UsageSnapshot
	if usage != nil {
		copy := *usage
		eventUsage = &copy
	}
	var eventInvocation *session.EventInvocation
	if invocation != nil {
		copy := session.CloneEventInvocation(*invocation)
		eventInvocation = &copy
	}
	if eventInvocation != nil && eventInvocation.Provider == "" && eventInvocation.Model == "" {
		eventInvocation = nil
	}
	h.publish(EventEnvelope{
		Cursor: h.allocateCursor(),
		Event: Event{
			Kind:            kind,
			HandleID:        h.handleID,
			RunID:           h.runID,
			TurnID:          h.turnID,
			SessionRef:      h.sessionRef,
			Origin:          canonicalOriginFromApproval(req, h.sessionRef, h.turnID),
			Meta:            canonicalApprovalEventMeta(req),
			Invocation:      eventInvocation,
			Usage:           eventUsage,
			ApprovalPayload: cloneApprovalPayload(payload),
		},
	})
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

func (h *turnHandle) nextApprovalReviewID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.approvalReviewSeq++
	return fmt.Sprintf("%s-approval-review-%d", h.handleID, h.approvalReviewSeq)
}

func (h *turnHandle) publish(env EventEnvelope) {
	h.publishWithACPProjection(env, true)
}

func (h *turnHandle) publishWithACPProjection(env EventEnvelope, projectACP bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if env.Cursor == "" {
		env.Cursor = h.allocateCursorLocked()
	}
	h.events = append(h.events, env)
	if h.closed || h.finished {
		return
	}
	h.liveQueue = append(h.liveQueue, env)
	if projectACP {
		for _, acpEnv := range acpprojector.ProjectGatewayEventEnvelope(env) {
			h.acpLiveQueue = append(h.acpLiveQueue, h.enrichACPEnvelopeLocked(acpEnv, "gateway_projection"))
		}
	}
	h.eventsCond.Broadcast()
}

func (h *turnHandle) publishACP(env eventstream.Envelope, bridgeSource string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if env.Cursor == "" {
		env.Cursor = h.allocateCursorLocked()
	}
	env = h.enrichACPEnvelopeLocked(env, bridgeSource)
	if h.closed || h.finished {
		return
	}
	h.acpLiveQueue = append(h.acpLiveQueue, env)
	h.eventsCond.Broadcast()
}

func (h *turnHandle) allocateCursor() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.allocateCursorLocked()
}

func (h *turnHandle) allocateCursorLocked() string {
	return h.handleID + "-cursor-" + time.Now().Format(time.RFC3339Nano)
}

func lastCursor(events []EventEnvelope) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1].Cursor
}

func (h *turnHandle) finish() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.finished {
		return
	}
	h.finished = true
	h.closeEventsLocked()
}

func (h *turnHandle) closeEventsLocked() {
	if (!h.eventsClosed && h.eventsStarted) || (!h.acpEventsClosed && h.acpEventsStarted) {
		h.eventsCond.Broadcast()
	}
}

func (h *turnHandle) dispatchLiveEvents() {
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

func (h *turnHandle) dispatchACPEvents() {
	for {
		h.mu.Lock()
		for len(h.acpLiveQueue) == 0 && !h.finished {
			h.eventsCond.Wait()
		}
		if len(h.acpLiveQueue) == 0 && h.finished {
			if !h.acpEventsClosed {
				h.acpEventsClosed = true
				close(h.acpEventsCh)
			}
			h.mu.Unlock()
			return
		}
		env := h.acpLiveQueue[0]
		copy(h.acpLiveQueue, h.acpLiveQueue[1:])
		h.acpLiveQueue = h.acpLiveQueue[:len(h.acpLiveQueue)-1]
		h.mu.Unlock()
		h.acpEventsCh <- env
	}
}

func (h *turnHandle) enrichACPEnvelopeLocked(env eventstream.Envelope, bridgeSource string) eventstream.Envelope {
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
	out := maps.Clone(meta)
	if out == nil {
		out = map[string]any{}
	}
	caelis, _ := out["caelis"].(map[string]any)
	if caelis == nil {
		caelis = map[string]any{}
	} else {
		caelis = maps.Clone(caelis)
	}
	caelis["version"] = 1
	if strings.TrimSpace(bridgeSource) != "" {
		bridge, _ := caelis["bridge"].(map[string]any)
		if bridge == nil {
			bridge = map[string]any{}
		} else {
			bridge = maps.Clone(bridge)
		}
		bridge["source"] = strings.TrimSpace(bridgeSource)
		caelis["bridge"] = bridge
	}
	out["caelis"] = caelis
	return out
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
