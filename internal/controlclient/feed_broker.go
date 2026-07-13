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

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
)

const (
	defaultRingEvents     = 1024
	defaultRingBytes      = 4 << 20
	defaultRingTTL        = 15 * time.Minute
	defaultSubscriberSize = 128
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
	Generation      string
	Now             func() time.Time
}

type feedRingItem struct {
	envelope eventstream.Envelope
	bytes    int
	at       time.Time
}

// FeedBroker owns delivery state for one Session. It does not own Runtime or
// task cancellation.
type FeedBroker struct {
	ref        session.SessionRef
	reader     session.PagedReader
	codec      *eventstream.CursorCodec
	ringEvents int
	ringBytes  int
	ringTTL    time.Duration
	queueSize  int
	generation string
	now        func() time.Time
	primeMu    sync.Mutex

	mu            sync.Mutex
	ring          []feedRingItem
	ringByteCount int
	seen          map[string]struct{}
	subscribers   map[*feedSubscription]struct{}
	latestDurable eventstream.DurableFeedPosition
	scannedSeq    uint64
	transientSeq  uint64
	closed        bool
}

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
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if strings.TrimSpace(cfg.Generation) == "" {
		cfg.Generation = randomFeedGeneration()
	}
	return &FeedBroker{
		ref: cfg.SessionRef, reader: cfg.Reader, codec: cfg.CursorCodec,
		ringEvents: cfg.RingEvents, ringBytes: cfg.RingBytes, ringTTL: cfg.RingTTL,
		queueSize: cfg.SubscriberQueue, generation: cfg.Generation, now: cfg.Now,
		seen: map[string]struct{}{}, subscribers: map[*feedSubscription]struct{}{},
	}, nil
}

// Prime incrementally publishes every newly committed durable projection from
// Session truth. The primeMu is the single durable sequencer: callers cannot
// advance the durable high-water mark or fan out a later projection while a
// storage gap is being filled.
func (b *FeedBroker) Prime(ctx context.Context) error {
	if b == nil {
		return errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.primeMu.Lock()
	defer b.primeMu.Unlock()
	return b.primeLocked(ctx)
}

func (b *FeedBroker) primeLocked(ctx context.Context) error {

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("controlclient: feed broker is closed")
	}
	afterSeq := b.scannedSeq
	b.mu.Unlock()
	if b.reader == nil {
		return nil
	}

	for {
		previousSeq := afterSeq
		page, err := b.reader.EventsPage(ctx, session.EventPageRequest{
			SessionRef: b.ref, AfterSeq: afterSeq, Visibility: session.EventPageClientReplay,
		})
		if err != nil {
			return err
		}
		for _, event := range page.Events {
			base := acpprojector.EnvelopeBaseFromSessionEvent(b.ref, event, acpprojector.SessionEventTransport{})
			for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
				if envelope.Position == nil || envelope.Position.Durable == nil {
					continue
				}
				b.mu.Lock()
				if b.closed {
					b.mu.Unlock()
					return errors.New("controlclient: feed broker is closed")
				}
				if err := b.publishLocked(envelope); err != nil {
					b.mu.Unlock()
					return err
				}
				b.mu.Unlock()
			}
		}
		if page.NextSeq > afterSeq {
			afterSeq = page.NextSeq
		}
		if !page.HasMore || page.NextSeq <= previousSeq {
			break
		}
	}

	b.mu.Lock()
	if afterSeq > b.scannedSeq {
		b.scannedSeq = afterSeq
	}
	b.mu.Unlock()
	return nil
}

