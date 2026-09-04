package appserver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestSessionTurnClientAttachesBeforePromptAndFiltersExactTarget(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-current", RunID: "run-current", TurnID: "turn-current"}
	between := eventstream.TurnCompleted("handle-old", "run-old", "turn-old", time.Now())
	between.SessionID = "session-1"
	between = sessionTurnTestExactEnvelope(between, "cursor-between", 1)
	current := sessionTurnTestMessage("session-1", target, "cursor-current", "done")
	foreignTerminal := eventstream.TurnCompleted("handle-other", "run-other", "turn-other", time.Now())
	foreignTerminal.SessionID = "session-1"
	foreignTerminal = sessionTurnTestExactEnvelope(foreignTerminal, "cursor-foreign", 2)
	terminal := eventstream.TurnCompleted(target.HandleID, target.RunID, target.TurnID, time.Now())
	terminal.SessionID = "session-1"
	terminal = sessionTurnTestExactEnvelope(terminal, "cursor-terminal", 3)

	subscription := newSessionTurnTestSubscription(
		[]eventstream.Envelope{between},
		[]eventstream.Envelope{current, foreignTerminal, terminal},
		nil,
	)
	client := &sessionTurnTestClient{
		inspectFn: func(_ context.Context, request StateRequest) (SessionState, error) {
			if request.SessionID != "session-1" {
				t.Fatalf("Inspect request = %#v", request)
			}
			return SessionState{
				SessionID:      "session-1",
				BoundaryCursor: "cursor-boundary",
			}, nil
		},
		reconnectFn: func(_ context.Context, request ReconnectRequest) (ReconnectResult, error) {
			if request.SessionID != "session-1" || request.Cursor != "cursor-boundary" {
				t.Fatalf("Reconnect request = %#v", request)
			}
			return ReconnectResult{
				State: SessionState{
					SessionID: "session-1",
					Revision:  7,
					Controller: session.ControllerBinding{
						EpochID: "epoch-1",
					},
				},
				Subscription: subscription,
			}, nil
		},
		promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
			if request.SessionID != "session-1" ||
				request.ExpectedRevision == nil ||
				*request.ExpectedRevision != 7 ||
				request.ExpectedControllerEpoch != "epoch-1" ||
				request.Input != "hello" ||
				!strings.HasPrefix(request.OperationID, "session-turn-prompt-") {
				t.Fatalf("Prompt request = %#v", request)
			}
			return CommandResult{
				OperationID: request.OperationID,
				Outcome:     OutcomeCommitted,
				SessionID:   request.SessionID,
				Revision:    8,
				Target:      target,
			}, nil
		},
	}
	starter, err := NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(context.Background(), SessionTurnStartRequest{
		SessionID: "session-1",
		Input:     " hello ",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()

	got := collectSessionTurnTestEvents(turn.Events())
	if len(got) != 2 ||
		got[0].Cursor != "cursor-current" ||
		!eventstream.IsTurnTerminalLifecycle(got[1]) ||
		got[1].Cursor != "cursor-terminal" {
		t.Fatalf("target events = %#v", got)
	}
	if err := turn.Err(); err != nil {
		t.Fatalf("Turn Err() = %v", err)
	}
}

func TestSessionTurnClientRejectsReplacementAfterTargetOutput(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	subscription := &sessionTurnTestSubscription{deliveries: make(chan FeedDelivery), stop: make(chan struct{})}
	go func() {
		defer close(subscription.deliveries)
		message := sessionTurnTestMessage("session-1", target, "cursor-1", "prefix")
		replacement := eventstream.CloneEnvelope(message)
		replacement.Cursor = ""
		replacement.Position = nil
		if !subscription.send(FeedDelivery{Kind: FeedDeliverySync, Source: FeedSourceExact}) ||
			!subscription.send(FeedDelivery{Kind: FeedDeliveryAppendPage, Source: FeedSourceExact, Events: []eventstream.Envelope{message}, NextCursor: message.Cursor}) ||
			!subscription.send(FeedDelivery{Kind: FeedDeliveryReplaceBegin, Source: FeedSourceReplacement, SnapshotID: "replacement"}) ||
			!subscription.send(FeedDelivery{Kind: FeedDeliveryReplacePage, Source: FeedSourceReplacement, SnapshotID: "replacement", Events: []eventstream.Envelope{replacement}}) {
			return
		}
		_ = subscription.send(FeedDelivery{Kind: FeedDeliveryReplaceEnd, Source: FeedSourceReplacement, SnapshotID: "replacement", Page: 1})
	}()
	client := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{SessionID: "session-1", BoundaryCursor: "cursor-boundary"}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{State: SessionState{SessionID: "session-1", Revision: 1}, Subscription: subscription}, nil
		},
		promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
			return CommandResult{OperationID: request.OperationID, Outcome: OutcomeCommitted, SessionID: request.SessionID, Target: target}, nil
		},
	}
	starter, err := NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(context.Background(), SessionTurnStartRequest{SessionID: "session-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	got := collectSessionTurnTestEvents(turn.Events())
	if len(got) != 1 || got[0].Cursor != "cursor-1" {
		t.Fatalf("events = %#v, want exact prefix only", got)
	}
	if !errorcode.Is(turn.Err(), errorcode.Conflict) {
		t.Fatalf("Turn Err() = %v, want conflict", turn.Err())
	}
}

func TestSessionTurnClientAcceptsImageOnlyPrompt(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	contentParts := []model.ContentPart{{
		Type: model.ContentPartImage, MimeType: "image/png", Data: "aW1n", FileName: "shot.png",
	}}
	subscription := newOpenSessionTurnTestSubscription()
	client := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{SessionID: "session-1", BoundaryCursor: "cursor-boundary"}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{
				State:        SessionState{SessionID: "session-1", Revision: 1},
				Subscription: subscription,
			}, nil
		},
		promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
			if request.Input != "" || len(request.ContentParts) != 1 || request.ContentParts[0] != contentParts[0] {
				t.Fatalf("Prompt request = %#v", request)
			}
			return CommandResult{
				OperationID: request.OperationID,
				Outcome:     OutcomeCommitted,
				SessionID:   request.SessionID,
				Target:      target,
			}, nil
		},
	}
	starter, err := NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(context.Background(), SessionTurnStartRequest{
		SessionID:    "session-1",
		ContentParts: contentParts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTurnClientKeepsTurnWhenUnknownOutcomeHasTarget(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	subscription := newOpenSessionTurnTestSubscription()
	client := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{SessionID: "session-1", BoundaryCursor: "cursor-boundary"}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{
				State:        SessionState{SessionID: "session-1", Revision: 1},
				Subscription: subscription,
			}, nil
		},
		promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
			return CommandResult{
				OperationID: request.OperationID,
				Outcome:     OutcomeUnknown,
				SessionID:   request.SessionID,
				Target:      target,
			}, NewOutcomeError(OutcomeUnknown, errors.New("effect outcome cannot be proven"))
		},
	}
	starter, err := NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(context.Background(), SessionTurnStartRequest{SessionID: "session-1", Input: "hello"})
	if err != nil {
		t.Fatalf("Start() = %v, want turn when unknown outcome still has a target", err)
	}
	if turn.Target() != target {
		t.Fatalf("Turn target = %#v", turn.Target())
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionTurnClientCancelsAfterPromptReceiptWriteFails(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	operations := &failFirstCompleteStore{OperationStore: NewMemoryOperationStore(), failLeft: 1}
	backend := &promptTargetBackend{target: target}
	service := newTestCommandService(t, allowAuthorizer{}, operations, backend)
	subscription := newOpenSessionTurnTestSubscription()
	client := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{
				SessionID:      "session-1",
				BoundaryCursor: "cursor-boundary",
				Controller:     session.ControllerBinding{EpochID: "epoch-1"},
			}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{
				State: SessionState{
					SessionID:  "session-1",
					Revision:   7,
					Controller: session.ControllerBinding{EpochID: "epoch-1"},
				},
				Subscription: subscription,
			}, nil
		},
		promptFn: func(ctx context.Context, request PromptRequest) (CommandResult, error) {
			return service.Prompt(ctx, Principal{ID: "owner"}, request)
		},
		cancelFn: func(ctx context.Context, request CancelRequest) (CommandResult, error) {
			return service.Cancel(ctx, Principal{ID: "owner"}, request)
		},
	}
	starter, err := NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(context.Background(), SessionTurnStartRequest{SessionID: "session-1", Input: "hello"})
	if err != nil {
		t.Fatalf("Start() = %v, want observed turn after receipt-write failure", err)
	}
	defer turn.Close()
	if turn.Target() != target {
		t.Fatalf("Turn target = %#v", turn.Target())
	}
	if err := turn.Cancel(context.Background(), "tui interrupt"); err != nil {
		t.Fatalf("Cancel() = %v", err)
	}
	if backend.cancel.Target != target {
		t.Fatalf("Cancel request = %#v, want target %#v", backend.cancel, target)
	}
}

