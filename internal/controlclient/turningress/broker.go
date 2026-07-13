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

	events  chan eventstream.Envelope
	batches chan []eventstream.Envelope
	done    chan struct{}

	startOnce sync.Once
	workers   sync.WaitGroup

	mu              sync.Mutex
	deliveryState   deliveryState
	cancelRequested bool
	sources         map[string]*source
	childRecorder   *internalcontrolclient.ChildRecorder
}

// The production stream subscription default is 100ms. The broker owns this
// poll timer so a main terminal can interrupt the wait and request one final
// Read without cancelling the task runtime.
const sourcePollInterval = 100 * time.Millisecond

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
		case env, ok := <-mainEvents:
			if !ok {
				b.finish(b.terminalForSourceEnd(failureReason, cancelled))
				return
			}
			mainScope := isMainFeedScope(env)
			if mainScope {
				if !b.identity.matches(env) {
					// The handle is one Turn source. A known foreign main identity
					// cannot enter this feed or become its terminal boundary.
					continue
				}
				if eventstream.IsTerminalLifecycle(env) {
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
	source := newSource(req, streams)
	b.sources[key] = source
	b.workers.Add(1)
	ctx := b.deliveryCtx
	b.mu.Unlock()

	go b.forwardSource(ctx, source)
}

func (b *Broker) forwardSource(ctx context.Context, source *source) {
	defer b.workers.Done()
	cursor := stream.CloneCursor(source.request.Cursor)
	for {
		finalRead := source.finalReadRequested()
		running, accepted, ok := b.readSourceSnapshot(ctx, source, &cursor)
		if !ok {
			return
		}
		if accepted && (finalRead || !running) {
			return
		}

		timer := time.NewTimer(sourcePollInterval)
		if finalRead {
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
				continue
			}
		}
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-source.finalRead:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			continue
		case <-timer.C:
		}
	}
}

// readSourceSnapshot accepts one complete runtime snapshot before advancing the
// source cursor. A source batch is intentionally indivisible: it preserves the
// source's raw frame order and ProjectStreamFrame's child-before-parent order.
func (b *Broker) readSourceSnapshot(ctx context.Context, source *source, cursor *stream.Cursor) (bool, bool, bool) {
	if b == nil || source == nil || source.service == nil || cursor == nil {
		return false, false, false
	}
	snapshot, err := source.service.Read(ctx, stream.ReadRequest{
		Ref:    source.request.Ref,
		Cursor: stream.CloneCursor(*cursor),
	})
	if err != nil || ctx.Err() != nil {
		return false, false, false
	}
	if !b.acceptSourceSnapshot(ctx, source.request, snapshot, cursor.Events) {
		return snapshot.Running, false, true
	}
	*cursor = stream.CloneCursor(snapshot.Cursor)
	return snapshot.Running, true, true
}

func (b *Broker) acceptSourceSnapshot(ctx context.Context, request acpprojector.StreamRequest, snapshot stream.Snapshot, afterEvents int64) bool {
	batch := make([]eventstream.Envelope, 0, len(snapshot.Frames)+1)
	for index, frame := range stream.FramesForSnapshot(snapshot) {
		if frame.Text == "" && frame.Event == nil && !frame.Closed {
			continue
		}
		if frame.Event != nil && b.childRecorder != nil {
			stored, err := b.childRecorder.Record(ctx, internalcontrolclient.ChildRecordRequest{
				SessionRef: request.SessionRef,
				Event:      frame.Event,
				Origin:     childOriginForStreamFrame(request, frame, afterEvents+int64(index)+1),
			})
			if err != nil {
				return false
			}
			frame.Event = stored
		}
		batch = append(batch, acpprojector.ProjectStreamFrame(request, frame)...)
	}
	if len(batch) == 0 {
		return true
	}
	select {
	case b.batches <- batch:
		return true
	case <-ctx.Done():
		return false
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
		SourceEventID: fmt.Sprintf("%s:%d", request.Key(), sourceSeq),
		ParentTool: session.EventParentTool{
			CallID: strings.TrimSpace(request.CallID),
			Name:   strings.TrimSpace(request.ToolName),
		},
	}
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

func (b *Broker) Cancel() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.cancelRequested = true
	b.mu.Unlock()
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

func (b *Broker) cancelWasRequested() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cancelRequested
}

// turnIdentity owns the feed's one transport identity policy. An ID is
// a mismatch only when both the handle and the source provide that ID and their
// values differ. This preserves legacy source events that omit transport IDs
// while rejecting a known foreign main-turn event.
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
	return expected == "" || actual == "" || expected == actual
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
