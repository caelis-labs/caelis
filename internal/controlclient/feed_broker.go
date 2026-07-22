package controlclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const (
	defaultRingEvents             = 1024
	defaultRingBytes              = 4 << 20
	defaultRingTTL                = 15 * time.Minute
	defaultSubscriberSize         = 128
	defaultSubscriberStallTimeout = 5 * time.Second
)

// FeedBrokerConfig configures one Session-scoped multi-subscriber broker.
type FeedBrokerConfig struct {
	SessionRef      session.SessionRef
	Reader          session.PagedReader
	CursorCodec     *eventstream.CursorCodec
	RingEvents      int
	RingBytes       int
	RingTTL         time.Duration
	SubscriberQueue int
	// SubscriberStallTimeout bounds how long an AttachTo ingress may wait for
	// its prepared Surface subscription. Ordinary Session fanout never waits.
	SubscriberStallTimeout time.Duration
	Generation             string
	Now                    func() time.Time
}

type feedRingItem struct {
	envelope eventstream.Envelope
	bytes    int
	at       time.Time
	acceptID uint64
}

// FeedBroker owns delivery state for one Session. It does not own Runtime or
// task cancellation.
type FeedBroker struct {
	ref          session.SessionRef
	reader       session.PagedReader
	codec        *eventstream.CursorCodec
	ringEvents   int
	ringBytes    int
	ringTTL      time.Duration
	queueSize    int
	stallTimeout time.Duration
	generation   string
	now          func() time.Time
	primeGate    chan struct{}
	ctx          context.Context
	cancel       context.CancelFunc
	done         chan struct{}

	mu            sync.Mutex
	ring          []feedRingItem
	ringByteCount int
	seen          map[string]struct{}
	subscribers   map[*feedSubscription]struct{}
	latestDurable eventstream.DurableFeedPosition
	scannedSeq    uint64
	transientSeq  uint64
	acceptID      uint64
	evictedAccept uint64
	// terminalOutputCursors tracks the last byte accepted for one physical
	// command stream. A later Turn may re-read that stream from zero; reconciling
	// its absolute output cursor here prevents a replayed prefix from reaching
	// every Surface while retaining legitimate identical deltas at new cursors.
	terminalOutputCursors map[string]int64
	// transientHistoryUnknown records that this broker reconstructed durable
	// history from storage or evicted an accepted transient frame. In either case
	// an empty-cursor replay cannot promise that every historical transient frame
	// is still available, even when its retained suffix is exact.
	transientHistoryUnknown bool
	closed                  bool
	// testBeforeAttachPublish is a deterministic race seam used only by package
	// tests to stop an attachment after receive and before target fencing.
	testBeforeAttachPublish func()
}

// Attached ingress owns no caller context, so recoverable durable-read
// failures use a small bounded retry window. A permanent projection or store
// failure must be observable by the Turn adapter instead of pinning its
// terminal frame forever.
const (
	attachPublishRetryInterval = 10 * time.Millisecond
	attachPublishMaxAttempts   = 3
)

