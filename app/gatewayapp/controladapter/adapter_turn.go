package controladapter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/internal/controlclient/turningress"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type gatewayTurn struct {
	handle gateway.TurnHandle
	feed   *turningress.Broker
	// sessionFeed remains available after the prepared subscription is closed.
	// Stop/failure takeover reattaches the same ingress here so sibling SSE/GUI
	// subscribers still observe the authoritative producer-barrier terminal.
	sessionFeed controlclientport.SessionFeed

	subscription   controlclientport.FeedSubscription
	attachment     <-chan error
	attach         func() <-chan error
	eventsOnce     sync.Once
	events         <-chan eventstream.Envelope
	relayStarted   atomic.Bool
	relayDone      chan struct{}
	relayDoneOnce  sync.Once
	stopOnce       sync.Once
	stopRequested  atomic.Bool
	stopMu         sync.RWMutex
	stopState      string
	stopErr        error
	signalInit     sync.Once
	stopSignal     chan struct{}
	stopSignalOnce sync.Once
	deliveryStop   chan struct{}
	deliveryOnce   sync.Once
	ownerStop      chan struct{}
	ownerStopOnce  sync.Once
}

func (t *gatewayTurn) HandleID() string { return t.handle.HandleID() }
func (t *gatewayTurn) RunID() string    { return t.handle.RunID() }
func (t *gatewayTurn) TurnID() string   { return t.handle.TurnID() }
func (t *gatewayTurn) Events() <-chan eventstream.Envelope {
	if t == nil {
		return eventstream.EnsureTerminalLifecycle(nil, "", "", "")
	}
	if t.subscription != nil {
		t.eventsOnce.Do(func() { t.events = t.turnSubscriptionEvents() })
		return t.events
	}
	return t.feed.Events()
}

func (t *gatewayTurn) turnSubscriptionEvents() <-chan eventstream.Envelope {
	t.ensureSignals()
	out := make(chan eventstream.Envelope)
	t.relayStarted.Store(true)
	go func() {
		defer close(out)
		defer t.relayDoneOnce.Do(func() { close(t.relayDone) })
		defer t.subscription.Close()
		defer t.stopOwnerContextWatch()
		events := t.subscription.Events()
		attachment := t.attachment
		if t.attach != nil {
			// SubscribeFromNow is established before BeginTurn so no event can
			// slip past the Turn boundary. Do not attach ingress until the Surface
			// has actually claimed Events: before Submit returns there is no legal
			// consumer, and a fast Turn would otherwise be misclassified as a slow
			// subscriber once the bounded feed queue fills.
			attachment = t.attach()
			t.attach = nil
		}
		for events != nil {
			select {
			case <-t.deliveryStop:
				return
			case envelope, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if t.stopRequested.Load() {
					t.finishStopped(out, attachment, false)
					return
				}
				// A Session feed may contain durable lifecycle history from older
				// Turns. Never forward a foreign main terminal: downstream Surfaces
				// correctly treat any main terminal as their current Turn boundary.
				if eventstream.IsTurnTerminalLifecycle(envelope) &&
					!t.isMainTerminal(envelope) {
					continue
				}
				if t.isMainTerminal(envelope) {
					t.stopOwnerContextWatch()
					t.sendFinal(out, envelope)
					return
				}
				if !t.sendLive(out, envelope) && t.deliveryStopped() {
					return
				}
			case err, ok := <-attachment:
				if !ok {
					attachment = nil
					continue
				}
				if err != nil {
					t.requestStop(eventstream.LifecycleStateFailed, err)
					t.finishStopped(out, attachment, true)
					return
				}
			}
		}
		if t.stopRequested.Load() {
			t.finishStopped(out, attachment, false)
			return
		}
		if err := t.subscription.Err(); err != nil {
			t.requestStop(eventstream.LifecycleStateInterrupted, err)
			t.finishStopped(out, attachment, false)
			return
		}
		t.stopOwnerContextWatch()
		terminal := eventstream.TurnCompleted(t.HandleID(), t.RunID(), t.TurnID(), time.Now())
		terminal.SessionID = t.handle.SessionRef().SessionID
		terminal.ScopeID = t.handle.SessionRef().SessionID
		t.sendFinal(out, terminal)
	}()
	return out
}

