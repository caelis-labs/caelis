package controladapter

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

// liveFeedBroker owns the transient client-delivery side of one gateway turn.
// It never owns the task runtime: cancelling its delivery context stops only
// its Read delivery workers, never a Spawn or RunCommand task.
type liveFeedBroker struct {
	handle   gateway.TurnHandle
	identity liveFeedTurnIdentity
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
	deliveryState   liveFeedDeliveryState
	cancelRequested bool
	sources         map[string]*liveFeedSource
}

// The production stream subscription default is 100ms. The broker owns this
// poll timer so a main terminal can interrupt the wait and request one final
// Read without cancelling the task runtime.
const liveFeedSourcePollInterval = 100 * time.Millisecond

type liveFeedDeliveryState uint8

const (
	liveFeedDeliveryLive liveFeedDeliveryState = iota
	liveFeedDeliveryFinalizing
	liveFeedDeliveryStopped
)

type liveFeedSource struct {
	request   acpprojector.StreamRequest
	service   stream.Service
	finalRead chan struct{}
	finalOnce sync.Once
}

func newLiveFeedSource(request acpprojector.StreamRequest, service stream.Service) *liveFeedSource {
	return &liveFeedSource{
		request:   request,
		service:   service,
		finalRead: make(chan struct{}),
	}
}

func (s *liveFeedSource) requestFinalRead() {
	if s == nil {
		return
	}
	s.finalOnce.Do(func() { close(s.finalRead) })
}