// Publish assigns a signed Cursor, stores a bounded clone, and fans it out
// without blocking on subscriber I/O.
func (b *FeedBroker) Publish(envelope eventstream.Envelope) error {
	if b == nil {
		return errors.New("controlclient: nil feed broker")
	}
	envelope = eventstream.CloneEnvelope(envelope)
	if isDurableFeedEnvelope(envelope) && b.reader != nil {
		b.primeMu.Lock()
		defer b.primeMu.Unlock()
		if err := b.primeLocked(context.Background()); err != nil {
			return err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.closed {
			return errors.New("controlclient: feed broker is closed")
		}
		position := *envelope.Position.Durable
		if position.Seq > b.scannedSeq {
			return fmt.Errorf("controlclient: durable feed position %d:%d is ahead of committed sequence %d", position.Seq, position.ProjectionIndex, b.scannedSeq)
		}
		return b.publishLocked(envelope)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("controlclient: feed broker is closed")
	}
	return b.publishLocked(envelope)
}

func (b *FeedBroker) publishLocked(envelope eventstream.Envelope) error {
	envelope = eventstream.CloneEnvelope(envelope)
	if err := b.prepareEnvelopeLocked(&envelope); err != nil {
		return err
	}
	if isDurableFeedEnvelope(envelope) && eventstream.CompareDurablePosition(*envelope.Position.Durable, b.latestDurable) <= 0 {
		return nil
	}
	dedupeKey := feedDedupeKey(envelope)
	if !isDurableFeedEnvelope(envelope) {
		if _, ok := b.seen[dedupeKey]; ok {
			return nil
		}
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("controlclient: encode feed envelope: %w", err)
	}
	item := feedRingItem{envelope: eventstream.CloneEnvelope(envelope), bytes: len(encoded), at: b.now()}
	b.ring = append(b.ring, item)
	b.ringByteCount += item.bytes
	if isDurableFeedEnvelope(envelope) {
		b.latestDurable = *envelope.Position.Durable
	} else {
		b.seen[dedupeKey] = struct{}{}
	}
	b.evictLocked()

	for subscriber := range b.subscribers {
		if subscriber.paused {
			if len(subscriber.pending) >= b.queueSize {
				b.stopSubscriberLocked(subscriber, controlport.ErrSlowConsumer)
				continue
			}
			subscriber.pending = append(subscriber.pending, eventstream.CloneEnvelope(envelope))
			continue
		}
		select {
		case subscriber.input <- eventstream.CloneEnvelope(envelope):
		default:
			b.stopSubscriberLocked(subscriber, controlport.ErrSlowConsumer)
		}
	}
	return nil
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
	switch mode {
	case eventstream.DeliveryCanonical, eventstream.DeliveryMirror:
		if envelope.Position == nil || envelope.Position.Durable == nil || envelope.Position.Validate() != nil {
			return errors.New("controlclient: durable feed envelope requires a durable position")
		}
	case eventstream.DeliveryTransient, "":
		b.transientSeq++
		envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
		envelope.Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Anchor: b.latestDurable, Generation: b.generation, Sequence: b.transientSeq,
		}}
	default:
		return fmt.Errorf("controlclient: unsupported delivery mode %q", mode)
	}
	cursor, err := b.codec.Encode(b.ref.SessionID, *envelope.Position)
	if err != nil {
		return err
	}
	envelope.Cursor = cursor
	return nil
}

// Subscribe atomically combines ring or durable backfill with live delivery.
func (b *FeedBroker) Subscribe(ctx context.Context, req controlport.SubscribeRequest) (controlport.SubscribeResult, error) {
	if b == nil {
		return controlport.SubscribeResult{}, errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(req.SessionID) != b.ref.SessionID {
		return controlport.SubscribeResult{}, eventstream.ErrCursorSessionMismatch
	}
	if err := b.Prime(ctx); err != nil {
		return controlport.SubscribeResult{}, err
	}
	var requested eventstream.FeedPosition
	var err error
	if strings.TrimSpace(req.Cursor) != "" {
		requested, err = b.codec.Decode(b.ref.SessionID, req.Cursor)
		if err != nil {
			return controlport.SubscribeResult{}, err
		}
	}

	subscriber := newFeedSubscription(b, b.queueSize)
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return controlport.SubscribeResult{}, errors.New("controlclient: feed broker is closed")
	}
	b.evictLocked()
	subscriber.paused = true
	b.subscribers[subscriber] = struct{}{}
	highWater := b.latestDurable
	ring := cloneFeedRing(b.ring)
	boundaryCursor := feedBoundaryCursor(ring)
	if boundaryCursor == "" && highWater.Seq > 0 {
		position := eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{
			Seq: highWater.Seq, ProjectionIndex: highWater.ProjectionIndex,
		}}
		boundaryCursor, _ = b.codec.Encode(b.ref.SessionID, position)
	}
	exactIndex := findRingCursor(ring, req.Cursor)
	b.mu.Unlock()

	mode := controlport.ResumeModeExact
	transientGap := false
	var initial []eventstream.Envelope
	switch {
	case strings.TrimSpace(req.Cursor) != "" && exactIndex >= 0:
		initial = ringEnvelopes(ring[exactIndex+1:])
	case strings.TrimSpace(req.Cursor) != "":
		mode = controlport.ResumeModeDurableFallback
		transientGap = true
		initial, err = b.durableBackfill(ctx, requested, highWater)
	default:
		initial, err = b.durableBackfill(ctx, eventstream.FeedPosition{}, highWater)
		initial = append(initial, ringTransientsAtOrAfter(ring, highWater)...)
	}
	if err != nil {
		_ = subscriber.Close()
		return controlport.SubscribeResult{}, err
	}

	b.mu.Lock()
	if _, exists := b.subscribers[subscriber]; !exists {
		err := subscriber.errLocked()
		b.mu.Unlock()
		if err == nil {
			err = errors.New("controlclient: subscription closed during backfill")
		}
		return controlport.SubscribeResult{}, err
	}
	initial = dedupeFeedEnvelopes(initial)
	subscriber.liveInitial = dedupeFeedEnvelopesAgainst(subscriber.pending, initial)
	subscriber.pending = nil
	subscriber.paused = false
	subscriber.initial = initial
	b.mu.Unlock()
	subscriber.start()

	return controlport.SubscribeResult{
		Subscription: subscriber, Mode: mode, TransientGap: transientGap, BoundaryCursor: boundaryCursor,
	}, nil
}

