package turningress

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	internalcontrolclient "github.com/caelis-labs/caelis/internal/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

// Broker owns the transient Control client-delivery side of one gateway turn.
// It never owns the task runtime: cancelling its delivery context stops only
// its Read delivery workers, never a Spawn or RunCommand task.
type Broker struct {
	handle   gateway.TurnHandle
	identity turnIdentity
	streams  func() stream.Service

	ctx    context.Context
	cancel context.CancelFunc

	deliveryCtx    context.Context
	deliveryCancel context.CancelFunc

	events   chan eventstream.Envelope
	batches  chan []eventstream.Envelope
	failures chan error
	done     chan struct{}

	startOnce sync.Once
	workers   sync.WaitGroup

	mu              sync.Mutex
	deliveryState   deliveryState
	cancelRequested bool
	stopState       string
	stopReason      string
	sources         map[string]*source
	childRecorder   *internalcontrolclient.ChildRecorder
	deliveryErr     error
	producerCancel  sync.Once
	finalMu         sync.RWMutex
	finalError      *eventstream.Envelope
	finalTerminal   *eventstream.Envelope
}

// The production stream subscription default is 100ms. The broker owns this
// poll timer so a main terminal can interrupt the wait and request one final
// Read without cancelling the task runtime.
const (
	sourcePollInterval           = 100 * time.Millisecond
	sourceReadTimeout            = 2 * time.Second
	sourceMaxConsecutiveFailures = 3
)

type deliveryState uint8

const (
	deliveryLive deliveryState = iota
	deliveryFinalizing
	deliveryStopped
)

type source struct {
	request   acpprojector.StreamRequest
	service   stream.Service
	finalRead chan struct{}
	finalOnce sync.Once
}

func newSource(request acpprojector.StreamRequest, service stream.Service) *source {
	return &source{
		request:   request,
		service:   service,
		finalRead: make(chan struct{}),
	}
}

func (s *source) requestFinalRead() {
	if s == nil {
		return
	}
	s.finalOnce.Do(func() { close(s.finalRead) })
}

func (s *source) finalReadRequested() bool {
	if s == nil {
		return false
	}
	select {
	case <-s.finalRead:
		return true
	default:
		return false
	}
}

// New constructs one shared gateway/task ingress for a request-scoped Control turn.
func New(handle gateway.TurnHandle, streams func() stream.Service, recorders ...*internalcontrolclient.ChildRecorder) *Broker {
	ctx, cancel := context.WithCancel(context.Background())
	deliveryCtx, deliveryCancel := context.WithCancel(ctx)
	broker := &Broker{
		handle:         handle,
		identity:       newTurnIdentity(handle),
		streams:        streams,
		ctx:            ctx,
		cancel:         cancel,
		deliveryCtx:    deliveryCtx,
		deliveryCancel: deliveryCancel,
		events:         make(chan eventstream.Envelope, 32),
		batches:        make(chan []eventstream.Envelope, 32),
		failures:       make(chan error, 1),
		done:           make(chan struct{}),
		sources:        map[string]*source{},
	}
	if len(recorders) > 0 && recorders[0] != nil {
		broker.childRecorder = recorders[0]
	}
	return broker
}

// Done is closed after the broker has stopped and its event channel is closed.
func (b *Broker) Done() <-chan struct{} {
	if b == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	b.start()
	return b.done
}

// SourceCount reports the number of task stream sources accepted by the broker.
func (b *Broker) SourceCount() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sources)
}

// Events starts the broker and returns its merged ACP envelope stream.
func (b *Broker) Events() <-chan eventstream.Envelope {
	if b == nil {
		return eventstream.EnsureTerminalLifecycle(nil, "", "", "")
	}
	b.start()
	return b.events
}

func (b *Broker) start() {
	if b == nil {
		return
	}
	b.startOnce.Do(func() {
		go b.run()
	})
}