// NewFeedBroker constructs a broker. CursorCodec and Session ID are required.
func NewFeedBroker(cfg FeedBrokerConfig) (*FeedBroker, error) {
	cfg.SessionRef = session.NormalizeSessionRef(cfg.SessionRef)
	if strings.TrimSpace(cfg.SessionRef.SessionID) == "" {
		return nil, errors.New("controlclient: feed broker session id is required")
	}
	if cfg.CursorCodec == nil {
		return nil, errors.New("controlclient: feed broker cursor codec is required")
	}
	if cfg.RingEvents <= 0 {
		cfg.RingEvents = defaultRingEvents
	}
	if cfg.RingBytes <= 0 {
		cfg.RingBytes = defaultRingBytes
	}
	if cfg.RingTTL <= 0 {
		cfg.RingTTL = defaultRingTTL
	}
	if cfg.SubscriberQueue <= 0 {
		cfg.SubscriberQueue = defaultSubscriberSize
	}
	if cfg.SubscriberStallTimeout <= 0 {
		cfg.SubscriberStallTimeout = defaultSubscriberStallTimeout
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if strings.TrimSpace(cfg.Generation) == "" {
		cfg.Generation = randomFeedGeneration()
	}
	ctx, cancel := context.WithCancel(context.Background())
	primeGate := make(chan struct{}, 1)
	primeGate <- struct{}{}
	return &FeedBroker{
		ref: cfg.SessionRef, reader: cfg.Reader, codec: cfg.CursorCodec,
		ringEvents: cfg.RingEvents, ringBytes: cfg.RingBytes, ringTTL: cfg.RingTTL,
		queueSize: cfg.SubscriberQueue, stallTimeout: cfg.SubscriberStallTimeout,
		generation: cfg.Generation, now: cfg.Now,
		primeGate: primeGate, ctx: ctx, cancel: cancel, done: make(chan struct{}),
		seen: map[string]struct{}{}, terminalOutputCursors: map[string]int64{},
		subscribers: map[*feedSubscription]struct{}{},
	}, nil
}

// Prime incrementally publishes every newly committed durable projection from
// Session truth. The prime gate is the single durable sequencer: callers cannot
// advance the durable high-water mark or fan out a later durable projection
// while a storage gap or reconnect checkpoint is being reconciled. Transient
// frames may interleave and retain their broker acceptance order.
func (b *FeedBroker) Prime(ctx context.Context) error {
	if b == nil {
		return errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := b.lockPrime(ctx); err != nil {
		return err
	}
	defer b.unlockPrime()
	return b.primeLocked(ctx, 0)
}

func (b *FeedBroker) lockPrime(ctx context.Context) error {
	if b == nil {
		return errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-b.done:
		return errors.New("controlclient: feed broker is closed")
	case <-b.primeGate:
	}
	select {
	case <-ctx.Done():
		b.unlockPrime()
		return ctx.Err()
	case <-b.done:
		b.unlockPrime()
		return errors.New("controlclient: feed broker is closed")
	default:
		return nil
	}
}

func (b *FeedBroker) unlockPrime() {
	if b != nil {
		b.primeGate <- struct{}{}
	}
}

func (b *FeedBroker) primeLocked(ctx context.Context, throughSeq uint64) error {
	_, err := b.primeStorageLocked(ctx, throughSeq, nil, nil)
	return err
}

// primeStorageLocked publishes committed storage projections in order. When
// before is non-nil, that exact durable position and every later position are
// excluded: the ingress Envelope remains authoritative for its transport IDs
// and payload extensions.
func (b *FeedBroker) primeStorageLocked(
	ctx context.Context,
	throughSeq uint64,
	before *eventstream.DurableFeedPosition,
	skipTarget *feedSubscription,
) (uint64, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, errors.New("controlclient: feed broker is closed")
	}
	afterSeq := b.scannedSeq
	b.mu.Unlock()
	if b.reader == nil {
		return afterSeq, nil
	}
	observedSeq := afterSeq

	for {
		previousSeq := afterSeq
		request := session.EventPageRequest{
			SessionRef: b.ref, AfterSeq: afterSeq, Visibility: session.EventPageClientReplay,
		}
		if throughSeq > 0 {
			request.ThroughSeq = throughSeq
		}
		page, err := b.reader.EventsPage(ctx, request)
		if err != nil {
			return observedSeq, err
		}
		for _, event := range page.Events {
			if throughSeq > 0 && event.Seq > throughSeq {
				break
			}
			if event.Seq > observedSeq {
				observedSeq = event.Seq
			}
			if suppressHistoricalChildStreamMirror(event) {
				continue
			}
			base := acpprojector.EnvelopeBaseFromSessionEvent(b.ref, event, acpprojector.SessionEventTransport{})
			for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
				if envelope.Position == nil || envelope.Position.Durable == nil {
					continue
				}
				if before != nil && eventstream.CompareDurablePosition(*envelope.Position.Durable, *before) >= 0 {
					continue
				}
				accepted, _, err := b.publishSerialized(envelope, skipTarget)
				if err != nil {
					return observedSeq, err
				}
				if accepted {
					b.mu.Lock()
					b.transientHistoryUnknown = true
					b.mu.Unlock()
				}
			}
		}
		if page.NextSeq > afterSeq {
			afterSeq = page.NextSeq
		}
		if page.NextSeq > observedSeq {
			observedSeq = page.NextSeq
		}
		if throughSeq > 0 && afterSeq >= throughSeq {
			afterSeq = throughSeq
			break
		}
		if !page.HasMore || page.NextSeq <= previousSeq {
			break
		}
	}

	b.mu.Lock()
	completeThrough := afterSeq
	if before != nil && before.Seq > 0 && completeThrough >= before.Seq {
		completeThrough = before.Seq - 1
	}
	if completeThrough > b.scannedSeq {
		b.scannedSeq = completeThrough
	}
	b.mu.Unlock()
	return observedSeq, nil
}

// Publish assigns a signed Cursor and stores a bounded clone. Every subscriber
// has an independent bounded queue; publication never waits on subscriber I/O.
// A slow subscriber is disconnected and can resume from its last Cursor.
func (b *FeedBroker) Publish(envelope eventstream.Envelope) error {
	if b == nil {
		return errors.New("controlclient: nil feed broker")
	}
	_, _, err := b.publish(b.ctx, envelope, nil)
	return err
}

