package controlclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// SessionTurnStartRequest starts one main Turn after atomically attaching to
// the addressed Session feed. ContentParts carries provider-neutral prompt
// text and inline images; filesystem attachment resolution remains a Surface
// concern.
type SessionTurnStartRequest struct {
	SessionID    string
	Input        string
	DisplayInput string
	ContentParts []model.ContentPart
}

// ApprovalResolution is the client-selected response for one permission
// Envelope. The SessionTurn supplies its own Session and Turn targets.
type ApprovalResolution struct {
	RequestID  eventstream.ApprovalRequestID
	Outcome    string
	OptionID   string
	Approved   bool
	Reason     string
	ReviewText string
}

// TargetTurn is one target-filtered main or participant Turn view. Closing the
// view detaches observation only; it does not cancel the Runtime Turn or
// durably close the Session.
type TargetTurn interface {
	SessionID() string
	Target() TurnTarget
	// Events is an intentionally unbuffered view for one consumer. Backpressure
	// is isolated to this Turn's independently bounded FeedSubscription; it
	// cannot block Runtime publication or sibling observers.
	Events() <-chan eventstream.Envelope
	ResolveApproval(context.Context, ApprovalResolution) error
	Cancel(context.Context, string) error
	LastCursor() string
	Err() error
	Close() error
}

// SessionTurn is a TargetTurn that accepts main-conversation steer input.
type SessionTurn interface {
	TargetTurn
	Steer(context.Context, string, string, []model.ContentPart) error
}

// SessionTurnStarter establishes one target-filtered main Turn view.
type SessionTurnStarter interface {
	Start(context.Context, SessionTurnStartRequest) (SessionTurn, error)
}

// SessionTurnClient owns the reconnect/feed splice and typed main-Turn writes
// shared by presentation clients. It deliberately depends only on
// SessionClient so in-process and transport clients retain identical
// semantics.
type SessionTurnClient struct {
	client SessionClient
}

// NewSessionTurnClient constructs the high-level main-Turn client.
func NewSessionTurnClient(client SessionClient) (*SessionTurnClient, error) {
	if client == nil {
		return nil, errors.New("controlclient: Session client is required")
	}
	return &SessionTurnClient{client: client}, nil
}