func (b *Broker) run() {
	defer close(b.done)
	defer close(b.events)
	defer b.cancel()

	if b.handle == nil {
		b.finish(b.terminalForSourceEnd("", false))
		return
	}

	mainEvents := b.handle.ACPEvents()
	if mainEvents == nil {
		b.finish(b.terminalForSourceEnd("", false))
		return
	}

	failureReason := ""
	cancelled := false
	for {
		select {
		case <-b.ctx.Done():
			b.stopLiveDelivery()
			return
		case batch := <-b.batches:
			if !b.emitBatch(batch) {
				b.stopLiveDelivery()
				return
			}
		case err := <-b.failures:
			failureReason = errorText(err)
			b.RequestStop(eventstream.LifecycleStateFailed, failureReason)
			b.cancelOwningProducer()
			// A task delivery failure is fatal, but it is not the Runtime
			// producer-close barrier. Continue draining ACPEvents until close so
			// the execution lease cannot outlive the failed terminal.
		case env, ok := <-mainEvents:
			if !ok {
				b.finish(b.terminalForSourceEnd(failureReason, cancelled))
				return
			}
			mainScope := isMainFeedScope(env)
			if mainScope {
				env = b.stampMainEnvelope(env)
				if !b.identity.matches(env) {
					// The handle is one Turn source. A known foreign main identity
					// cannot enter this feed or become its terminal boundary.
					continue
				}
				if eventstream.IsTerminalLifecycle(env) {
					if b.deliveryFailure() != nil {
						// Runtime may report cancellation before its producer exits.
						// Preserve the source failure and wait for channel close.
						continue
					}
					b.finish(env)
					return
				}
				if env.Err != nil || env.Kind == eventstream.KindError {
					failureReason = strings.TrimSpace(firstNonEmpty(env.Error, errorText(env.Err)))
					cancelled = eventstream.IsCancelledReason(failureReason)
				}
			}
			if !b.emit(env) {
				b.stopLiveDelivery()
				return
			}
			// The main tool update is visible before this begins its task delivery.
			if mainScope {
				b.startSource(env)
			}
		}
	}
}

// finish prevents new source workers, requests one immediate final Read from
// each existing source, drains every batch those reads accepted, and only then
// emits the main terminal. This makes the terminal a deterministic final feed
// frame without guessing at scheduling delays or cancelling task runtime.
func (b *Broker) finish(terminal eventstream.Envelope) {
	b.requestFinalSourceReads()
	workersDone := b.deliveryWorkersDone()
	for {
		select {
		case <-b.ctx.Done():
			return
		case batch := <-b.batches:
			if !b.emitBatch(batch) {
				return
			}
		case <-workersDone:
			for {
				select {
				case batch := <-b.batches:
					if !b.emitBatch(batch) {
						return
					}
				default:
					// All source workers acknowledged their final Read. End only
					// broker-owned delivery now; this context does not own task
					// runtime execution.
					b.stopLiveSources()
					if deliveryErr := b.deliveryFailure(); deliveryErr != nil {
						failure := b.stampMainEnvelope(eventstream.Error(deliveryErr))
						b.recordFinalError(failure)
						b.emit(failure)
						terminal = b.terminalForSourceEnd(deliveryErr.Error(), false)
					}
					terminal = b.stampMainEnvelope(terminal)
					b.recordFinalTerminal(terminal)
					b.emit(terminal)
					return
				}
			}
		}
	}
}

func (b *Broker) startSource(env eventstream.Envelope) {
	if b == nil || b.streams == nil {
		return
	}
	req, ok := StreamRequestFromACPEvent(env)
	if !ok || req.Key() == "" {
		return
	}
	streams := b.streams()
	if streams == nil {
		return
	}

	key := req.Key()
	b.mu.Lock()
	if b.deliveryState != deliveryLive {
		b.mu.Unlock()
		return
	}
	if _, exists := b.sources[key]; exists {
		b.mu.Unlock()
		return
	}
	// The first TASK wait seen by a later Turn is the new delivery owner for
	// the already-running physical source. Preserve the original parent tool
	// identity so child semantics close the Spawn panel and command bytes keep
	// using the RunCommand terminal. A same-Turn observer never reaches this
	// promotion because the stable source key above already exists.
	req = promoteObserverSource(req)
	source := newSource(req, streams)
	b.sources[key] = source
	b.workers.Add(1)
	ctx := b.deliveryCtx
	b.mu.Unlock()

	go b.forwardSource(ctx, source)
}