func (b *FeedBroker) publish(
	ctx context.Context,
	envelope eventstream.Envelope,
	target *feedSubscription,
) (accepted bool, targetActive bool, err error) {
	if b == nil {
		return false, false, errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	holdingTarget := false
	if target != nil {
		active, err := b.beginTargetHold(target)
		if err != nil {
			return false, false, err
		}
		if !active {
			return false, false, nil
		}
		holdingTarget = true
	}
	defer func() {
		if holdingTarget {
			b.abortTargetHold(target)
		}
	}()
	envelope = eventstream.CloneEnvelope(envelope)
	// Reject malformed durable declarations before they can enter the storage
	// sequencer. Validation inside publishSerialized remains the final defense,
	// but it is too late to protect scannedSeq from an invalid gap-fill target.
	if err := eventstream.ValidateEnvelopeDelivery(envelope); err != nil {
		return false, false, fmt.Errorf("controlclient: feed envelope delivery: %w", err)
	}
	if isDurableFeedEnvelope(envelope) && b.reader != nil {
		if err := b.lockPrime(ctx); err != nil {
			return false, false, err
		}
		position := *envelope.Position.Durable
		committedSeq, err := b.primeStorageLocked(ctx, position.Seq, &position, target)
		if err != nil {
			b.unlockPrime()
			return false, false, err
		}
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			b.unlockPrime()
			return false, false, errors.New("controlclient: feed broker is closed")
		}
		if position.Seq > committedSeq {
			b.mu.Unlock()
			b.unlockPrime()
			return false, false, fmt.Errorf("controlclient: durable feed position %d:%d is ahead of committed sequence %d", position.Seq, position.ProjectionIndex, committedSeq)
		}
		b.mu.Unlock()
		_, _, err = b.publishSerialized(envelope, target)
		b.unlockPrime()
		if err != nil {
			return false, false, err
		}
		active := b.flushTargetHold(ctx, target)
		holdingTarget = false
		return true, active, nil
	}
	if isMainTerminalEnvelope(envelope) && b.reader != nil {
		// A Runtime terminal is the final delivery barrier for one Turn, while
		// canonical assistant output is durable Session truth. Reconcile storage
		// under the same sequencer before accepting the transient terminal so a
		// fast provider cannot close a Surface before its committed final answer.
		if err := b.lockPrime(ctx); err != nil {
			return false, false, err
		}
		if err := b.primeLocked(ctx, 0); err != nil {
			b.unlockPrime()
			return false, false, err
		}
		_, _, err = b.publishSerialized(envelope, target)
		b.unlockPrime()
		if err != nil {
			return false, false, err
		}
		active := b.flushTargetHold(ctx, target)
		holdingTarget = false
		return true, active, nil
	}
	_, _, err = b.publishSerialized(envelope, target)
	if err != nil {
		return false, false, err
	}
	active := b.flushTargetHold(ctx, target)
	holdingTarget = false
	return true, active, nil
}

func isMainTerminalEnvelope(envelope eventstream.Envelope) bool {
	return eventstream.IsTurnTerminalLifecycle(envelope)
}

func (b *FeedBroker) publishSerialized(
	envelope eventstream.Envelope,
	skipTarget *feedSubscription,
) (bool, eventstream.Envelope, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishLocked(envelope, skipTarget)
}

func (b *FeedBroker) publishLocked(
	envelope eventstream.Envelope,
	skipTarget *feedSubscription,
) (bool, eventstream.Envelope, error) {
	if b.closed {
		return false, eventstream.Envelope{}, errors.New("controlclient: feed broker is closed")
	}
	envelope = eventstream.CloneEnvelope(envelope)
	envelope, terminalCursorKey, terminalCursorEnd := b.reconcileTerminalOutputLocked(envelope)
	if err := b.prepareEnvelopeLocked(&envelope); err != nil {
		return false, eventstream.Envelope{}, err
	}
	if isDurableFeedEnvelope(envelope) && eventstream.CompareDurablePosition(*envelope.Position.Durable, b.latestDurable) <= 0 {
		return false, eventstream.Envelope{}, nil
	}
	dedupeKey := feedDedupeKey(envelope)
	if !isDurableFeedEnvelope(envelope) {
		if _, ok := b.seen[dedupeKey]; ok {
			return false, eventstream.Envelope{}, nil
		}
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return false, eventstream.Envelope{}, fmt.Errorf("controlclient: encode feed envelope: %w", err)
	}
	b.acceptID++
	item := feedRingItem{
		envelope: eventstream.CloneEnvelope(envelope), bytes: len(encoded), at: b.now(), acceptID: b.acceptID,
	}
	b.ring = append(b.ring, item)
	b.ringByteCount += item.bytes
	if isDurableFeedEnvelope(envelope) {
		b.latestDurable = *envelope.Position.Durable
	} else {
		b.seen[dedupeKey] = struct{}{}
	}
	if terminalCursorKey != "" && terminalCursorEnd > b.terminalOutputCursors[terminalCursorKey] {
		b.terminalOutputCursors[terminalCursorKey] = terminalCursorEnd
	}
	b.evictLocked()

	for subscriber := range b.subscribers {
		if isDurableFeedEnvelope(envelope) && envelope.Position.Durable.Seq <= subscriber.ignoreDurableThrough {
			continue
		}
		if subscriber == skipTarget {
			b.appendTargetPendingLocked(subscriber, envelope, len(encoded))
			continue
		}
		if subscriber.targetHold {
			b.appendTargetPendingLocked(subscriber, envelope, len(encoded))
			continue
		}
		if !subscriber.tryReserve() {
			b.stopSubscriberLocked(subscriber, controlport.ErrSlowConsumer)
			continue
		}
		select {
		case subscriber.input <- eventstream.CloneEnvelope(envelope):
		default:
			subscriber.release()
			b.stopSubscriberLocked(subscriber, controlport.ErrSlowConsumer)
		}
	}
	return true, eventstream.CloneEnvelope(envelope), nil
}