func TestSessionTurnClientReusesCancelOperationID(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	var cancelIDs []string
	subscription := newOpenSessionTurnTestSubscription()
	client := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{SessionID: "session-1", BoundaryCursor: "cursor-boundary"}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{
				State:        SessionState{SessionID: "session-1", Revision: 1, Controller: session.ControllerBinding{EpochID: "epoch-1"}},
				Subscription: subscription,
			}, nil
		},
		promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
			return CommandResult{OperationID: request.OperationID, Outcome: OutcomeCommitted, SessionID: request.SessionID, Target: target}, nil
		},
		cancelFn: func(_ context.Context, request CancelRequest) (CommandResult, error) {
			cancelIDs = append(cancelIDs, request.OperationID)
			if len(cancelIDs) == 1 {
				return CommandResult{}, context.DeadlineExceeded
			}
			return CommandResult{Outcome: OutcomeCommitted}, nil
		},
	}
	starter, err := NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(context.Background(), SessionTurnStartRequest{SessionID: "session-1", Input: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	if err := turn.Cancel(context.Background(), "tui interrupt"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Cancel() = %v, want deadline", err)
	}
	if err := turn.Cancel(context.Background(), "tui interrupt"); err != nil {
		t.Fatalf("retry Cancel() = %v", err)
	}
	if len(cancelIDs) != 2 || cancelIDs[0] == "" || cancelIDs[0] != cancelIDs[1] {
		t.Fatalf("Cancel operation IDs = %#v, want one stable ID reused after failure", cancelIDs)
	}
}

func TestSessionTurnClientRotatesCancelOperationIDAfterProvenNoEffect(t *testing.T) {
	tests := []struct {
		name     string
		firstErr error
	}{
		{name: "in-process replay", firstErr: nil},
		{name: "HTTP-style outcome error", firstErr: NewOutcomeError(OutcomeRejected, errors.New("cancel rejected"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
			var cancelIDs []string
			client := &sessionTurnTestClient{
				inspectFn: func(context.Context, StateRequest) (SessionState, error) {
					return SessionState{SessionID: "session-1", BoundaryCursor: "cursor-boundary"}, nil
				},
				reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
					return ReconnectResult{
						State:        SessionState{SessionID: "session-1", Revision: 1},
						Subscription: newOpenSessionTurnTestSubscription(),
					}, nil
				},
				promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
					return CommandResult{OperationID: request.OperationID, Outcome: OutcomeCommitted, SessionID: request.SessionID, Target: target}, nil
				},
				cancelFn: func(_ context.Context, request CancelRequest) (CommandResult, error) {
					cancelIDs = append(cancelIDs, request.OperationID)
					if len(cancelIDs) == 1 {
						return CommandResult{OperationID: request.OperationID, Outcome: OutcomeRejected, Detail: "cancel rejected"}, test.firstErr
					}
					return CommandResult{OperationID: request.OperationID, Outcome: OutcomeCommitted}, nil
				},
			}
			starter, err := NewSessionTurnClient(client)
			if err != nil {
				t.Fatal(err)
			}
			turn, err := starter.Start(context.Background(), SessionTurnStartRequest{SessionID: "session-1", Input: "hello"})
			if err != nil {
				t.Fatal(err)
			}
			defer turn.Close()
			var receipt *CommandReceiptError
			if err := turn.Cancel(context.Background(), "tui interrupt"); !errors.As(err, &receipt) || receipt.Receipt.Outcome != OutcomeRejected {
				t.Fatalf("first Cancel() = %v, want rejected receipt", err)
			}
			if err := turn.Cancel(context.Background(), "tui interrupt"); err != nil {
				t.Fatalf("retry Cancel() = %v", err)
			}
			if len(cancelIDs) != 2 || cancelIDs[0] == "" || cancelIDs[0] == cancelIDs[1] {
				t.Fatalf("Cancel operation IDs = %#v, want a fresh ID after proven rejection", cancelIDs)
			}
		})
	}
}