func promoteObserverSource(req acpprojector.StreamRequest) acpprojector.StreamRequest {
	if !req.Observer {
		return req
	}
	parentTool := firstNonEmpty(strings.TrimSpace(req.ParentToolName), parentToolForTaskKind(req.TargetKind))
	if parentTool == "" {
		return req
	}
	parentCall := strings.TrimSpace(req.ParentCallID)
	if parentCall == "" {
		return req
	}
	req.CallID = parentCall
	req.ToolName = parentTool
	req.DisplayTerminalID = firstNonEmpty(parentCall, strings.TrimSpace(req.DisplayTerminalID))
	req.Cursor = stream.Cursor{}
	req.Observer = false
	// TASK's action/wait input describes the observer call, not the physical
	// Spawn or RunCommand source being projected.
	req.RawInput = nil
	return req
}

func (b *Broker) forwardSource(ctx context.Context, source *source) {
	defer b.workers.Done()
	cursor := stream.CloneCursor(source.request.Cursor)
	consecutiveFailures := 0
	for {
		finalRead := source.finalReadRequested()
		running, accepted, err := b.readSourceSnapshot(ctx, source, &cursor)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			consecutiveFailures++
			if consecutiveFailures >= sourceMaxConsecutiveFailures {
				b.reportDeliveryFailure(fmt.Errorf("task stream %q delivery failed after %d attempts: %w", source.request.Key(), consecutiveFailures, err))
				return
			}
			if !waitSourcePoll(ctx, source, finalRead) {
				return
			}
			continue
		}
		consecutiveFailures = 0
		if accepted && (finalRead || !running) {
			return
		}
		if !waitSourcePoll(ctx, source, finalRead) {
			return
		}
	}
}

func waitSourcePoll(ctx context.Context, source *source, finalRead bool) bool {
	timer := time.NewTimer(sourcePollInterval)
	defer timer.Stop()
	if finalRead {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	}
	select {
	case <-ctx.Done():
		return false
	case <-source.finalRead:
		return true
	case <-timer.C:
		return true
	}
}

// readSourceSnapshot accepts one complete runtime snapshot before advancing the
// source cursor. A source batch is intentionally indivisible: it preserves the
// source's raw frame order and ProjectStreamFrame's child-before-parent order.
func (b *Broker) readSourceSnapshot(ctx context.Context, source *source, cursor *stream.Cursor) (bool, bool, error) {
	if b == nil || source == nil || source.service == nil || cursor == nil {
		return false, false, fmt.Errorf("turningress: task stream source is unavailable")
	}
	readCtx, cancel := context.WithTimeout(ctx, sourceReadTimeout)
	defer cancel()
	snapshot, err := source.service.Read(readCtx, stream.ReadRequest{
		Ref:    source.request.Ref,
		Cursor: stream.CloneCursor(*cursor),
	})
	if err != nil {
		return false, false, err
	}
	if ctx.Err() != nil {
		return false, false, ctx.Err()
	}
	if err := b.acceptSourceSnapshot(ctx, source.request, snapshot, cursor.Events); err != nil {
		return snapshot.Running, false, err
	}
	*cursor = stream.CloneCursor(snapshot.Cursor)
	return snapshot.Running, true, nil
}