func (b *FeedBroker) reconcileTerminalOutputLocked(envelope eventstream.Envelope) (eventstream.Envelope, string, int64) {
	update, ok := envelope.Update.(schema.ToolCallUpdate)
	if !ok {
		return envelope, "", 0
	}
	terminalOutput, ok := metautil.TerminalOutput(update.Meta)
	if !ok {
		return envelope, "", 0
	}
	taskMeta := metautil.RuntimeSection(update.Meta, metautil.RuntimeTask)
	taskID, _ := taskMeta[metautil.RuntimeTaskID].(string)
	taskID = strings.TrimSpace(taskID)
	terminalID, _ := taskMeta[metautil.RuntimeTaskTerminalID].(string)
	terminalID = strings.TrimSpace(terminalID)
	outputCursor, ok := feedInt64(taskMeta["output_cursor"])
	if taskID == "" || terminalID == "" || !ok || outputCursor < 0 {
		return envelope, "", 0
	}
	dataBytes := []byte(terminalOutput.Data)
	startCursor := outputCursor - int64(len(dataBytes))
	if startCursor < 0 {
		return envelope, "", 0
	}
	key := strings.Join([]string{taskID, terminalID}, "|")
	acceptedCursor, seen := b.terminalOutputCursors[key]
	if !seen {
		return envelope, key, outputCursor
	}

	switch {
	case outputCursor <= acceptedCursor:
		update.Meta = withoutTerminalOutput(update.Meta)
	case startCursor < acceptedCursor:
		offset := acceptedCursor - startCursor
		if offset < 0 || offset > int64(len(dataBytes)) || !utf8.Valid(dataBytes[offset:]) {
			// Internal stream cursors are UTF-8 byte boundaries. If an external or
			// malformed producer violates that contract, preserve its payload instead
			// of slicing into invalid text.
			return envelope, "", 0
		}
		update.Meta = metautil.WithTerminalOutput(update.Meta, terminalOutput.TerminalID, string(dataBytes[offset:]))
	default:
		// The incoming frame begins at or after the accepted boundary. It is a new
		// exact delta (including a legitimately identical repeated line).
	}
	if startCursor <= acceptedCursor {
		// Truncation is relative to the source reader's replay-from-zero cursor. If
		// the Session feed already accepted through this frame's start, Surfaces do
		// not have a visible gap and must not render the source-local warning.
		update.Meta = metautil.WithoutRuntimeSectionKeys(
			update.Meta,
			metautil.RuntimeStream,
			metautil.RuntimeStreamTruncated,
			metautil.RuntimeStreamBefore,
		)
	}
	envelope.Update = update
	return envelope, key, outputCursor
}

func withoutTerminalOutput(meta map[string]any) map[string]any {
	out := metautil.CloneMap(meta)
	delete(out, metautil.TerminalOutputKey)
	if len(out) == 0 {
		return nil
	}
	return out
}

func feedInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		converted := int64(typed)
		return converted, typed >= 0 && float64(converted) == typed
	case json.Number:
		converted, err := typed.Int64()
		return converted, err == nil
	default:
		return 0, false
	}
}

func (b *FeedBroker) appendTargetPendingLocked(subscriber *feedSubscription, envelope eventstream.Envelope, encodedBytes int) {
	if subscriber == nil || !subscriber.targetHold {
		return
	}
	if len(subscriber.targetPending) >= b.ringEvents || subscriber.targetPendingBytes+encodedBytes > b.ringBytes {
		b.stopSubscriberLocked(subscriber, controlport.ErrSlowConsumer)
		return
	}
	subscriber.targetPending = append(subscriber.targetPending, eventstream.CloneEnvelope(envelope))
	subscriber.targetPendingBytes += encodedBytes
}

