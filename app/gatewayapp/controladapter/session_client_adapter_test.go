package controladapter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestSessionClientAdapterRoutesMainTurnWritesAndObservationThroughTypedClient(t *testing.T) {
	target := controlclient.TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	subscription := newSessionClientAdapterTestSubscription()
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: subscription,
	}
	legacy := &Adapter{
		stack: &RuntimeStack{Session: SessionRuntimeDeps{
			AppName: "caelis",
			UserID:  "owner",
			Workspace: session.WorkspaceRef{
				Key: "workspace", CWD: t.TempDir(),
			},
		}},
		session: session.Session{
			SessionRef: session.SessionRef{
				AppName: "caelis", UserID: "owner",
				SessionID: "session-1", WorkspaceKey: "workspace",
			},
		},
		hasSession: true,
		bindingKey: "cli-tui",
	}
	adapter, err := NewSessionClientAdapter(legacy, client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if turn == nil || turn.HandleID() != target.HandleID {
		t.Fatalf("Turn = %#v", turn)
	}
	if _, err := adapter.Submit(context.Background(), controlprompt.Submission{
		Text: "continue",
		Mode: controlprompt.SubmissionModeActiveTurn,
	}); err != nil {
		t.Fatal(err)
	}
	if err := turn.SubmitApproval(context.Background(), controlprompt.ApprovalDecision{
		RequestID: "approval-1",
		Approved:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}

	message := eventstream.Envelope{
		Kind: eventstream.KindNotice, Cursor: "cursor-1",
		SessionID: "session-1", HandleID: target.HandleID,
		RunID: target.RunID, TurnID: target.TurnID, Notice: "typed",
	}
	terminal := eventstream.TurnCancelled(
		target.HandleID,
		target.RunID,
		target.TurnID,
		"cancelled",
		time.Now(),
	)
	terminal.SessionID = "session-1"
	terminal.Cursor = "cursor-2"
	subscription.events <- message
	subscription.events <- terminal
	close(subscription.events)

	got := collectSessionClientAdapterEvents(turn.Events())
	if len(got) != 2 || got[0].Notice != "typed" || !eventstream.IsTurnTerminalLifecycle(got[1]) {
		t.Fatalf("Turn events = %#v", got)
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
	if client.prompt.Input != "hello" ||
		client.steer.Input != "continue" ||
		client.steer.Target != target ||
		client.approval.Target != target ||
		client.approval.ApprovalRequestID != "approval-1" ||
		client.cancel.Target != target {
		t.Fatalf(
			"typed requests prompt=%#v steer=%#v approval=%#v cancel=%#v",
			client.prompt,
			client.steer,
			client.approval,
			client.cancel,
		)
	}
	if _, err := adapter.Submit(context.Background(), controlprompt.Submission{
		Text: "too late",
		Mode: controlprompt.SubmissionModeActiveTurn,
	}); err == nil {
		t.Fatal("active steer remained available after Turn close")
	}
}

func TestSessionClientAdapterRoutesReviewThroughTypedParticipantClient(t *testing.T) {
	target := controlclient.TurnTarget{HandleID: "participant-handle", RunID: "participant-run", TurnID: "participant-turn"}
	subscription := newSessionClientAdapterTestSubscription()
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: subscription,
		state: controlclient.SessionState{
			SessionID: "session-1",
			Revision:  7,
			CWD:       t.TempDir(),
			Controller: session.ControllerBinding{
				EpochID: "epoch-1",
			},
		},
	}
	participants := &sessionClientAdapterTestParticipantClient{target: target}
	legacy := &Adapter{
		stack: &RuntimeStack{Session: SessionRuntimeDeps{
			AppName: "caelis",
			UserID:  "owner",
			Workspace: session.WorkspaceRef{
				Key: "workspace", CWD: t.TempDir(),
			},
		}},
		session: session.Session{SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "owner", SessionID: "session-1", WorkspaceKey: "workspace",
		}},
		hasSession: true,
		bindingKey: "acp",
	}
	adapter, err := NewSessionClientAdapterWithParticipants(legacy, client, participants)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := adapter.StartReview(context.Background(), "inspect typed routing", nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn == nil || turn.HandleID() != target.HandleID {
		t.Fatalf("review Turn = %#v, want typed participant target", turn)
	}
	request := participants.start
	if request.SessionID != "session-1" || request.Handle != "reviewer" ||
		request.Source != "slash_review" || request.DisplayAddress != "/review" ||
		!request.Transient || request.DetachSource != "side_agent_complete" ||
		!strings.HasPrefix(request.Label, "@") ||
		!strings.Contains(request.Input, "inspect typed routing") {
		t.Fatalf("typed review request = %#v", request)
	}
	terminal := eventstream.TurnCompleted(target.HandleID, target.RunID, target.TurnID, time.Now())
	terminal.SessionID = "session-1"
	subscription.events <- terminal
	close(subscription.events)
	if got := collectSessionClientAdapterEvents(turn.Events()); len(got) != 1 || !eventstream.IsTurnTerminalLifecycle(got[0]) {
		t.Fatalf("review Turn events = %#v", got)
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
}

func collectSessionClientAdapterEvents(events <-chan eventstream.Envelope) []eventstream.Envelope {
	var out []eventstream.Envelope
	for envelope := range events {
		out = append(out, envelope)
	}
	return out
}

type sessionClientAdapterTestClient struct {
	target       controlclient.TurnTarget
	subscription *sessionClientAdapterTestSubscription
	state        controlclient.SessionState

	mu       sync.Mutex
	prompt   controlclient.PromptRequest
	steer    controlclient.SteerRequest
	approval controlclient.ResolveApprovalRequest
	cancel   controlclient.CancelRequest
}

func (*sessionClientAdapterTestClient) Initialize(context.Context) (controlclient.ServerInfo, error) {
	return controlclient.ServerInfo{}, nil
}

func (*sessionClientAdapterTestClient) ListSessions(context.Context, controlclient.ListSessionsRequest) (session.SessionList, error) {
	return session.SessionList{}, nil
}

func (*sessionClientAdapterTestClient) CreateSession(context.Context, controlclient.CreateSessionRequest) (controlclient.CommandResult, error) {
	return controlclient.CommandResult{}, errors.New("unexpected CreateSession")
}

func (*sessionClientAdapterTestClient) CloseSession(context.Context, controlclient.CloseSessionRequest) (controlclient.CommandResult, error) {
	return controlclient.CommandResult{}, errors.New("unexpected CloseSession")
}

func (c *sessionClientAdapterTestClient) InspectSession(context.Context, controlclient.StateRequest) (controlclient.SessionState, error) {
	state := c.state
	if state.SessionID == "" {
		state.SessionID = "session-1"
	}
	state.BoundaryCursor = "boundary-0"
	return state, nil
}

func (c *sessionClientAdapterTestClient) Reconnect(context.Context, controlclient.ReconnectRequest) (controlclient.ReconnectResult, error) {
	state := c.state
	if state.SessionID == "" {
		state.SessionID = "session-1"
	}
	if state.Revision == 0 {
		state.Revision = 4
	}
	if state.Controller.EpochID == "" {
		state.Controller.EpochID = "epoch-1"
	}
	return controlclient.ReconnectResult{
		State:        state,
		Subscription: c.subscription,
	}, nil
}

func (c *sessionClientAdapterTestClient) Prompt(_ context.Context, request controlclient.PromptRequest) (controlclient.CommandResult, error) {
	c.mu.Lock()
	c.prompt = request
	c.mu.Unlock()
	return controlclient.CommandResult{
		OperationID: request.OperationID,
		Outcome:     controlclient.OutcomeCommitted,
		SessionID:   request.SessionID,
		Target:      c.target,
	}, nil
}

func (c *sessionClientAdapterTestClient) Steer(_ context.Context, request controlclient.SteerRequest) (controlclient.CommandResult, error) {
	c.mu.Lock()
	c.steer = request
	c.mu.Unlock()
	return controlclient.CommandResult{Outcome: controlclient.OutcomeCommitted}, nil
}

func (c *sessionClientAdapterTestClient) Cancel(_ context.Context, request controlclient.CancelRequest) (controlclient.CommandResult, error) {
	c.mu.Lock()
	c.cancel = request
	c.mu.Unlock()
	return controlclient.CommandResult{Outcome: controlclient.OutcomeCommitted}, nil
}

func (c *sessionClientAdapterTestClient) ResolveApproval(_ context.Context, request controlclient.ResolveApprovalRequest) (controlclient.CommandResult, error) {
	c.mu.Lock()
	c.approval = request
	c.mu.Unlock()
	return controlclient.CommandResult{Outcome: controlclient.OutcomeCommitted}, nil
}

type sessionClientAdapterTestParticipantClient struct {
	target controlclient.TurnTarget
	start  controlclient.StartParticipantRequest
}

func (*sessionClientAdapterTestParticipantClient) Handles(context.Context, string) ([]string, error) {
	return []string{"reviewer"}, nil
}

func (c *sessionClientAdapterTestParticipantClient) StartParticipant(_ context.Context, request controlclient.StartParticipantRequest) (controlclient.CommandResult, error) {
	c.start = request
	return controlclient.CommandResult{
		OperationID:   request.OperationID,
		Outcome:       controlclient.OutcomeCommitted,
		SessionID:     request.SessionID,
		Target:        c.target,
		ParticipantID: "participant-1",
	}, nil
}

func (*sessionClientAdapterTestParticipantClient) PromptParticipant(context.Context, controlclient.PromptParticipantRequest) (controlclient.CommandResult, error) {
	return controlclient.CommandResult{}, errors.New("unexpected PromptParticipant")
}

func (*sessionClientAdapterTestParticipantClient) CancelParticipant(context.Context, controlclient.CancelParticipantRequest) (controlclient.CommandResult, error) {
	return controlclient.CommandResult{Outcome: controlclient.OutcomeCommitted}, nil
}

type sessionClientAdapterTestSubscription struct {
	backfill     chan eventstream.Envelope
	backfillDone chan struct{}
	events       chan eventstream.Envelope
	closeOnce    sync.Once
}

func newSessionClientAdapterTestSubscription() *sessionClientAdapterTestSubscription {
	backfill := make(chan eventstream.Envelope)
	close(backfill)
	backfillDone := make(chan struct{})
	close(backfillDone)
	return &sessionClientAdapterTestSubscription{
		backfill: backfill, backfillDone: backfillDone,
		events: make(chan eventstream.Envelope, 4),
	}
}

func (s *sessionClientAdapterTestSubscription) Backfill() <-chan eventstream.Envelope {
	return s.backfill
}

func (s *sessionClientAdapterTestSubscription) Events() <-chan eventstream.Envelope {
	return s.events
}

func (s *sessionClientAdapterTestSubscription) BackfillDone() <-chan struct{} {
	return s.backfillDone
}

func (s *sessionClientAdapterTestSubscription) Close() error {
	s.closeOnce.Do(func() {})
	return nil
}

func (*sessionClientAdapterTestSubscription) Err() error         { return nil }
func (*sessionClientAdapterTestSubscription) LastCursor() string { return "" }

var _ controlclient.SessionClient = (*sessionClientAdapterTestClient)(nil)
var _ controlclient.ParticipantClient = (*sessionClientAdapterTestParticipantClient)(nil)
var _ controlclient.FeedSubscription = (*sessionClientAdapterTestSubscription)(nil)
