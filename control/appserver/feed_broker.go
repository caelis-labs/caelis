package appserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	"github.com/caelis-labs/caelis/control/streamspool"
)

const (
	sessionEnvelopeRecordType uint16 = 1
	canonicalFollowInterval          = 25 * time.Millisecond
	feedLifecycleSealTimeout         = 5 * time.Second
)

// FeedBrokerConfig configures one Session-scoped Control spool owner. The old
// ring and subscriber queue fields are intentionally gone: file quota belongs
// to streamspool and every consumer owns only its reader cursor.
type FeedBrokerConfig struct {
	SessionRef  session.SessionRef
	Reader      session.PagedReader
	Spool       streamspool.Store
	CursorCodec *CursorCodec
	Now         func() time.Time
}

// FeedBroker validates and serializes Session Envelopes into one append-only
// spool partition. Session storage remains canonical; the spool is a lossy
// delivery trace and never participates in model-context reconstruction.
type FeedBroker struct {
	ref    session.SessionRef
	reader session.PagedReader
	spool  streamspool.Store
	codec  *CursorCodec
	now    func() time.Time

	primeMu       sync.Mutex
	acceptMu      sync.Mutex
	sealMu        sync.Mutex
	writer        streamspool.Writer
	key           streamspool.Key
	latestDurable eventstream.DurableFeedPosition
	scannedSeq    uint64
	spoolErr      error
	lastTerminal  *eventstream.Envelope
	sealed        bool
	writerSealed  bool
	sealedCh      chan struct{}
	closed        bool

	liveNarratives map[feedNarrativeKey]struct{}

	subsMu sync.Mutex
	subs   map[*feedSubscription]struct{}
}