// SubscribeFromNow creates an internal Surface subscription without replaying
// history. It is used when a Turn is registered before its ingress starts.
func (b *FeedBroker) SubscribeFromNow() (controlport.FeedSubscription, error) {
	if b == nil {
		return nil, errors.New("controlclient: nil feed broker")
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
	return subscriber, nil
}

// Attach immediately consumes one Turn ingress and publishes it into this
// Session. Terminal Envelopes do not close the Session broker.
func (b *FeedBroker) Attach(events <-chan eventstream.Envelope) {
	if b == nil || events == nil {
		return
	}
	go func() {
		for envelope := range events {
			_ = b.Publish(envelope)
		}
	}()
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
	for subscriber := range b.subscribers {
		b.stopSubscriberLocked(subscriber, nil)
	}
	return nil
}

func (b *FeedBroker) durableBackfill(ctx context.Context, requested eventstream.FeedPosition, through eventstream.DurableFeedPosition) ([]eventstream.Envelope, error) {
	if through.Seq == 0 {
		return nil, nil
	}
	if b.reader == nil {
		return nil, errors.New("controlclient: durable feed reader is unavailable")
	}
	anchor := requested.DurableAnchor()
	afterSeq := anchor.Seq
	if requested.Durable != nil && anchor.Seq > 0 {
		afterSeq--
	}
	var out []eventstream.Envelope
	for afterSeq < through.Seq {
		page, err := b.reader.EventsPage(ctx, session.EventPageRequest{
			SessionRef: b.ref, AfterSeq: afterSeq, ThroughSeq: through.Seq,
			Visibility: session.EventPageClientReplay,
		})
		if err != nil {
			return nil, err
		}
		for _, event := range page.Events {
			base := acpprojector.EnvelopeBaseFromSessionEvent(b.ref, event, acpprojector.SessionEventTransport{})
			for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
				if envelope.Position == nil || envelope.Position.Durable == nil {
					continue
				}
				if requested.Durable != nil && eventstream.CompareDurablePosition(*envelope.Position.Durable, *requested.Durable) <= 0 {
					continue
				}
				cursor, err := b.codec.Encode(b.ref.SessionID, *envelope.Position)
				if err != nil {
					return nil, err
				}
				envelope.Cursor = cursor
				out = append(out, envelope)
			}
		}
		if page.NextSeq <= afterSeq {
			break
		}
		afterSeq = page.NextSeq
		if !page.HasMore && afterSeq >= through.Seq {
			break
		}
	}
	return out, nil
}