func (b *Broker) acceptSourceSnapshot(ctx context.Context, request acpprojector.StreamRequest, snapshot stream.Snapshot, afterEvents int64) error {
	frames := stream.FramesForSnapshot(snapshot)
	if b.childRecorder != nil {
		records := make([]internalcontrolclient.ChildRecordRequest, 0, len(frames))
		indexes := make([]int, 0, len(frames))
		for index, frame := range frames {
			if frame.Event == nil {
				continue
			}
			sourceSeq := afterEvents + int64(index) + 1
			records = append(records, internalcontrolclient.ChildRecordRequest{
				SessionRef:            request.SessionRef,
				Event:                 frame.Event,
				Origin:                childOriginForStreamFrame(request, frame, sourceSeq),
				FallbackSourceEventID: childFallbackSourceEventID(request, sourceSeq),
			})
			indexes = append(indexes, index)
		}
		if len(records) > 0 {
			stored, err := b.childRecorder.RecordBatch(ctx, records)
			if err != nil {
				return err
			}
			if len(stored) != len(indexes) {
				return fmt.Errorf("turningress: child recorder returned %d events for %d frames", len(stored), len(indexes))
			}
			for index, frameIndex := range indexes {
				frames[frameIndex].Event = stored[index]
			}
		}
	}

	batch := make([]eventstream.Envelope, 0, len(frames))
	for _, frame := range frames {
		if frame.Text == "" && frame.Event == nil && !frame.Closed {
			continue
		}
		batch = append(batch, acpprojector.ProjectStreamFrame(request, frame)...)
	}
	if len(batch) == 0 {
		return nil
	}
	select {
	case b.batches <- batch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func childOriginForStreamFrame(request acpprojector.StreamRequest, frame stream.Frame, sourceSeq int64) session.EventChildOrigin {
	taskID := firstNonEmpty(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(request.Ref.TaskID))
	scopeID := taskID
	participantID := strings.TrimSpace(request.ParticipantID)
	acpSessionID := ""
	if request.Origin != nil {
		participantID = firstNonEmpty(participantID, strings.TrimSpace(request.Origin.ParticipantID))
		acpSessionID = strings.TrimSpace(request.Origin.ParticipantSessionID)
	}
	if frame.Event != nil && frame.Event.Scope != nil {
		participantID = firstNonEmpty(participantID, strings.TrimSpace(frame.Event.Scope.Participant.ID))
		acpSessionID = firstNonEmpty(acpSessionID, strings.TrimSpace(frame.Event.Scope.ACP.SessionID))
	}
	return session.EventChildOrigin{
		Scope:         session.EventChildScopeSubagent,
		ScopeID:       scopeID,
		TaskID:        taskID,
		DelegationID:  firstNonEmpty(taskID, scopeID),
		ParticipantID: participantID,
		ACPSessionID:  acpSessionID,
		// Keep the pre-physical-source identity as the primary durable key so
		// mirrors written by released versions dedupe during upgrade replay.
		// ChildRecorder switches to the physical-source fallback only when this
		// legacy position collides with a genuinely different continuation.
		SourceEventID: fmt.Sprintf("%s:%d", legacyChildSourceKey(request), sourceSeq),
		ParentTool: session.EventParentTool{
			CallID: firstNonEmpty(strings.TrimSpace(request.ParentCallID), strings.TrimSpace(request.CallID)),
			Name:   firstNonEmpty(strings.TrimSpace(request.ParentToolName), strings.TrimSpace(request.ToolName)),
		},
	}
}

func legacyChildSourceKey(request acpprojector.StreamRequest) string {
	return strings.Join([]string{
		strings.TrimSpace(request.SessionRef.SessionID),
		strings.TrimSpace(request.Ref.TaskID),
		strings.TrimSpace(request.Ref.TerminalID),
		strings.TrimSpace(request.CallID),
	}, "|")
}

func childFallbackSourceEventID(request acpprojector.StreamRequest, sourceSeq int64) string {
	legacy := legacyChildSourceKey(request)
	physical := request.Key()
	if physical == "" || physical == legacy {
		return ""
	}
	return fmt.Sprintf("%s:%d", physical, sourceSeq)
}

func (b *Broker) emitBatch(batch []eventstream.Envelope) bool {
	for _, env := range batch {
		if !b.emit(env) {
			return false
		}
	}
	return true
}

func (b *Broker) emit(env eventstream.Envelope) bool {
	if b == nil {
		return false
	}
	select {
	case b.events <- eventstream.CloneEnvelope(env):
		return true
	case <-b.ctx.Done():
		return false
	}
}

func (b *Broker) reportDeliveryFailure(err error) {
	if b == nil || err == nil {
		return
	}
	b.mu.Lock()
	first := false
	if b.deliveryErr == nil {
		b.deliveryErr = err
		first = true
	}
	b.mu.Unlock()
	if !first {
		return
	}
	select {
	case b.failures <- err:
	case <-b.ctx.Done():
	default:
	}
}

func (b *Broker) deliveryFailure() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.deliveryErr
}

func (b *Broker) stampMainEnvelope(env eventstream.Envelope) eventstream.Envelope {
	if b == nil || !isMainFeedScope(env) {
		return env
	}
	env = eventstream.CloneEnvelope(env)
	env.Scope = eventstream.ScopeMain
	if b.handle != nil {
		env.SessionID = firstNonEmpty(strings.TrimSpace(env.SessionID), strings.TrimSpace(b.handle.SessionRef().SessionID))
		env.ScopeID = firstNonEmpty(strings.TrimSpace(env.ScopeID), strings.TrimSpace(b.handle.SessionRef().SessionID))
	}
	env.HandleID = firstNonEmpty(strings.TrimSpace(env.HandleID), b.identity.handleID)
	env.RunID = firstNonEmpty(strings.TrimSpace(env.RunID), b.identity.runID)
	env.TurnID = firstNonEmpty(strings.TrimSpace(env.TurnID), b.identity.turnID)
	return env
}

// RequestCancel records the owning Turn's cancellation without stopping feed
// delivery. The broker must continue draining its handle through the main
// producer-close barrier before clients may observe the cancelled terminal.
func (b *Broker) RequestCancel() {
	b.RequestStop(eventstream.LifecycleStateCancelled, "")
}

// RequestStop records the terminal outcome to publish after the Runtime
// producer-close and final-source barriers. It does not stop feed delivery or
// own Runtime cancellation.
func (b *Broker) RequestStop(state string, reason string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.stopState == "" {
		b.stopState = strings.TrimSpace(state)
		b.stopReason = strings.TrimSpace(reason)
	}
	b.cancelRequested = b.stopState == eventstream.LifecycleStateCancelled
	b.mu.Unlock()
}

// Cancel records cancellation and stops only this broker's live delivery.
// Prefer RequestCancel when a caller must retain the producer-close barrier.
func (b *Broker) Cancel() {
	if b == nil {
		return
	}
	b.RequestCancel()
	b.stopLiveDelivery()
}

func (b *Broker) Close() {
	if b == nil {
		return
	}
	b.start()
	b.cancel()
	b.stopLiveDelivery()
	<-b.done
}

func (b *Broker) stopLiveDelivery() {
	if b == nil {
		return
	}
	b.stopLiveSources()
	b.workers.Wait()
}

func (b *Broker) requestFinalSourceReads() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.deliveryState == deliveryLive {
		b.deliveryState = deliveryFinalizing
		for _, source := range b.sources {
			source.requestFinalRead()
		}
	}
	b.mu.Unlock()
}

