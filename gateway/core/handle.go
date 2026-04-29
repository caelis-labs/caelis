package core

import (
	"context"
	"slices"
	"sync"
	"time"

	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

type turnHandleConfig struct {
	handleID   string
	runID      string
	turnID     string
	sessionRef sdksession.SessionRef
	createdAt  time.Time
	cancel     func() bool
}

type turnHandle struct {
	handleID   string
	runID      string
	turnID     string
	sessionRef sdksession.SessionRef
	createdAt  time.Time
	cancelFn   func() bool

	mu                sync.Mutex
	events            []EventEnvelope
	eventsCh          chan EventEnvelope
	eventsClosed      bool
	closed            bool
	finished          bool
	runner            sdkruntime.Runner
	pendingApprovalCh chan ApprovalDecision
}

func newTurnHandle(cfg turnHandleConfig) *turnHandle {
	return &turnHandle{
		handleID:   cfg.handleID,
		runID:      cfg.runID,
		turnID:     cfg.turnID,
		sessionRef: cfg.sessionRef,
		createdAt:  cfg.createdAt,
		cancelFn:   cfg.cancel,
		eventsCh:   make(chan EventEnvelope, 32),
	}
}

func (h *turnHandle) HandleID() string                  { return h.handleID }
func (h *turnHandle) RunID() string                     { return h.runID }
func (h *turnHandle) TurnID() string                    { return h.turnID }
func (h *turnHandle) SessionRef() sdksession.SessionRef { return h.sessionRef }
func (h *turnHandle) CreatedAt() time.Time              { return h.createdAt }
func (h *turnHandle) Events() <-chan EventEnvelope      { return h.eventsCh }

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
	h.mu.Unlock()
	if runner == nil {
		return &Error{
			Kind:        KindUnsupported,
			Code:        CodeSubmissionUnsupported,
			UserVisible: true,
			Message:     "gateway: submission is not available for this handle",
		}
	}
	return runner.Submit(sdkruntime.Submission{
		Kind:     string(req.Kind),
		Text:     req.Text,
		Metadata: cloneMap(req.Metadata),
	})
}

func (h *turnHandle) Cancel() bool {
	if h.cancelFn != nil {
		return h.cancelFn()
	}
	h.mu.Lock()
	runner := h.runner
	h.mu.Unlock()
	if runner == nil {
		return false
	}
	return runner.Cancel()
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

func (h *turnHandle) setRunner(runner sdkruntime.Runner) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runner = runner
}

func (h *turnHandle) setPendingApproval() <-chan ApprovalDecision {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan ApprovalDecision, 1)
	h.pendingApprovalCh = ch
	return ch
}

func (h *turnHandle) publishSessionEvent(event *sdksession.Event) {
	if event == nil {
		return
	}
	h.publish(EventEnvelope{
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
			Usage:       usageSnapshotFromSessionEvent(event),
			Narrative:   canonicalNarrativePayload(event),
			ToolCall:    canonicalToolCallPayload(event),
			ToolResult:  canonicalToolResultPayload(event),
			Plan:        canonicalPlanPayload(event),
			Participant: canonicalParticipantPayload(event),
			Lifecycle:   canonicalLifecyclePayload(event),
		},
	})
}

func (h *turnHandle) publishApproval(req *sdkruntime.ApprovalRequest) <-chan ApprovalDecision {
	wait := h.setPendingApproval()
	h.publish(EventEnvelope{
		Cursor: h.allocateCursor(),
		Event: Event{
			Kind:            EventKindApprovalRequested,
			HandleID:        h.handleID,
			RunID:           h.runID,
			TurnID:          h.turnID,
			SessionRef:      h.sessionRef,
			Origin:          canonicalOriginFromApproval(req, h.sessionRef, h.turnID),
			ApprovalPayload: canonicalApprovalPayload(req),
		},
	})
	return wait
}

func (h *turnHandle) publish(env EventEnvelope) {
	h.mu.Lock()
	if env.Cursor == "" {
		env.Cursor = h.allocateCursorLocked()
	}
	h.events = append(h.events, env)
	if h.closed || h.finished {
		h.mu.Unlock()
		return
	}
	ch := h.eventsCh
	h.mu.Unlock()
	ch <- env
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
	if h.eventsClosed {
		return
	}
	h.eventsClosed = true
	close(h.eventsCh)
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