func TestParticipantTurnClientRotatesCancelOperationIDAfterConflict(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "participant-handle", RunID: "participant-run", TurnID: "participant-turn"}
	sessions := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{SessionID: "session-1", BoundaryCursor: "cursor-boundary"}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{
				State:        SessionState{SessionID: "session-1", Revision: 1},
				Subscription: newOpenSessionTurnTestSubscription(),
			}, nil
		},
	}
	var cancelIDs []string
	participants := &participantTurnTestClient{
		startFn: func(_ context.Context, request StartParticipantRequest) (CommandResult, error) {
			return CommandResult{
				OperationID: request.OperationID, Outcome: OutcomeCommitted, SessionID: request.SessionID,
				ParticipantID: "participant-1", Target: target,
			}, nil
		},
		cancelFn: func(_ context.Context, request CancelParticipantRequest) (CommandResult, error) {
			cancelIDs = append(cancelIDs, request.OperationID)
			if len(cancelIDs) == 1 {
				return CommandResult{OperationID: request.OperationID, Outcome: OutcomeConflicted, Detail: "cancel conflicted"}, nil
			}
			return CommandResult{OperationID: request.OperationID, Outcome: OutcomeCommitted}, nil
		},
	}
	client, err := NewParticipantTurnClient(sessions, participants)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := client.Start(context.Background(), ParticipantTurnStartRequest{
		SessionID: "session-1", Handle: "reviewer", Input: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	var receipt *CommandReceiptError
	if err := turn.Cancel(context.Background(), "tui interrupt"); !errors.As(err, &receipt) || receipt.Receipt.Outcome != OutcomeConflicted {
		t.Fatalf("first Cancel() = %v, want conflicted receipt", err)
	}
	if err := turn.Cancel(context.Background(), "tui interrupt"); err != nil {
		t.Fatalf("retry Cancel() = %v", err)
	}
	if len(cancelIDs) != 2 || cancelIDs[0] == "" || cancelIDs[0] == cancelIDs[1] {
		t.Fatalf("CancelParticipant operation IDs = %#v, want a fresh ID after conflict", cancelIDs)
	}
}

func TestParticipantTurnClientSteersExactObservedTarget(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "participant-handle", RunID: "participant-run", TurnID: "participant-turn"}
	var steerRequest SteerRequest
	sessions := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{SessionID: "session-1", BoundaryCursor: "cursor-boundary"}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{
				State: SessionState{
					SessionID:  "session-1",
					Revision:   7,
					Controller: session.ControllerBinding{EpochID: "epoch-7"},
				},
				Subscription: newOpenSessionTurnTestSubscription(),
			}, nil
		},
		steerFn: func(_ context.Context, request SteerRequest) (CommandResult, error) {
			steerRequest = request
			return CommandResult{OperationID: request.OperationID, Outcome: OutcomeCommitted}, nil
		},
	}
	participants := &participantTurnTestClient{
		startFn: func(_ context.Context, request StartParticipantRequest) (CommandResult, error) {
			return CommandResult{
				OperationID:   request.OperationID,
				Outcome:       OutcomeCommitted,
				SessionID:     request.SessionID,
				ParticipantID: "participant-1",
				Target:        target,
			}, nil
		},
		cancelFn: func(_ context.Context, request CancelParticipantRequest) (CommandResult, error) {
			return CommandResult{OperationID: request.OperationID, Outcome: OutcomeCommitted}, nil
		},
	}
	client, err := NewParticipantTurnClient(sessions, participants)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := client.Start(context.Background(), ParticipantTurnStartRequest{
		SessionID: "session-1", Handle: "reviewer", Input: "review",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer observed.Close()
	turn, ok := observed.(SessionTurn)
	if !ok {
		t.Fatalf("participant Turn = %T, want SessionTurn steering capability", observed)
	}
	parts := []model.ContentPart{{Type: model.ContentPartText, Text: "part"}}
	if err := turn.Steer(context.Background(), "guide", "display guide", parts); err != nil {
		t.Fatalf("Steer() error = %v", err)
	}
	if steerRequest.OperationID == "" || steerRequest.SessionID != "session-1" || steerRequest.ExpectedControllerEpoch != "epoch-7" {
		t.Fatalf("Steer() write fence = %#v, want session-1/epoch-7 with operation ID", steerRequest.WriteBase)
	}
	if steerRequest.Target != target || steerRequest.Input != "guide" || steerRequest.DisplayInput != "display guide" {
		t.Fatalf("Steer() request = %#v, want exact participant target and input", steerRequest)
	}
	if len(steerRequest.ContentParts) != 1 || steerRequest.ContentParts[0].Text != "part" {
		t.Fatalf("Steer() content parts = %#v, want preserved part", steerRequest.ContentParts)
	}

	sessions.steerFn = func(_ context.Context, request SteerRequest) (CommandResult, error) {
		return CommandResult{
			OperationID: request.OperationID,
			Outcome:     OutcomeRejected,
			Detail:      "steer rejected",
		}, nil
	}
	var receipt *CommandReceiptError
	if err := turn.Steer(context.Background(), "retry", "", nil); !errors.As(err, &receipt) || receipt.Receipt.Outcome != OutcomeRejected {
		t.Fatalf("Steer(replayed rejection) = %v, want rejected receipt", err)
	}
}

func TestSessionTurnClientReportsUnknownAdmissionWithoutTarget(t *testing.T) {
	t.Parallel()

	subscription := newOpenSessionTurnTestSubscription()
	client := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{SessionID: "session-1", BoundaryCursor: "cursor-boundary"}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{
				State:        SessionState{SessionID: "session-1", Revision: 1},
				Subscription: subscription,
			}, nil
		},
		promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
			return CommandResult{
				OperationID: request.OperationID,
				Outcome:     OutcomeUnknown,
				SessionID:   request.SessionID,
			}, NewOutcomeError(OutcomeUnknown, errors.New("effect outcome cannot be proven"))
		},
	}
	starter, err := NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(context.Background(), SessionTurnStartRequest{SessionID: "session-1", Input: "hello"})
	if turn != nil {
		t.Fatal("Start() returned a Turn without a proven target")
	}
	var receipt *CommandReceiptError
	if !errors.As(err, &receipt) || receipt.Receipt.Outcome != OutcomeUnknown {
		t.Fatalf("Start() = %v, want unknown admission receipt", err)
	}
}

