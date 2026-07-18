package controlclient

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

var (
	errFeedSubscriptionStopped             = errors.New("controlclient: feed subscription stopped")
	errDurableCheckpointBehindAcceptedFeed = errors.New("controlclient: durable checkpoint is behind the accepted feed")
)

type feedBackfillPlan struct {
	ctx            context.Context
	broker         *FeedBroker
	requested      eventstream.FeedPosition
	throughSeq     uint64
	startAcceptID  uint64
	ring           []feedRingItem
	firstPage      session.EventPage
	hasFirstPage   bool
	boundaryCursor string
}

// Subscribe prepares a bounded page+ring replay and returns before the full
// history is constructed. The worker splices into ordinary live fanout only
// after the captured prefix has been delivered.
func (b *FeedBroker) Subscribe(ctx context.Context, req controlport.SubscribeRequest) (controlport.SubscribeResult, error) {
	result, _, err := b.subscribeCheckpoint(ctx, req)
	return result, err
}

// subscribeCheckpoint also returns the durable Session cut used by reconnect
// state assembly. It is intentionally package-private so the public feed port
// remains presentation-neutral and compact.
func (b *FeedBroker) subscribeCheckpoint(
	ctx context.Context,
	req controlport.SubscribeRequest,
) (controlport.SubscribeResult, session.EventCheckpoint, error) {
	if b == nil {
		return controlport.SubscribeResult{}, session.EventCheckpoint{}, errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.SessionID) != b.ref.SessionID {
		return controlport.SubscribeResult{}, session.EventCheckpoint{}, eventstream.ErrCursorSessionMismatch
	}
	var requested eventstream.FeedPosition
	var err error
	if strings.TrimSpace(req.Cursor) != "" {
		requested, err = b.codec.Decode(b.ref.SessionID, req.Cursor)
		if err != nil {
			return controlport.SubscribeResult{}, session.EventCheckpoint{}, err
		}
	}

	checkpointReader, hasCheckpoint := b.reader.(session.EventCheckpointReader)
	if !hasCheckpoint {
		return b.subscribePrimedFallback(ctx, req, requested)
	}

	checkpoint, ring, startAcceptID, transientHistoryUnknown, err := b.captureCheckpoint(
		ctx,
		checkpointReader,
		requested,
	)
	if err != nil {
		return controlport.SubscribeResult{}, session.EventCheckpoint{}, err
	}

	result, plan, err := b.prepareBackfillPlan(ctx, req, requested, checkpoint, ring, startAcceptID, transientHistoryUnknown)
	if err != nil {
		return controlport.SubscribeResult{}, session.EventCheckpoint{}, err
	}
	b.mu.Lock()
	overtaken := b.evictedAccept > startAcceptID
	b.mu.Unlock()
	if overtaken {
		return controlport.SubscribeResult{}, session.EventCheckpoint{}, &controlport.FeedGapError{
			Cause:       errors.New("controlclient: reconnect splice was overtaken before subscription handoff"),
			RetryCursor: b.initialRetryCursor(req.Cursor),
			Mode:        controlport.ResumeModeDurableFallback, TransientGap: true,
		}
	}
	subscriber := newFeedSubscription(b, b.queueSize)
	subscriber.retryCursor = b.initialRetryCursor(req.Cursor)
	plan.boundaryCursor = result.BoundaryCursor
	subscriber.backfill = plan.run
	result.Subscription = subscriber
	subscriber.start()
	return result, checkpoint, nil
}