func (b *FeedBroker) beginTargetHold(target *feedSubscription) (bool, error) {
	if target == nil {
		return true, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, active := b.subscribers[target]; !active {
		return false, nil
	}
	if target.targetHold {
		return false, errors.New("controlclient: subscription already has an active attachment")
	}
	target.targetHold = true
	target.targetPending = nil
	target.targetPendingBytes = 0
	return true, nil
}

func (b *FeedBroker) abortTargetHold(target *feedSubscription) {
	if b == nil || target == nil {
		return
	}
	b.mu.Lock()
	if !target.targetHold {
		b.mu.Unlock()
		return
	}
	if len(target.targetPending) == 0 {
		// Nothing entered the global Session sequence during this attempt. Clear
		// the fence so a recoverable durable-read failure can retry the same
		// ingress without making the prepared subscription look inactive.
		target.targetHold = false
		target.targetPendingBytes = 0
	} else {
		// A prefix has already entered the global sequence but could not be
		// delivered to this target. Disconnect it explicitly: silently clearing
		// the prefix would permit a later retry to overtake or lose those events.
		b.stopSubscriberLocked(target, errors.New("controlclient: target attachment failed after partial publication"))
	}
	b.mu.Unlock()
}

func (b *FeedBroker) flushTargetHold(ctx context.Context, target *feedSubscription) bool {
	if target == nil {
		return true
	}
	for {
		b.mu.Lock()
		if _, active := b.subscribers[target]; !active {
			b.mu.Unlock()
			return false
		}
		if len(target.targetPending) == 0 {
			target.targetHold = false
			b.mu.Unlock()
			return true
		}
		batch := eventstream.CloneEnvelopes(target.targetPending)
		target.targetPending = nil
		target.targetPendingBytes = 0
		b.mu.Unlock()
		if !b.deliverTargetBatch(ctx, target, batch) {
			return false
		}
	}
}

func (b *FeedBroker) deliverTargetBatch(
	ctx context.Context,
	target *feedSubscription,
	batch []eventstream.Envelope,
) bool {
	if target == nil {
		return true
	}
	for _, envelope := range batch {
		reserved, stalled := target.reserve(ctx, b.done, b.stallTimeout)
		if !reserved {
			if stalled {
				b.mu.Lock()
				b.stopSubscriberLocked(target, controlport.ErrSlowConsumer)
				b.mu.Unlock()
			}
			return false
		}
		b.mu.Lock()
		_, active := b.subscribers[target]
		if active {
			select {
			case target.input <- eventstream.CloneEnvelope(envelope):
			default:
				target.release()
				b.stopSubscriberLocked(target, controlport.ErrSlowConsumer)
				active = false
			}
		} else {
			target.release()
		}
		b.mu.Unlock()
		if !active {
			return false
		}
	}
	b.mu.Lock()
	_, active := b.subscribers[target]
	b.mu.Unlock()
	return active
}

func isDurableFeedEnvelope(envelope eventstream.Envelope) bool {
	if envelope.Delivery == nil {
		return false
	}
	if envelope.Delivery.Mode != eventstream.DeliveryCanonical && envelope.Delivery.Mode != eventstream.DeliveryMirror {
		return false
	}
	return envelope.Position != nil && envelope.Position.Durable != nil
}

func (b *FeedBroker) prepareEnvelopeLocked(envelope *eventstream.Envelope) error {
	if envelope == nil {
		return errors.New("controlclient: nil feed envelope")
	}
	if strings.TrimSpace(envelope.SessionID) == "" {
		envelope.SessionID = b.ref.SessionID
	}
	if strings.TrimSpace(envelope.SessionID) != b.ref.SessionID {
		return fmt.Errorf("controlclient: feed envelope session %q does not match %q", envelope.SessionID, b.ref.SessionID)
	}
	mode := eventstream.DeliveryMode("")
	if envelope.Delivery != nil {
		mode = envelope.Delivery.Mode
	}
	if err := eventstream.ValidateEnvelopeDelivery(*envelope); err != nil {
		return fmt.Errorf("controlclient: feed envelope delivery: %w", err)
	}
	switch mode {
	case eventstream.DeliveryCanonical, eventstream.DeliveryMirror:
	case eventstream.DeliveryTransient, "":
		b.transientSeq++
		envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
		envelope.Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Anchor: b.latestDurable, Generation: b.generation, Sequence: b.transientSeq,
		}}
	}
	cursor, err := b.codec.Encode(b.ref.SessionID, *envelope.Position)
	if err != nil {
		return err
	}
	envelope.Cursor = cursor
	return nil
}

// SubscribeFromNow creates an internal Surface subscription without replaying
// history. It is registered before BeginTurn and its ingress is attached only
// after the Surface claims Events. Like every Session subscription, it has an
// independent bounded queue and is disconnected if its consumer falls behind;
// it can never stall another ingress or the durable sequencer.
func (b *FeedBroker) SubscribeFromNow(ctx context.Context) (controlport.FeedSubscription, error) {
	if b == nil {
		return nil, errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// Establish the durable baseline and register the subscriber under the
	// same sequencer used by Publish. Existing Session history is therefore
	// never injected by the first event of a new Turn, while a concurrent
	// durable publish cannot slip between the baseline and registration.
	if err := b.lockPrime(ctx); err != nil {
		return nil, err
	}
	defer b.unlockPrime()
	if err := b.primeLocked(ctx, 0); err != nil {
		return nil, err
	}
	subscriber := newFeedSubscription(b, b.queueSize)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, errors.New("controlclient: feed broker is closed")
	}
	b.subscribers[subscriber] = struct{}{}
	b.mu.Unlock()
	subscriber.start()
	go subscriber.closeWhenContextDone(ctx)
	return subscriber, nil
}

// Attach immediately consumes one Turn ingress and publishes it into this
// Session. Terminal Envelopes do not close the Session broker.
func (b *FeedBroker) Attach(events <-chan eventstream.Envelope) <-chan error {
	return b.attach(nil, events)
}