func NewFeedBroker(cfg FeedBrokerConfig) (*FeedBroker, error) {
	cfg.SessionRef = session.NormalizeSessionRef(cfg.SessionRef)
	if strings.TrimSpace(cfg.SessionRef.SessionID) == "" {
		return nil, errors.New("controlclient: feed broker session id is required")
	}
	if cfg.CursorCodec == nil {
		return nil, errors.New("controlclient: feed broker cursor codec is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	b := &FeedBroker{
		ref: cfg.SessionRef, reader: cfg.Reader, spool: cfg.Spool, codec: cfg.CursorCodec, now: cfg.Now,
		subs: map[*feedSubscription]struct{}{}, sealedCh: make(chan struct{}),
	}
	originComplete := true
	if checkpointReader, ok := cfg.Reader.(session.EventCheckpointReader); ok {
		checkpoint, err := checkpointReader.EventCheckpoint(context.Background(), cfg.SessionRef)
		if err != nil {
			return nil, fmt.Errorf("controlclient: initialize Session feed checkpoint: %w", err)
		}
		b.scannedSeq = checkpoint.ThroughSeq
		if position := checkpointBoundaryPosition(cfg.SessionRef, checkpoint.LastClientReplayEvent); position != nil && position.Durable != nil {
			b.latestDurable = *position.Durable
		}
		originComplete = checkpoint.ThroughSeq == 0
	} else if cfg.Reader != nil {
		// A reader without an atomic checkpoint cannot prove an exact origin.
		originComplete = false
	}
	if cfg.Spool != nil {
		logical := streamspool.LogicalKey{
			Namespace: streamspool.NamespaceSession,
			Digest:    streamspool.DigestStrings(cfg.SessionRef.SessionID),
		}
		writer, err := cfg.Spool.Register(context.Background(), logical, streamspool.WriterOptions{OriginComplete: originComplete})
		if err != nil {
			b.spoolErr = err
		} else {
			b.writer = writer
			b.key = writer.Key()
		}
	}
	return b, nil
}

// Prime appends newly committed canonical Session projections. It pages
// directly from the authoritative store and never materializes full history.
func (b *FeedBroker) Prime(ctx context.Context) error {
	if b == nil {
		return errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.primeMu.Lock()
	defer b.primeMu.Unlock()
	return b.primeCanonicalLocked(ctx, 0)
}

// primeCanonicalLocked fills the authoritative prefix through throughSeq. A
// zero boundary scans all currently committed events. The caller holds primeMu
// so a later producer event cannot overtake a missing canonical predecessor.
func (b *FeedBroker) primeCanonicalLocked(ctx context.Context, throughSeq uint64) error {
	if b.reader == nil {
		return nil
	}
	for {
		b.acceptMu.Lock()
		after := b.scannedSeq
		closed := b.closed
		sealed := b.sealed
		b.acceptMu.Unlock()
		if closed {
			return errors.New("controlclient: feed broker is closed")
		}
		if sealed || (throughSeq > 0 && after >= throughSeq) {
			return nil
		}
		page, err := b.reader.EventsPage(ctx, session.EventPageRequest{
			SessionRef: b.ref, AfterSeq: after, ThroughSeq: throughSeq, Visibility: session.EventPageClientReplay,
		})
		if err != nil {
			return err
		}
		observed := after
		for _, event := range page.Events {
			if event == nil {
				continue
			}
			observed = max(observed, event.Seq)
			if suppressHistoricalChildStreamMirror(event) {
				continue
			}
			if err := b.publishCanonicalCatchup(ctx, event); err != nil {
				return err
			}
		}
		b.acceptMu.Lock()
		b.scannedSeq = max(b.scannedSeq, observed, page.NextSeq)
		b.acceptMu.Unlock()
		if page.NextSeq <= after {
			return nil
		}
	}
}

// Publish accepts one normalized producer Envelope. Durable events are first
// reconciled from Session truth so a transient terminal cannot overtake the
// canonical final response. Spool failure degrades observation only.
func (b *FeedBroker) Publish(envelope eventstream.Envelope) error {
	if b == nil {
		return errors.New("controlclient: nil feed broker")
	}
	if err := ValidateEnvelopeDelivery(envelope); err != nil {
		return fmt.Errorf("controlclient: feed envelope delivery: %w", err)
	}
	// A normalized durable producer Envelope carries the active Handle/Run/Turn
	// target that canonical Session replay intentionally does not retain. Keep
	// that exact delivery identity in the lossy spool, but first fill any missing
	// durable predecessor from Session truth. Append-before-Publish producers can
	// race across steering, so accepting seq N must not advance the watermark
	// past an unpublished seq N-1.
	if isDurableFeedEnvelope(envelope) {
		b.primeMu.Lock()
		defer b.primeMu.Unlock()
		if seq := envelope.Position.Durable.Seq; seq > 1 {
			if err := b.primeCanonicalLocked(brokerContext(b), seq-1); err != nil {
				return err
			}
			b.acceptMu.Lock()
			contiguous := b.scannedSeq >= seq-1
			b.acceptMu.Unlock()
			if !contiguous {
				return fmt.Errorf("controlclient: durable feed gap before sequence %d", seq)
			}
		}
		return b.publishAccepted(brokerContext(b), envelope)
	}
	// Prime remains the ordering barrier before a terminal lifecycle so any
	// canonical facts not observed through a producer are appended first.
	if isMainTerminalEnvelope(envelope) {
		// Prime is an ordering optimization for canonical facts, not authority
		// over the producer terminal. A read-side failure must not suppress the
		// basic terminal fallback.
		_ = b.Prime(brokerContext(b))
	}
	return b.publishAccepted(brokerContext(b), envelope)
}

func brokerContext(_ *FeedBroker) context.Context { return context.Background() }

func (b *FeedBroker) publishAccepted(ctx context.Context, envelope eventstream.Envelope) error {
	b.acceptMu.Lock()
	defer b.acceptMu.Unlock()
	if b.closed || b.sealed {
		return errors.New("controlclient: feed broker is closed")
	}
	envelope = eventstream.CloneEnvelope(envelope)
	if strings.TrimSpace(envelope.SessionID) == "" {
		envelope.SessionID = b.ref.SessionID
	}
	if strings.TrimSpace(envelope.SessionID) != b.ref.SessionID {
		return fmt.Errorf("controlclient: feed envelope session %q does not match %q", envelope.SessionID, b.ref.SessionID)
	}
	if err := ValidateEnvelopeDelivery(envelope); err != nil {
		return fmt.Errorf("controlclient: feed envelope delivery: %w", err)
	}
	b.noteTerminalLocked(envelope)
	if isDurableFeedEnvelope(envelope) && compareDurablePosition(*envelope.Position.Durable, b.latestDurable) <= 0 {
		return nil
	}
	if b.writer == nil || b.spoolErr != nil {
		b.acceptWithoutSpoolLocked(envelope)
		return nil
	}
	bounds, err := b.writer.Bounds(ctx)
	if err != nil {
		b.disableSpoolLocked(err)
		b.acceptWithoutSpoolLocked(envelope)
		return nil
	}
	recordOffset := bounds.High
	if envelope.Delivery == nil || envelope.Delivery.Mode == "" || envelope.Delivery.Mode == eventstream.DeliveryTransient {
		envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
		envelope.Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
			Anchor: b.latestDurable, Generation: sessionSpoolGeneration(b.key), Sequence: uint64(recordOffset) + 1,
		}}
	}
	cursor, err := b.codec.EncodeSpool(b.ref.SessionID, sessionSpoolCursor{Key: b.key, Offset: recordOffset + 1}, *envelope.Position)
	if err != nil {
		b.disableSpoolLocked(err)
		b.acceptWithoutSpoolLocked(envelope)
		return nil
	}
	envelope.Cursor = cursor
	payload, err := json.Marshal(envelope)
	if err != nil {
		b.disableSpoolLocked(fmt.Errorf("controlclient: encode feed envelope: %w", err))
		b.acceptWithoutSpoolLocked(envelope)
		return nil
	}
	if _, err := b.writer.Append(ctx, sessionEnvelopeRecordType, b.now(), payload); err != nil {
		b.disableSpoolLocked(err)
		b.acceptWithoutSpoolLocked(envelope)
		return nil
	}
	b.acceptDurableLocked(envelope)
	b.observeLiveNarrativeLocked(envelope)
	return nil
}

func (b *FeedBroker) acceptWithoutSpoolLocked(envelope eventstream.Envelope) {
	b.acceptDurableLocked(envelope)
	if isMainTerminalEnvelope(envelope) {
		b.offerTerminalFallbackLocked()
	}
}

// noteTerminalLocked retains one small semantic result, never payload history.
// It is used only when the file trace cannot deliver the terminal record.
func (b *FeedBroker) noteTerminalLocked(envelope eventstream.Envelope) {
	if isMainTerminalEnvelope(envelope) {
		clone := eventstream.CloneEnvelope(envelope)
		clone.Cursor = ""
		clone.Position = nil
		clone.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
		b.lastTerminal = &clone
		return
	}
	if b.lastTerminal == nil || envelope.Scope == eventstream.ScopeSubagent || envelope.Scope == eventstream.ScopeParticipant {
		return
	}
	if sameTurnIdentity(*b.lastTerminal, envelope) {
		return
	}
	if strings.TrimSpace(envelope.HandleID) != "" || strings.TrimSpace(envelope.RunID) != "" || strings.TrimSpace(envelope.TurnID) != "" {
		b.lastTerminal = nil
	}
}

func sameTurnIdentity(left, right eventstream.Envelope) bool {
	return strings.TrimSpace(left.HandleID) == strings.TrimSpace(right.HandleID) &&
		strings.TrimSpace(left.RunID) == strings.TrimSpace(right.RunID) &&
		strings.TrimSpace(left.TurnID) == strings.TrimSpace(right.TurnID)
}