// captureCheckpoint observes the durable store cut and the broker acceptance
// boundary under the same durable sequencer used by Publish. A new broker can
// install the store high-water directly; a warm broker first publishes only
// its missing durable suffix so every accepted transient keeps its true place.
func (b *FeedBroker) captureCheckpoint(
	ctx context.Context,
	reader session.EventCheckpointReader,
	requested eventstream.FeedPosition,
) (session.EventCheckpoint, []feedRingItem, uint64, bool, error) {
	if err := b.lockPrime(ctx); err != nil {
		return session.EventCheckpoint{}, nil, 0, false, err
	}
	defer b.unlockPrime()

	checkpoint, err := reader.EventCheckpoint(ctx, b.ref)
	if err != nil {
		return session.EventCheckpoint{}, nil, 0, false, err
	}
	checkpoint.Session = session.CloneSession(checkpoint.Session)
	checkpoint.LastClientReplayEvent = session.CloneEvent(checkpoint.LastClientReplayEvent)
	if id := strings.TrimSpace(checkpoint.Session.SessionID); id != "" && id != b.ref.SessionID {
		return session.EventCheckpoint{}, nil, 0, false, eventstream.ErrCursorSessionMismatch
	}
	if requested.DurableAnchor().Seq > checkpoint.ThroughSeq {
		return session.EventCheckpoint{}, nil, 0, false, eventstream.ErrInvalidCursor
	}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return session.EventCheckpoint{}, nil, 0, false, errors.New("controlclient: feed broker is closed")
	}
	checkpointPosition := checkpointBoundaryPosition(b.ref, checkpoint.LastClientReplayEvent)
	if b.latestDurable.Seq > 0 && (checkpointPosition == nil || checkpointPosition.Durable == nil ||
		eventstream.CompareDurablePosition(b.latestDurable, *checkpointPosition.Durable) > 0) {
		b.mu.Unlock()
		return session.EventCheckpoint{}, nil, 0, false, errDurableCheckpointBehindAcceptedFeed
	}
	cold := b.acceptID == 0 && b.scannedSeq == 0 && len(b.ring) == 0 && len(b.subscribers) == 0
	if cold {
		b.installColdCheckpointLocked(checkpoint)
		b.evictLocked()
		ring := cloneFeedRing(b.ring)
		startAcceptID := b.acceptID
		unknown := b.transientHistoryUnknown
		b.mu.Unlock()
		return checkpoint, ring, startAcceptID, unknown, nil
	}
	b.mu.Unlock()

	if checkpoint.ThroughSeq > 0 {
		if _, err := b.primeStorageLocked(ctx, checkpoint.ThroughSeq, nil, nil); err != nil {
			return session.EventCheckpoint{}, nil, 0, false, err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return session.EventCheckpoint{}, nil, 0, false, errors.New("controlclient: feed broker is closed")
	}
	b.evictLocked()
	return checkpoint, cloneFeedRing(b.ring), b.acceptID, b.transientHistoryUnknown, nil
}

func (b *FeedBroker) installColdCheckpointLocked(checkpoint session.EventCheckpoint) {
	if checkpoint.ThroughSeq > b.scannedSeq {
		b.scannedSeq = checkpoint.ThroughSeq
	}
	position := checkpointBoundaryPosition(b.ref, checkpoint.LastClientReplayEvent)
	if position != nil && position.Durable != nil &&
		eventstream.CompareDurablePosition(*position.Durable, b.latestDurable) > 0 {
		b.latestDurable = *position.Durable
	}
	if checkpoint.ThroughSeq > 0 {
		// Durable storage cannot prove whether process-local frames existed before
		// this broker, even though its canonical prefix remains reconstructable.
		b.transientHistoryUnknown = true
	}
}

// subscribePrimedFallback preserves support for lightweight test/page readers
// that do not implement the atomic checkpoint contract. Production stores use
// the checkpoint path above and never scan full history before returning.
func (b *FeedBroker) subscribePrimedFallback(
	ctx context.Context,
	req controlport.SubscribeRequest,
	requested eventstream.FeedPosition,
) (controlport.SubscribeResult, session.EventCheckpoint, error) {
	if err := b.Prime(ctx); err != nil {
		return controlport.SubscribeResult{}, session.EventCheckpoint{}, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return controlport.SubscribeResult{}, session.EventCheckpoint{}, errors.New("controlclient: feed broker is closed")
	}
	b.evictLocked()
	ring := cloneFeedRing(b.ring)
	startAcceptID := b.acceptID
	unknown := b.transientHistoryUnknown
	throughSeq := b.scannedSeq
	b.mu.Unlock()
	checkpoint := session.EventCheckpoint{ThroughSeq: throughSeq}
	result, plan, err := b.prepareBackfillPlan(ctx, req, requested, checkpoint, ring, startAcceptID, unknown)
	if err != nil {
		return controlport.SubscribeResult{}, session.EventCheckpoint{}, err
	}
	subscriber := newFeedSubscription(b, b.queueSize)
	subscriber.retryCursor = b.initialRetryCursor(req.Cursor)
	plan.boundaryCursor = result.BoundaryCursor
	subscriber.backfill = plan.run
	result.Subscription = subscriber
	subscriber.start()
	return result, checkpoint, nil
}