// AttachTo consumes one Turn ingress through its prepared internal
// subscription. Capacity is reserved before each event enters the Session
// sequencer, so only this ingress waits for its Surface while sibling
// publication remains non-blocking.
func (b *FeedBroker) AttachTo(
	subscription controlport.FeedSubscription,
	events <-chan eventstream.Envelope,
) <-chan error {
	target, ok := subscription.(*feedSubscription)
	if !ok || target == nil || target.broker != b {
		return completedAttachResult(errors.New("controlclient: attached subscription does not belong to feed broker"))
	}
	b.mu.Lock()
	_, active := b.subscribers[target]
	b.mu.Unlock()
	if !active {
		err := target.Err()
		if err == nil {
			err = errors.New("controlclient: attached subscription is closed")
		}
		return completedAttachResult(err)
	}
	return b.attach(target, events)
}

func completedAttachResult(err error) <-chan error {
	result := make(chan error, 1)
	if err != nil {
		result <- err
	}
	close(result)
	return result
}

func (b *FeedBroker) attach(
	target *feedSubscription,
	events <-chan eventstream.Envelope,
) <-chan error {
	if b == nil {
		return completedAttachResult(errors.New("controlclient: nil feed broker"))
	}
	if events == nil {
		return completedAttachResult(nil)
	}
	result := make(chan error, 1)
	go func() {
		defer close(result)
		targetCtx, cancelTarget := context.WithCancel(b.ctx)
		defer cancelTarget()
		currentTarget := target
		publishCtx := b.ctx
		if currentTarget != nil {
			publishCtx = targetCtx
			attachedTarget := currentTarget
			go func() {
				select {
				case <-attachedTarget.stop:
					cancelTarget()
				case <-targetCtx.Done():
				}
			}()
		}
		detachedByClose := false
		for {
			var envelope eventstream.Envelope
			select {
			case <-publishCtx.Done():
				if currentTarget != nil && b.ctx.Err() == nil {
					detachedByClose = currentTarget.Err() == nil
					currentTarget = nil
					publishCtx = b.ctx
					continue
				}
				return
			case next, ok := <-events:
				if !ok {
					return
				}
				envelope = next
			}
			if b.testBeforeAttachPublish != nil {
				b.testBeforeAttachPublish()
			}

			for {
				attemptCtx := publishCtx
				attemptTarget := currentTarget
				var lastErr error
				accepted := false
				targetActive := true
				detachAndRetry := false
				for attempt := 1; attempt <= attachPublishMaxAttempts; attempt++ {
					var err error
					accepted, targetActive, err = b.publish(attemptCtx, envelope, attemptTarget)
					if err == nil {
						// An inactive target with a prior error means the failed attempt
						// published a prefix and disconnected it. Preserve the original
						// error instead of treating the retry as a successful no-op.
						if !targetActive && lastErr != nil {
							break
						}
						lastErr = nil
						break
					}
					lastErr = err
					if attemptTarget != nil && attemptCtx.Err() != nil && b.ctx.Err() == nil {
						if targetErr := attemptTarget.Err(); targetErr == nil || errors.Is(targetErr, controlport.ErrSlowConsumer) {
							detachedByClose = targetErr == nil
							detachAndRetry = true
							break
						}
					}
					if attemptTarget != nil && attemptTarget.Err() != nil {
						break
					}
					if attempt == attachPublishMaxAttempts {
						break
					}
					timer := time.NewTimer(attachPublishRetryInterval)
					select {
					case <-attemptCtx.Done():
						if !timer.Stop() {
							<-timer.C
						}
						if attemptTarget != nil && b.ctx.Err() == nil {
							if targetErr := attemptTarget.Err(); targetErr == nil || errors.Is(targetErr, controlport.ErrSlowConsumer) {
								detachedByClose = targetErr == nil
								detachAndRetry = true
								break
							}
						}
						return
					case <-timer.C:
					}
				}
				if detachAndRetry {
					currentTarget = nil
					publishCtx = b.ctx
					continue
				}
				if !targetActive && lastErr == nil {
					if attemptTarget != nil {
						detachedByClose = attemptTarget.Err() == nil
						currentTarget = nil
						publishCtx = b.ctx
					}
					if !accepted {
						// The target closed after this worker received the Envelope but
						// before beginTargetHold. Nothing entered the Session sequence;
						// retry the same Envelope untargeted instead of dropping it.
						continue
					}
					// The Envelope is already globally represented. Detach only the
					// stopped target and continue with the next ingress Envelope.
					break
				}
				if lastErr != nil {
					if detachedByClose && currentTarget == nil {
						// The target owner explicitly tore down delivery. A durable
						// Envelope that cannot be recovered from storage must not turn
						// that delivery teardown into a new Session failure; later ingress
						// (especially the terminal) still publishes untargeted.
						break
					}
					result <- fmt.Errorf("controlclient: attach feed ingress: %w", lastErr)
					return
				}
				break
			}
		}
	}()
	return result
}