// offerTerminalFallbackLocked is non-blocking and bounded to one coalesced
// terminal per subscriber. acceptMu must be held by the caller.
func (b *FeedBroker) offerTerminalFallbackLocked() {
	if b.lastTerminal == nil {
		return
	}
	b.subsMu.Lock()
	defer b.subsMu.Unlock()
	for sub := range b.subs {
		sub.offerTerminal(*b.lastTerminal)
	}
}

func (b *FeedBroker) acceptDurableLocked(envelope eventstream.Envelope) {
	if !isDurableFeedEnvelope(envelope) {
		return
	}
	b.latestDurable = *envelope.Position.Durable
	b.scannedSeq = max(b.scannedSeq, envelope.Position.Durable.Seq)
}

func (b *FeedBroker) disableSpoolLocked(err error) {
	if err != nil && b.spoolErr == nil {
		b.spoolErr = err
		b.liveNarratives = nil
	}
}

func isDurableFeedEnvelope(envelope eventstream.Envelope) bool {
	return envelope.Delivery != nil &&
		(envelope.Delivery.Mode == eventstream.DeliveryCanonical || envelope.Delivery.Mode == eventstream.DeliveryMirror) &&
		envelope.Position != nil && envelope.Position.Durable != nil
}

func isMainTerminalEnvelope(envelope eventstream.Envelope) bool {
	return eventstream.IsTurnTerminalLifecycle(envelope)
}

func sessionSpoolGeneration(key streamspool.Key) string {
	return hex.EncodeToString(key.Epoch[:]) + "." + hex.EncodeToString(key.Incarnation[:])
}

func (b *FeedBroker) Subscribe(ctx context.Context, req SubscribeRequest) (SubscribeResult, error) {
	result, _, err := b.subscribeCheckpoint(ctx, req)
	return result, err
}

func (b *FeedBroker) subscribeCheckpoint(ctx context.Context, req SubscribeRequest) (SubscribeResult, session.EventCheckpoint, error) {
	if b == nil {
		return SubscribeResult{}, session.EventCheckpoint{}, errors.New("controlclient: nil feed broker")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.acceptMu.Lock()
	closed := b.closed
	b.acceptMu.Unlock()
	if closed {
		return SubscribeResult{}, session.EventCheckpoint{}, errors.New("controlclient: feed broker is closed")
	}
	if strings.TrimSpace(req.SessionID) != b.ref.SessionID {
		return SubscribeResult{}, session.EventCheckpoint{}, ErrCursorSessionMismatch
	}
	checkpoint, err := b.eventCheckpoint(ctx)
	if err != nil {
		return SubscribeResult{}, session.EventCheckpoint{}, err
	}
	if strings.TrimSpace(req.Cursor) != "" {
		point, resumePosition, err := b.codec.decodeResume(b.ref.SessionID, req.Cursor)
		if err != nil {
			return SubscribeResult{}, session.EventCheckpoint{}, err
		}
		if point != nil {
			bounds, anchor, exactErr := b.exactBounds(ctx, *point)
			if exactErr == nil {
				boundary := sessionSpoolCursor{Key: point.Key, Offset: bounds.High}
				boundaryPosition := sessionBoundaryPosition(anchor, point.Key, bounds.High)
				cursor, encodeErr := b.codec.EncodeSpool(b.ref.SessionID, boundary, boundaryPosition)
				if encodeErr != nil {
					return SubscribeResult{}, session.EventCheckpoint{}, encodeErr
				}
				sub := b.startSubscription(ctx, point.Offset, bounds.High, nil, 0, durableAnchor(resumePosition), req.Cursor, cursor)
				return SubscribeResult{
					Subscription: sub, BoundaryCursor: cursor, BoundaryPosition: &boundaryPosition,
				}, checkpoint, nil
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return SubscribeResult{}, session.EventCheckpoint{}, ctxErr
			}
			// A valid cursor authenticates product identity, not cache
			// availability. Stale epochs, GC, poison, and missing files all select
			// one authoritative replacement below.
		}
	}

	// Publish holds acceptMu across Bounds, Append, and durable acceptance. Read
	// the canonical cut and physical high-water under that same mutex so a
	// record cannot land between the replacement boundary and exact follow.
	b.acceptMu.Lock()
	d0 := b.latestDurable
	writer, key, spoolErr := b.writer, b.key, b.spoolErr
	var bounds streamspool.Bounds
	var boundsErr error
	if writer != nil && spoolErr == nil {
		bounds, boundsErr = writer.Bounds(ctx)
		if boundsErr != nil || bounds.State == streamspool.StatePoisoned || bounds.State == streamspool.StateStoreClosed {
			if boundsErr == nil {
				boundsErr = streamspool.ErrUnavailable
			}
			b.disableSpoolLocked(boundsErr)
			spoolErr = b.spoolErr
		}
	}
	b.acceptMu.Unlock()
	if writer != nil && spoolErr == nil && boundsErr == nil {
		if bounds.OriginComplete && bounds.Low == 0 && bounds.State != streamspool.StatePoisoned && bounds.State != streamspool.StateStoreClosed {
			position := sessionBoundaryPosition(d0, key, bounds.High)
			cursor, err := b.codec.EncodeSpool(b.ref.SessionID, sessionSpoolCursor{Key: key, Offset: bounds.High}, position)
			if err != nil {
				return SubscribeResult{}, session.EventCheckpoint{}, err
			}
			sub := b.startSubscription(ctx, 0, bounds.High, nil, 0, eventstream.DurableFeedPosition{}, "", cursor)
			return SubscribeResult{Subscription: sub, BoundaryCursor: cursor, BoundaryPosition: &position}, checkpoint, nil
		}
	}
	h0 := streamspool.Offset(0)
	if writer != nil && spoolErr == nil && boundsErr == nil {
		h0 = bounds.High
	}
	position := eventstream.FeedPosition{Durable: &eventstream.DurableFeedPosition{
		Seq: d0.Seq, ProjectionIndex: d0.ProjectionIndex,
	}}
	boundaryCursor := ""
	if writer != nil && spoolErr == nil && boundsErr == nil {
		position = sessionBoundaryPosition(d0, key, h0)
		boundaryCursor, _ = b.codec.EncodeSpool(b.ref.SessionID, sessionSpoolCursor{Key: key, Offset: h0}, position)
	} else {
		boundaryCursor, _ = b.codec.Encode(b.ref.SessionID, position)
	}
	// The replacement covers exactly through D0, not through the Session
	// checkpoint. A canonical commit may be visible to EventCheckpoint before
	// its producer reaches Publish; following from checkpoint.ThroughSeq would
	// skip that committed event forever.
	sub := b.startSubscription(ctx, h0, h0, &d0, d0.Seq, d0, "", boundaryCursor)
	return SubscribeResult{
		Subscription: sub, BoundaryCursor: boundaryCursor, BoundaryPosition: &position,
	}, checkpoint, nil
}

func (b *FeedBroker) exactBounds(ctx context.Context, point sessionSpoolCursor) (streamspool.Bounds, eventstream.DurableFeedPosition, error) {
	b.acceptMu.Lock()
	defer b.acceptMu.Unlock()
	anchor := b.latestDurable
	if b.spool == nil || point.Key != b.key || b.spoolErr != nil {
		return streamspool.Bounds{}, anchor, streamspool.ErrExpired
	}
	bounds, err := b.spool.Bounds(ctx, point.Key)
	if err != nil {
		return streamspool.Bounds{}, anchor, err
	}
	if point.Offset < bounds.Low || point.Offset > bounds.High || bounds.State == streamspool.StatePoisoned || bounds.State == streamspool.StateStoreClosed {
		return streamspool.Bounds{}, anchor, streamspool.ErrExpired
	}
	return bounds, anchor, nil
}

func (b *FeedBroker) eventCheckpoint(ctx context.Context) (session.EventCheckpoint, error) {
	if reader, ok := b.reader.(session.EventCheckpointReader); ok {
		checkpoint, err := reader.EventCheckpoint(ctx, b.ref)
		if err != nil {
			return session.EventCheckpoint{}, err
		}
		checkpoint.Session = session.CloneSession(checkpoint.Session)
		checkpoint.LastClientReplayEvent = session.CloneEvent(checkpoint.LastClientReplayEvent)
		return checkpoint, nil
	}
	b.acceptMu.Lock()
	through := b.scannedSeq
	b.acceptMu.Unlock()
	return session.EventCheckpoint{ThroughSeq: through}, nil
}

func sessionBoundaryPosition(anchor eventstream.DurableFeedPosition, key streamspool.Key, offset streamspool.Offset) eventstream.FeedPosition {
	return eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
		Anchor: anchor, Generation: sessionSpoolGeneration(key), Sequence: uint64(offset) + 1,
	}}
}