func (b *FeedBroker) prepareBackfillPlan(
	ctx context.Context,
	req controlport.SubscribeRequest,
	requested eventstream.FeedPosition,
	checkpoint session.EventCheckpoint,
	ring []feedRingItem,
	startAcceptID uint64,
	transientHistoryUnknown bool,
) (controlport.SubscribeResult, *feedBackfillPlan, error) {
	exactIndex := findRingCursor(ring, req.Cursor)
	mode := controlport.ResumeModeExact
	transientGap := false
	selectedRing := ring
	switch {
	case strings.TrimSpace(req.Cursor) != "" && exactIndex >= 0:
		selectedRing = cloneFeedRing(ring[exactIndex+1:])
	case strings.TrimSpace(req.Cursor) != "":
		mode = controlport.ResumeModeDurableFallback
		transientGap = true
		// A missing transient cursor cannot prove which retained process-local
		// frames the caller already saw. Rebuild only durable truth and let the
		// captured post-cut ring splice provide the live continuation.
		selectedRing = nil
	default:
		transientGap = transientHistoryUnknown
	}

	boundaryPosition := checkpointBoundaryPosition(b.ref, checkpoint.LastClientReplayEvent)
	boundaryPosition = laterBoundaryPosition(boundaryPosition, feedBoundaryPosition(ring))
	boundaryCursor := ""
	if boundaryPosition != nil {
		var err error
		boundaryCursor, err = b.codec.Encode(b.ref.SessionID, *boundaryPosition)
		if err != nil {
			return controlport.SubscribeResult{}, nil, err
		}
	}

	plan := &feedBackfillPlan{
		ctx: ctx, broker: b, requested: requested, throughSeq: checkpoint.ThroughSeq,
		startAcceptID: startAcceptID, ring: selectedRing,
	}
	if checkpoint.ThroughSeq > 0 {
		if b.reader == nil {
			return controlport.SubscribeResult{}, nil, errors.New("controlclient: durable feed reader is unavailable")
		}
		pageRequest := durablePageRequest(b.ref, requested, checkpoint.ThroughSeq)
		page, err := b.reader.EventsPage(ctx, pageRequest)
		if err != nil {
			return controlport.SubscribeResult{}, nil, err
		}
		if err := validateBackfillPage(pageRequest, page); err != nil {
			return controlport.SubscribeResult{}, nil, err
		}
		plan.firstPage = page
		plan.hasFirstPage = true
	}
	return controlport.SubscribeResult{
		Mode: mode, TransientGap: transientGap, BoundaryCursor: boundaryCursor,
		BoundaryPosition: eventstream.CloneFeedPosition(boundaryPosition),
	}, plan, nil
}

func (p *feedBackfillPlan) run(subscriber *feedSubscription) error {
	if p == nil || p.broker == nil || subscriber == nil {
		return errors.New("controlclient: incomplete feed backfill plan")
	}
	iterator := newDurableEnvelopeIterator(p)
	current, hasCurrent, err := iterator.next()
	if err != nil {
		return p.gapError(subscriber, err)
	}
	for _, item := range p.ring {
		for hasCurrent && durableEnvelopeBeforeRing(current, item.envelope.Position) {
			if !subscriber.deliverBackfill(current) {
				return errFeedSubscriptionStopped
			}
			current, hasCurrent, err = iterator.next()
			if err != nil {
				return p.gapError(subscriber, err)
			}
		}
		if hasCurrent && sameDurablePosition(current.Position, item.envelope.Position) {
			current, hasCurrent, err = iterator.next()
			if err != nil {
				return p.gapError(subscriber, err)
			}
		}
		if !subscriber.deliverBackfill(eventstream.CloneEnvelope(item.envelope)) {
			return errFeedSubscriptionStopped
		}
	}
	for hasCurrent {
		if !subscriber.deliverBackfill(current) {
			return errFeedSubscriptionStopped
		}
		current, hasCurrent, err = iterator.next()
		if err != nil {
			return p.gapError(subscriber, err)
		}
	}

	live, err := p.broker.installPreparedSubscription(subscriber, p.startAcceptID, p.throughSeq)
	if err != nil {
		return p.gapError(subscriber, err)
	}
	// Install the live continuation before publishing the phase boundary. On
	// failure, the runner records the typed gap before it closes Backfill, so a
	// finite consumer cannot observe a clean end while the failure is pending.
	subscriber.finishBackfill()
	for _, envelope := range live {
		if !subscriber.deliver(envelope) {
			return errFeedSubscriptionStopped
		}
	}
	return nil
}

