package tuiapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

const sessionObservationRecoveryHint = "Session connection lost; reconnecting…"
const sessionObservationRecoveryBudget = 35 * time.Second
const sessionObservationRequestTimeout = 5 * time.Second
const sessionObservationStableDuration = 5 * time.Second

type sessionObservationRecoveringMsg struct{}
type sessionApprovalRefreshMsg struct{ prompt PromptRequestMsg }

// sessionObserver owns only observation. Recovery never submits, approves, or
// interrupts work, and never promotes transport completion to Turn completion.
type sessionObserver struct {
	sender           *ProgramSender
	ctx              context.Context
	generation       uint64
	sessionID        string
	reconnect        controlprompt.SessionReconnect
	closed           bool
	recovered        bool
	live             bool
	automatic        bool
	recoveryDeadline time.Time
}

func (o *sessionObserver) send(msg tea.Msg) { o.sender.sessionSend(o.generation)(msg) }

func (o *sessionObserver) observeClosure(env eventstream.Envelope) {
	if env.SessionID == o.sessionID && env.Kind == eventstream.KindLifecycle &&
		env.Lifecycle != nil && env.Lifecycle.State == "closed" &&
		env.TurnID == "" && env.ApprovalRequestID == "" &&
		(env.Scope == "" || env.Scope == eventstream.ScopeMain) {
		o.closed = true
	}
}

func (o *sessionObserver) backfill(first *appserver.FeedDelivery) error {
	ctx := o.ctx
	if o.recovered {
		if o.recoveryDeadline.IsZero() {
			o.recoveryDeadline = time.Now().Add(sessionObservationRecoveryBudget)
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, minObservationDeadline(time.Now().Add(sessionObservationRequestTimeout), o.recoveryDeadline))
		defer cancel()
		o.sender.mu.Lock()
		o.sender.recoveryCancel = cancel
		o.sender.mu.Unlock()
		defer func() {
			o.sender.mu.Lock()
			if o.sender.viewGeneration == o.generation {
				o.sender.recoveryCancel = nil
			}
			o.sender.mu.Unlock()
		}()
	}
	if err := streamReconnectBackfillObserved(ctx, o.reconnect, o.send, first, o.observeClosure); err != nil {
		return err
	}
	o.send(sessionHistoryReadyMsg{})
	if first == nil {
		for _, env := range o.reconnect.BootstrapEvents() {
			o.send(env)
			if req := approvalPayloadFromACPEvent(env); req != nil {
				send := o.sender.sessionSend(o.generation)
				if o.recovered {
					scopedSend := send
					send = func(msg tea.Msg) {
						if prompt, ok := msg.(PromptRequestMsg); ok {
							msg = sessionApprovalRefreshMsg{prompt: prompt}
						}
						scopedSend(msg)
					}
				}
				sendApprovalPrompt(o.ctx, o.reconnect, req, send)
			}
		}
	}
	return nil
}

func (o *sessionObserver) run(err error) {
	defer func() {
		_ = o.reconnect.Close()
		o.sender.releaseSessionView(o.generation)
	}()
	for attempt := 0; ; {
		stable := false
		if err == nil {
			stable, err = o.follow()
			if stable {
				attempt = 0
			}
		}
		if o.ctx.Err() != nil {
			return
		}
		// A closed Session replays finitely. A newly resumed stream that ends
		// normally without a live tail also cannot establish a following view.
		if o.closed {
			err = appserver.ErrSessionClosed
		} else if o.recovered && !o.live && !stable && err == nil {
			err = errors.New("session observation ended after resume; use /resume to reconnect")
		}
		if o.recoveryDeadline.IsZero() {
			o.recoveryDeadline = time.Now().Add(sessionObservationRecoveryBudget)
		}
		if !time.Now().Before(o.recoveryDeadline) {
			err = fmt.Errorf("reconnect budget exhausted: %w", context.DeadlineExceeded)
		}
		if o.sender.resumeSession == nil || !retrySessionObservation(err) || !time.Now().Before(o.recoveryDeadline) {
			if err == nil {
				err = errors.New("session observation closed; use /resume to reconnect")
			}
			o.send(sessionObservationErrorMsg{err: fmt.Errorf("session observation unavailable: %w", err)})
			return
		}
		o.send(sessionObservationRecoveringMsg{})
		_ = o.reconnect.Close()
		err = o.recover(attempt)
		attempt++
	}
}

