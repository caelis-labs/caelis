package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

type draftPromptCall struct {
	owner         *draftClient
	sessionID     string
	inner         *acpsdk.PreparedRequest[v2.PromptResponse]
	activityID    string
	echoFloor     uint64
	activityFloor uint64
	done          chan struct{}
	result        PromptResponse
	err           error
	finished      bool
}

func draftPromptBlocks(prompt []json.RawMessage) ([]v2.ContentBlock, error) {
	blocks := make([]v2.ContentBlock, 0, len(prompt))
	for _, raw := range prompt {
		var block v2.ContentBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			return nil, err
		}
		if err := block.Validate(); err != nil {
			return nil, err
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func (d *draftClient) preparePrompt(sessionID string, prompt []json.RawMessage, meta map[string]any) (*PromptCall, error) {
	blocks, err := draftPromptBlocks(prompt)
	if err != nil {
		return nil, err
	}
	rawMeta, err := rawMessagesFromValues(meta)
	if err != nil {
		return nil, err
	}
	if _, supplied := rawMeta[acpdraft.ActiveOnlyMetaKey]; supplied {
		return nil, errors.New("acp draft: initial prompt cannot supply a conditional guard")
	}
	if rawMeta == nil {
		rawMeta = make(map[string]json.RawMessage)
	}
	for key, value := range acpdraft.IdleOnlyMeta() {
		rawMeta[key] = value
	}
	call, err := acpsdk.PrepareRequest[v2.PromptResponse](d.client.conn, v2.AgentMethodSessionPrompt, v2.PromptRequest{SessionId: v2.SessionId(sessionID), Prompt: blocks, Meta: rawMeta})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	session, err := d.ensureSessionLocked(sessionID)
	if err != nil {
		call.Abandon()
		return nil, err
	}
	if session.active || session.prompt != nil {
		call.Abandon()
		return nil, errors.New("acp draft: Session already has foreground work")
	}
	prepared := &draftPromptCall{owner: d, sessionID: sessionID, inner: call, done: make(chan struct{}), echoFloor: session.messageSequence, activityFloor: session.activitySequence}
	session.prompt = prepared
	return &PromptCall{draft: prepared}, nil
}

func (c *draftPromptCall) finishLocked(result PromptResponse, err error) {
	if c.finished {
		return
	}
	c.result, c.err, c.finished = result, err, true
	close(c.done)
}

func (c *draftPromptCall) observeAuthRequired(observer func() error) error {
	return c.inner.ObserveResponse(func(_ context.Context, response acpsdk.RPCResponse) error {
		if response.Error == nil || response.Error.Code != ErrorCodeAuthRequired || observer == nil {
			return nil
		}
		return observer()
	})
}

func (c *draftPromptCall) wait(ctx context.Context) (PromptResponse, error) {
	accepted, err := c.inner.Wait(ctx)
	if err != nil {
		if !PromptOutcomeUnknown(err) {
			c.owner.mu.Lock()
			if session := c.owner.sessionLocked(c.sessionID); session.prompt == c {
				session.prompt = nil
			}
			c.owner.mu.Unlock()
			return PromptResponse{}, err
		}
		if failure := c.owner.failureCause(); failure != nil {
			err = errors.Join(err, failure)
		}
		return PromptResponse{}, &draftInsertionOutcomeError{err: err}
	}
	if strings.TrimSpace(string(accepted.MessageId)) == "" {
		return PromptResponse{}, &draftInsertionOutcomeError{err: errors.New("accepted prompt has no message identity")}
	}
	if err := c.bindReceiptActivity(acpdraft.ActivityID(accepted.Meta)); err != nil {
		c.owner.fail(err)
		return PromptResponse{}, &draftInsertionOutcomeError{err: err}
	}
	if err := c.owner.waitUserEcho(ctx, c.sessionID, string(accepted.MessageId), c.echoFloor); err != nil {
		return PromptResponse{}, &draftInsertionOutcomeError{err: err}
	}
	// Prefer an already known settlement when cancellation or connection loss
	// arrives concurrently. A settled Activity is positive completion evidence.
	select {
	case <-c.done:
		return c.settledResult()
	default:
	}
	select {
	case <-c.done:
		return c.settledResult()
	case <-ctx.Done():
		select {
		case <-c.done:
			return c.settledResult()
		default:
		}
		return PromptResponse{}, &draftInsertionOutcomeError{err: ctx.Err()}
	case <-c.owner.client.conn.Done():
		select {
		case <-c.done:
			return c.settledResult()
		default:
		}
		return PromptResponse{}, &draftInsertionOutcomeError{err: errors.Join(fmt.Errorf("connection lost before Activity settlement: %w", acpsdk.ErrConnectionClosed), c.owner.failureCause())}
	}

}

// The receipt binds this input to its real owner Activity. Notifications that
// happen before admission may describe unrelated callers' work; their order
// cannot supply that identity. Earlier settlement of this exact Activity is
// retained so that idle-before-receipt remains a valid completion.
func (c *draftPromptCall) bindReceiptActivity(activity string) error {
	if activity == "" {
		return errors.New("acp draft: inserted prompt receipt has no Activity identity")
	}
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	session := c.owner.sessionLocked(c.sessionID)
	if settled, ok := session.settledActivities[activity]; ok {
		if settled.sequence <= c.activityFloor {
			return errors.New("acp draft: inserted prompt receipt reused an earlier Activity")
		}
		c.activityID = activity
		c.finishLocked(settled.result())
		return nil
	}
	if session.activityID == activity && session.activeSequence <= c.activityFloor {
		return errors.New("acp draft: inserted prompt receipt reused an earlier Activity")
	}
	c.activityID = activity
	return nil
}

func (c *draftPromptCall) settledResult() (PromptResponse, error) {
	c.owner.mu.Lock()
	defer c.owner.mu.Unlock()
	if session := c.owner.sessionLocked(c.sessionID); session.prompt == c {
		session.prompt = nil
	}
	var settled *draftActivityError
	if c.err != nil && !errors.As(c.err, &settled) {
		return c.result, &draftInsertionOutcomeError{err: c.err}
	}
	return c.result, c.err
}

// A state_update idle(error) conclusively ends foreground work, while a lost
// connection leaves that outcome unknown to the owner.
type draftActivityError struct{}

func (*draftActivityError) Error() string { return "acp draft: foreground Activity failed" }

// draftInsertionOutcomeError records uncertainty after dispatch or insertion
// acknowledgement. It cannot be mistaken for a proven rejection by input
// owners that classify transport admission independently of Turn completion.
type draftInsertionOutcomeError struct{ err error }

func (e *draftInsertionOutcomeError) Error() string {
	return "acp draft: input insertion outcome is unknown: " + e.err.Error()
}

func (e *draftInsertionOutcomeError) Unwrap() error { return e.err }

func (c *draftPromptCall) abandon() {
	c.inner.Abandon()
	c.owner.mu.Lock()
	session := c.owner.sessionLocked(c.sessionID)
	if session.prompt == c {
		session.prompt = nil
	}
	c.owner.mu.Unlock()
}

func (d *draftClient) waitUserEcho(ctx context.Context, sessionID, messageID string, floor uint64) error {
	for {
		d.mu.Lock()
		session := d.sessionLocked(sessionID)
		seen := session.userMessages[messageID] > floor
		if seen {
			delete(session.userMessages, messageID)
		}
		changed := session.changed
		d.mu.Unlock()
		if seen {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-d.client.conn.Done():
			return errors.Join(fmt.Errorf("acp draft: accepted prompt echo unavailable after connection loss: %w", acpsdk.ErrConnectionClosed), d.failureCause())
		}
	}
}

func (d *draftClient) steer(ctx context.Context, sessionID string, prompt []json.RawMessage, abort func()) (SessionSteeringResponse, error) {
	blocks, err := draftPromptBlocks(prompt)
	if err != nil {
		return SessionSteeringResponse{}, err
	}
	d.mu.Lock()
	session, err := d.ensureSessionLocked(sessionID)
	if err != nil {
		d.mu.Unlock()
		return SessionSteeringResponse{}, err
	}
	activity, active, floor := session.activityID, session.active, session.messageSequence
	if active && activity != "" {
		session.receiptWaiters++
	}
	d.mu.Unlock()
	if !active || activity == "" {
		return SessionSteeringResponse{Outcome: SessionSteeringPromptRequired, Reason: "noRunningTurn"}, nil
	}
	defer func() {
		d.mu.Lock()
		d.sessionLocked(sessionID).receiptWaiters--
		d.mu.Unlock()
	}()
	call, err := acpsdk.PrepareRequest[v2.PromptResponse](d.client.conn, v2.AgentMethodSessionPrompt, v2.PromptRequest{SessionId: v2.SessionId(sessionID), Prompt: blocks, Meta: acpdraft.ActiveOnlyMeta(activity)})
	if err != nil {
		return SessionSteeringResponse{}, err
	}
	defer call.Abandon()
	if err := call.Dispatch(ctx, acpsdk.DispatchOptions{Abort: adaptAbort(abort)}); err != nil {
		return SessionSteeringResponse{}, err
	}
	response, err := call.Wait(ctx)
	if acpdraft.IsInactivePromptError(err) {
		return SessionSteeringResponse{Outcome: SessionSteeringPromptRequired, Reason: "noRunningTurn"}, nil
	}
	if err != nil {
		if PromptOutcomeUnknown(err) {
			return SessionSteeringResponse{}, &draftInsertionOutcomeError{err: err}
		}
		return SessionSteeringResponse{}, err
	}
	if strings.TrimSpace(string(response.MessageId)) == "" {
		return SessionSteeringResponse{}, &draftInsertionOutcomeError{err: errors.New("inserted steering has no message identity")}
	}
	if acpdraft.ActivityID(response.Meta) != activity {
		err := errors.New("acp draft: injected prompt receipt has a different Activity identity")
		d.fail(err)
		return SessionSteeringResponse{}, &draftInsertionOutcomeError{err: err}
	}
	if err := d.waitUserEcho(ctx, sessionID, string(response.MessageId), floor); err != nil {
		return SessionSteeringResponse{}, &draftInsertionOutcomeError{err: err}
	}
	return SessionSteeringResponse{Outcome: SessionSteeringInjected}, nil
}