// finishStopped crosses the producer-close barrier before exposing a terminal.
// Target teardown makes AttachTo continue untargeted, so its clean completion
// proves the shared Session feed consumed through ACPEvents closure. Only an
// attachment failure requires fallback takeover of the remaining ingress.
func (t *gatewayTurn) finishStopped(
	out chan<- eventstream.Envelope,
	attachment <-chan error,
	attachmentFailed bool,
) {
	needsFallback := attachment == nil || attachmentFailed
	if attachment != nil {
		for err := range attachment {
			needsFallback = needsFallback || err != nil
		}
	}
	if needsFallback && t != nil && t.feed != nil {
		reattached := false
		if t.sessionFeed != nil {
			// Recover a durable Envelope that AttachTo may have fetched but not
			// accepted when its target was cancelled, then publish the remaining
			// ingress (including the main terminal) through the shared Session
			// broker for every other subscriber.
			primeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = t.sessionFeed.Prime(primeCtx)
			cancel()
			fallback := t.sessionFeed.Attach(t.feed.Events())
			fallbackFailed := false
			for err := range fallback {
				fallbackFailed = fallbackFailed || err != nil
			}
			reattached = !fallbackFailed
		}
		if !reattached {
			for range t.feed.Events() {
			}
		}
	}
	t.emitStopped(out)
}

func (t *gatewayTurn) emitStopped(out chan<- eventstream.Envelope) {
	if t == nil || out == nil {
		return
	}
	t.stopMu.RLock()
	state := t.stopState
	err := t.stopErr
	t.stopMu.RUnlock()
	if t.feed != nil {
		final := t.feed.FinalEnvelopes()
		terminalFound := false
		errorFound := false
		for _, envelope := range final {
			if eventstream.IsTurnTerminalLifecycle(envelope) {
				terminalFound = true
			}
			if envelope.Kind == eventstream.KindError || envelope.Err != nil {
				errorFound = true
			}
		}
		if terminalFound {
			if !errorFound && (state == eventstream.LifecycleStateFailed || state == eventstream.LifecycleStateInterrupted) {
				failure := eventstream.Error(err)
				failure.SessionID = t.handle.SessionRef().SessionID
				failure.HandleID = t.HandleID()
				failure.RunID = t.RunID()
				failure.TurnID = t.TurnID()
				failure.Scope = eventstream.ScopeMain
				failure.ScopeID = t.handle.SessionRef().SessionID
				if !t.sendFinal(out, failure) {
					return
				}
			}
			for _, envelope := range final {
				if !t.sendFinal(out, envelope) {
					return
				}
			}
			return
		}
	}
	reason := "turn cancelled"
	if err != nil {
		reason = err.Error()
	}
	if state == eventstream.LifecycleStateFailed || state == eventstream.LifecycleStateInterrupted {
		failure := eventstream.Error(err)
		failure.SessionID = t.handle.SessionRef().SessionID
		failure.HandleID = t.HandleID()
		failure.RunID = t.RunID()
		failure.TurnID = t.TurnID()
		failure.Scope = eventstream.ScopeMain
		failure.ScopeID = t.handle.SessionRef().SessionID
		if !t.sendFinal(out, failure) {
			return
		}
	}
	if state == "" {
		state = eventstream.LifecycleStateCancelled
	}
	terminal := eventstream.TurnLifecycle(
		t.HandleID(), t.RunID(), t.TurnID(),
		state, reason, "", time.Now(),
	)
	terminal.SessionID = t.handle.SessionRef().SessionID
	terminal.ScopeID = t.handle.SessionRef().SessionID
	t.sendFinal(out, terminal)
}

func (t *gatewayTurn) requestStop(state string, err error) {
	if t == nil {
		return
	}
	t.stopOnce.Do(func() {
		t.ensureSignals()
		if err == nil {
			err = errors.New("turn cancelled")
		}
		t.stopMu.Lock()
		t.stopState = strings.TrimSpace(state)
		t.stopErr = err
		t.stopRequested.Store(true)
		t.stopMu.Unlock()
		t.stopSignalOnce.Do(func() { close(t.stopSignal) })
		// Closing only this prepared subscription releases a blocked AttachTo;
		// it does not close the Session broker or cancel detached child work.
		if t.subscription != nil {
			_ = t.subscription.Close()
		}
		if t.feed != nil {
			t.feed.RequestStop(state, err.Error())
			t.feed.CancelProducer()
		} else if t.handle != nil {
			_ = t.handle.Cancel()
		}
	})
}