func (b *FeedBroker) startSubscription(ctx context.Context, start, initialHigh streamspool.Offset, replayThrough *eventstream.DurableFeedPosition, canonicalAfter uint64, lastDurable eventstream.DurableFeedPosition, initialCursor, syncCursor string) *feedSubscription {
	sub := newFeedSubscription(ctx, b, start, initialHigh, replayThrough, canonicalAfter, lastDurable, initialCursor, syncCursor)
	b.acceptMu.Lock()
	closed := b.closed
	b.subsMu.Lock()
	if closed {
		sub.cancel()
	} else {
		b.subs[sub] = struct{}{}
	}
	b.subsMu.Unlock()
	if !closed && (b.writer == nil || b.spoolErr != nil) && b.lastTerminal != nil {
		sub.offerTerminal(*b.lastTerminal)
	}
	b.acceptMu.Unlock()
	go sub.run()
	return sub
}

func (b *FeedBroker) Boundary() (*eventstream.FeedPosition, string) {
	if b == nil {
		return nil, ""
	}
	b.acceptMu.Lock()
	writer, key, spoolErr := b.writer, b.key, b.spoolErr
	if writer == nil || spoolErr != nil {
		b.acceptMu.Unlock()
		return nil, ""
	}
	bounds, err := writer.Bounds(context.Background())
	anchor := b.latestDurable
	b.acceptMu.Unlock()
	if err != nil {
		return nil, ""
	}
	position := sessionBoundaryPosition(anchor, key, bounds.High)
	cursor, err := b.codec.EncodeSpool(b.ref.SessionID, sessionSpoolCursor{Key: key, Offset: bounds.High}, position)
	if err != nil {
		return nil, ""
	}
	return &position, cursor
}

// Seal permanently closes producer admission and the physical writer while
// allowing existing subscribers to drain every accepted record to EOF.
func (b *FeedBroker) Seal(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.sealMu.Lock()
	defer b.sealMu.Unlock()
	b.acceptMu.Lock()
	if !b.sealed {
		b.sealed = true
		b.liveNarratives = nil
		close(b.sealedCh)
	}
	writer := b.writer
	writerSealed := b.writerSealed
	if writer == nil {
		b.writerSealed = true
	}
	b.acceptMu.Unlock()
	if writer == nil || writerSealed {
		return nil
	}
	// Product-address cleanup must not leak a registration merely because the
	// initiating request was canceled. Preserve context values, but give the
	// physical close one independent, bounded attempt; a non-context failure
	// remains retryable because writerSealed is not advanced.
	sealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), feedLifecycleSealTimeout)
	err := writer.Seal(sealCtx)
	if err == nil {
		err = writer.Close()
	}
	cancel()
	if err != nil {
		return err
	}
	b.acceptMu.Lock()
	b.writerSealed = true
	b.acceptMu.Unlock()
	return nil
}