func (p *feedBackfillPlan) gapError(subscriber *feedSubscription, cause error) error {
	if errors.Is(cause, errFeedSubscriptionStopped) {
		return cause
	}
	retryCursor := p.boundaryCursor
	if subscriber != nil {
		retryCursor = firstNonEmptyCursor(subscriber.retryFrom(), retryCursor)
	}
	return &controlport.FeedGapError{
		Cause: cause, RetryCursor: retryCursor,
		Mode: controlport.ResumeModeDurableFallback, TransientGap: true,
	}
}

func (b *FeedBroker) installPreparedSubscription(
	subscriber *feedSubscription,
	startAcceptID uint64,
	throughSeq uint64,
) ([]eventstream.Envelope, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, errors.New("controlclient: feed broker is closed")
	}
	b.evictLocked()
	if b.evictedAccept > startAcceptID {
		return nil, errors.New("controlclient: reconnect live suffix exceeded the retained feed ring")
	}
	live := make([]eventstream.Envelope, 0, len(b.ring))
	for _, item := range b.ring {
		if item.acceptID <= startAcceptID {
			continue
		}
		if isDurableFeedEnvelope(item.envelope) && item.envelope.Position.Durable.Seq <= throughSeq {
			continue
		}
		live = append(live, eventstream.CloneEnvelope(item.envelope))
	}
	subscriber.ignoreDurableThrough = throughSeq
	b.subscribers[subscriber] = struct{}{}
	return live, nil
}

type durableEnvelopeIterator struct {
	plan      *feedBackfillPlan
	page      session.EventPage
	pageReady bool
	envelopes []eventstream.Envelope
	index     int
	afterSeq  uint64
	done      bool
}

func newDurableEnvelopeIterator(plan *feedBackfillPlan) *durableEnvelopeIterator {
	it := &durableEnvelopeIterator{plan: plan}
	if plan != nil {
		it.afterSeq = durablePageRequest(plan.broker.ref, plan.requested, plan.throughSeq).AfterSeq
	}
	if plan != nil && plan.hasFirstPage {
		it.page = plan.firstPage
		it.pageReady = true
	}
	return it
}

func (it *durableEnvelopeIterator) next() (eventstream.Envelope, bool, error) {
	for {
		if it.index < len(it.envelopes) {
			envelope := it.envelopes[it.index]
			it.index++
			return eventstream.CloneEnvelope(envelope), true, nil
		}
		if it.done || it.plan == nil || it.plan.throughSeq == 0 {
			return eventstream.Envelope{}, false, nil
		}
		page, err := it.takePage()
		if err != nil {
			return eventstream.Envelope{}, false, err
		}
		request := session.EventPageRequest{AfterSeq: it.afterSeq, ThroughSeq: it.plan.throughSeq}
		if err := validateBackfillPage(request, page); err != nil {
			return eventstream.Envelope{}, false, err
		}
		it.envelopes, err = it.projectPage(page)
		if err != nil {
			return eventstream.Envelope{}, false, err
		}
		it.index = 0
		if page.NextSeq > it.afterSeq {
			it.afterSeq = page.NextSeq
		}
		it.done = !page.HasMore || page.NextSeq >= it.plan.throughSeq
		if len(it.envelopes) == 0 && page.NextSeq == 0 {
			it.done = true
		}
	}
}

func (it *durableEnvelopeIterator) takePage() (session.EventPage, error) {
	if it.pageReady {
		it.pageReady = false
		return it.page, nil
	}
	request := session.EventPageRequest{
		SessionRef: it.plan.broker.ref, AfterSeq: it.afterSeq, ThroughSeq: it.plan.throughSeq,
		Visibility: session.EventPageClientReplay,
	}
	return it.plan.broker.reader.EventsPage(it.plan.ctx, request)
}

func (it *durableEnvelopeIterator) projectPage(page session.EventPage) ([]eventstream.Envelope, error) {
	out := make([]eventstream.Envelope, 0, len(page.Events))
	for _, event := range page.Events {
		base := acpprojector.EnvelopeBaseFromSessionEvent(it.plan.broker.ref, event, acpprojector.SessionEventTransport{})
		for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
			if envelope.Position == nil || envelope.Position.Durable == nil ||
				!durablePositionAfterRequested(*envelope.Position.Durable, it.plan.requested) {
				continue
			}
			cursor, err := it.plan.broker.codec.Encode(it.plan.broker.ref.SessionID, *envelope.Position)
			if err != nil {
				return nil, err
			}
			envelope.Cursor = cursor
			out = append(out, envelope)
		}
	}
	return out, nil
}