func TestSessionTurnClientRoutesApprovalAndCancelWithoutClosingSession(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	subscription := newOpenSessionTurnTestSubscription()
	var approvalRequest ResolveApprovalRequest
	var cancelRequest CancelRequest
	var steerRequest SteerRequest
	client := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{
				SessionID:      "session-1",
				BoundaryCursor: "cursor-boundary",
			}, nil
		},
		reconnectFn: func(context.Context, ReconnectRequest) (ReconnectResult, error) {
			return ReconnectResult{
				State: SessionState{
					SessionID: "session-1",
					Revision:  11,
					Controller: session.ControllerBinding{
						EpochID: "epoch-1",
					},
				},
				Subscription: subscription,
			}, nil
		},
		promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
			return CommandResult{
				OperationID: request.OperationID,
				Outcome:     OutcomeCommitted,
				SessionID:   request.SessionID,
				Target:      target,
			}, nil
		},
		resolveApprovalFn: func(_ context.Context, request ResolveApprovalRequest) (CommandResult, error) {
			approvalRequest = request
			return CommandResult{Outcome: OutcomeCommitted}, nil
		},
		cancelFn: func(_ context.Context, request CancelRequest) (CommandResult, error) {
			cancelRequest = request
			return CommandResult{Outcome: OutcomeCommitted}, nil
		},
		steerFn: func(_ context.Context, request SteerRequest) (CommandResult, error) {
			steerRequest = request
			return CommandResult{Outcome: OutcomeCommitted}, nil
		},
	}
	starter, err := NewSessionTurnClient(client)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := starter.Start(context.Background(), SessionTurnStartRequest{
		SessionID: "session-1",
		Input:     "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := turn.ResolveApproval(context.Background(), ApprovalResolution{
		RequestID: "approval-1",
		Outcome:   "selected",
		OptionID:  "allow_once",
		Approved:  true,
	}); err != nil {
		t.Fatal(err)
	}
	contentParts := []model.ContentPart{{Type: model.ContentPartText, Text: "continue"}}
	if err := turn.Steer(context.Background(), " continue ", " continue shown ", contentParts); err != nil {
		t.Fatal(err)
	}
	if err := turn.Cancel(context.Background(), "test cancellation"); err != nil {
		t.Fatal(err)
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
	if approvalRequest.SessionID != "session-1" ||
		approvalRequest.ExpectedControllerEpoch != "epoch-1" ||
		approvalRequest.Target != target ||
		approvalRequest.ApprovalRequestID != "approval-1" ||
		approvalRequest.OptionID != "allow_once" ||
		!approvalRequest.Approved {
		t.Fatalf("ResolveApproval request = %#v", approvalRequest)
	}
	if cancelRequest.SessionID != "session-1" ||
		cancelRequest.ExpectedControllerEpoch != "epoch-1" ||
		cancelRequest.Target != target ||
		cancelRequest.Reason != "test cancellation" {
		t.Fatalf("Cancel request = %#v", cancelRequest)
	}
	if steerRequest.SessionID != "session-1" ||
		steerRequest.ExpectedControllerEpoch != "epoch-1" ||
		steerRequest.Target != target ||
		steerRequest.Input != "continue" ||
		steerRequest.DisplayInput != "continue shown" ||
		len(steerRequest.ContentParts) != 1 ||
		steerRequest.ContentParts[0] != contentParts[0] {
		t.Fatalf("Steer request = %#v", steerRequest)
	}
	if client.closeSessionCalls != 0 {
		t.Fatalf("CloseSession calls = %d, want zero", client.closeSessionCalls)
	}
}