func (b *FeedBroker) Close() error {
	if b == nil {
		return nil
	}
	sealErr := b.Seal(context.Background())
	b.acceptMu.Lock()
	if b.closed {
		b.acceptMu.Unlock()
		return sealErr
	}
	b.closed = true
	b.acceptMu.Unlock()
	b.subsMu.Lock()
	subs := make([]*feedSubscription, 0, len(b.subs))
	for sub := range b.subs {
		subs = append(subs, sub)
	}
	b.subsMu.Unlock()
	for _, sub := range subs {
		_ = sub.Close()
	}
	return sealErr
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

type feedSubscription struct {
	ctx            context.Context
	cancel         context.CancelFunc
	broker         *FeedBroker
	start          streamspool.Offset
	initialHigh    streamspool.Offset
	replayThrough  *eventstream.DurableFeedPosition
	canonicalAfter uint64
	lastDurable    eventstream.DurableFeedPosition
	replacementID  string
	synced         bool
	syncCursor     string

	deliveries         chan FeedDelivery
	terminals          chan eventstream.Envelope
	done               chan struct{}
	closeOnce          sync.Once
	mu                 sync.Mutex
	err                error
	lastCursor         string
	lastTerminalHandle string
	lastTerminalRun    string
	lastTerminalTurn   string
}

func newFeedSubscription(parent context.Context, broker *FeedBroker, start, initialHigh streamspool.Offset, replayThrough *eventstream.DurableFeedPosition, canonicalAfter uint64, lastDurable eventstream.DurableFeedPosition, initialCursor, syncCursor string) *feedSubscription {
	ctx, cancel := context.WithCancel(parent)
	var replay *eventstream.DurableFeedPosition
	if replayThrough != nil {
		copy := *replayThrough
		replay = &copy
	}
	sub := &feedSubscription{
		ctx: ctx, cancel: cancel, broker: broker, start: start, initialHigh: initialHigh, replayThrough: replay,
		deliveries: make(chan FeedDelivery), terminals: make(chan eventstream.Envelope, 1), done: make(chan struct{}), lastCursor: strings.TrimSpace(initialCursor),
		canonicalAfter: canonicalAfter, lastDurable: lastDurable, syncCursor: strings.TrimSpace(syncCursor),
	}
	sub.refreshReplacementID()
	return sub
}

func (s *feedSubscription) run() {
	defer close(s.done)
	defer close(s.deliveries)
	defer s.unregister()
	if s.replayThrough != nil {
		if err := s.deliverCanonicalReplacement(); err != nil {
			s.setErr(err)
			return
		}
		if !s.spoolAvailable() {
			if err := s.followCanonical(s.lastDurable); err != nil {
				s.setErr(err)
			}
			return
		}
	}
	if err := s.followSpool(s.start); err != nil {
		if s.ctx.Err() != nil {
			return
		}
		if errors.Is(err, io.EOF) {
			// A complete sealed trace needs no replacement. If final Prime was
			// interrupted, however, a canonical tail may complete transient text
			// already delivered in this trace (or before its resume cursor).
			if s.broker.reader != nil {
				if _, hasCheckpoint := s.broker.reader.(session.EventCheckpointReader); !hasCheckpoint {
					if err := s.replaceAndFollowCanonical(); err != nil {
						s.setErr(err)
					}
					return
				}
				checkpoint, err := s.broker.eventCheckpoint(s.ctx)
				if err != nil {
					s.setErr(err)
					return
				}
				position := checkpointBoundaryPosition(s.broker.ref, checkpoint.LastClientReplayEvent)
				if position != nil && position.Durable != nil && compareDurablePosition(*position.Durable, s.lastDurable) > 0 {
					if err := s.replaceAndFollowCanonical(); err != nil {
						s.setErr(err)
					}
				}
			}
			return
		}
		s.disableSpool(err)
		// Canonical events contain complete messages, while the exact prefix
		// may contain transient fragments beyond lastDurable. Replace that
		// prefix atomically; appending the canonical message would duplicate it.
		if err := s.replaceAndFollowCanonical(); err != nil {
			s.setErr(err)
		}
	}
}

func (s *feedSubscription) deliverCanonicalReplacement() error {
	if !s.deliver(FeedDelivery{Kind: FeedDeliveryReplaceBegin, Source: FeedSourceReplacement, SnapshotID: s.replacementID}) {
		return s.ctx.Err()
	}
	page, err := s.replayCanonical()
	if err != nil {
		return err
	}
	if !s.deliver(FeedDelivery{Kind: FeedDeliveryReplaceEnd, Source: FeedSourceReplacement, SnapshotID: s.replacementID, Page: page}) {
		return s.ctx.Err()
	}
	if !s.deliver(FeedDelivery{Kind: FeedDeliverySync, Source: FeedSourceExact, NextCursor: s.syncCursor}) {
		return s.ctx.Err()
	}
	s.synced = true
	return nil
}

func (s *feedSubscription) refreshReplacementID() {
	if s == nil || s.broker == nil || s.replayThrough == nil {
		return
	}
	raw := fmt.Sprintf("%s:%d:%d:%d:%d:%s", s.broker.ref.SessionID, s.start, s.replayThrough.Seq,
		s.replayThrough.ProjectionIndex, s.canonicalAfter, s.lastCursor)
	digest := sha256.Sum256([]byte(raw))
	s.replacementID = hex.EncodeToString(digest[:])
}

func (s *feedSubscription) spoolAvailable() bool {
	if s == nil || s.broker == nil {
		return false
	}
	s.broker.acceptMu.Lock()
	available := s.broker.spool != nil && s.broker.writer != nil && s.broker.spoolErr == nil
	s.broker.acceptMu.Unlock()
	return available
}

func (s *feedSubscription) disableSpool(err error) {
	if s == nil || s.broker == nil || err == nil {
		return
	}
	s.broker.acceptMu.Lock()
	s.broker.disableSpoolLocked(err)
	s.broker.offerTerminalFallbackLocked()
	s.broker.acceptMu.Unlock()
}

func (s *feedSubscription) replaceAndFollowCanonical() error {
	if s == nil || s.broker == nil || s.broker.reader == nil {
		return streamspool.ErrUnavailable
	}
	if err := s.broker.Prime(s.ctx); err != nil {
		return err
	}
	checkpoint, err := s.broker.eventCheckpoint(s.ctx)
	if err != nil {
		return err
	}
	through := eventstream.DurableFeedPosition{}
	if position := checkpointBoundaryPosition(s.broker.ref, checkpoint.LastClientReplayEvent); position != nil && position.Durable != nil {
		through = *position.Durable
	} else {
		s.broker.acceptMu.Lock()
		through = s.broker.latestDurable
		s.broker.acceptMu.Unlock()
	}
	s.replayThrough = &through
	// Replacement covers only the last client-replay projection. Journal-only
	// or concurrently committed records after it must still be scanned.
	s.canonicalAfter = through.Seq
	s.lastDurable = through
	s.syncCursor, _ = s.broker.codec.Encode(s.broker.ref.SessionID, eventstream.FeedPosition{Durable: &through})
	s.refreshReplacementID()
	if err := s.deliverCanonicalReplacement(); err != nil {
		return err
	}
	return s.followCanonical(s.lastDurable)
}

func (s *feedSubscription) replayCanonical() (uint32, error) {
	if s.broker == nil || s.broker.reader == nil || s.replayThrough == nil || s.replayThrough.Seq == 0 {
		return 0, nil
	}
	after := uint64(0)
	pageNumber := uint32(0)
	for after < s.replayThrough.Seq {
		page, err := s.broker.reader.EventsPage(s.ctx, session.EventPageRequest{
			SessionRef: s.broker.ref, AfterSeq: after, ThroughSeq: s.replayThrough.Seq,
			Visibility: session.EventPageClientReplay,
		})
		if err != nil {
			return pageNumber, err
		}
		for _, event := range page.Events {
			if event == nil || suppressHistoricalChildStreamMirror(event) {
				continue
			}
			base := acpprojector.EnvelopeBaseFromSessionEvent(s.broker.ref, event, acpprojector.SessionEventTransport{})
			for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
				if envelope.Position == nil || envelope.Position.Durable == nil || compareDurablePosition(*envelope.Position.Durable, *s.replayThrough) > 0 {
					continue
				}
				// Replacement pages are atomic snapshots, not resumable records.
				// Keep the durable position as canonical provenance, but only the
				// matching Sync carries the boundary cursor after commit.
				envelope.Cursor = ""
				raw, err := json.Marshal(envelope)
				if err != nil {
					return pageNumber, err
				}
				if len(raw) > maxFeedReplacementPageBytes {
					return pageNumber, errorcode.New(errorcode.ResourceExhausted, "controlclient: Session replacement page exceeds byte limit")
				}
				if !s.deliver(FeedDelivery{
					Kind: FeedDeliveryReplacePage, Source: FeedSourceReplacement,
					SnapshotID: s.replacementID, Page: pageNumber,
					Events: []eventstream.Envelope{eventstream.CloneEnvelope(envelope)},
				}) {
					return pageNumber, s.ctx.Err()
				}
				pageNumber++
			}
		}
		if page.NextSeq <= after || len(page.Events) == 0 {
			break
		}
		after = page.NextSeq
	}
	return pageNumber, nil
}