func (s *liveFeedSource) finalReadRequested() bool {
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

func newGatewayTurn(handle gateway.TurnHandle, streams func() stream.Service) *gatewayTurn {
	return &gatewayTurn{
		handle: handle,
		feed:   newLiveFeedBroker(handle, streams),
	}
}

func (d *Adapter) newGatewayTurn(handle gateway.TurnHandle) *gatewayTurn {
	return newGatewayTurn(handle, func() stream.Service {
		provider, err := d.gatewayStreams()
		if err != nil || provider == nil {
			return nil
		}
		return provider.Streams()
	})
}

func newLiveFeedBroker(handle gateway.TurnHandle, streams func() stream.Service) *liveFeedBroker {
	ctx, cancel := context.WithCancel(context.Background())
	deliveryCtx, deliveryCancel := context.WithCancel(ctx)
	return &liveFeedBroker{
		handle:         handle,
		identity:       newLiveFeedTurnIdentity(handle),
		streams:        streams,
		ctx:            ctx,
		cancel:         cancel,
		deliveryCtx:    deliveryCtx,
		deliveryCancel: deliveryCancel,
		events:         make(chan eventstream.Envelope, 32),
		batches:        make(chan []eventstream.Envelope, 32),
		done:           make(chan struct{}),
		sources:        map[string]*liveFeedSource{},
	}
}

func (b *liveFeedBroker) Events() <-chan eventstream.Envelope {
	if b == nil {
		return eventstream.EnsureTerminalLifecycle(nil, "", "", "")
	}
	b.start()
	return b.events
}

func (b *liveFeedBroker) start() {
	if b == nil {
		return
	}
	b.startOnce.Do(func() {
		go b.run()
	})
}

func (b *liveFeedBroker) run() {
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
func (b *liveFeedBroker) finish(terminal eventstream.Envelope) {
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

func (b *liveFeedBroker) startSource(env eventstream.Envelope) {
	if b == nil || b.streams == nil {
		return
	}
	req, ok := streamRequestFromACPEvent(env)
	if !ok || req.Key() == "" {
		return
	}
	streams := b.streams()
	if streams == nil {
		return
	}

	key := req.Key()
	b.mu.Lock()
	if b.deliveryState != liveFeedDeliveryLive {
		b.mu.Unlock()
		return
	}
	if _, exists := b.sources[key]; exists {
		b.mu.Unlock()
		return
	}
	source := newLiveFeedSource(req, streams)
	b.sources[key] = source
	b.workers.Add(1)
	ctx := b.deliveryCtx
	b.mu.Unlock()

	go b.forwardSource(ctx, source)
}

func (b *liveFeedBroker) forwardSource(ctx context.Context, source *liveFeedSource) {
	defer b.workers.Done()
	cursor := stream.CloneCursor(source.request.Cursor)
	for {
		if source.finalReadRequested() {
			b.readSourceSnapshot(ctx, source, &cursor)
			return
		}
		running, ok := b.readSourceSnapshot(ctx, source, &cursor)
		if !ok || !running {
			return
		}

		timer := time.NewTimer(liveFeedSourcePollInterval)
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
			b.readSourceSnapshot(ctx, source, &cursor)
			return
		case <-timer.C:
		}
	}
}

// readSourceSnapshot accepts one complete runtime snapshot before advancing the
// source cursor. A source batch is intentionally indivisible: it preserves the
// source's raw frame order and ProjectStreamFrame's child-before-parent order.
func (b *liveFeedBroker) readSourceSnapshot(ctx context.Context, source *liveFeedSource, cursor *stream.Cursor) (bool, bool) {
	if b == nil || source == nil || source.service == nil || cursor == nil {
		return false, false
	}
	snapshot, err := source.service.Read(ctx, stream.ReadRequest{
		Ref:    source.request.Ref,
		Cursor: stream.CloneCursor(*cursor),
	})
	if err != nil || ctx.Err() != nil {
		return false, false
	}
	if !b.acceptSourceSnapshot(ctx, source.request, snapshot) {
		return false, false
	}
	*cursor = stream.CloneCursor(snapshot.Cursor)
	return snapshot.Running, true
}

func (b *liveFeedBroker) acceptSourceSnapshot(ctx context.Context, request acpprojector.StreamRequest, snapshot stream.Snapshot) bool {
	batch := make([]eventstream.Envelope, 0, len(snapshot.Frames)+1)
	for _, frame := range stream.FramesForSnapshot(snapshot) {
		if frame.Text == "" && frame.Event == nil && !frame.Closed {
			continue
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

func (b *liveFeedBroker) emitBatch(batch []eventstream.Envelope) bool {
	for _, env := range batch {
		if !b.emit(env) {
			return false
		}
	}
	return true
}

func (b *liveFeedBroker) emit(env eventstream.Envelope) bool {
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

func (b *liveFeedBroker) Cancel() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.cancelRequested = true
	b.mu.Unlock()
	b.stopLiveDelivery()
}

func (b *liveFeedBroker) Close() {
	if b == nil {
		return
	}
	b.start()
	b.cancel()
	b.stopLiveDelivery()
	<-b.done
}

func (b *liveFeedBroker) stopLiveDelivery() {
	if b == nil {
		return
	}
	b.stopLiveSources()
	b.workers.Wait()
}

func (b *liveFeedBroker) requestFinalSourceReads() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.deliveryState == liveFeedDeliveryLive {
		b.deliveryState = liveFeedDeliveryFinalizing
		for _, source := range b.sources {
			source.requestFinalRead()
		}
	}
	b.mu.Unlock()
}

func (b *liveFeedBroker) stopLiveSources() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.deliveryState != liveFeedDeliveryStopped {
		b.deliveryState = liveFeedDeliveryStopped
		b.deliveryCancel()
	}
	b.mu.Unlock()
}

func (b *liveFeedBroker) deliveryWorkersDone() <-chan struct{} {
	done := make(chan struct{})
	go func() {
		b.workers.Wait()
		close(done)
	}()
	return done
}

func (b *liveFeedBroker) terminalForSourceEnd(failureReason string, cancelled bool) eventstream.Envelope {
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

func (b *liveFeedBroker) cancelWasRequested() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cancelRequested
}

// liveFeedTurnIdentity owns the feed's one transport identity policy. An ID is
// a mismatch only when both the handle and the source provide that ID and their
// values differ. This preserves legacy source events that omit transport IDs
// while rejecting a known foreign main-turn event.
type liveFeedTurnIdentity struct {
	handleID string
	runID    string
	turnID   string
}

func newLiveFeedTurnIdentity(handle gateway.TurnHandle) liveFeedTurnIdentity {
	if handle == nil {
		return liveFeedTurnIdentity{}
	}
	return liveFeedTurnIdentity{
		handleID: strings.TrimSpace(handle.HandleID()),
		runID:    strings.TrimSpace(handle.RunID()),
		turnID:   strings.TrimSpace(handle.TurnID()),
	}
}

func (identity liveFeedTurnIdentity) matches(env eventstream.Envelope) bool {
	return identity.matchesID(identity.handleID, env.HandleID) &&
		identity.matchesID(identity.runID, env.RunID) &&
		identity.matchesID(identity.turnID, env.TurnID)
}

func (liveFeedTurnIdentity) matchesID(expected string, actual string) bool {
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