func (b *Broker) stopLiveSources() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.deliveryState != deliveryStopped {
		b.deliveryState = deliveryStopped
		b.deliveryCancel()
	}
	b.mu.Unlock()
}

func (b *Broker) deliveryWorkersDone() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		b.workers.Wait()
		close(done)
	}()
	return done
}

func (b *Broker) terminalForSourceEnd(failureReason string, cancelled bool) eventstream.Envelope {
	if deliveryErr := b.deliveryFailure(); deliveryErr != nil {
		return eventstream.TurnFailed(b.identity.handleID, b.identity.runID, b.identity.turnID, deliveryErr.Error(), time.Now())
	}
	if state, reason := b.requestedStop(); state != "" {
		if reason == "" {
			reason = failureReason
		}
		return eventstream.TurnLifecycle(
			b.identity.handleID, b.identity.runID, b.identity.turnID,
			state, reason, "", time.Now(),
		)
	}
	if b.cancelWasRequested() {
		cancelled = true
	}
	switch {
	case cancelled:
		return eventstream.TurnCancelled(b.identity.handleID, b.identity.runID, b.identity.turnID, failureReason, time.Now())
	case strings.TrimSpace(failureReason) != "":
		return eventstream.TurnFailed(b.identity.handleID, b.identity.runID, b.identity.turnID, failureReason, time.Now())
	default:
		return eventstream.TurnCompleted(b.identity.handleID, b.identity.runID, b.identity.turnID, time.Now())
	}
}