func durablePageRequest(ref session.SessionRef, requested eventstream.FeedPosition, throughSeq uint64) session.EventPageRequest {
	afterSeq := requested.DurableAnchor().Seq
	if requested.Durable != nil && afterSeq > 0 {
		afterSeq--
	}
	return session.EventPageRequest{
		SessionRef: ref, AfterSeq: afterSeq, ThroughSeq: throughSeq,
		Visibility: session.EventPageClientReplay,
	}
}

func validateBackfillPage(request session.EventPageRequest, page session.EventPage) error {
	if page.NextSeq < request.AfterSeq {
		return errors.New("controlclient: durable backfill reader moved backward")
	}
	if request.ThroughSeq > 0 && page.NextSeq > request.ThroughSeq {
		return errors.New("controlclient: durable backfill reader exceeded checkpoint")
	}
	if page.NextSeq == request.AfterSeq && request.AfterSeq < request.ThroughSeq {
		return errors.New("controlclient: durable backfill reader did not advance to checkpoint")
	}
	return nil
}

func durablePositionAfterRequested(position eventstream.DurableFeedPosition, requested eventstream.FeedPosition) bool {
	switch {
	case requested.Durable != nil:
		return eventstream.CompareDurablePosition(position, *requested.Durable) > 0
	case requested.Transient != nil:
		return eventstream.CompareDurablePosition(position, requested.Transient.Anchor) > 0
	default:
		return true
	}
}

func checkpointBoundaryPosition(ref session.SessionRef, event *session.Event) *eventstream.FeedPosition {
	if event == nil {
		return nil
	}
	base := acpprojector.EnvelopeBaseFromSessionEvent(ref, event, acpprojector.SessionEventTransport{})
	projected := acpprojector.ProjectSessionEventEnvelope(base, event)
	for index := len(projected) - 1; index >= 0; index-- {
		if projected[index].Position != nil && projected[index].Position.Durable != nil {
			return eventstream.CloneFeedPosition(projected[index].Position)
		}
	}
	return nil
}

func feedBoundaryPosition(ring []feedRingItem) *eventstream.FeedPosition {
	if len(ring) == 0 {
		return nil
	}
	return eventstream.CloneFeedPosition(ring[len(ring)-1].envelope.Position)
}

func laterBoundaryPosition(left, right *eventstream.FeedPosition) *eventstream.FeedPosition {
	if left == nil {
		return eventstream.CloneFeedPosition(right)
	}
	if right == nil {
		return eventstream.CloneFeedPosition(left)
	}
	leftAnchor := left.DurableAnchor()
	rightAnchor := right.DurableAnchor()
	comparison := eventstream.CompareDurablePosition(leftAnchor, rightAnchor)
	if comparison > 0 || (comparison == 0 && left.Transient != nil && right.Transient == nil) {
		return eventstream.CloneFeedPosition(left)
	}
	return eventstream.CloneFeedPosition(right)
}

func durableEnvelopeBeforeRing(envelope eventstream.Envelope, ringPosition *eventstream.FeedPosition) bool {
	if envelope.Position == nil || envelope.Position.Durable == nil || ringPosition == nil {
		return false
	}
	switch {
	case ringPosition.Durable != nil:
		return eventstream.CompareDurablePosition(*envelope.Position.Durable, *ringPosition.Durable) < 0
	case ringPosition.Transient != nil:
		return eventstream.CompareDurablePosition(*envelope.Position.Durable, ringPosition.Transient.Anchor) <= 0
	default:
		return false
	}
}

func sameDurablePosition(left, right *eventstream.FeedPosition) bool {
	return left != nil && right != nil && left.Durable != nil && right.Durable != nil &&
		eventstream.CompareDurablePosition(*left.Durable, *right.Durable) == 0
}

func firstNonEmptyCursor(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (b *FeedBroker) initialRetryCursor(requested string) string {
	if requested = strings.TrimSpace(requested); requested != "" {
		return requested
	}
	if b == nil || b.codec == nil {
		return ""
	}
	cursor, _ := b.codec.Encode(b.ref.SessionID, eventstream.FeedPosition{
		Durable: &eventstream.DurableFeedPosition{},
	})
	return cursor
}
