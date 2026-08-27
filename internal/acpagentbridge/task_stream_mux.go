package acpagentbridge

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// acpTaskStreamMux keeps the standard ACP bridge compatible with mounted
// RunCommand and Spawn terminals without flattening subagent semantics into
// the main session/update stream. After its parent prompt is sealed, active
// command streams and the current subagent activity remain available until
// their terminal boundary; later subagent activities belong to a later Prompt.
// Stopping the mux closes only delivery subscriptions, never Tasks.
type acpTaskStreamMux struct {
	ctx       context.Context
	cancel    context.CancelFunc
	client    taskstream.Client
	sessionID string
	events    chan eventstream.Envelope

	mu           sync.Mutex
	observations map[string]*acpTaskStreamObservation
	active       int
	sealed       bool
	eventsOnce   sync.Once
	wg           sync.WaitGroup

	// beforeBoundarySignal is a deterministic test hook. Production leaves it
	// nil; each observation generation still owns the boundary passed to it.
	beforeBoundarySignal func(string, chan struct{})
}

func newACPTaskStreamMux(parent context.Context, service taskstream.Service, principal taskstream.Principal, sessionID string) *acpTaskStreamMux {
	if service == nil {
		return nil
	}
	client, err := taskstream.BindClient(service, principal)
	if err != nil {
		return nil
	}
	return newACPTaskStreamClientMux(parent, client, sessionID)
}