func (o *sessionObserver) follow() (bool, error) {
	stable := false
	followingSince := time.Now()
	markStable := func() {
		if time.Since(followingSince) >= sessionObservationStableDuration {
			stable = true
			o.recoveryDeadline = time.Time{}
		}
	}
	assembler := &appserver.FeedDeliveryAssembler{}
	ticker := time.NewTicker(eventStreamBatchInterval)
	defer ticker.Stop()
	var batcher eventStreamNarrativeBatcher
	defer batcher.flush(o.send)
	for {
		select {
		case <-o.ctx.Done():
			return stable, o.ctx.Err()
		case now := <-ticker.C:
			markStable()
			batcher.flushReady(now, o.send)
		case delivery, open := <-o.reconnect.Deliveries():
			markStable()
			if !open {
				return stable, o.reconnect.Err()
			}
			if delivery.Kind == appserver.FeedDeliveryReplaceBegin {
				batcher.flush(o.send)
				o.send(sessionHistoryReplacementMsg{state: o.reconnect.State()})
				if err := o.backfill(&delivery); err != nil {
					return stable, err
				}
				followingSince = time.Now()
				continue
			}
			events, replacement, err := assembler.Accept(delivery)
			if err != nil {
				return stable, err
			}
			if replacement {
				return stable, errors.New("session history changed; reattach to refresh the view")
			}
			for _, env := range events {
				if env.SessionID != "" && env.SessionID != o.sessionID {
					continue
				}
				o.live = true
				o.observeClosure(env)
				if observer, ok := o.reconnect.(interface{ ObserveEvent(eventstream.Envelope) }); ok {
					observer.ObserveEvent(env)
				}
				if !batcher.enqueue(env, o.send) {
					o.send(env)
					if req := approvalPayloadFromACPEvent(env); req != nil {
						sendApprovalPrompt(o.ctx, o.reconnect, req, o.sender.sessionSend(o.generation))
					}
				}
			}
		}
	}
}

func (o *sessionObserver) recover(attempt int) error {
	ctx, cancel := context.WithDeadline(o.ctx, o.recoveryDeadline)
	defer cancel()
	o.sender.mu.Lock()
	o.sender.recoveryCancel = cancel
	o.sender.mu.Unlock()
	defer func() {
		o.sender.mu.Lock()
		if o.sender.viewGeneration == o.generation {
			o.sender.recoveryCancel = nil
		}
		o.sender.mu.Unlock()
	}()
	delay := min(200*time.Millisecond<<min(attempt, 5), 5*time.Second)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	err := o.resume(ctx)
	if err != nil {
		return err
	}
	return o.backfill(nil)
}

func (o *sessionObserver) resume(ctx context.Context) error {
	if err := o.sender.lockSessionCommandsForObservation(ctx); err != nil {
		return err
	}
	defer o.sender.sessionCommands.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	selected, generation := o.sender.sessionView()
	if selected != o.sessionID || generation != o.generation {
		return context.Canceled
	}
	requestCtx, requestCancel := context.WithTimeout(ctx, sessionObservationRequestTimeout)
	snapshot, err := o.sender.resumeSession(requestCtx, o.sessionID)
	if err == nil {
		err = requestCtx.Err()
	}
	requestCancel()
	if err != nil || ctx.Err() != nil {
		if snapshot.Reconnect != nil {
			_ = snapshot.Reconnect.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if snapshot.Reconnect == nil {
		return errors.New("session attach returned no observation")
	}
	state := snapshot.Reconnect.State()
	if state.SessionID != o.sessionID {
		_ = snapshot.Reconnect.Close()
		return errors.New("session observation belongs to another Session")
	}
	o.ctx, o.generation = o.sender.replaceSessionView(o.ctx, o.sessionID)
	o.reconnect, o.recovered, o.live = snapshot.Reconnect, true, false
	o.sender.SendMsg(sessionViewStartMsg{generation: o.generation, state: state, recovery: true, automatic: o.automatic})
	return nil
}

func (s *ProgramSender) lockSessionCommandsForObservation(ctx context.Context) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.sessionCommands.TryLock() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *ProgramSender) cancelSessionRecovery() {
	s.mu.Lock()
	cancel := s.recoveryCancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func retrySessionObservation(err error) bool {
	if errors.Is(err, appserver.ErrSessionClosed) {
		return false
	}
	switch errorcode.CodeOf(err) {
	case errorcode.Unavailable:
		return true
	case errorcode.Timeout:
		return errors.Is(err, context.DeadlineExceeded)
	case errorcode.Unknown:
		return err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
			errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) ||
			errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
	default:
		return false
	}
}

// A new view cancels its predecessor; the episode's absolute deadline must
// survive that generation change so partial backfills cannot renew recovery.
func minObservationDeadline(attempt, episode time.Time) time.Time {
	if !episode.IsZero() && episode.Before(attempt) {
		return episode
	}
	return attempt
}