// followCanonical is the availability fallback for a disabled or lost spool.
// It polls only the authoritative paged Session store, retains no payload
// queue, and advances by durable sequence. Transient trace events are allowed
// to disappear in this mode by design.
func (s *feedSubscription) followCanonical(after eventstream.DurableFeedPosition) error {
	if s == nil || s.broker == nil || s.broker.reader == nil {
		return streamspool.ErrUnavailable
	}
	ticker := time.NewTicker(canonicalFollowInterval)
	defer ticker.Stop()
	pageAfter := after.Seq
	if after.Seq > 0 {
		pageAfter--
	}
	var pendingTerminal *eventstream.Envelope
	var pendingThrough eventstream.DurableFeedPosition
	for {
		page, err := s.broker.reader.EventsPage(s.ctx, session.EventPageRequest{
			SessionRef: s.broker.ref, AfterSeq: pageAfter, Visibility: session.EventPageClientReplay,
		})
		if err != nil {
			return err
		}
		for _, event := range page.Events {
			if event == nil || suppressHistoricalChildStreamMirror(event) {
				continue
			}
			base := acpprojector.EnvelopeBaseFromSessionEvent(s.broker.ref, event, acpprojector.SessionEventTransport{})
			for _, envelope := range acpprojector.ProjectSessionEventEnvelope(base, event) {
				if envelope.Position == nil || envelope.Position.Durable == nil {
					continue
				}
				if compareDurablePosition(*envelope.Position.Durable, after) <= 0 {
					continue
				}
				cursor, encodeErr := s.broker.codec.Encode(s.broker.ref.SessionID, *envelope.Position)
				if encodeErr != nil {
					return encodeErr
				}
				envelope.Cursor = cursor
				if !s.deliver(FeedDelivery{
					Kind: FeedDeliveryAppendPage, Source: FeedSourceExact,
					Events: []eventstream.Envelope{eventstream.CloneEnvelope(envelope)}, NextCursor: cursor,
				}) {
					return s.ctx.Err()
				}
				after = *envelope.Position.Durable
				s.lastDurable = after
			}
		}
		if page.NextSeq > pageAfter {
			pageAfter = page.NextSeq
			s.canonicalAfter = pageAfter
			continue
		}
		if pendingTerminal != nil {
			if compareDurablePosition(after, pendingThrough) < 0 {
				return errors.New("controlclient: canonical Session feed did not reach the accepted durable watermark before terminal")
			}
			if !s.deliverFallbackTerminal(*pendingTerminal) {
				return s.ctx.Err()
			}
			pendingTerminal = nil
		}
		sealed, complete := s.broker.canonicalSealState(after)
		if complete {
			select {
			case terminal := <-s.terminals:
				if !s.deliverFallbackTerminal(terminal) {
					return s.ctx.Err()
				}
			default:
			}
			return nil
		}
		if sealed {
			select {
			case <-s.ctx.Done():
				return s.ctx.Err()
			case terminal := <-s.terminals:
				clone := eventstream.CloneEnvelope(terminal)
				pendingTerminal = &clone
				pendingThrough = s.canonicalWatermark()
				continue
			case <-ticker.C:
			}
			continue
		}
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case terminal := <-s.terminals:
			clone := eventstream.CloneEnvelope(terminal)
			pendingTerminal = &clone
			pendingThrough = s.canonicalWatermark()
			continue
		case <-s.broker.sealedCh:
		case <-ticker.C:
		}
	}
}