func (b *FeedBroker) evictLocked() {
	cutoff := b.now().Add(-b.ringTTL)
	for len(b.ring) > 0 && (len(b.ring) > b.ringEvents || b.ringByteCount > b.ringBytes || b.ring[0].at.Before(cutoff)) {
		item := b.ring[0]
		b.ring = b.ring[1:]
		b.ringByteCount -= item.bytes
		if !isDurableFeedEnvelope(item.envelope) {
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
	subscriber.setErrLocked(err)
	subscriber.stopOnce.Do(func() { close(subscriber.stop) })
}

type feedSubscription struct {
	broker       *FeedBroker
	input        chan eventstream.Envelope
	out          chan eventstream.Envelope
	stop         chan struct{}
	done         chan struct{}
	backfillDone chan struct{}

	startOnce    sync.Once
	stopOnce     sync.Once
	backfillOnce sync.Once
	paused       bool
	initial      []eventstream.Envelope
	liveInitial  []eventstream.Envelope
	pending      []eventstream.Envelope

	stateMu    sync.RWMutex
	err        error
	lastCursor string
}

func newFeedSubscription(broker *FeedBroker, queueSize int) *feedSubscription {
	return &feedSubscription{
		broker: broker, input: make(chan eventstream.Envelope, queueSize),
		out: make(chan eventstream.Envelope), stop: make(chan struct{}), done: make(chan struct{}),
		backfillDone: make(chan struct{}),
	}
}

func (s *feedSubscription) start() {
	s.startOnce.Do(func() { go s.run() })
}

func (s *feedSubscription) run() {
	defer close(s.done)
	defer close(s.out)
	defer s.finishBackfill()
	for _, envelope := range s.initial {
		if !s.deliver(envelope) {
			return
		}
	}
	s.initial = nil
	s.finishBackfill()
	for _, envelope := range s.liveInitial {
		if !s.deliver(envelope) {
			return
		}
	}
	s.liveInitial = nil
	for {
		select {
		case <-s.stop:
			return
		case envelope := <-s.input:
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
	s.backfillOnce.Do(func() { close(s.backfillDone) })
}

func (s *feedSubscription) Close() error {
	if s == nil || s.broker == nil {
		return nil
	}
	s.broker.mu.Lock()
	s.broker.stopSubscriberLocked(s, nil)
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

func (s *feedSubscription) errLocked() error { return s.Err() }

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

func ringEnvelopes(ring []feedRingItem) []eventstream.Envelope {
	out := make([]eventstream.Envelope, 0, len(ring))
	for _, item := range ring {
		out = append(out, eventstream.CloneEnvelope(item.envelope))
	}
	return out
}

func ringTransientsAtOrAfter(ring []feedRingItem, highWater eventstream.DurableFeedPosition) []eventstream.Envelope {
	var out []eventstream.Envelope
	for _, item := range ring {
		position := item.envelope.Position
		if position == nil || position.Transient == nil {
			continue
		}
		if eventstream.CompareDurablePosition(position.Transient.Anchor, highWater) >= 0 {
			out = append(out, eventstream.CloneEnvelope(item.envelope))
		}
	}
	return out
}

func feedBoundaryCursor(ring []feedRingItem) string {
	if len(ring) == 0 {
		return ""
	}
	return ring[len(ring)-1].envelope.Cursor
}

func dedupeFeedEnvelopes(in []eventstream.Envelope) []eventstream.Envelope {
	seen := make(map[string]struct{}, len(in))
	out := make([]eventstream.Envelope, 0, len(in))
	for _, envelope := range in {
		key := feedDedupeKey(envelope)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, eventstream.CloneEnvelope(envelope))
	}
	return out
}

func dedupeFeedEnvelopesAgainst(in []eventstream.Envelope, previous []eventstream.Envelope) []eventstream.Envelope {
	seen := make(map[string]struct{}, len(previous)+len(in))
	for _, envelope := range previous {
		seen[feedDedupeKey(envelope)] = struct{}{}
	}
	out := make([]eventstream.Envelope, 0, len(in))
	for _, envelope := range in {
		key := feedDedupeKey(envelope)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, eventstream.CloneEnvelope(envelope))
	}
	return out
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
	Reader          session.PagedReader
	CursorCodec     *eventstream.CursorCodec
	RingEvents      int
	RingBytes       int
	RingTTL         time.Duration
	SubscriberQueue int
	Now             func() time.Time
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
		RingTTL: r.config.RingTTL, SubscriberQueue: r.config.SubscriberQueue, Now: r.config.Now,
	})
	if err != nil {
		return nil, err
	}
	r.feeds[ref.SessionID] = broker
	return broker, nil
}