// Boundary returns the latest published position and signed Cursor.
func (b *FeedBroker) Boundary() (*eventstream.FeedPosition, string) {
	if b == nil {
		return nil, ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.evictLocked()
	if len(b.ring) > 0 {
		last := b.ring[len(b.ring)-1].envelope
		return eventstream.CloneFeedPosition(last.Position), last.Cursor
	}
	if b.latestDurable.Seq == 0 {
		return nil, ""
	}
	position := &eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{
		Seq: b.latestDurable.Seq, ProjectionIndex: b.latestDurable.ProjectionIndex,
	}}
	cursor, _ := b.codec.Encode(b.ref.SessionID, *position)
	return position, cursor
}

// Close disconnects subscribers without cancelling any Runtime work.
func (b *FeedBroker) Close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	b.cancel()
	close(b.done)
	for subscriber := range b.subscribers {
		b.stopSubscriberLocked(subscriber, nil)
	}
	return nil
}

func (b *FeedBroker) evictLocked() {
	cutoff := b.now().Add(-b.ringTTL)
	for len(b.ring) > 0 && (len(b.ring) > b.ringEvents || b.ringByteCount > b.ringBytes || b.ring[0].at.Before(cutoff)) {
		item := b.ring[0]
		b.ring = b.ring[1:]
		b.ringByteCount -= item.bytes
		if item.acceptID > b.evictedAccept {
			b.evictedAccept = item.acceptID
		}
		if !isDurableFeedEnvelope(item.envelope) {
			b.transientHistoryUnknown = true
			delete(b.seen, feedDedupeKey(item.envelope))
		}
	}
}

func (b *FeedBroker) stopSubscriberLocked(subscriber *feedSubscription, err error) {
	if subscriber == nil {
		return
	}
	if _, ok := b.subscribers[subscriber]; !ok {
		return
	}
	delete(b.subscribers, subscriber)
	if errors.Is(err, controlport.ErrSlowConsumer) {
		err = &controlport.FeedGapError{
			Cause: err, RetryCursor: subscriber.retryFrom(),
			Mode: controlport.ResumeModeDurableFallback, TransientGap: true,
		}
	}
	subscriber.targetHold = false
	subscriber.targetPending = nil
	subscriber.targetPendingBytes = 0
	subscriber.setErrLocked(err)
	subscriber.stopOnce.Do(func() { close(subscriber.stop) })
}

type feedSubscription struct {
	broker       *FeedBroker
	input        chan eventstream.Envelope
	slots        chan struct{}
	backfillOut  chan eventstream.Envelope
	out          chan eventstream.Envelope
	stop         chan struct{}
	done         chan struct{}
	backfillDone chan struct{}
	backfill     func(*feedSubscription) error

	startOnce          sync.Once
	stopOnce           sync.Once
	backfillOnce       sync.Once
	targetHold         bool
	targetPending      []eventstream.Envelope
	targetPendingBytes int

	stateMu              sync.RWMutex
	err                  error
	lastCursor           string
	retryCursor          string
	ignoreDurableThrough uint64
}

func newFeedSubscription(broker *FeedBroker, queueSize int) *feedSubscription {
	return &feedSubscription{
		broker: broker, input: make(chan eventstream.Envelope, queueSize),
		slots:       make(chan struct{}, queueSize),
		backfillOut: make(chan eventstream.Envelope),
		out:         make(chan eventstream.Envelope), stop: make(chan struct{}), done: make(chan struct{}),
		backfillDone: make(chan struct{}),
	}
}

func (s *feedSubscription) start() {
	s.startOnce.Do(func() { go s.run() })
}

func (s *feedSubscription) closeWhenContextDone(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	select {
	case <-ctx.Done():
		_ = s.Close()
	case <-s.done:
	case <-s.broker.done:
	}
}