func sessionTurnTestMessage(
	sessionID string,
	target TurnTarget,
	cursor string,
	text string,
) eventstream.Envelope {
	return sessionTurnTestExactEnvelope(eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: sessionID,
		HandleID:  target.HandleID,
		RunID:     target.RunID,
		TurnID:    target.TurnID,
		Scope:     eventstream.ScopeMain,
		Update: eventstream.ContentChunk{
			SessionUpdate: eventstream.UpdateAgentMessage,
			Content:       eventstream.TextContent{Type: "text", Text: text},
		},
	}, cursor, 1)
}

func sessionTurnTestExactEnvelope(envelope eventstream.Envelope, cursor string, sequence uint64) eventstream.Envelope {
	envelope.Cursor = cursor
	envelope.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	envelope.Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{
		Generation: "session-turn-test", Sequence: sequence,
	}}
	return envelope
}

func collectSessionTurnTestEvents(events <-chan eventstream.Envelope) []eventstream.Envelope {
	var result []eventstream.Envelope
	for envelope := range events {
		result = append(result, envelope)
	}
	return result
}

type sessionTurnTestClient struct {
	inspectFn         func(context.Context, StateRequest) (SessionState, error)
	reconnectFn       func(context.Context, ReconnectRequest) (ReconnectResult, error)
	promptFn          func(context.Context, PromptRequest) (CommandResult, error)
	steerFn           func(context.Context, SteerRequest) (CommandResult, error)
	resolveApprovalFn func(context.Context, ResolveApprovalRequest) (CommandResult, error)
	cancelFn          func(context.Context, CancelRequest) (CommandResult, error)
	closeSessionCalls int
}

