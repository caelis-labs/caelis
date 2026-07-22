package turningress

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// Broker owns delivery of one gateway Turn into the Control-owned Session
// feed. Task output is deliberately absent: each Task is delivered through
// control/taskstream and can outlive this broker.
type Broker struct {
	handle   kernel.TurnHandle
	identity turnIdentity

	ctx    context.Context
	cancel context.CancelFunc

	events chan eventstream.Envelope
	done   chan struct{}

	startOnce      sync.Once
	mu             sync.Mutex
	stopState      string
	stopReason     string
	producerCancel sync.Once

	finalMu       sync.RWMutex
	finalError    *eventstream.Envelope
	finalTerminal *eventstream.Envelope
}

// New constructs the main-only ingress for one request-scoped Control turn.
func New(handle kernel.TurnHandle) *Broker {
	ctx, cancel := context.WithCancel(context.Background())
	return &Broker{
		handle:   handle,
		identity: newTurnIdentity(handle),
		ctx:      ctx,
		cancel:   cancel,
		events:   make(chan eventstream.Envelope, 32),
		done:     make(chan struct{}),
	}
}

// Done is closed after the Runtime ACP producer closes and the broker emits
// its main terminal boundary.
func (b *Broker) Done() <-chan struct{} {
	if b == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	b.start()
	return b.done
}

// Events starts the broker and returns only the main Session feed ingress.
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
	b.startOnce.Do(func() { go b.run() })
}

func (b *Broker) run() {
	defer close(b.done)
	defer close(b.events)
	defer b.cancel()

	if b.handle == nil {
		b.finish(b.terminalForProducerEnd("", false))
		return
	}
	mainEvents := b.handle.ACPEvents()
	if mainEvents == nil {
		b.finish(b.terminalForProducerEnd("", false))
		return
	}

	failureReason := ""
	cancelled := false
	var producerTerminal *eventstream.Envelope
	for {
		select {
		case <-b.ctx.Done():
			return
		case env, ok := <-mainEvents:
			if !ok {
				b.finish(b.terminalAfterProducerClose(producerTerminal, failureReason, cancelled))
				return
			}
			// Scoped child live output belongs exclusively to Task Stream. Scope
			// describes the producer, though, so child approvals and participant
			// lifecycle facts must remain on the Session feed.
			if isSubagentTaskStreamObservation(env) {
				continue
			}
			mainScope := isMainFeedScope(env)
			if mainScope {
				env = b.stampMainEnvelope(env)
				if !b.identity.matches(env) {
					continue
				}
			}
			if err := validateIngressEnvelope(env); err != nil {
				failureReason = err.Error()
				failure := b.stampMainEnvelope(eventstream.Error(err))
				b.recordFinalError(failure)
				if !b.emit(failure) {
					return
				}
				b.RequestStop(eventstream.LifecycleStateFailed, failureReason)
				b.cancelOwningProducer()
				continue
			}
			if mainScope {
				if eventstream.IsTurnTerminalLifecycle(env) {
					if producerTerminal == nil {
						clone := eventstream.CloneEnvelope(env)
						producerTerminal = &clone
					}
					continue
				}
				if env.Err != nil || env.Kind == eventstream.KindError {
					failureReason = strings.TrimSpace(firstNonEmpty(env.Error, errorText(env.Err)))
					cancelled = eventstream.IsCancelledReason(failureReason)
				}
			}
			if !b.emit(env) {
				return
			}
		}
	}
}

// finish publishes the terminal as soon as the owning ACP producer closes.
// It never reads or waits for Task streams.
func (b *Broker) finish(terminal eventstream.Envelope) {
	terminal = b.stampMainEnvelope(terminal)
	b.recordFinalTerminal(terminal)
	b.emit(terminal)
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

// RequestCancel records the owning Turn's cancellation without bypassing the
// Runtime producer-close barrier.
func (b *Broker) RequestCancel() {
	b.RequestStop(eventstream.LifecycleStateCancelled, "")
}

// RequestStop records the terminal outcome to publish after the Runtime
// producer closes. It does not affect detached Task execution.
func (b *Broker) RequestStop(state string, reason string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.stopState == "" {
		b.stopState = strings.TrimSpace(state)
		b.stopReason = strings.TrimSpace(reason)
	}
	b.mu.Unlock()
}

// Cancel stops only this broker's delivery. Use RequestCancel when the caller
// still needs the Runtime producer-close barrier.
func (b *Broker) Cancel() {
	if b == nil {
		return
	}
	b.RequestCancel()
	b.cancel()
}

// Close stops broker delivery. It never cancels a Task.
func (b *Broker) Close() {
	if b == nil {
		return
	}
	b.start()
	b.cancel()
	<-b.done
}

func (b *Broker) terminalForProducerEnd(failureReason string, cancelled bool) eventstream.Envelope {
	if state, reason := b.requestedStop(); state != "" {
		if reason == "" {
			reason = failureReason
		}
		return eventstream.TurnLifecycle(
			b.identity.handleID, b.identity.runID, b.identity.turnID,
			state, reason, "", time.Now(),
		)
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

func (b *Broker) terminalAfterProducerClose(
	producerTerminal *eventstream.Envelope,
	failureReason string,
	cancelled bool,
) eventstream.Envelope {
	state, _ := b.requestedStop()
	if state != "" || cancelled || strings.TrimSpace(failureReason) != "" {
		return b.terminalForProducerEnd(failureReason, cancelled)
	}
	if producerTerminal != nil {
		return eventstream.CloneEnvelope(*producerTerminal)
	}
	return b.terminalForProducerEnd("", false)
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

// CancelProducer idempotently cancels the owning main Turn. Detached Tasks use
// their existing Control cancellation path and are not touched here.
func (b *Broker) CancelProducer() { b.cancelOwningProducer() }

// FinalEnvelopes returns the broker-authored terminal outcome.
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

func (b *Broker) recordFinalTerminal(envelope eventstream.Envelope) {
	b.finalMu.Lock()
	clone := eventstream.CloneEnvelope(envelope)
	b.finalTerminal = &clone
	b.finalMu.Unlock()
}

func (b *Broker) recordFinalError(envelope eventstream.Envelope) {
	b.finalMu.Lock()
	clone := eventstream.CloneEnvelope(envelope)
	b.finalError = &clone
	b.finalMu.Unlock()
}

type turnIdentity struct {
	handleID string
	runID    string
	turnID   string
}

func newTurnIdentity(handle kernel.TurnHandle) turnIdentity {
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

func isSubagentTaskStreamObservation(env eventstream.Envelope) bool {
	if env.Scope != eventstream.ScopeSubagent {
		return false
	}
	if strings.TrimSpace(string(env.ApprovalRequestID)) != "" {
		return false
	}
	switch env.Kind {
	case eventstream.KindRequestPermission, eventstream.KindApprovalReview, eventstream.KindParticipant:
		return false
	default:
		return true
	}
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
