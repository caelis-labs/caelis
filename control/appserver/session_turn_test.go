package appserver

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestSessionTurnClientAttachesBeforePromptAndFiltersExactTarget(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-current", RunID: "run-current", TurnID: "turn-current"}
	between := eventstream.TurnCompleted("handle-old", "run-old", "turn-old", time.Now())
	between.Cursor = "cursor-between"
	between.SessionID = "session-1"
	current := sessionTurnTestMessage("session-1", target, "cursor-current", "done")
	foreignTerminal := eventstream.TurnCompleted("handle-other", "run-other", "turn-other", time.Now())
	foreignTerminal.Cursor = "cursor-foreign"
	foreignTerminal.SessionID = "session-1"
	terminal := eventstream.TurnCompleted(target.HandleID, target.RunID, target.TurnID, time.Now())
	terminal.Cursor = "cursor-terminal"
	terminal.SessionID = "session-1"

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
	if turn.LastCursor() != "cursor-terminal" {
		t.Fatalf("LastCursor() = %q", turn.LastCursor())
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

func TestSessionTurnClientRecoversGapFromLastAcceptedCursor(t *testing.T) {
	t.Parallel()

	target := TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	first := newSessionTurnTestSubscription(
		nil,
		[]eventstream.Envelope{
			sessionTurnTestMessage("session-1", target, "cursor-1", "provisional"),
		},
		&FeedGapError{
			Cause:        ErrSlowConsumer,
			RetryCursor:  "cursor-1",
			Mode:         ResumeModeDurableFallback,
			TransientGap: true,
		},
	)
	final := sessionTurnTestMessage("session-1", target, "cursor-2", "final")
	final.Final = true
	terminal := eventstream.TurnCompleted(target.HandleID, target.RunID, target.TurnID, time.Now())
	terminal.SessionID = "session-1"
	terminal.Cursor = "cursor-3"
	second := newSessionTurnTestSubscription(
		[]eventstream.Envelope{final, terminal},
		nil,
		nil,
	)

	var reconnectMu sync.Mutex
	var reconnects []ReconnectRequest
	client := &sessionTurnTestClient{
		inspectFn: func(context.Context, StateRequest) (SessionState, error) {
			return SessionState{
				SessionID:      "session-1",
				BoundaryCursor: "cursor-boundary",
			}, nil
		},
		reconnectFn: func(_ context.Context, request ReconnectRequest) (ReconnectResult, error) {
			reconnectMu.Lock()
			reconnects = append(reconnects, request)
			call := len(reconnects)
			reconnectMu.Unlock()
			switch call {
			case 1:
				return ReconnectResult{
					State:        SessionState{SessionID: "session-1", Revision: 1},
					Subscription: first,
				}, nil
			case 2:
				return ReconnectResult{
					State:        SessionState{SessionID: "session-1", Revision: 2},
					Subscription: second,
				}, nil
			default:
				return ReconnectResult{}, errors.New("unexpected reconnect")
			}
		},
		promptFn: func(_ context.Context, request PromptRequest) (CommandResult, error) {
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
		SessionID: "session-1",
		Input:     "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()

	got := collectSessionTurnTestEvents(turn.Events())
	if len(got) != 3 ||
		got[0].Cursor != "cursor-1" ||
		got[1].Cursor != "cursor-2" ||
		got[2].Cursor != "cursor-3" {
		t.Fatalf("recovered events = %#v", got)
	}
	if err := turn.Err(); err != nil {
		t.Fatalf("Turn Err() = %v", err)
	}
	reconnectMu.Lock()
	defer reconnectMu.Unlock()
	if len(reconnects) != 2 ||
		reconnects[0].Cursor != "cursor-boundary" ||
		reconnects[1].Cursor != "cursor-1" {
		t.Fatalf("Reconnect requests = %#v", reconnects)
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
	return eventstream.Envelope{
		Kind:      eventstream.KindSessionUpdate,
		SessionID: sessionID,
		HandleID:  target.HandleID,
		RunID:     target.RunID,
		TurnID:    target.TurnID,
		Scope:     eventstream.ScopeMain,
		Cursor:    cursor,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
		},
	}
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
	backfill     chan eventstream.Envelope
	events       chan eventstream.Envelope
	backfillDone chan struct{}
	stop         chan struct{}

	closeOnce sync.Once
	mu        sync.RWMutex
	err       error
	last      string
}

func newSessionTurnTestSubscription(
	backfill []eventstream.Envelope,
	events []eventstream.Envelope,
	err error,
) *sessionTurnTestSubscription {
	subscription := &sessionTurnTestSubscription{
		backfill:     make(chan eventstream.Envelope),
		events:       make(chan eventstream.Envelope),
		backfillDone: make(chan struct{}),
		stop:         make(chan struct{}),
		err:          err,
	}
	go subscription.deliver(backfill, events)
	return subscription
}

func newOpenSessionTurnTestSubscription() *sessionTurnTestSubscription {
	backfill := make(chan eventstream.Envelope)
	close(backfill)
	backfillDone := make(chan struct{})
	close(backfillDone)
	return &sessionTurnTestSubscription{
		backfill:     backfill,
		events:       make(chan eventstream.Envelope),
		backfillDone: backfillDone,
		stop:         make(chan struct{}),
	}
}

func (s *sessionTurnTestSubscription) deliver(
	backfill []eventstream.Envelope,
	events []eventstream.Envelope,
) {
	defer close(s.events)
	for _, envelope := range backfill {
		select {
		case <-s.stop:
			close(s.backfill)
			close(s.backfillDone)
			return
		case s.backfill <- envelope:
			s.record(envelope.Cursor)
		}
	}
	close(s.backfill)
	close(s.backfillDone)
	for _, envelope := range events {
		select {
		case <-s.stop:
			return
		case s.events <- envelope:
			s.record(envelope.Cursor)
		}
	}
}

func (s *sessionTurnTestSubscription) record(cursor string) {
	s.mu.Lock()
	s.last = cursor
	s.mu.Unlock()
}

func (s *sessionTurnTestSubscription) Backfill() <-chan eventstream.Envelope {
	return s.backfill
}

func (s *sessionTurnTestSubscription) Events() <-chan eventstream.Envelope {
	return s.events
}

func (s *sessionTurnTestSubscription) BackfillDone() <-chan struct{} {
	return s.backfillDone
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

func (s *sessionTurnTestSubscription) LastCursor() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last
}

var _ SessionClient = (*sessionTurnTestClient)(nil)
var _ FeedSubscription = (*sessionTurnTestSubscription)(nil)