type participantTurnTestClient struct {
	startFn  func(context.Context, StartParticipantRequest) (CommandResult, error)
	promptFn func(context.Context, PromptParticipantRequest) (CommandResult, error)
	cancelFn func(context.Context, CancelParticipantRequest) (CommandResult, error)
}

func (*participantTurnTestClient) Handles(context.Context, string) ([]string, error) {
	return nil, nil
}

func (c *participantTurnTestClient) StartParticipant(ctx context.Context, request StartParticipantRequest) (CommandResult, error) {
	return c.startFn(ctx, request)
}

func (c *participantTurnTestClient) PromptParticipant(ctx context.Context, request PromptParticipantRequest) (CommandResult, error) {
	if c.promptFn == nil {
		return CommandResult{}, errors.New("unexpected PromptParticipant")
	}
	return c.promptFn(ctx, request)
}

func (c *participantTurnTestClient) CancelParticipant(ctx context.Context, request CancelParticipantRequest) (CommandResult, error) {
	return c.cancelFn(ctx, request)
}

func (*sessionTurnTestClient) Initialize(context.Context) (ServerInfo, error) {
	return ServerInfo{}, nil
}

func (*sessionTurnTestClient) ListSessions(context.Context, ListSessionsRequest) (session.SessionList, error) {
	return session.SessionList{}, nil
}