func (s *feedSubscription) tryReserve() bool {
	if s == nil {
		return false
	}
	select {
	case s.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *feedSubscription) reserve(
	ctx context.Context,
	brokerDone <-chan struct{},
	timeout time.Duration,
) (reserved bool, stalled bool) {
	if s == nil {
		return false, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case s.slots <- struct{}{}:
		return true, false
	default:
	}
	if timeout <= 0 {
		timeout = defaultSubscriberStallTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case s.slots <- struct{}{}:
		return true, false
	case <-s.stop:
		return false, false
	case <-brokerDone:
		return false, false
	case <-ctx.Done():
		return false, false
	case <-timer.C:
		return false, true
	}
}

func (s *feedSubscription) release() {
	if s == nil {
		return
	}
	select {
	case <-s.slots:
	default:
	}
}

func (s *feedSubscription) run() {
	defer close(s.done)
	defer close(s.out)
	defer s.finishBackfill()
	if s.backfill != nil {
		if err := s.backfill(s); err != nil {
			if !errors.Is(err, errFeedSubscriptionStopped) {
				s.setErr(err)
			}
			return
		}
		s.backfill = nil
	}
	s.finishBackfill()
	for {
		select {
		case <-s.stop:
			return
		case envelope := <-s.input:
			// A slot accounts for queued input, not the Envelope currently being
			// handed to the consumer. Release it as soon as the runner dequeues the
			// item; otherwise a queue of one can falsely classify an actively
			// receiving subscriber as slow in the scheduling window after send.
			s.release()
			if !s.deliver(envelope) {
				return
			}
		}
	}
}

func (s *feedSubscription) deliver(envelope eventstream.Envelope) bool {
	select {
	case <-s.stop:
		return false
	case s.out <- envelope:
		s.stateMu.Lock()
		s.lastCursor = envelope.Cursor
		s.stateMu.Unlock()
		return true
	}
}

func (s *feedSubscription) deliverBackfill(envelope eventstream.Envelope) bool {
	select {
	case <-s.stop:
		return false
	case s.backfillOut <- envelope:
		s.stateMu.Lock()
		s.lastCursor = envelope.Cursor
		s.stateMu.Unlock()
		return true
	}
}

func (s *feedSubscription) Backfill() <-chan eventstream.Envelope {
	if s == nil {
		closed := make(chan eventstream.Envelope)
		close(closed)
		return closed
	}
	return s.backfillOut
}

func (s *feedSubscription) Events() <-chan eventstream.Envelope { return s.out }

func (s *feedSubscription) BackfillDone() <-chan struct{} {
	if s == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return s.backfillDone
}

func (s *feedSubscription) finishBackfill() {
	s.backfillOnce.Do(func() {
		close(s.backfillOut)
		close(s.backfillDone)
	})
}

func (s *feedSubscription) Close() error {
	if s == nil || s.broker == nil {
		return nil
	}
	s.broker.mu.Lock()
	if _, active := s.broker.subscribers[s]; active {
		s.broker.stopSubscriberLocked(s, nil)
	} else {
		s.stopOnce.Do(func() { close(s.stop) })
	}
	s.broker.mu.Unlock()
	return nil
}

func (s *feedSubscription) Err() error {
	if s == nil {
		return nil
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.err
}

func (s *feedSubscription) LastCursor() string {
	if s == nil {
		return ""
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.lastCursor
}

func (s *feedSubscription) setErrLocked(err error) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.err == nil {
		s.err = err
	}
}

func (s *feedSubscription) setErr(err error) {
	if s == nil || err == nil {
		return
	}
	s.stateMu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.stateMu.Unlock()
}

func (s *feedSubscription) retryFrom() string {
	if s == nil {
		return ""
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	if strings.TrimSpace(s.lastCursor) != "" {
		return s.lastCursor
	}
	return s.retryCursor
}

func feedDedupeKey(envelope eventstream.Envelope) string {
	if id := strings.TrimSpace(envelope.ProjectionID); id != "" {
		return "projection:" + id
	}
	return "cursor:" + strings.TrimSpace(envelope.Cursor)
}

func cloneFeedRing(in []feedRingItem) []feedRingItem {
	out := make([]feedRingItem, len(in))
	for index, item := range in {
		out[index] = item
		out[index].envelope = eventstream.CloneEnvelope(item.envelope)
	}
	return out
}

func findRingCursor(ring []feedRingItem, cursor string) int {
	if strings.TrimSpace(cursor) == "" {
		return -1
	}
	for index, item := range ring {
		if item.envelope.Cursor == cursor {
			return index
		}
	}
	return -1
}

func randomFeedGeneration() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("generation-%d", time.Now().UnixNano())
}

// FeedRegistry owns at most one broker for each Session ID.
type FeedRegistry struct {
	config FeedRegistryConfig
	mu     sync.Mutex
	feeds  map[string]*FeedBroker
}

// FeedRegistryConfig supplies shared broker dependencies and limits.
type FeedRegistryConfig struct {
	Reader                 session.PagedReader
	CursorCodec            *eventstream.CursorCodec
	RingEvents             int
	RingBytes              int
	RingTTL                time.Duration
	SubscriberQueue        int
	SubscriberStallTimeout time.Duration
	Now                    func() time.Time
}

// NewFeedRegistry constructs a process-local Session broker registry.
func NewFeedRegistry(config FeedRegistryConfig) (*FeedRegistry, error) {
	if config.CursorCodec == nil {
		return nil, errors.New("controlclient: feed registry cursor codec is required")
	}
	return &FeedRegistry{config: config, feeds: map[string]*FeedBroker{}}, nil
}

// Session returns the stable broker for one Session.
func (r *FeedRegistry) Session(ref session.SessionRef) (controlport.SessionFeed, error) {
	if r == nil {
		return nil, errors.New("controlclient: nil feed registry")
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return nil, errors.New("controlclient: session id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if broker := r.feeds[ref.SessionID]; broker != nil {
		return broker, nil
	}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: ref, Reader: r.config.Reader, CursorCodec: r.config.CursorCodec,
		RingEvents: r.config.RingEvents, RingBytes: r.config.RingBytes,
		RingTTL: r.config.RingTTL, SubscriberQueue: r.config.SubscriberQueue,
		SubscriberStallTimeout: r.config.SubscriberStallTimeout, Now: r.config.Now,
	})
	if err != nil {
		return nil, err
	}
	r.feeds[ref.SessionID] = broker
	return broker, nil
}