// canonicalWatermark captures the accepted durable cut that a pending terminal
// must follow. Later unrelated publications do not move this terminal's cut.
func (s *feedSubscription) canonicalWatermark() eventstream.DurableFeedPosition {
	if s == nil || s.broker == nil {
		return eventstream.DurableFeedPosition{}
	}
	s.broker.acceptMu.Lock()
	through := s.broker.latestDurable
	s.broker.acceptMu.Unlock()
	return through
}

func (b *FeedBroker) canonicalSealState(after eventstream.DurableFeedPosition) (sealed, complete bool) {
	if b == nil {
		return true, true
	}
	b.acceptMu.Lock()
	sealed = b.sealed || b.closed
	through := b.latestDurable
	b.acceptMu.Unlock()
	return sealed, sealed && compareDurablePosition(after, through) >= 0
}

func (s *feedSubscription) followSpool(offset streamspool.Offset) error {
	if s.broker == nil {
		return streamspool.ErrUnavailable
	}
	s.broker.acceptMu.Lock()
	spool, writer, key, spoolErr := s.broker.spool, s.broker.writer, s.broker.key, s.broker.spoolErr
	s.broker.acceptMu.Unlock()
	if spool == nil || writer == nil || spoolErr != nil {
		return streamspool.ErrUnavailable
	}
	reader, err := spool.Reader(s.ctx, key, offset)
	if err != nil {
		return err
	}
	defer reader.Close()
	type nextResult struct {
		record streamspool.Record
		err    error
	}
	pumpCtx, stopPump := context.WithCancel(s.ctx)
	defer stopPump()
	next := make(chan nextResult, 1)
	go func() {
		for {
			record, err := reader.Next(pumpCtx)
			select {
			case <-pumpCtx.Done():
				return
			case next <- nextResult{record: record, err: err}:
			}
			if err != nil {
				return
			}
		}
	}()
	current := offset
	for {
		if !s.synced && current >= s.initialHigh {
			if !s.deliver(FeedDelivery{Kind: FeedDeliverySync, Source: FeedSourceExact, NextCursor: s.syncCursor}) {
				return s.ctx.Err()
			}
			s.synced = true
		}
		var record streamspool.Record
		var err error
		select {
		case <-s.ctx.Done():
			return s.ctx.Err()
		case terminal := <-s.terminals:
			if s.broker.reader == nil {
				if !s.deliverFallbackTerminal(terminal) {
					return s.ctx.Err()
				}
			} else {
				// A terminal must not let consumers exit before the canonical
				// replacement repairs output lost with the optional trace.
				s.offerTerminal(terminal)
			}
			return streamspool.ErrUnavailable
		case result := <-next:
			record, err = result.record, result.err
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if record.Type != sessionEnvelopeRecordType || record.Offset != current {
			return errors.New("controlclient: invalid Session spool record")
		}
		var envelope eventstream.Envelope
		if err := json.Unmarshal(record.Payload, &envelope); err != nil {
			return fmt.Errorf("controlclient: decode Session spool record: %w", err)
		}
		current++
		if !s.deliver(FeedDelivery{
			Kind: FeedDeliveryAppendPage, Source: FeedSourceExact,
			Events: []eventstream.Envelope{eventstream.CloneEnvelope(envelope)}, NextCursor: envelope.Cursor,
		}) {
			return s.ctx.Err()
		}
		if isMainTerminalEnvelope(envelope) {
			s.rememberDeliveredTerminal(envelope)
		}
		if envelope.Position != nil && envelope.Position.Durable != nil && compareDurablePosition(*envelope.Position.Durable, s.lastDurable) > 0 {
			s.lastDurable = *envelope.Position.Durable
		}
	}
}

func (s *feedSubscription) deliverFallbackTerminal(envelope eventstream.Envelope) bool {
	if s == nil || !isMainTerminalEnvelope(envelope) || s.terminalDelivered(envelope) {
		return true
	}
	envelope = eventstream.CloneEnvelope(envelope)
	envelope.Cursor = ""
	envelope.Position = nil
	envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	if !s.deliver(FeedDelivery{
		Kind: FeedDeliveryAppendPage, Source: FeedSourceResult,
		Events: []eventstream.Envelope{envelope},
	}) {
		return false
	}
	s.rememberDeliveredTerminal(envelope)
	return true
}

func (s *feedSubscription) offerTerminal(envelope eventstream.Envelope) {
	if s == nil || !isMainTerminalEnvelope(envelope) || s.terminalDelivered(envelope) {
		return
	}
	envelope = eventstream.CloneEnvelope(envelope)
	select {
	case s.terminals <- envelope:
		return
	default:
	}
	select {
	case <-s.terminals:
	default:
	}
	select {
	case s.terminals <- envelope:
	default:
	}
}

func (s *feedSubscription) terminalDelivered(envelope eventstream.Envelope) bool {
	handleID := strings.TrimSpace(envelope.HandleID)
	runID := strings.TrimSpace(envelope.RunID)
	turnID := strings.TrimSpace(envelope.TurnID)
	if handleID == "" && runID == "" && turnID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastTerminalHandle == handleID &&
		s.lastTerminalRun == runID &&
		s.lastTerminalTurn == turnID
}

func (s *feedSubscription) rememberDeliveredTerminal(envelope eventstream.Envelope) {
	s.mu.Lock()
	s.lastTerminalHandle = strings.TrimSpace(envelope.HandleID)
	s.lastTerminalRun = strings.TrimSpace(envelope.RunID)
	s.lastTerminalTurn = strings.TrimSpace(envelope.TurnID)
	s.mu.Unlock()
}

func (s *feedSubscription) deliver(delivery FeedDelivery) bool {
	select {
	case <-s.ctx.Done():
		return false
	case s.deliveries <- delivery:
		if delivery.NextCursor != "" {
			s.lastCursor = delivery.NextCursor
		}
		return true
	}
}

func (s *feedSubscription) unregister() {
	if s.broker == nil {
		return
	}
	s.broker.subsMu.Lock()
	delete(s.broker.subs, s)
	s.broker.subsMu.Unlock()
}

func (s *feedSubscription) Deliveries() <-chan FeedDelivery { return s.deliveries }

func (s *feedSubscription) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(s.cancel)
	return nil
}