// Start registers a continuation from the inspected feed boundary before
// Prompt and returns only Envelopes belonging to the accepted target. This
// avoids replaying the historical prefix, closes the race where a fast Turn
// publishes before its caller begins observing, and prevents an older terminal
// from ending the new Turn.
func (c *SessionTurnClient) Start(
	ctx context.Context,
	request SessionTurnStartRequest,
) (SessionTurn, error) {
	if c == nil || c.client == nil {
		return nil, errors.New("controlclient: Session turn client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	sessionID := strings.TrimSpace(request.SessionID)
	input := strings.TrimSpace(request.Input)
	displayInput := strings.TrimSpace(request.DisplayInput)
	if sessionID == "" {
		return nil, errors.New("controlclient: Session turn requires a Session ID")
	}
	if input == "" && len(request.ContentParts) == 0 {
		return nil, errors.New("controlclient: Session turn requires prompt input")
	}
	if displayInput == input {
		displayInput = ""
	}

	feedCtx, stopFeed, reconnected, err := openTargetObservation(ctx, c.client, sessionID)
	if err != nil {
		return nil, err
	}
	revision := reconnected.State.Revision
	result, err := c.client.Prompt(ctx, PromptRequest{
		WriteBase: WriteBase{
			OperationID:             newSessionTurnOperationID("prompt"),
			SessionID:               sessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: strings.TrimSpace(reconnected.State.Controller.EpochID),
		},
		Input:        input,
		DisplayInput: displayInput,
		ContentParts: append([]model.ContentPart(nil), request.ContentParts...),
	})
	if err != nil {
		_ = reconnected.Subscription.Close()
		stopFeed()
		return nil, err
	}
	if result.Outcome != OutcomeCommitted && result.Outcome != OutcomeAccepted {
		_ = reconnected.Subscription.Close()
		stopFeed()
		return nil, fmt.Errorf(
			"controlclient: prompt operation %q ended with outcome %q",
			result.OperationID,
			result.Outcome,
		)
	}
	if !validSessionTurnTarget(result.Target) {
		_ = reconnected.Subscription.Close()
		stopFeed()
		return nil, errors.New("controlclient: prompt returned no complete Turn target")
	}

	turn := newTargetTurn(c.client, sessionID, reconnected, feedCtx, stopFeed, result.Target)
	turn.steerFn = func(steerCtx context.Context, input, displayInput string, contentParts []model.ContentPart) error {
		_, steerErr := c.client.Steer(steerCtx, SteerRequest{
			WriteBase: WriteBase{
				OperationID:             newSessionTurnOperationID("steer"),
				SessionID:               sessionID,
				ExpectedControllerEpoch: turn.controllerEpoch,
			},
			Target:       turn.target,
			Input:        input,
			DisplayInput: displayInput,
			ContentParts: append([]model.ContentPart(nil), contentParts...),
		})
		return steerErr
	}
	turn.cancelFn = func(cancelCtx context.Context, reason string) error {
		_, cancelErr := c.client.Cancel(cancelCtx, CancelRequest{
			WriteBase: WriteBase{
				OperationID:             newSessionTurnOperationID("cancel"),
				SessionID:               sessionID,
				ExpectedControllerEpoch: turn.controllerEpoch,
			},
			Target: turn.target,
			Reason: reason,
		})
		return cancelErr
	}
	go turn.relay()
	return turn, nil
}

// openTargetObservation establishes the feed cut before a command is
// admitted, preserving fast target output without replaying the old Session
// prefix. The caller owns stopFeed and the returned subscription on success.
func openTargetObservation(
	ctx context.Context,
	client SessionClient,
	sessionID string,
) (context.Context, context.CancelFunc, ReconnectResult, error) {
	inspected, err := client.InspectSession(ctx, StateRequest{SessionID: sessionID})
	if err != nil {
		return nil, nil, ReconnectResult{}, err
	}
	feedCtx, stopFeed := context.WithCancel(context.WithoutCancel(ctx))
	reconnected, err := client.Reconnect(feedCtx, ReconnectRequest{
		SessionID: sessionID,
		Cursor:    strings.TrimSpace(inspected.BoundaryCursor),
	})
	if err != nil {
		stopFeed()
		return nil, nil, ReconnectResult{}, err
	}
	if reconnected.Subscription == nil {
		stopFeed()
		return nil, nil, ReconnectResult{}, errors.New("controlclient: Session reconnect returned no subscription")
	}
	return feedCtx, stopFeed, reconnected, nil
}

func newTargetTurn(
	client SessionClient,
	sessionID string,
	reconnected ReconnectResult,
	feedCtx context.Context,
	stopFeed context.CancelFunc,
	target TurnTarget,
) *sessionTurn {
	return &sessionTurn{
		client:          client,
		sessionID:       strings.TrimSpace(sessionID),
		controllerEpoch: strings.TrimSpace(reconnected.State.Controller.EpochID),
		target:          target,
		feedCtx:         feedCtx,
		stopFeed:        stopFeed,
		subscription:    reconnected.Subscription,
		events:          make(chan eventstream.Envelope),
		done:            make(chan struct{}),
	}
}

type sessionTurn struct {
	client          SessionClient
	sessionID       string
	controllerEpoch string
	target          TurnTarget
	feedCtx         context.Context
	stopFeed        context.CancelFunc
	steerFn         func(context.Context, string, string, []model.ContentPart) error
	cancelFn        func(context.Context, string) error

	mu           sync.RWMutex
	subscription FeedSubscription
	lastCursor   string
	err          error

	events    chan eventstream.Envelope
	done      chan struct{}
	closeOnce sync.Once
}

func (t *sessionTurn) SessionID() string {
	if t == nil {
		return ""
	}
	return t.sessionID
}

func (t *sessionTurn) Target() TurnTarget {
	if t == nil {
		return TurnTarget{}
	}
	return t.target
}

func (t *sessionTurn) Events() <-chan eventstream.Envelope {
	if t == nil {
		closed := make(chan eventstream.Envelope)
		close(closed)
		return closed
	}
	return t.events
}

func (t *sessionTurn) ResolveApproval(
	ctx context.Context,
	resolution ApprovalResolution,
) error {
	if t == nil || t.client == nil {
		return errors.New("controlclient: Session turn is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestID := strings.TrimSpace(string(resolution.RequestID))
	if requestID == "" {
		return errors.New("controlclient: approval request ID is required")
	}
	_, err := t.client.ResolveApproval(ctx, ResolveApprovalRequest{
		WriteBase: WriteBase{
			OperationID:             newSessionTurnOperationID("approval"),
			SessionID:               t.sessionID,
			ExpectedControllerEpoch: t.controllerEpoch,
		},
		Target:            t.target,
		ApprovalRequestID: requestID,
		Outcome:           strings.TrimSpace(resolution.Outcome),
		OptionID:          strings.TrimSpace(resolution.OptionID),
		Approved:          resolution.Approved,
		Reason:            strings.TrimSpace(resolution.Reason),
		ReviewText:        strings.TrimSpace(resolution.ReviewText),
	})
	return err
}

// Steer submits additional prompt content to this exact active main Turn.
// The target and controller epoch captured at admission fence the write
// without asking a Surface to reconstruct live identity.
func (t *sessionTurn) Steer(
	ctx context.Context,
	input string,
	displayInput string,
	contentParts []model.ContentPart,
) error {
	if t == nil || t.client == nil {
		return errors.New("controlclient: Session turn is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	input = strings.TrimSpace(input)
	displayInput = strings.TrimSpace(displayInput)
	if input == "" && len(contentParts) == 0 {
		return errors.New("controlclient: Session steer requires prompt input")
	}
	if displayInput == input {
		displayInput = ""
	}
	if t.steerFn == nil {
		return errors.New("controlclient: target Turn does not accept main-conversation steer input")
	}
	return t.steerFn(ctx, input, displayInput, contentParts)
}

func (t *sessionTurn) Cancel(ctx context.Context, reason string) error {
	if t == nil || t.client == nil {
		return errors.New("controlclient: Session turn is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if t.cancelFn == nil {
		return errors.New("controlclient: target Turn cannot be cancelled")
	}
	return t.cancelFn(ctx, strings.TrimSpace(reason))
}

func (t *sessionTurn) LastCursor() string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.lastCursor != "" {
		return t.lastCursor
	}
	if t.subscription != nil {
		return t.subscription.LastCursor()
	}
	return ""
}

func (t *sessionTurn) Err() error {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.err
}

func (t *sessionTurn) Close() error {
	if t == nil {
		return nil
	}
	var closeErr error
	t.closeOnce.Do(func() {
		t.stopFeed()
		t.mu.RLock()
		subscription := t.subscription
		t.mu.RUnlock()
		if subscription != nil {
			closeErr = subscription.Close()
		}
		<-t.done
	})
	return closeErr
}

func (t *sessionTurn) relay() {
	defer close(t.events)
	defer close(t.done)
	defer t.stopFeed()
	defer func() {
		t.mu.RLock()
		subscription := t.subscription
		t.mu.RUnlock()
		if subscription != nil {
			_ = subscription.Close()
		}
	}()

	for {
		t.mu.RLock()
		subscription := t.subscription
		t.mu.RUnlock()
		if subscription == nil {
			t.setErr(errors.New("controlclient: Session turn lost its feed subscription"))
			return
		}

		// FeedSubscription exposes a strict Backfill -> Events sequence. Even
		// though the inspected boundary normally makes this prefix empty, an
		// Envelope accepted between Inspect and Reconnect belongs here. Draining
		// it is required before the producer can install and expose the live
		// continuation.
		terminal, delivered := t.forwardTargetEvents(subscription.Backfill())
		if terminal || !delivered {
			return
		}
		terminal, delivered = t.forwardTargetEvents(subscription.Events())
		if terminal || !delivered {
			return
		}
		subscriptionErr := subscription.Err()
		if subscriptionErr == nil {
			t.setErr(errors.New("controlclient: Session feed closed before the target terminal"))
			return
		}
		var gap *FeedGapError
		if !errors.As(subscriptionErr, &gap) {
			t.setErr(subscriptionErr)
			return
		}
		cursor := strings.TrimSpace(gap.RetryCursor)
		if cursor == "" {
			cursor = strings.TrimSpace(subscription.LastCursor())
		}
		_ = subscription.Close()

		reconnected, err := t.client.Reconnect(t.feedCtx, ReconnectRequest{
			SessionID: t.sessionID,
			Cursor:    cursor,
		})
		if err != nil {
			t.setErr(err)
			return
		}
		if reconnected.Subscription == nil {
			t.setErr(errors.New("controlclient: Session reconnect returned no subscription"))
			return
		}
		t.mu.Lock()
		t.subscription = reconnected.Subscription
		t.mu.Unlock()
	}
}

// forwardTargetEvents returns terminal=true after delivering the target
// terminal. delivered=false means observation was explicitly detached.
func (t *sessionTurn) forwardTargetEvents(
	events <-chan eventstream.Envelope,
) (terminal bool, delivered bool) {
	for {
		select {
		case <-t.feedCtx.Done():
			return false, false
		case envelope, ok := <-events:
			if !ok {
				return false, true
			}
			t.recordCursor(envelope.Cursor)
			if !sessionTurnEnvelopeMatches(t.sessionID, t.target, envelope) {
				continue
			}
			select {
			case <-t.feedCtx.Done():
				return false, false
			case t.events <- envelope:
			}
			if eventstream.IsTurnTerminalLifecycle(envelope) {
				return true, true
			}
		}
	}
}

func (t *sessionTurn) recordCursor(cursor string) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return
	}
	t.mu.Lock()
	t.lastCursor = cursor
	t.mu.Unlock()
}

func (t *sessionTurn) setErr(err error) {
	if err == nil {
		return
	}
	t.mu.Lock()
	if t.err == nil {
		t.err = err
	}
	t.mu.Unlock()
}

func sessionTurnEnvelopeMatches(
	sessionID string,
	target TurnTarget,
	envelope eventstream.Envelope,
) bool {
	if actual := strings.TrimSpace(envelope.SessionID); actual != "" && actual != sessionID {
		return false
	}
	return strings.TrimSpace(envelope.HandleID) == strings.TrimSpace(target.HandleID) &&
		strings.TrimSpace(envelope.RunID) == strings.TrimSpace(target.RunID) &&
		strings.TrimSpace(envelope.TurnID) == strings.TrimSpace(target.TurnID)
}

func validSessionTurnTarget(target TurnTarget) bool {
	return strings.TrimSpace(target.HandleID) != "" &&
		strings.TrimSpace(target.RunID) != "" &&
		strings.TrimSpace(target.TurnID) != ""
}

func newSessionTurnOperationID(kind string) string {
	return "session-turn-" + strings.TrimSpace(kind) + "-" + uuid.NewString()
}

var _ SessionTurnStarter = (*SessionTurnClient)(nil)
var _ SessionTurn = (*sessionTurn)(nil)
var _ TargetTurn = (*sessionTurn)(nil)