func newACPTaskStreamClientMux(parent context.Context, client taskstream.Client, sessionID string) *acpTaskStreamMux {
	if client == nil {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	return &acpTaskStreamMux{
		ctx: ctx, cancel: cancel, client: client,
		sessionID: strings.TrimSpace(sessionID), events: make(chan eventstream.Envelope, 128),
		observations: map[string]*acpTaskStreamObservation{},
	}
}

func (a *RuntimeAgent) startACPTaskStreamMux(parent context.Context, sessionID string) *acpTaskStreamMux {
	if a == nil {
		return nil
	}
	mux := newACPTaskStreamClientMux(context.WithoutCancel(parent), a.taskStreamClient, sessionID)
	if mux == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	if a.taskMuxes == nil {
		a.taskMuxes = map[string]map[*acpTaskStreamMux]struct{}{}
	}
	if a.taskMuxes[sessionID] == nil {
		a.taskMuxes[sessionID] = map[*acpTaskStreamMux]struct{}{}
	}
	a.taskMuxes[sessionID][mux] = struct{}{}
	a.mu.Unlock()
	return mux
}

// detachACPTaskStreamMux seals discovery after the parent Prompt ends, then
// keeps forwarding already-subscribed terminal delivery until those Task
// streams end or the Session is closed.
func (a *RuntimeAgent) detachACPTaskStreamMux(parent context.Context, mux *acpTaskStreamMux, cb PromptCallbacks, sessionID string, filter *acpNarrativeFilter) {
	if a == nil || mux == nil {
		return
	}
	mux.Seal()
	deliveryCtx := context.WithoutCancel(parent)
	go func() {
		defer a.unregisterACPTaskStreamMux(sessionID, mux)
		for envelope := range mux.Events() {
			if err := a.emitControlEnvelope(deliveryCtx, cb, sessionID, nil, envelope, filter); err != nil {
				mux.Close()
				return
			}
		}
	}()
}

func (a *RuntimeAgent) unregisterACPTaskStreamMux(sessionID string, mux *acpTaskStreamMux) {
	if a == nil || mux == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	delete(a.taskMuxes[sessionID], mux)
	if len(a.taskMuxes[sessionID]) == 0 {
		delete(a.taskMuxes, sessionID)
	}
	a.mu.Unlock()
}

func (a *RuntimeAgent) closeACPTaskStreamMuxes(sessionID string) {
	if a == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	muxes := make([]*acpTaskStreamMux, 0, len(a.taskMuxes[sessionID]))
	for mux := range a.taskMuxes[sessionID] {
		muxes = append(muxes, mux)
	}
	delete(a.taskMuxes, sessionID)
	a.mu.Unlock()
	for _, mux := range muxes {
		mux.Close()
	}
}

func (m *acpTaskStreamMux) Events() <-chan eventstream.Envelope {
	if m == nil {
		return nil
	}
	return m.events
}

func (m *acpTaskStreamMux) Observe(envelope eventstream.Envelope) {
	if m == nil {
		return
	}
	anchor, ok := acpTaskStreamAnchorFromEnvelope(envelope)
	if !ok {
		return
	}
	if anchor.parentTerminal {
		generation := m.closeParentObservation(anchor.callID)
		if generation != nil {
			generation.cancel()
			m.signalGenerationBoundary(anchor.callID, generation.boundary)
		}
		return
	}
	generation := m.claimObservation(anchor.callID, anchor.kind == task.KindSubagent)
	if generation == nil {
		return
	}
	// Every resolve generation owns its boundary. In particular, a retryable
	// attach miss may expose Idle before its goroutine runs deferred cleanup;
	// that older generation must never signal a later successful attachment.
	go m.resolveAndForward(anchor, generation)
}

type acpTaskStreamAnchor struct {
	callID         string
	handle         string
	taskID         string
	kind           task.Kind
	parentTerminal bool
}

func (m *acpTaskStreamMux) resolveAndForward(
	anchor acpTaskStreamAnchor,
	generation *acpTaskStreamObservationGeneration,
) {
	defer m.wg.Done()
	defer m.finishOperation()
	defer close(generation.settled)
	defer m.signalGenerationBoundary(anchor.callID, generation.boundary)
	defer generation.cancel()

	attachment, attempts, err := m.resolveSubscriptionWithGrace(anchor, generation)
	if err != nil {
		m.finishResolveFailure(anchor, generation, err, attempts)
		return
	}
	if !m.attachObservation(anchor.callID, generation) {
		_ = attachment.result.Subscription.Close()
		m.closeObservation(anchor.callID, generation, "")
		return
	}

	taskID := attachment.taskID
	subscription := attachment.result.Subscription
	resumeCursor := ""
	for {
		lastCursor, streamErr := m.forwardUntilClose(anchor, subscription, generation)
		_ = subscription.Close()
		m.recordObservationCursor(anchor.callID, generation, lastCursor)
		if lastCursor != "" {
			resumeCursor = lastCursor
		}
		if streamErr == nil || generation.ctx.Err() != nil {
			m.closeObservation(anchor.callID, generation, lastCursor)
			return
		}
		if !acpTaskStreamResolveRetryable(streamErr) || resumeCursor == "" {
			m.finishActiveFailure(anchor, generation, streamErr, resumeCursor != "", false)
			return
		}
		if !m.beginObservationResume(anchor.callID, generation) {
			m.closeObservation(anchor.callID, generation, lastCursor)
			return
		}

		next, used, resumeErr := m.resumeSubscriptionWithGrace(
			anchor.callID,
			generation,
			taskID,
			resumeCursor,
			anchor.kind == task.KindSubagent,
			acpTaskStreamResumeMaxAttempts,
		)
		if resumeErr != nil {
			if m.observationRetryStopped(anchor.callID, generation) {
				m.closeObservation(anchor.callID, generation, lastCursor)
				return
			}
			m.finishActiveFailure(anchor, generation, resumeErr, true, acpTaskStreamResolveRetryable(resumeErr) &&
				used >= acpTaskStreamResumeMaxAttempts)
			return
		}
		if !m.finishObservationResume(anchor.callID, generation) {
			_ = next.result.Subscription.Close()
			m.closeObservation(anchor.callID, generation, lastCursor)
			return
		}
		subscription = next.result.Subscription
	}
}

type acpTaskStreamAttachment struct {
	taskID string
	result taskstream.SubscribeResult
}

func (m *acpTaskStreamMux) forwardUntilClose(
	anchor acpTaskStreamAnchor,
	subscription taskstream.Subscription,
	generation *acpTaskStreamObservationGeneration,
) (string, error) {
	deliveryCtx := generation.ctx
	for {
		select {
		case <-deliveryCtx.Done():
			return strings.TrimSpace(subscription.LastCursor()), deliveryCtx.Err()
		case envelope, ok := <-subscription.Events():
			if !ok {
				return strings.TrimSpace(subscription.LastCursor()), subscription.Err()
			}
			if taskstream.IsTransientGapEnvelope(envelope) {
				continue
			}
			cursor := strings.TrimSpace(envelope.Cursor)
			if cursor == "" {
				cursor = strings.TrimSpace(subscription.LastCursor())
			}
			m.recordObservationCursor(anchor.callID, generation, cursor)
			if !acpTaskStreamEnvelopeAllowed(anchor, envelope) {
				continue
			}
			select {
			case <-deliveryCtx.Done():
				return strings.TrimSpace(subscription.LastCursor()), deliveryCtx.Err()
			case m.events <- envelope:
			}
			if anchor.kind == task.KindSubagent && envelope.Kind == eventstream.KindLifecycle && envelope.Lifecycle != nil {
				terminal := eventstream.IsTerminalLifecycleState(envelope.Lifecycle.State)
				sealed := m.observeSubagentActivityLifecycle(anchor.callID, generation, terminal)
				if terminal {
					// Parent Task read/wait projection may be waiting for this child
					// lifecycle before closing its mounted terminal. Signaling the
					// boundary does not end a live following subscription.
					m.signalGenerationBoundary(anchor.callID, generation.boundary)
				}
				if terminal && sealed {
					return strings.TrimSpace(subscription.LastCursor()), nil
				}
			}
		}
	}
}

func (m *acpTaskStreamMux) resolveSubscriptionWithGrace(
	anchor acpTaskStreamAnchor,
	generation *acpTaskStreamObservationGeneration,
) (acpTaskStreamAttachment, int, error) {
	return retryACPTaskStream(
		generation.ctx,
		acpTaskStreamResolveMaxAttempts,
		func() bool { return !m.observationRetryStopped(anchor.callID, generation) },
		func() (acpTaskStreamAttachment, error) {
			return m.resolveSubscription(anchor, generation)
		},
	)
}

func (m *acpTaskStreamMux) resolveSubscription(
	anchor acpTaskStreamAnchor,
	generation *acpTaskStreamObservationGeneration,
) (acpTaskStreamAttachment, error) {
	if taskID := strings.TrimSpace(anchor.taskID); taskID != "" {
		result, err := m.subscribeTaskStream(generation.ctx, taskID, generation.cursor, anchor.kind == task.KindSubagent)
		if err != nil {
			return acpTaskStreamAttachment{}, err
		}
		return acpTaskStreamAttachment{taskID: taskID, result: result}, nil
	}
	directory, err := m.client.List(generation.ctx, taskstream.ListRequest{SessionID: m.sessionID})
	if err != nil {
		return acpTaskStreamAttachment{}, err
	}
	var taskID string
	for _, descriptor := range directory.Tasks {
		if descriptor.Kind != anchor.kind || strings.TrimSpace(descriptor.ParentTool.ToolCallID) != anchor.callID ||
			descriptor.ParentTool.ToolName != taskStreamParentToolName(anchor.kind) {
			continue
		}
		if descriptorHandle := task.NormalizeHandle(descriptor.Handle); descriptorHandle != "" && descriptorHandle != task.NormalizeHandle(anchor.handle) {
			continue
		}
		if taskID != "" && taskID != strings.TrimSpace(descriptor.TaskID) {
			return acpTaskStreamAttachment{}, errorcode.New(
				errorcode.Conflict,
				fmt.Sprintf("multiple %s Tasks match the parent tool call", anchor.kind),
			)
		}
		taskID = strings.TrimSpace(descriptor.TaskID)
	}
	if taskID == "" {
		return acpTaskStreamAttachment{}, errorcode.New(errorcode.NotFound, "task is not discoverable yet")
	}
	result, err := m.subscribeTaskStream(generation.ctx, taskID, generation.cursor, anchor.kind == task.KindSubagent)
	if err != nil {
		return acpTaskStreamAttachment{}, err
	}
	return acpTaskStreamAttachment{taskID: taskID, result: result}, nil
}

func (m *acpTaskStreamMux) resumeSubscriptionWithGrace(
	callID string,
	generation *acpTaskStreamObservationGeneration,
	taskID string,
	cursor string,
	follow bool,
	maxAttempts int,
) (acpTaskStreamAttachment, int, error) {
	return retryACPTaskStream(
		generation.ctx,
		maxAttempts,
		func() bool { return !m.observationRetryStopped(callID, generation) },
		func() (acpTaskStreamAttachment, error) {
			result, err := m.subscribeTaskStream(generation.ctx, taskID, cursor, follow)
			if err != nil {
				return acpTaskStreamAttachment{}, err
			}
			return acpTaskStreamAttachment{taskID: taskID, result: result}, nil
		},
	)
}

func (m *acpTaskStreamMux) subscribeTaskStream(
	ctx context.Context,
	taskID string,
	cursor string,
	follow bool,
) (taskstream.SubscribeResult, error) {
	result, err := m.client.Subscribe(ctx, taskstream.SubscribeRequest{
		SessionID: m.sessionID,
		TaskID:    taskID,
		Cursor:    cursor,
		Follow:    follow,
	})
	if err != nil {
		return taskstream.SubscribeResult{}, err
	}
	if result.Subscription == nil {
		return taskstream.SubscribeResult{}, errorcode.New(errorcode.Unavailable, "task stream subscription was not created")
	}
	return result, nil
}

func (m *acpTaskStreamMux) finishResolveFailure(
	anchor acpTaskStreamAnchor,
	generation *acpTaskStreamObservationGeneration,
	err error,
	attempts int,
) {
	if m == nil {
		return
	}
	retryable := acpTaskStreamResolveRetryable(err)
	if !m.prepareResolveFailure(anchor.callID, generation) {
		return
	}
	m.reportNoticeOnce(generation, acpTaskStreamNoticeFacts{
		kind:           acpTaskStreamNoticeAttachFailed,
		anchor:         anchor,
		err:            err,
		retryExhausted: retryable && attempts >= acpTaskStreamResolveMaxAttempts,
	})
	m.completeResolveFailure(anchor.callID, generation, retryable)
}

func (m *acpTaskStreamMux) finishActiveFailure(
	anchor acpTaskStreamAnchor,
	generation *acpTaskStreamObservationGeneration,
	err error,
	hasCursor bool,
	resumeExhausted bool,
) {
	if !m.failActiveObservation(anchor.callID, generation) {
		return
	}
	m.reportNoticeOnce(generation, acpTaskStreamNoticeFacts{
		kind:            acpTaskStreamNoticeInterrupted,
		anchor:          anchor,
		err:             err,
		hasCursor:       hasCursor,
		resumeExhausted: resumeExhausted,
	})
}

func (m *acpTaskStreamMux) reportNoticeOnce(
	generation *acpTaskStreamObservationGeneration,
	facts acpTaskStreamNoticeFacts,
) {
	if m == nil || generation == nil || facts.err == nil || generation.ctx.Err() != nil {
		return
	}
	if !m.claimObservationNotice(facts.anchor.callID, generation, facts.kind) {
		return
	}
	notice := buildACPTaskStreamNotice(m.sessionID, facts)
	select {
	case <-generation.ctx.Done():
	case m.events <- notice:
	}
}

func (m *acpTaskStreamMux) signalBoundary(callID string) {
	if m == nil {
		return
	}
	boundary := m.observationBoundary(callID)
	if boundary == nil {
		return
	}
	m.signalGenerationBoundary(callID, boundary)
}

func (m *acpTaskStreamMux) signalGenerationBoundary(callID string, boundary chan struct{}) {
	if m == nil || boundary == nil {
		return
	}
	if m.beforeBoundarySignal != nil {
		m.beforeBoundarySignal(callID, boundary)
	}
	select {
	case boundary <- struct{}{}:
	default:
	}
}

func (m *acpTaskStreamMux) parentBoundary(callID string) <-chan struct{} {
	if m == nil {
		return nil
	}
	return m.observationBoundary(callID)
}

func (m *acpTaskStreamMux) Close() {
	if m == nil {
		return
	}
	m.Seal()
	m.cancel()
	m.wg.Wait()
	m.closeEvents()
}

// Seal prevents discovery of new terminal-backed Tasks. Command streams and a
// currently running subagent activity may finish; a following subagent stream
// already parked at an activity boundary is released immediately.
func (m *acpTaskStreamMux) Seal() {
	if m == nil {
		return
	}
	generations := m.sealObservations()
	m.mu.Lock()
	closeEvents := m.active == 0
	m.mu.Unlock()
	for _, generation := range generations {
		generation.cancel()
	}
	if closeEvents {
		m.closeEvents()
	}
}

func (m *acpTaskStreamMux) finishOperation() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.active > 0 {
		m.active--
	}
	closeEvents := m.sealed && m.active == 0
	m.mu.Unlock()
	if closeEvents {
		m.closeEvents()
	}
}

func (m *acpTaskStreamMux) closeEvents() {
	if m == nil {
		return
	}
	m.eventsOnce.Do(func() { close(m.events) })
}