func (s *feedSubscription) Err() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *feedSubscription) setErr(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// feedRegistry owns at most one broker for each Session ID.
type feedRegistry struct {
	config FeedRegistryConfig
	mu     sync.Mutex
	feeds  map[string]*FeedBroker
	closed bool
}

type FeedRegistryConfig struct {
	Reader      session.PagedReader
	Spool       streamspool.Store
	CursorCodec *CursorCodec
	Now         func() time.Time
}

func NewFeedRegistry(config FeedRegistryConfig) (FeedRegistryLifecycle, error) {
	if config.CursorCodec == nil {
		return nil, errors.New("controlclient: feed registry cursor codec is required")
	}
	return &feedRegistry{config: config, feeds: map[string]*FeedBroker{}}, nil
}

func (r *feedRegistry) Session(ref session.SessionRef) (SessionFeed, error) {
	if r == nil {
		return nil, errors.New("controlclient: nil feed registry")
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return nil, errors.New("controlclient: session id is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, errors.New("controlclient: feed registry is closed")
	}
	if broker := r.feeds[ref.SessionID]; broker != nil {
		return broker, nil
	}
	if stateReader, ok := r.config.Reader.(session.StateReader); ok {
		closed, err := IsSessionClosed(context.Background(), stateReader, ref)
		if err != nil {
			return nil, err
		}
		if closed {
			// Closed Sessions remain canonically readable, but they never allocate
			// another process-lifetime writer or registry entry.
			broker, err := NewFeedBroker(FeedBrokerConfig{
				SessionRef: ref, Reader: r.config.Reader, CursorCodec: r.config.CursorCodec, Now: r.config.Now,
			})
			if err != nil {
				return nil, err
			}
			if err := broker.Seal(context.Background()); err != nil {
				_ = broker.Close()
				return nil, err
			}
			return broker, nil
		}
	}
	broker, err := NewFeedBroker(FeedBrokerConfig{
		SessionRef: ref, Reader: r.config.Reader, Spool: r.config.Spool,
		CursorCodec: r.config.CursorCodec, Now: r.config.Now,
	})
	if err != nil {
		return nil, err
	}
	r.feeds[ref.SessionID] = broker
	return broker, nil
}

// CloseSession removes one permanently closed Session from the live registry.
// Prime captures the final durable lifecycle fact before Seal lets current
// readers drain to EOF; a later read of the closed Session is finite and uses
// canonical replay without allocating a spool writer.
func (r *feedRegistry) CloseSession(ctx context.Context, ref session.SessionRef) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ref = session.NormalizeSessionRef(ref)
	if strings.TrimSpace(ref.SessionID) == "" {
		return errors.New("controlclient: session id is required")
	}
	r.mu.Lock()
	broker := r.feeds[ref.SessionID]
	r.mu.Unlock()
	if broker == nil {
		return nil
	}
	primeErr := broker.Prime(ctx)
	sealErr := broker.Seal(ctx)
	if sealErr == nil {
		r.mu.Lock()
		if r.feeds[ref.SessionID] == broker {
			delete(r.feeds, ref.SessionID)
		}
		r.mu.Unlock()
	}
	return errors.Join(primeErr, sealErr)
}

// Close stops every live broker before the shared spool Store closes.
func (r *feedRegistry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	r.closed = true
	type candidate struct {
		sessionID string
		broker    *FeedBroker
	}
	brokers := make([]candidate, 0, len(r.feeds))
	for sessionID, broker := range r.feeds {
		brokers = append(brokers, candidate{sessionID: sessionID, broker: broker})
	}
	r.mu.Unlock()
	var joined error
	for _, item := range brokers {
		err := item.broker.Close()
		joined = errors.Join(joined, err)
		if err == nil {
			r.mu.Lock()
			if r.feeds[item.sessionID] == item.broker {
				delete(r.feeds, item.sessionID)
			}
			r.mu.Unlock()
		}
	}
	return joined
}

var _ SessionFeed = (*FeedBroker)(nil)
var _ FeedRegistry = (*feedRegistry)(nil)
var _ FeedRegistryLifecycle = (*feedRegistry)(nil)
var _ FeedSubscription = (*feedSubscription)(nil)