func (*sessionTurnTestClient) CreateSession(context.Context, CreateSessionRequest) (CommandResult, error) {
	return CommandResult{}, errors.New("unexpected CreateSession")
}

func (c *sessionTurnTestClient) CloseSession(context.Context, CloseSessionRequest) (CommandResult, error) {
	c.closeSessionCalls++
	return CommandResult{}, errors.New("unexpected CloseSession")
}

func (*sessionTurnTestClient) CompactSession(context.Context, CompactSessionRequest) (CommandResult, error) {
	return CommandResult{}, errors.New("unexpected CompactSession")
}

func (c *sessionTurnTestClient) InspectSession(
	ctx context.Context,
	request StateRequest,
) (SessionState, error) {
	if c.inspectFn == nil {
		return SessionState{}, errors.New("unexpected InspectSession")
	}
	return c.inspectFn(ctx, request)
}

func (c *sessionTurnTestClient) Reconnect(ctx context.Context, request ReconnectRequest) (ReconnectResult, error) {
	return c.reconnectFn(ctx, request)
}

func (c *sessionTurnTestClient) Prompt(ctx context.Context, request PromptRequest) (CommandResult, error) {
	return c.promptFn(ctx, request)
}

func (c *sessionTurnTestClient) Steer(ctx context.Context, request SteerRequest) (CommandResult, error) {
	if c.steerFn == nil {
		return CommandResult{}, errors.New("unexpected Steer")
	}
	return c.steerFn(ctx, request)
}