func (t *gatewayTurn) ensureSignals() {
	if t == nil {
		return
	}
	t.signalInit.Do(func() {
		t.stopSignal = make(chan struct{})
		t.deliveryStop = make(chan struct{})
		t.relayDone = make(chan struct{})
	})
}

func (t *gatewayTurn) deliveryStopped() bool {
	if t == nil {
		return true
	}
	t.ensureSignals()
	select {
	case <-t.deliveryStop:
		return true
	default:
		return false
	}
}

func (t *gatewayTurn) sendLive(out chan<- eventstream.Envelope, envelope eventstream.Envelope) bool {
	if t == nil || out == nil {
		return false
	}
	t.ensureSignals()
	if t.deliveryStopped() {
		return false
	}
	select {
	case out <- envelope:
		return true
	case <-t.stopSignal:
		return false
	case <-t.deliveryStop:
		return false
	}
}

func (t *gatewayTurn) sendFinal(out chan<- eventstream.Envelope, envelope eventstream.Envelope) bool {
	if t == nil || out == nil {
		return false
	}
	t.ensureSignals()
	if t.deliveryStopped() {
		return false
	}
	select {
	case out <- envelope:
		return true
	case <-t.deliveryStop:
		return false
	}
}

func (t *gatewayTurn) watchOwnerContext(ctx context.Context) {
	if t == nil || ctx == nil || ctx.Done() == nil || t.subscription == nil {
		return
	}
	t.ownerStop = make(chan struct{})
	t.ensureSignals()
	go func() {
		select {
		case <-ctx.Done():
			t.requestStop(eventstream.LifecycleStateCancelled, ctx.Err())
		case <-t.ownerStop:
		case <-t.deliveryStop:
		}
	}()
}

func (t *gatewayTurn) stopOwnerContextWatch() {
	if t == nil || t.ownerStop == nil {
		return
	}
	t.ownerStopOnce.Do(func() { close(t.ownerStop) })
}

func (t *gatewayTurn) isMainTerminal(envelope eventstream.Envelope) bool {
	if !eventstream.IsTurnTerminalLifecycle(envelope) {
		return false
	}
	if strings.TrimSpace(envelope.HandleID) != strings.TrimSpace(t.HandleID()) {
		return false
	}
	if strings.TrimSpace(envelope.RunID) != strings.TrimSpace(t.RunID()) {
		return false
	}
	if strings.TrimSpace(envelope.TurnID) != strings.TrimSpace(t.TurnID()) {
		return false
	}
	switch strings.TrimSpace(envelope.Lifecycle.State) {
	case eventstream.LifecycleStateCompleted, eventstream.LifecycleStateFailed,
		eventstream.LifecycleStateInterrupted, eventstream.LifecycleStateCancelled:
		return true
	default:
		return false
	}
}

func (t *gatewayTurn) SubmitApproval(ctx context.Context, decision ApprovalDecision) error {
	return t.handle.Submit(ctx, gateway.SubmitRequest{
		Kind: gateway.SubmissionKindApproval,
		Approval: &gateway.ApprovalDecision{
			RequestID:  decision.RequestID,
			Outcome:    strings.TrimSpace(decision.Outcome),
			OptionID:   strings.TrimSpace(decision.OptionID),
			Approved:   decision.Approved,
			Reason:     strings.TrimSpace(decision.Reason),
			ReviewText: strings.TrimSpace(decision.ReviewText),
		},
	})
}

func (t *gatewayTurn) Cancel() {
	t.requestStop(eventstream.LifecycleStateCancelled, errors.New("turn cancelled"))
}

func (t *gatewayTurn) Close() error {
	if t == nil {
		return nil
	}
	t.ensureSignals()
	t.deliveryOnce.Do(func() { close(t.deliveryStop) })
	t.stopOwnerContextWatch()
	if t.subscription != nil {
		_ = t.subscription.Close()
	}
	if t.feed != nil {
		t.feed.Close()
	}
	if t.relayStarted.Load() {
		<-t.relayDone
	}
	if t.handle != nil {
		return t.handle.Close()
	}
	return nil
}