func (b *Broker) requestedStop() (string, string) {
	if b == nil {
		return "", ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopState, b.stopReason
}

func (b *Broker) cancelOwningProducer() {
	if b == nil || b.handle == nil {
		return
	}
	b.producerCancel.Do(func() { _ = b.handle.Cancel() })
}

// CancelProducer idempotently cancels this Turn's owning Runtime handle. It is
// shared by gateway cancellation and source-delivery failure so concurrent
// stop causes cannot issue duplicate physical cancellation.
func (b *Broker) CancelProducer() {
	b.cancelOwningProducer()
}

// FinalEnvelopes returns the broker-authored terminal outcome after its
// producer and final-source barriers. Callers normally use it after Events has
// closed; before then it may return an empty slice.
func (b *Broker) FinalEnvelopes() []eventstream.Envelope {
	if b == nil {
		return nil
	}
	b.finalMu.RLock()
	defer b.finalMu.RUnlock()
	result := make([]eventstream.Envelope, 0, 2)
	if b.finalError != nil {
		result = append(result, eventstream.CloneEnvelope(*b.finalError))
	}
	if b.finalTerminal != nil {
		result = append(result, eventstream.CloneEnvelope(*b.finalTerminal))
	}
	return result
}

func (b *Broker) recordFinalError(envelope eventstream.Envelope) {
	b.finalMu.Lock()
	clone := eventstream.CloneEnvelope(envelope)
	b.finalError = &clone
	b.finalMu.Unlock()
}

func (b *Broker) recordFinalTerminal(envelope eventstream.Envelope) {
	b.finalMu.Lock()
	clone := eventstream.CloneEnvelope(envelope)
	b.finalTerminal = &clone
	b.finalMu.Unlock()
}

func (b *Broker) cancelWasRequested() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cancelRequested
}

// turnIdentity owns the feed's one transport identity policy. Broker ingress
// stamps missing IDs from its authoritative handle before matching; consumers
// can therefore reject unstamped historical lifecycle frames strictly.
type turnIdentity struct {
	handleID string
	runID    string
	turnID   string
}

func newTurnIdentity(handle gateway.TurnHandle) turnIdentity {
	if handle == nil {
		return turnIdentity{}
	}
	return turnIdentity{
		handleID: strings.TrimSpace(handle.HandleID()),
		runID:    strings.TrimSpace(handle.RunID()),
		turnID:   strings.TrimSpace(handle.TurnID()),
	}
}

func (identity turnIdentity) matches(env eventstream.Envelope) bool {
	return identity.matchesID(identity.handleID, env.HandleID) &&
		identity.matchesID(identity.runID, env.RunID) &&
		identity.matchesID(identity.turnID, env.TurnID)
}

func (turnIdentity) matchesID(expected string, actual string) bool {
	expected = strings.TrimSpace(expected)
	actual = strings.TrimSpace(actual)
	return expected == "" || expected == actual
}

func isMainFeedScope(env eventstream.Envelope) bool {
	return env.Scope == "" || env.Scope == eventstream.ScopeMain
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