func (c *sessionTurnTestClient) Cancel(ctx context.Context, request CancelRequest) (CommandResult, error) {
	if c.cancelFn == nil {
		return CommandResult{}, errors.New("unexpected Cancel")
	}
	return c.cancelFn(ctx, request)
}

func (c *sessionTurnTestClient) ResolveApproval(ctx context.Context, request ResolveApprovalRequest) (CommandResult, error) {
	if c.resolveApprovalFn == nil {
		return CommandResult{}, errors.New("unexpected ResolveApproval")
	}
	return c.resolveApprovalFn(ctx, request)
}

type sessionTurnTestSubscription struct {
	deliveries chan FeedDelivery
	stop       chan struct{}

	closeOnce sync.Once
	mu        sync.RWMutex
	err       error
}

func newSessionTurnTestSubscription(
	backfill []eventstream.Envelope,
	events []eventstream.Envelope,
	err error,
) *sessionTurnTestSubscription {
	subscription := &sessionTurnTestSubscription{
		deliveries: make(chan FeedDelivery),
		stop:       make(chan struct{}),
		err:        err,
	}
	go subscription.deliver(backfill, events)
	return subscription
}

func newOpenSessionTurnTestSubscription() *sessionTurnTestSubscription {
	subscription := &sessionTurnTestSubscription{deliveries: make(chan FeedDelivery), stop: make(chan struct{})}
	go func() {
		select {
		case <-subscription.stop:
			close(subscription.deliveries)
		case subscription.deliveries <- FeedDelivery{Kind: FeedDeliverySync, Source: FeedSourceExact}:
			<-subscription.stop
			close(subscription.deliveries)
		}
	}()
	return subscription
}

func (s *sessionTurnTestSubscription) deliver(
	backfill []eventstream.Envelope,
	events []eventstream.Envelope,
) {
	defer close(s.deliveries)
	if len(backfill) > 0 {
		backfill = cloneEnvelopes(backfill)
		for index := range backfill {
			backfill[index].Cursor = ""
			backfill[index].Position = nil
		}
		if !s.send(FeedDelivery{Kind: FeedDeliveryReplaceBegin, Source: FeedSourceReplacement, SnapshotID: "test-snapshot"}) {
			return
		}
		if !s.send(FeedDelivery{Kind: FeedDeliveryReplacePage, Source: FeedSourceReplacement, SnapshotID: "test-snapshot", Events: backfill}) {
			return
		}
		if !s.send(FeedDelivery{Kind: FeedDeliveryReplaceEnd, Source: FeedSourceReplacement, SnapshotID: "test-snapshot", Page: 1}) {
			return
		}
	}
	if !s.send(FeedDelivery{Kind: FeedDeliverySync, Source: FeedSourceExact}) {
		return
	}
	for _, envelope := range events {
		if !s.send(FeedDelivery{Kind: FeedDeliveryAppendPage, Source: FeedSourceExact, Events: []eventstream.Envelope{envelope}, NextCursor: envelope.Cursor}) {
			return
		}
	}
}

func (s *sessionTurnTestSubscription) send(delivery FeedDelivery) bool {
	select {
	case <-s.stop:
		return false
	case s.deliveries <- delivery:
		return true
	}
}

func (s *sessionTurnTestSubscription) Deliveries() <-chan FeedDelivery {
	return s.deliveries
}

func (s *sessionTurnTestSubscription) Close() error {
	s.closeOnce.Do(func() { close(s.stop) })
	return nil
}

func (s *sessionTurnTestSubscription) Err() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

var _ SessionClient = (*sessionTurnTestClient)(nil)
var _ FeedSubscription = (*sessionTurnTestSubscription)(nil)
