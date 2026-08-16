package appserveradapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/caelis-labs/caelis/control/appserver/wirev1"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func TestSessionClientAdapterRoutesMainTurnWritesAndObservationThroughTypedClient(t *testing.T) {
	target := appserver.TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"}
	subscription := newSessionClientAdapterTestSubscription()
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: subscription,
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			subscription,
		},
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
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

func TestSessionClientAdapterInterruptsBlockedMainTurnAdmission(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "handle-pending", RunID: "run-pending", TurnID: "turn-pending"}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		promptStarted: started,
		promptRelease: release,
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")

	done := make(chan struct {
		turn controlprompt.Turn
		err  error
	}, 1)
	go func() {
		turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
		done <- struct {
			turn controlprompt.Turn
			err  error
		}{turn, err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Prompt() did not start")
	}
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- adapter.Interrupt(context.Background())
	}()
	time.Sleep(20 * time.Millisecond)
	client.mu.Lock()
	cancelled := client.cancel.Target
	client.mu.Unlock()
	if cancelled != (appserver.TurnTarget{}) {
		t.Fatalf("Cancel() during blocked Start = %#v, want no cancel until admission finishes", cancelled)
	}

	close(release)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("Submit() after pending interrupt = %v", got.err)
		}
		if got.turn == nil {
			t.Fatal("Submit() Turn = nil, want admitted turn so the feed can observe cancellation")
		}
		defer got.turn.Close()
	case <-time.After(time.Second):
		t.Fatal("Submit() did not return after admission resumed")
	}
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Interrupt() = %v, want formal cancel after admission", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Interrupt() did not return after admission")
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.cancel.Target != target {
		t.Fatalf("Cancel() after admission = %#v, want formal cancel of %#v", client.cancel.Target, target)
	}
}

func TestSessionClientAdapterInterruptBoundsUncooperativeAdmissionAndCancelsLateTurn(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "handle-late", RunID: "run-late", TurnID: "turn-late"}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	cancelled := make(chan appserver.CancelRequest, 1)
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		promptStarted:       started,
		promptRelease:       release,
		promptIgnoreContext: true,
		cancelCalled:        cancelled,
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")

	submitDone := make(chan error, 1)
	go func() {
		_, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
		submitDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Prompt() did not start")
	}
	startedAt := time.Now()
	if err := adapter.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt() = %v, want bounded local admission interrupt", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("Interrupt() took %v, want bounded Esc response", elapsed)
	}
	select {
	case err := <-submitDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Submit() = %v, want cancelled admission", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit() remained stuck after Esc")
	}

	// Simulate a transport that ignored cancellation and reports a committed
	// target later. The adapter must still issue an exact-target cancel instead
	// of orphaning that Turn.
	close(release)
	select {
	case request := <-cancelled:
		if request.Target != target {
			t.Fatalf("late Cancel() target = %#v, want %#v", request.Target, target)
		}
	case <-time.After(time.Second):
		t.Fatal("late admitted Turn was not cancelled")
	}
}

func TestSessionClientAdapterFailedAdmissionAfterInterruptDoesNotCancelNextTurn(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "handle-next", RunID: "run-next", TurnID: "turn-next"}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		promptStarted: started,
		promptRelease: release,
		promptErr:     errors.New("admission failed"),
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")

	done := make(chan error, 1)
	go func() {
		_, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Prompt() did not start")
	}
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- adapter.Interrupt(context.Background())
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Submit() error = nil, want failed admission")
		}
	case <-time.After(time.Second):
		t.Fatal("Submit() did not return failed admission")
	}
	select {
	case err := <-interruptDone:
		if err == nil {
			t.Fatal("Interrupt() = nil, want failed admission reported instead of accepted interrupt")
		}
	case <-time.After(time.Second):
		t.Fatal("Interrupt() did not return after failed admission")
	}

	client.mu.Lock()
	client.promptErr = nil
	client.promptRelease = nil
	client.mu.Unlock()
	turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "retry"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.cancel.Target != (appserver.TurnTarget{}) {
		t.Fatalf("next Turn was cancelled = %#v, want leftover pending interrupt cleared", client.cancel.Target)
	}
}

func TestSessionClientAdapterUnknownAdmissionWithoutTargetDoesNotAcceptInterrupt(t *testing.T) {
	t.Parallel()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	unknown := appserver.NewOutcomeError(appserver.OutcomeUnknown, errors.New("effect outcome cannot be proven"))
	client := &sessionClientAdapterTestClient{
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		promptStarted:    started,
		promptRelease:    release,
		promptErr:        unknown,
		promptOutcome:    appserver.OutcomeUnknown,
		omitPromptTarget: true,
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")

	done := make(chan error, 1)
	go func() {
		_, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Prompt() did not start")
	}
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- adapter.Interrupt(context.Background())
	}()
	time.Sleep(20 * time.Millisecond)
	close(release)
	select {
	case err := <-done:
		var receipt *appserver.CommandReceiptError
		if !errors.As(err, &receipt) || receipt.Receipt.Outcome != appserver.OutcomeUnknown {
			t.Fatalf("Submit() = %v, want unknown admission receipt", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Submit() did not return unknown admission")
	}
	select {
	case err := <-interruptDone:
		var receipt *appserver.CommandReceiptError
		if !errors.As(err, &receipt) || receipt.Receipt.Outcome != appserver.OutcomeUnknown {
			t.Fatalf("Interrupt() = %v, want unknown instead of accepted interrupt", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Interrupt() did not return after unknown admission")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.cancel.Target != (appserver.TurnTarget{}) {
		t.Fatalf("Cancel() = %#v, want no ambient cancel without a proven target", client.cancel.Target)
	}
}

func TestSessionClientAdapterUnknownAdmissionWithTargetKeepsTurnForCancel(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "handle-unknown", RunID: "run-unknown", TurnID: "turn-unknown"}
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		promptErr:     appserver.NewOutcomeError(appserver.OutcomeUnknown, errors.New("effect outcome cannot be proven")),
		promptOutcome: appserver.OutcomeUnknown,
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
	if err != nil {
		t.Fatalf("Submit() = %v, want admitted turn when unknown outcome still has a target", err)
	}
	defer turn.Close()
	if err := adapter.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.cancel.Target != target {
		t.Fatalf("Cancel() = %#v, want proven unknown-admission target", client.cancel.Target)
	}
}

func TestSessionClientAdapterReportsFailedCancelAfterAdmission(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "handle-cancel", RunID: "run-cancel", TurnID: "turn-cancel"}
	cancelErr := errors.New("cancel rejected")
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		cancelErr: cancelErr,
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	if err := adapter.Interrupt(context.Background()); !errors.Is(err, cancelErr) {
		t.Fatalf("Interrupt() = %v, want cancel error", err)
	}
}

func TestSessionClientAdapterInterruptsBlockedReviewAdmission(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "review-handle", RunID: "review-run", TurnID: "review-turn"}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		state: appserver.SessionState{
			SessionID: "session-1",
			Revision:  3,
			Controller: session.ControllerBinding{
				EpochID: "epoch-review",
			},
		},
	}
	participants := &sessionClientAdapterTestParticipantClient{
		target:       target,
		startStarted: started,
		startRelease: release,
	}
	adapter := newSessionClientAdapterForTest(t, client, participants, "session-1", "cli-tui")
	done := make(chan struct {
		turn controlprompt.Turn
		err  error
	}, 1)
	go func() {
		turn, err := adapter.StartReview(context.Background(), "check the change", nil)
		done <- struct {
			turn controlprompt.Turn
			err  error
		}{turn, err}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("StartParticipant() did not start")
	}
	interruptDone := make(chan error, 1)
	go func() {
		interruptDone <- adapter.Interrupt(context.Background())
	}()
	close(release)
	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("StartReview() = %v", got.err)
		}
		defer got.turn.Close()
	case <-time.After(time.Second):
		t.Fatal("StartReview() did not return")
	}
	select {
	case err := <-interruptDone:
		if err != nil {
			t.Fatalf("Interrupt() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Interrupt() did not return after review admission")
	}
	if participants.cancel.Target != target {
		t.Fatalf("CancelParticipant() = %#v, want %#v", participants.cancel.Target, target)
	}
}

func TestSessionClientAdapterRetriesCancelAfterTransientFailure(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "handle-retry", RunID: "run-retry", TurnID: "turn-retry"}
	firstErr := context.DeadlineExceeded
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		cancelErrs: []error{firstErr, nil},
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	if err := adapter.Interrupt(context.Background()); !errors.Is(err, firstErr) {
		t.Fatalf("first Interrupt() = %v, want deadline", err)
	}
	if err := adapter.Interrupt(context.Background()); err != nil {
		t.Fatalf("retry Interrupt() = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.cancelIDs) != 2 || client.cancelIDs[0] == "" || client.cancelIDs[0] != client.cancelIDs[1] {
		t.Fatalf("Cancel operation IDs = %#v, want one stable ID reused after failure", client.cancelIDs)
	}
}

func TestSessionClientAdapterRetriesRejectedCancelWithFreshOperationID(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "handle-rejected", RunID: "run-rejected", TurnID: "turn-rejected"}
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		cancelOutcomes: []appserver.Outcome{appserver.OutcomeRejected, appserver.OutcomeCommitted},
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	var receipt *appserver.CommandReceiptError
	if err := adapter.Interrupt(context.Background()); !errors.As(err, &receipt) || receipt.Receipt.Outcome != appserver.OutcomeRejected {
		t.Fatalf("first Interrupt() = %v, want rejected receipt", err)
	}
	if err := adapter.Interrupt(context.Background()); err != nil {
		t.Fatalf("retry Interrupt() = %v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.cancelIDs) != 2 || client.cancelIDs[0] == "" || client.cancelIDs[0] == client.cancelIDs[1] {
		t.Fatalf("Cancel operation IDs = %#v, want a fresh ID after rejection", client.cancelIDs)
	}
}

func TestSessionClientAdapterBoundsUncooperativeActiveCancel(t *testing.T) {
	t.Parallel()

	target := appserver.TurnTarget{HandleID: "handle-cancel-blocked", RunID: "run-cancel-blocked", TurnID: "turn-cancel-blocked"}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			newSessionClientAdapterTestSubscription(),
			newSessionClientAdapterTestSubscription(),
		},
		cancelStarted: started,
		cancelRelease: release,
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	defer turn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	interruptDone := make(chan error, 1)
	go func() { interruptDone <- adapter.Interrupt(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("Cancel() did not start")
	}
	select {
	case err := <-interruptDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Interrupt() = %v, want bounded deadline", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Interrupt() remained stuck in an uncooperative Cancel")
	}
	close(release)
}

func TestSessionClientAdapterCreatesSessionOnlyWhenMainPromptStartsWork(t *testing.T) {
	target := appserver.TurnTarget{HandleID: "handle-main", RunID: "run-main", TurnID: "turn-main"}
	sessions := &sessionClientAdapterTestClient{
		createSessionID: "session-main",
		target:          target,
		subscription:    newSessionClientAdapterTestSubscription(),
		state: appserver.SessionState{
			Revision:   1,
			Controller: session.ControllerBinding{EpochID: "epoch-main"},
		},
	}
	adapter := newSessionClientAdapterForTest(t, sessions, &sessionClientAdapterTestParticipantClient{}, "", "cli-tui")
	if _, err := adapter.LightweightStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Submit(context.Background(), controlprompt.Submission{Mode: controlprompt.SubmissionModeActiveTurn, Text: "steer"}); err == nil {
		t.Fatal("active-turn submission without a Turn succeeded")
	}
	if sessions.create.OperationID != "" {
		t.Fatalf("idle status or steer created Session: %#v", sessions.create)
	}
	if _, err := adapter.Submit(context.Background(), controlprompt.Submission{}); err == nil {
		t.Fatal("empty prompt succeeded")
	}
	if sessions.create.OperationID != "" {
		t.Fatalf("empty prompt created Session: %#v", sessions.create)
	}
	turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "start work"})
	if err != nil {
		t.Fatal(err)
	}
	if turn == nil {
		t.Fatal("Submit() Turn = nil")
	}
	if !strings.HasPrefix(sessions.create.OperationID, "session-main-prompt-") ||
		sessions.prompt.SessionID != "session-main" || adapter.clientSessionID() != "session-main" {
		t.Fatalf("create=%#v prompt=%#v active=%q", sessions.create, sessions.prompt, adapter.clientSessionID())
	}
	if err := turn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionClientAdapterParticipantPromptOwnsSessionCreation(t *testing.T) {
	tests := []struct {
		name  string
		start func(*SessionClientAdapter, *sessionClientAdapterTestClient) (controlprompt.Turn, error)
	}{
		{
			name: "review",
			start: func(adapter *SessionClientAdapter, _ *sessionClientAdapterTestClient) (controlprompt.Turn, error) {
				return adapter.StartReview(context.Background(), "check the change", nil)
			},
		},
		{
			name: "direct Agent prompt",
			start: func(adapter *SessionClientAdapter, sessions *sessionClientAdapterTestClient) (controlprompt.Turn, error) {
				if _, err := adapter.StartAgentRun(context.Background(), "orbit", "", nil); err == nil {
					return nil, errors.New("empty direct Agent prompt succeeded")
				}
				if sessions.create.OperationID != "" {
					return nil, fmt.Errorf("empty direct Agent prompt created Session: %#v", sessions.create)
				}
				return adapter.StartAgentRun(context.Background(), "orbit", "inspect the change", nil)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := appserver.TurnTarget{HandleID: "handle-participant", RunID: "run-participant", TurnID: "turn-participant"}
			sessions := &sessionClientAdapterTestClient{
				createSessionID: "session-participant",
				target:          target,
				subscription:    newSessionClientAdapterTestSubscription(),
				state: appserver.SessionState{
					Revision:   1,
					Controller: session.ControllerBinding{EpochID: "epoch-participant"},
				},
			}
			participants := &sessionClientAdapterTestParticipantClient{target: target}
			adapter := newSessionClientAdapterForTest(t, sessions, participants, "", "cli-tui")
			turn, err := test.start(adapter, sessions)
			if err != nil {
				t.Fatal(err)
			}
			if turn == nil {
				t.Fatal("participant Turn = nil")
			}
			if !strings.HasPrefix(sessions.create.OperationID, "session-participant-prompt-") ||
				participants.start.SessionID != "session-participant" || sessions.prompt.OperationID != "" {
				t.Fatalf("create=%#v participant=%#v mainPrompt=%#v", sessions.create, participants.start, sessions.prompt)
			}
			if err := turn.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionClientAdapterRoutesReviewThroughTypedParticipantClient(t *testing.T) {
	target := appserver.TurnTarget{HandleID: "participant-handle", RunID: "participant-run", TurnID: "participant-turn"}
	subscription := newSessionClientAdapterTestSubscription()
	client := &sessionClientAdapterTestClient{
		target:       target,
		subscription: subscription,
		state: appserver.SessionState{
			SessionID: "session-1",
			Revision:  7,
			CWD:       t.TempDir(),
			Controller: session.ControllerBinding{
				EpochID: "epoch-1",
			},
		},
	}
	participants := &sessionClientAdapterTestParticipantClient{target: target}
	adapter := newSessionClientAdapterForTest(t, client, participants, "session-1", "acp")
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

func TestSessionClientAdapterRoutesSideAgentStartAndFollowUpThroughTypedClients(t *testing.T) {
	target := appserver.TurnTarget{HandleID: "participant-handle", RunID: "participant-run", TurnID: "participant-turn"}
	sessions := &sessionClientAdapterTestClient{
		target:       target,
		subscription: newSessionClientAdapterTestSubscription(),
		state: appserver.SessionState{
			SessionID: "session-1", Revision: 7, CWD: t.TempDir(),
			Controller: session.ControllerBinding{EpochID: "epoch-1"},
		},
	}
	participants := &sessionClientAdapterTestParticipantClient{target: target}
	adapter := newSessionClientAdapterForTest(t, sessions, participants, "session-1", "cli-tui")

	started, err := adapter.StartAgentRun(context.Background(), "orbit", " inspect ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if started == nil || participants.start.SessionID != "session-1" ||
		participants.start.Handle != "orbit" || participants.start.Source != "slash_profile_orbit" ||
		participants.start.Role != session.ParticipantRoleSidecar || participants.start.Input != "inspect" {
		t.Fatalf("typed participant start = %#v, Turn=%#v", participants.start, started)
	}
	if err := started.Close(); err != nil {
		t.Fatal(err)
	}

	sessions.subscription = newSessionClientAdapterTestSubscription()
	sessions.state.Participants = []session.ParticipantBinding{{
		ID: "participant-1", Kind: session.ParticipantKindACP,
		Role: session.ParticipantRoleSidecar, Label: participants.start.Label, Source: participants.start.Source,
	}}
	followUp, err := adapter.ContinueAgentRun(context.Background(), participants.start.Label, " continue ", nil)
	if err != nil {
		t.Fatal(err)
	}
	if followUp == nil || participants.prompt.SessionID != "session-1" ||
		participants.prompt.ParticipantID != "participant-1" || participants.prompt.Input != "continue" ||
		participants.prompt.Source != "user_side_agent" {
		t.Fatalf("typed participant follow-up = %#v, Turn=%#v", participants.prompt, followUp)
	}
	if err := followUp.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionClientAdapterRoutesCompactThroughTypedSessionClient(t *testing.T) {
	client := &sessionClientAdapterTestClient{state: appserver.SessionState{
		SessionID: "session-1",
		Revision:  7,
		Controller: session.ControllerBinding{
			EpochID: "epoch-1",
		},
	}}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	if err := adapter.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := client.compact
	if request.SessionID != "session-1" ||
		request.ExpectedRevision == nil || *request.ExpectedRevision != 7 ||
		request.ExpectedControllerEpoch != "epoch-1" ||
		!strings.HasPrefix(request.OperationID, "compact-") {
		t.Fatalf("typed compact request = %#v", request)
	}
}

func newSessionClientAdapterForTest(
	t *testing.T,
	sessions appserver.SessionClient,
	participants appserver.ParticipantClient,
	sessionID string,
	surface string,
) *SessionClientAdapter {
	t.Helper()
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		SessionID:     sessionID,
		WorkspaceKey:  "workspace",
		WorkspaceDir:  t.TempDir(),
		Surface:       surface,
		Sessions:      sessions,
		Participants:  participants,
		Status:        sessionClientAdapterTestStatusClient{},
		Configuration: sessionClientAdapterTestConfigurationClient{},
		Agents:        &sessionClientAdapterTestAgentClient{},
		Completion:    &sessionClientAdapterTestCompletionClient{},
		Plugins:       &sessionClientAdapterTestPluginClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return adapter
}

func TestAppServerAdapterRoutesSessionLifecycleThroughTypedClient(t *testing.T) {
	replay := newSessionClientAdapterTestSubscription()
	presence := newSessionClientAdapterTestSubscription()
	client := &sessionClientAdapterTestClient{
		createSessionID: "session-new",
		reconnectSubscriptions: []*sessionClientAdapterTestSubscription{
			replay,
			presence,
		},
		state: appserver.SessionState{
			Revision: 7,
			Controller: session.ControllerBinding{
				EpochID: "epoch-1",
			},
		},
		list: session.SessionList{Sessions: []session.SessionSummary{{
			SessionRef: session.SessionRef{SessionID: "session-listed"}, Title: "listed", CWD: t.TempDir(), UpdatedAt: time.Now(),
		}}},
	}
	workspaceDir := t.TempDir()
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		WorkspaceKey:  "workspace",
		WorkspaceDir:  workspaceDir,
		Surface:       "cli-tui",
		Sessions:      client,
		Participants:  &sessionClientAdapterTestParticipantClient{},
		Status:        sessionClientAdapterTestStatusClient{},
		Configuration: sessionClientAdapterTestConfigurationClient{},
		Agents:        &sessionClientAdapterTestAgentClient{},
		Completion:    &sessionClientAdapterTestCompletionClient{},
		Plugins:       &sessionClientAdapterTestPluginClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	if err := adapter.ResetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.create.OperationID != "" || adapter.clientSessionID() != "" {
		t.Fatalf("ResetSession created or selected a Session: request=%#v active=%q", client.create, adapter.clientSessionID())
	}
	listed, err := adapter.ListSessions(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].SessionID != "session-listed" || listed[0].Title != "listed" {
		t.Fatalf("listed = %#v", listed)
	}
	resumed, err := adapter.ResumeSession(context.Background(), "session-resumed")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.SessionID != "session-resumed" || resumed.Reconnect == nil || adapter.clientSessionID() != "session-resumed" {
		t.Fatalf("resumed = %#v active=%q", resumed, adapter.clientSessionID())
	}
	if replay.closed || presence.closed {
		t.Fatalf("ResumeSession closed live replay/presence: replay=%v presence=%v", replay.closed, presence.closed)
	}
	if err := resumed.Reconnect.Close(); err != nil {
		t.Fatal(err)
	}
	if !replay.closed || presence.closed {
		t.Fatalf("closing transcript reconnect affected presence: replay=%v presence=%v", replay.closed, presence.closed)
	}
	if err := adapter.ResetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !presence.closed || adapter.clientSessionID() != "" {
		t.Fatalf("ResetSession did not detach presence: closed=%v active=%q", presence.closed, adapter.clientSessionID())
	}
}

func TestResetSessionDetachesActiveTurnWithoutCancellingHostWork(t *testing.T) {
	target := &detachOnlyTargetTurn{
		target: appserver.TurnTarget{HandleID: "handle-1", RunID: "run-1", TurnID: "turn-1"},
		events: make(chan eventstream.Envelope),
	}
	adapter := newSessionClientAdapterForTest(
		t,
		&sessionClientAdapterTestClient{},
		&sessionClientAdapterTestParticipantClient{},
		"session-running",
		"cli-tui",
	)
	adapter.setActiveTurn(&sessionClientTurn{turn: target})

	if err := adapter.ResetSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if target.closeCalls != 1 || target.cancelCalls != 0 {
		t.Fatalf("reset calls close=%d cancel=%d, want detach-only close", target.closeCalls, target.cancelCalls)
	}
	if got := adapter.clientSessionID(); got != "" {
		t.Fatalf("active Session after reset = %q, want none", got)
	}
}

func TestTUIResumeRejectsRetiredExternalMainControllerSession(t *testing.T) {
	subscription := newSessionClientAdapterTestSubscription()
	client := &sessionClientAdapterTestClient{
		subscription: subscription,
		state: appserver.SessionState{
			SessionID: "retired-controller-session",
			Controller: session.ControllerBinding{
				Kind: session.ControllerKindACP, ControllerID: "retired-main-controller", EpochID: "retired-epoch",
			},
		},
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "", "cli-tui")
	resumed, err := adapter.ResumeSession(context.Background(), "retired-controller-session")
	if err == nil || !strings.Contains(err.Error(), "no longer supported in the TUI") {
		t.Fatalf("ResumeSession(retired ACP controller) = %#v, %v", resumed, err)
	}
	if adapter.clientSessionID() != "" {
		t.Fatalf("retired ACP controller Session became active: %q", adapter.clientSessionID())
	}
	if !subscription.closed {
		t.Fatal("rejected reconnect subscription was not closed")
	}
}

func TestTUIWorkRejectsRetiredExternalMainControllerSession(t *testing.T) {
	tests := []struct {
		name              string
		configuredID      string
		preferredID       string
		createSessionID   string
		wantCreateRequest bool
	}{
		{name: "configured Session", configuredID: "retired-controller-session"},
		{
			name: "preferred Session", preferredID: "retired-controller-session",
			createSessionID: "retired-controller-session", wantCreateRequest: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &sessionClientAdapterTestClient{
				createSessionID: test.createSessionID,
				state: appserver.SessionState{
					SessionID: "retired-controller-session",
					Controller: session.ControllerBinding{
						Kind: session.ControllerKindACP, ControllerID: "retired-main-controller", EpochID: "retired-epoch",
					},
				},
			}
			adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, test.configuredID, "cli-tui")
			adapter.preferredID = test.preferredID
			turn, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "start work"})
			if err == nil || !strings.Contains(err.Error(), "no longer supported in the TUI") {
				t.Fatalf("Submit(retired ACP controller) = %#v, %v", turn, err)
			}
			if adapter.clientSessionID() != test.configuredID {
				t.Fatalf("rejected ACP controller changed active Session: %q, want %q", adapter.clientSessionID(), test.configuredID)
			}
			if got := client.create.OperationID != ""; got != test.wantCreateRequest {
				t.Fatalf("CreateSession called = %v, want %v: %#v", got, test.wantCreateRequest, client.create)
			}
			if client.prompt.OperationID != "" {
				t.Fatalf("rejected ACP controller started a prompt: %#v", client.prompt)
			}
		})
	}
}

func TestTUISelectedRetiredExternalMainControllerRejectsParticipantContinuationAndMutation(t *testing.T) {
	client := &sessionClientAdapterTestClient{state: appserver.SessionState{
		SessionID: "retired-controller-session",
		Controller: session.ControllerBinding{
			Kind: session.ControllerKindACP, ControllerID: "retired-main-controller", EpochID: "retired-epoch",
		},
		Participants: []session.ParticipantBinding{{
			ID: "participant-1", Kind: session.ParticipantKindACP,
			Role: session.ParticipantRoleSidecar, Label: "@side", Source: "slash_profile_orbit",
		}},
	}}
	participants := &sessionClientAdapterTestParticipantClient{}
	adapter := newSessionClientAdapterForTest(t, client, participants, "retired-controller-session", "cli-tui")
	configuration := &modelConfigurationClientProbe{}
	adapter.configClient = configuration

	turn, err := adapter.ContinueAgentRun(context.Background(), "@side", "continue", nil)
	if err == nil || !strings.Contains(err.Error(), "no longer supported in the TUI") {
		t.Fatalf("ContinueAgentRun(retired ACP controller) = %#v, %v", turn, err)
	}
	if participants.prompt.OperationID != "" {
		t.Fatalf("rejected ACP controller prompted a participant: %#v", participants.prompt)
	}
	if err := adapter.Compact(context.Background()); err == nil || !strings.Contains(err.Error(), "no longer supported in the TUI") {
		t.Fatalf("Compact(retired ACP controller) error = %v", err)
	}
	if client.compact.OperationID != "" {
		t.Fatalf("rejected ACP controller compacted the Session: %#v", client.compact)
	}
	if _, err := adapter.UseModel(context.Background(), "ollama/late"); err == nil || !strings.Contains(err.Error(), "no longer supported in the TUI") {
		t.Fatalf("UseModel(retired ACP controller) error = %v", err)
	}
	if configuration.sessionCalls != 0 || configuration.hostCalls != 0 {
		t.Fatalf("rejected ACP controller changed a model: %#v", configuration.sessionRequest)
	}
}

func TestAppServerAdapterReconnectsActiveTurnForSteerAndClonesState(t *testing.T) {
	target := appserver.TurnTarget{HandleID: "handle-resumed", RunID: "run-resumed", TurnID: "turn-resumed"}
	client := &sessionClientAdapterTestClient{
		subscription: newSessionClientAdapterTestSubscription(),
		state: appserver.SessionState{
			SessionID: "session-resumed", Revision: 7,
			Controller: session.ControllerBinding{EpochID: "epoch-resumed"},
			Run: appserver.RunState{
				Active: true, HandleID: target.HandleID, RunID: target.RunID, TurnID: target.TurnID,
			},
			Participants: []session.ParticipantBinding{{
				ID: "participant-1",
				Placement: placement.Placement{
					Kind: placement.KindAgent, Agent: "codex",
					SessionConfigValues: map[string]string{"mode": "plan"},
				},
			}},
		},
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "", "cli-tui")
	resumed, err := adapter.ResumeSession(context.Background(), "session-resumed")
	if err != nil {
		t.Fatal(err)
	}
	firstState := resumed.Reconnect.State()
	firstState.Participants[0].Placement.SessionConfigValues["mode"] = "mutated"
	secondState := resumed.Reconnect.State()
	if got := secondState.Participants[0].Placement.SessionConfigValues["mode"]; got != "plan" {
		t.Fatalf("Reconnect.State participant mode = %q after caller mutation, want plan", got)
	}

	if _, err := adapter.Submit(context.Background(), controlprompt.Submission{
		Text: "continue resumed Turn", Mode: controlprompt.SubmissionModeActiveTurn,
	}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	steer := client.steer
	client.mu.Unlock()
	if steer.Input != "continue resumed Turn" || steer.Target != target ||
		steer.ExpectedRevision == nil || *steer.ExpectedRevision != 7 ||
		steer.ExpectedControllerEpoch != "epoch-resumed" {
		t.Fatalf("reconnected steer = %#v, want exact resumed target and fence", steer)
	}
	if err := adapter.Interrupt(context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	cancel := client.cancel
	client.mu.Unlock()
	if cancel.Target != target || cancel.ExpectedRevision == nil || *cancel.ExpectedRevision != 7 ||
		cancel.ExpectedControllerEpoch != "epoch-resumed" {
		t.Fatalf("reconnected cancel = %#v, want exact resumed target and fence", cancel)
	}

	if err := resumed.Reconnect.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Submit(context.Background(), controlprompt.Submission{
		Text: "too late", Mode: controlprompt.SubmissionModeActiveTurn,
	}); err == nil {
		t.Fatal("reconnected Turn remained steerable after reconnect closed")
	}
}

func TestAppServerAdapterResumeFailurePreservesCurrentSession(t *testing.T) {
	client := &sessionClientAdapterTestClient{
		reconnectErr: errors.New("bootstrap failed"),
		state:        appserver.SessionState{SessionID: "session-current"},
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-current", "cli-tui")
	if _, err := adapter.ResumeSession(context.Background(), "session-target"); !errors.Is(err, client.reconnectErr) {
		t.Fatalf("ResumeSession() error = %v, want %v", err, client.reconnectErr)
	}
	if got := adapter.clientSessionID(); got != "session-current" {
		t.Fatalf("active Session after failed resume = %q, want session-current", got)
	}
}

func TestAppServerAdapterStatusWithoutSessionDoesNotInspectOrCreate(t *testing.T) {
	sessions := &sessionClientAdapterTestClient{}
	status := &recordingStatusClient{}
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		WorkspaceKey:  "workspace",
		WorkspaceDir:  t.TempDir(),
		Surface:       "cli-tui",
		Sessions:      sessions,
		Participants:  &sessionClientAdapterTestParticipantClient{},
		Status:        status,
		Configuration: sessionClientAdapterTestConfigurationClient{},
		Agents:        &sessionClientAdapterTestAgentClient{},
		Completion:    &sessionClientAdapterTestCompletionClient{},
		Plugins:       &sessionClientAdapterTestPluginClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.LightweightStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status.request.SessionID != "" || status.request.IncludeDiagnostics {
		t.Fatalf("status request = %#v, want Host-scoped lightweight status", status.request)
	}
	if sessions.create.OperationID != "" {
		t.Fatalf("status created Session: %#v", sessions.create)
	}
}

func TestAppServerAdapterRoutesSlashDiscoveryAndPluginsThroughTypedClients(t *testing.T) {
	sessions := &sessionClientAdapterTestClient{state: appserver.SessionState{
		SessionID: "session-typed", CWD: t.TempDir(), Revision: 3,
		Controller: session.ControllerBinding{EpochID: "epoch-1"},
	}}
	completion := &recordingCompletionClient{}
	plugins := &recordingPluginClient{}
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		SessionID:     "session-typed",
		WorkspaceKey:  "workspace",
		WorkspaceDir:  sessions.state.CWD,
		Surface:       "cli-tui",
		Sessions:      sessions,
		Participants:  &sessionClientAdapterTestParticipantClient{},
		Status:        sessionClientAdapterTestStatusClient{},
		Configuration: sessionClientAdapterTestConfigurationClient{},
		Agents:        &sessionClientAdapterTestAgentClient{},
		Completion:    completion,
		Plugins:       plugins,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := adapter.CompleteSlashArg(context.Background(), "model use", "mi", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Value != "mimo" ||
		completion.slash.SessionID != "session-typed" || completion.slash.Surface != "cli-tui" ||
		completion.slash.Command != "model use" || completion.slash.Query != "mi" || completion.slash.Limit != 7 {
		t.Fatalf("slash candidates/request = %#v / %#v", candidates, completion.slash)
	}
	skills, err := adapter.CompleteSkill(context.Background(), "review", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].Value != "review-code" || completion.skill.SessionID != "session-typed" {
		t.Fatalf("skill candidates/request = %#v / %#v", skills, completion.skill)
	}
	resolved, err := adapter.ResolveSkill(context.Background(), "review-code")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Canonical != "review-code" || completion.resolve.Name != "review-code" || completion.resolve.SessionID != "session-typed" {
		t.Fatalf("skill result/request = %#v / %#v", resolved, completion.resolve)
	}
	plugin, err := adapter.EnablePlugin(context.Background(), "plugin-1")
	if err != nil {
		t.Fatal(err)
	}
	if plugin.ID != "plugin-1" || plugins.enable.ID != "plugin-1" || plugins.enable.SessionID != "" {
		t.Fatalf("plugin/request = %#v / %#v", plugin, plugins.enable)
	}
}

func TestAppServerAdapterFallsBackToHostSkillCatalogWhenSessionSnapshotIsUnavailable(t *testing.T) {
	sessions := &sessionClientAdapterTestClient{state: appserver.SessionState{SessionID: "session-gone", CWD: t.TempDir(), Revision: 3}}
	completion := &fallbackSkillCompletionClient{}
	adapter, err := NewAppServerAdapter(AppServerAdapterConfig{
		SessionID: "session-gone", WorkspaceKey: "workspace", WorkspaceDir: sessions.state.CWD, Surface: "cli-tui",
		Sessions: sessions, Participants: &sessionClientAdapterTestParticipantClient{}, Status: sessionClientAdapterTestStatusClient{},
		Configuration: sessionClientAdapterTestConfigurationClient{}, Agents: &sessionClientAdapterTestAgentClient{},
		Completion: completion, Plugins: &sessionClientAdapterTestPluginClient{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidates, err := adapter.CompleteSkill(context.Background(), "review", 5); err != nil || len(candidates) != 1 {
		t.Fatalf("CompleteSkill() = %#v, %v", candidates, err)
	}
	if resolved, err := adapter.ResolveSkill(context.Background(), "review-code"); err != nil || resolved.Canonical != "review-code" {
		t.Fatalf("ResolveSkill() = %#v, %v", resolved, err)
	}
	want := []string{"session-gone", "", "session-gone", ""}
	if !reflect.DeepEqual(completion.sessionIDs, want) {
		t.Fatalf("skill fallback Session IDs = %#v, want %#v", completion.sessionIDs, want)
	}
}

func TestSessionClientAdapterSandboxMutationUsesHostRevisionAndObservesOnlyCommittedReceipt(t *testing.T) {
	committedStatus := controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: 41},
		SandboxStatus: controlstatus.StatusSandbox{ResolvedBackend: "host"},
	}
	statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
		{Configuration: controlstatus.StatusConfiguration{Revision: 41}},
		committedStatus,
	}}
	configuration := &sandboxConfigurationClientProbe{result: appserver.CommandResult{Outcome: appserver.OutcomeCommitted}}
	adapter := &SessionClientAdapter{
		statusClient: statusClient, configClient: configuration, surface: "tui", sessionID: "session-1",
	}
	status, err := adapter.SetSandboxBackend(context.Background(), "host")
	if err != nil || !reflect.DeepEqual(status, committedStatus) {
		t.Fatalf("SetSandboxBackend() = %#v, %v", status, err)
	}
	if len(statusClient.requests) != 2 || statusClient.requests[0].SessionID != "" || statusClient.requests[1].SessionID != "session-1" {
		t.Fatalf("status requests = %#v", statusClient.requests)
	}
	if configuration.calls != 1 || configuration.request.SessionID != "" || configuration.request.Backend != "host" ||
		configuration.request.OperationID == "" || configuration.request.ExpectedRevision == nil || *configuration.request.ExpectedRevision != 41 {
		t.Fatalf("sandbox command request = %#v calls=%d", configuration.request, configuration.calls)
	}

	unknownErr := errors.New("effect outcome cannot be proven")
	statusClient = &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{Configuration: controlstatus.StatusConfiguration{Revision: 42}}}}
	configuration = &sandboxConfigurationClientProbe{
		result: appserver.CommandResult{Outcome: appserver.OutcomeUnknown}, err: unknownErr,
	}
	adapter.statusClient, adapter.configClient = statusClient, configuration
	if _, err := adapter.SetSandboxBackend(context.Background(), "auto"); !errors.Is(err, unknownErr) {
		t.Fatalf("SetSandboxBackend(unknown) error = %v", err)
	}
	if len(statusClient.requests) != 1 {
		t.Fatalf("unknown outcome performed %d status reads, want precondition only", len(statusClient.requests))
	}
	var receiptErr *appserver.CommandReceiptError
	if _, err := adapter.SetSandboxBackend(context.Background(), "auto"); !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("SetSandboxBackend(unknown replay) receipt error = %#v, %v", receiptErr, err)
	}

	statusClient = &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{Configuration: controlstatus.StatusConfiguration{Revision: 42}}}}
	configuration = &sandboxConfigurationClientProbe{result: appserver.CommandResult{Outcome: appserver.OutcomeAccepted}}
	adapter.statusClient, adapter.configClient = statusClient, configuration
	receiptErr = nil
	if _, err := adapter.SetSandboxBackend(context.Background(), "auto"); !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeAccepted {
		t.Fatalf("SetSandboxBackend(accepted) receipt error = %#v, %v", receiptErr, err)
	}
	if len(statusClient.requests) != 1 {
		t.Fatalf("accepted outcome performed %d status reads, want precondition only", len(statusClient.requests))
	}

	observationErr := errors.New("status unavailable")
	statusClient = &sandboxStatusClientProbe{
		statuses: []controlstatus.StatusSnapshot{{Configuration: controlstatus.StatusConfiguration{Revision: 43}}, {}},
		errors:   []error{nil, observationErr},
	}
	configuration = &sandboxConfigurationClientProbe{result: appserver.CommandResult{Outcome: appserver.OutcomeCommitted}}
	adapter.statusClient, adapter.configClient = statusClient, configuration
	if _, err := adapter.SetSandboxBackend(context.Background(), "host"); !errors.Is(err, observationErr) ||
		!errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeCommitted ||
		!strings.Contains(err.Error(), "do not retry blindly") {
		t.Fatalf("SetSandboxBackend(observation failure) error = %v", err)
	}
}

func TestSessionClientAdapterAgentBindingMutationUsesHostRevisionAndObservesOnlyCommittedReceipt(t *testing.T) {
	statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{
		Configuration: controlstatus.StatusConfiguration{Revision: 51},
	}}}
	agents := &agentBindingClientProbe{
		result: appserver.CommandResult{Outcome: appserver.OutcomeCommitted},
		status: agentbinding.Status{Sets: []agentbinding.BindingSetStatus{{Name: "baseline"}}},
	}
	adapter := &SessionClientAdapter{statusClient: statusClient, agentClient: agents, surface: "tui", sessionID: "session-1"}
	got, err := adapter.BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleOrbit, ProfileID: "provider:mimo", Effort: "high",
	})
	if err != nil || !reflect.DeepEqual(got, agents.status) {
		t.Fatalf("BindAgentBinding() = %#v, %v", got, err)
	}
	request := agents.bindRequest
	if agents.bindCalls != 1 || agents.statusCalls != 1 || request.SessionID != "" || request.ExpectedControllerEpoch != "" ||
		request.ExpectedRevision == nil || *request.ExpectedRevision != 51 || !strings.HasPrefix(request.OperationID, "agent-binding-") {
		t.Fatalf("Agent binding request/calls = %#v / %d / %d", request, agents.bindCalls, agents.statusCalls)
	}

	commandErr := errors.New("binding outcome unknown")
	statusClient = &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{
		Configuration: controlstatus.StatusConfiguration{Revision: 52},
	}}}
	agents = &agentBindingClientProbe{
		result: appserver.CommandResult{Outcome: appserver.OutcomeUnknown},
		err:    commandErr,
	}
	adapter.statusClient, adapter.agentClient = statusClient, agents
	_, err = adapter.BindAgentBinding(context.Background(), agentbinding.Binding{Handle: agentbinding.HandleOrbit})
	var receiptErr *appserver.CommandReceiptError
	if !errors.Is(err, commandErr) || !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("BindAgentBinding(unknown) = %#v, %v", receiptErr, err)
	}
	if agents.statusCalls != 0 || len(statusClient.requests) != 1 {
		t.Fatalf("unknown binding observed status: Agent=%d Host=%d", agents.statusCalls, len(statusClient.requests))
	}

	observationErr := errors.New("binding status unavailable")
	statusClient = &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{
		Configuration: controlstatus.StatusConfiguration{Revision: 53},
	}}}
	agents = &agentBindingClientProbe{
		result:    appserver.CommandResult{Outcome: appserver.OutcomeCommitted},
		statusErr: observationErr,
	}
	adapter.statusClient, adapter.agentClient = statusClient, agents
	_, err = adapter.ResetAgentBinding(context.Background(), agentbinding.HandleOrbit)
	receiptErr = nil
	if !errors.Is(err, observationErr) || !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeCommitted ||
		!strings.Contains(err.Error(), "do not retry blindly") {
		t.Fatalf("ResetAgentBinding(observation failure) = %#v, %v", receiptErr, err)
	}
}

func TestSessionClientAdapterDisconnectUsesRevisionBoundCandidateAndReceipt(t *testing.T) {
	agents := &disconnectAgentClientProbe{
		snapshot: appserver.DisconnectCandidatesSnapshot{
			Revision: 61,
			Candidates: []controlagents.DisconnectCandidate{{
				AgentID: "codex", Name: "Codex", ConnectionID: "codex-connection", LastOnConnection: true,
			}},
		},
		result: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 62},
	}
	adapter := &SessionClientAdapter{agentClient: agents, surface: "tui", sessionID: "session-1"}
	got, err := adapter.DisconnectACP(context.Background(), "codex")
	if err != nil || got.Agent.ID != "codex" || got.ConnectionID != "codex-connection" || !got.ConnectionRemoved {
		t.Fatalf("DisconnectACP() = %#v, %v", got, err)
	}
	request := agents.request
	if agents.candidateCalls != 1 || agents.disconnectCalls != 1 || request.SessionID != "" || request.ExpectedControllerEpoch != "" ||
		request.ExpectedRevision == nil || *request.ExpectedRevision != 61 || !strings.HasPrefix(request.OperationID, "agent-acp-disconnect-") {
		t.Fatalf("DisconnectACP() request/calls = %#v/%d/%d", request, agents.candidateCalls, agents.disconnectCalls)
	}

	wantErr := errors.New("disconnect outcome unknown")
	agents.result = appserver.CommandResult{Outcome: appserver.OutcomeUnknown, Revision: 0}
	agents.err = wantErr
	_, err = adapter.DisconnectACP(context.Background(), "codex")
	var receiptErr *appserver.CommandReceiptError
	if !errors.Is(err, wantErr) || !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeUnknown {
		t.Fatalf("DisconnectACP(unknown) = %#v, %v", receiptErr, err)
	}

	agents.result = appserver.CommandResult{
		OperationID: "agent-acp-disconnect-warning", Outcome: appserver.OutcomeCommitted, Revision: 62,
		Detail: "Agent assembly refresh failed",
	}
	agents.err = nil
	got, err = adapter.DisconnectACP(context.Background(), "codex")
	receiptErr = nil
	if got.Agent.ID != "codex" || !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeCommitted ||
		!strings.Contains(err.Error(), "do not retry blindly") || !strings.Contains(err.Error(), "assembly refresh failed") {
		t.Fatalf("DisconnectACP(committed warning) = %#v, %#v, %v", got, receiptErr, err)
	}
}

func TestSessionClientAdapterACPOnboardingUsesPreparationReceipts(t *testing.T) {
	preparation := controlagents.ACPPreparation{
		Ref: "prep-1", State: controlagents.PreparationStateReady,
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
		},
		Connection:    controlagents.Connection{ID: "custom", Name: "Custom"},
		Discovery:     controlagents.DiscoverySnapshot{SelectedModelID: controlagents.DefaultRemoteModelID},
		ContentDigest: strings.Repeat("a", 64), ExpiresAt: time.Now().Add(time.Minute),
	}
	agents := &acpOnboardingAgentClientProbe{preparation: preparation}
	status := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
		{Configuration: controlstatus.StatusConfiguration{Revision: 71}},
		{Configuration: controlstatus.StatusConfiguration{Revision: 71}},
	}}
	adapter := &SessionClientAdapter{
		agentClient: agents, statusClient: status, surface: "tui",
		acpPreparations: map[string]controlagents.ACPPreparation{},
	}

	result, err := adapter.ConnectACP(context.Background(), controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
		CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
	})
	if err != nil || len(result.Profiles) != 1 || result.Profiles[0].ID != "acp:custom:default" {
		t.Fatalf("ConnectACP() = %#v, %v", result, err)
	}
	if agents.prepareCalls != 1 || agents.observeCalls != 1 || agents.connectCalls != 1 {
		t.Fatalf("ACP client calls prepare=%d observe=%d connect=%d", agents.prepareCalls, agents.observeCalls, agents.connectCalls)
	}
	if agents.prepareRequest.SessionID != "" || agents.prepareRequest.ExpectedControllerEpoch != "" ||
		agents.prepareRequest.ExpectedRevision == nil || *agents.prepareRequest.ExpectedRevision != 71 ||
		!strings.HasPrefix(agents.prepareRequest.OperationID, "agent-acp-prepare-") {
		t.Fatalf("PrepareACP request = %#v", agents.prepareRequest)
	}
	if agents.connectRequest.PreparationRef != preparation.Ref || agents.connectRequest.PreparationDigest != preparation.ContentDigest ||
		agents.connectRequest.ExpectedRevision == nil || *agents.connectRequest.ExpectedRevision != 71 ||
		!strings.HasPrefix(agents.connectRequest.OperationID, "agent-acp-connect-") {
		t.Fatalf("ConnectACP request = %#v", agents.connectRequest)
	}
}

func TestSessionClientAdapterACPPreparationWarningPreservesReceiptAndCachedEvidence(t *testing.T) {
	preparation := controlagents.ACPPreparation{
		Ref: "prep-warning", State: controlagents.PreparationStateReady,
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
		},
		Connection: controlagents.Connection{ID: "custom", Name: "Custom"},
		Discovery: controlagents.DiscoverySnapshot{
			ConnectionID: "custom", SelectedModelID: controlagents.DefaultRemoteModelID,
		},
		ContentDigest: strings.Repeat("b", 64), ExpiresAt: time.Now().Add(time.Minute),
	}
	agents := &acpOnboardingAgentClientProbe{
		preparation:   preparation,
		prepareDetail: "preparation directory sync failed after commit",
	}
	status := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
		{Configuration: controlstatus.StatusConfiguration{Revision: 81}},
		{Configuration: controlstatus.StatusConfiguration{Revision: 81}},
	}}
	adapter := &SessionClientAdapter{
		agentClient: agents, statusClient: status, surface: "tui",
		acpPreparations: map[string]controlagents.ACPPreparation{},
	}
	request := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
		CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
	}
	discovery, err := adapter.DiscoverACPConnection(context.Background(), request)
	var receiptErr *appserver.CommandReceiptError
	if discovery.ConnectionID != "custom" || !errors.As(err, &receiptErr) ||
		receiptErr.Receipt.Outcome != appserver.OutcomeCommitted ||
		!strings.Contains(err.Error(), "do not retry blindly") || !strings.Contains(err.Error(), "directory sync failed") {
		t.Fatalf("DiscoverACPConnection(warning) = %#v, %#v, %v", discovery, receiptErr, err)
	}
	if agents.prepareCalls != 1 || agents.observeCalls != 1 || agents.connectCalls != 0 {
		t.Fatalf("warning calls prepare=%d observe=%d connect=%d", agents.prepareCalls, agents.observeCalls, agents.connectCalls)
	}

	agents.prepareDetail = ""
	connected, err := adapter.ConnectACP(context.Background(), request)
	if err != nil || len(connected.Profiles) != 1 {
		t.Fatalf("ConnectACP(cached preparation) = %#v, %v", connected, err)
	}
	if agents.prepareCalls != 1 || agents.observeCalls != 1 || agents.connectCalls != 1 {
		t.Fatalf("cached calls prepare=%d observe=%d connect=%d", agents.prepareCalls, agents.observeCalls, agents.connectCalls)
	}
}

func TestSessionClientAdapterACPPrepareObservationRetryDoesNotRepeatEffect(t *testing.T) {
	preparation := controlagents.ACPPreparation{
		Ref: "prep-pending", State: controlagents.PreparationStateReady,
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
		},
		Connection:    controlagents.Connection{ID: "custom", Name: "Custom"},
		Discovery:     controlagents.DiscoverySnapshot{ConnectionID: "custom", SelectedModelID: controlagents.DefaultRemoteModelID},
		ContentDigest: strings.Repeat("1", 64), ExpiresAt: time.Now().Add(time.Minute),
	}
	agents := &acpOnboardingAgentClientProbe{
		preparation:   preparation,
		observeErrors: []error{context.Canceled, nil},
	}
	status := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{
		Configuration: controlstatus.StatusConfiguration{Revision: 111},
	}}}
	adapter := &SessionClientAdapter{agentClient: agents, statusClient: status, surface: "tui"}
	request := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
		CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
	}

	if _, err := adapter.DiscoverACPConnection(context.Background(), request); !errors.Is(err, context.Canceled) {
		t.Fatalf("DiscoverACPConnection(first) error = %v, want canceled observation", err)
	}
	if agents.prepareCalls != 1 || agents.observeCalls != 1 || len(adapter.acpPending) != 1 {
		t.Fatalf("first calls/pending = prepare %d observe %d pending %d", agents.prepareCalls, agents.observeCalls, len(adapter.acpPending))
	}
	discovery, err := adapter.DiscoverACPConnection(context.Background(), request)
	if err != nil || discovery.ConnectionID != "custom" {
		t.Fatalf("DiscoverACPConnection(retry) = %#v, %v", discovery, err)
	}
	if agents.prepareCalls != 1 || agents.observeCalls != 2 || len(adapter.acpPending) != 0 {
		t.Fatalf("retry calls/pending = prepare %d observe %d pending %d", agents.prepareCalls, agents.observeCalls, len(adapter.acpPending))
	}
}

func TestSessionClientAdapterACPPendingObservationDigestMismatchFailsClosed(t *testing.T) {
	preparation := controlagents.ACPPreparation{
		Ref: "prep-digest-pending", State: controlagents.PreparationStateReady,
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
		},
		Connection:    controlagents.Connection{ID: "custom", Name: "Custom"},
		Discovery:     controlagents.DiscoverySnapshot{ConnectionID: "custom", SelectedModelID: controlagents.DefaultRemoteModelID},
		ContentDigest: strings.Repeat("4", 64), ExpiresAt: time.Now().Add(time.Minute),
	}
	agents := &acpOnboardingAgentClientProbe{
		preparation:   preparation,
		observeErrors: []error{errors.New("temporary transport failure")},
	}
	status := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{
		Configuration: controlstatus.StatusConfiguration{Revision: 112},
	}}}
	adapter := &SessionClientAdapter{agentClient: agents, statusClient: status, surface: "tui"}
	request := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
		CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
	}

	if _, err := adapter.DiscoverACPConnection(context.Background(), request); err == nil {
		t.Fatal("initial failed observation returned success")
	}
	agents.preparation.ContentDigest = strings.Repeat("5", 64)
	if _, err := adapter.DiscoverACPConnection(context.Background(), request); err == nil || !strings.Contains(err.Error(), "changed before observation") {
		t.Fatalf("digest mismatch error = %v", err)
	}
	if agents.prepareCalls != 1 || agents.observeCalls != 2 || len(adapter.acpPending) != 1 {
		t.Fatalf("mismatch calls/pending = prepare %d observe %d pending %d", agents.prepareCalls, agents.observeCalls, len(adapter.acpPending))
	}
	agents.preparation.ContentDigest = strings.Repeat("4", 64)
	if _, err := adapter.DiscoverACPConnection(context.Background(), request); err != nil {
		t.Fatalf("matching pending observation error = %v", err)
	}
	if agents.prepareCalls != 1 || agents.observeCalls != 3 || len(adapter.acpPending) != 0 {
		t.Fatalf("recovered calls/pending = prepare %d observe %d pending %d", agents.prepareCalls, agents.observeCalls, len(adapter.acpPending))
	}
}

func TestSessionClientAdapterACPPendingObservationRetriesThroughSameHTTPClient(t *testing.T) {
	preparation := controlagents.ACPPreparation{
		Ref: "prep-http-pending", State: controlagents.PreparationStateReady,
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
		},
		Connection:    controlagents.Connection{ID: "custom", Name: "Custom"},
		Discovery:     controlagents.DiscoverySnapshot{ConnectionID: "custom", SelectedModelID: controlagents.DefaultRemoteModelID},
		ContentDigest: strings.Repeat("6", 64), ExpiresAt: time.Now().Add(time.Minute),
	}
	prepareCalls := 0
	observeCalls := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == wirev1.APIPrefix+"/status":
			writeControlAdapterWireJSON(t, w, http.StatusOK, controlstatus.StatusSnapshot{
				Configuration: controlstatus.StatusConfiguration{Revision: 131},
			})
		case r.Method == http.MethodPost && r.URL.Path == wirev1.APIPrefix+"/agents/prepare-acp":
			prepareCalls++
			writeControlAdapterWireJSON(t, w, http.StatusOK, appserver.CommandResult{
				OperationID: r.Header.Get("Idempotency-Key"), Outcome: appserver.OutcomeCommitted,
				Resource: &appserver.CommandResource{
					Kind: appserver.CommandResourceACPPreparation, Ref: preparation.Ref, Digest: preparation.ContentDigest,
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == wirev1.APIPrefix+"/agents/acp-preparations/"+preparation.Ref:
			observeCalls++
			if observeCalls == 1 {
				http.Error(w, "temporary observation failure", http.StatusServiceUnavailable)
				return
			}
			writeControlAdapterWireJSON(t, w, http.StatusOK, preparation)
		default:
			http.NotFound(w, r)
		}
	})
	transport := controlAdapterRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response.Result(), nil
	})
	remote, err := httpclient.New(httpclient.Config{
		BaseURL:       "https://control.test",
		BearerToken:   "test-token",
		HTTPClient:    &http.Client{Transport: transport},
		Compatibility: appserver.CurrentCompatibility(),
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &SessionClientAdapter{agentClient: remote, statusClient: remote, surface: "tui"}
	request := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
		CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
	}
	if _, err := adapter.DiscoverACPConnection(context.Background(), request); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("HTTP first observation error = %v", err)
	}
	discovery, err := adapter.DiscoverACPConnection(context.Background(), request)
	if err != nil || discovery.ConnectionID != "custom" {
		t.Fatalf("HTTP pending retry = %#v, %v", discovery, err)
	}
	if prepareCalls != 1 || observeCalls != 2 || len(adapter.acpPending) != 0 {
		t.Fatalf("HTTP calls/pending = prepare %d observe %d pending %d", prepareCalls, observeCalls, len(adapter.acpPending))
	}
}

func TestSessionClientAdapterACPOnboardingCompletesExplicitAuthenticationChallenge(t *testing.T) {
	now := time.Now()
	challenge := controlagents.ACPPreparation{
		Ref: "prep-challenge", State: controlagents.PreparationStateNeedsAuth,
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
			CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
		},
		Connection: controlagents.Connection{ID: "custom", Name: "Custom"},
		AuthenticationMethods: []controlagents.AuthenticationChallengeMethod{{
			ID: "agent-login", Name: "Agent login", Type: controlagents.AuthenticationAgent,
		}},
		ContentDigest: strings.Repeat("c", 64), ExpiresAt: now.Add(time.Minute),
	}
	ready := challenge
	ready.Ref = "prep-ready"
	ready.ParentRef = challenge.Ref
	ready.State = controlagents.PreparationStateReady
	ready.AuthenticationMethods = append([]controlagents.AuthenticationChallengeMethod(nil), challenge.AuthenticationMethods...)
	ready.SelectedAuthentication = controlagents.Authentication{MethodID: "agent-login", Type: controlagents.AuthenticationAgent}
	ready.Connection.Authentication = ready.SelectedAuthentication
	ready.Discovery = controlagents.DiscoverySnapshot{ConnectionID: "custom", SelectedModelID: controlagents.DefaultRemoteModelID}
	ready.ContentDigest = strings.Repeat("d", 64)
	agents := &acpOnboardingAgentClientProbe{preparation: challenge, authenticatedPreparation: ready}
	status := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
		{Configuration: controlstatus.StatusConfiguration{Revision: 91}},
		{Configuration: controlstatus.StatusConfiguration{Revision: 91}},
		{Configuration: controlstatus.StatusConfiguration{Revision: 91}},
	}}
	adapter := &SessionClientAdapter{
		agentClient: agents, statusClient: status, surface: "tui",
		acpPreparations: map[string]controlagents.ACPPreparation{},
	}
	result, err := adapter.ConnectACP(context.Background(), controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
		CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
	})
	if err != nil || len(result.Profiles) != 1 {
		t.Fatalf("ConnectACP(needs_auth) = %#v, %v", result, err)
	}
	if agents.prepareCalls != 1 || agents.authCalls != 1 || agents.observeCalls != 2 || agents.connectCalls != 1 {
		t.Fatalf("challenge calls prepare=%d auth=%d observe=%d connect=%d", agents.prepareCalls, agents.authCalls, agents.observeCalls, agents.connectCalls)
	}
	if agents.authRequest.PreparationRef != challenge.Ref || agents.authRequest.PreparationDigest != challenge.ContentDigest ||
		agents.authRequest.MethodID != "agent-login" || agents.authRequest.ExpectedRevision == nil || *agents.authRequest.ExpectedRevision != 91 {
		t.Fatalf("PrepareACPAuthentication request = %#v", agents.authRequest)
	}
	if agents.connectRequest.PreparationRef != ready.Ref || agents.connectRequest.PreparationDigest != ready.ContentDigest {
		t.Fatalf("ConnectACP request = %#v, want authenticated preparation", agents.connectRequest)
	}
}

func TestSessionClientAdapterACPAuthenticationWarningCachesReadyEvidence(t *testing.T) {
	request := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
		CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
	}
	challenge := controlagents.ACPPreparation{
		Ref: "prep-auth-warning", State: controlagents.PreparationStateNeedsAuth,
		Request: controlagents.ACPPrepareRequest{
			AdapterID: request.AdapterID, Launcher: request.Launcher, CommandLine: request.CommandLine,
			ModelID: request.ModelID, CWD: request.CWD,
		},
		Connection: controlagents.Connection{ID: "custom", Name: "Custom"},
		AuthenticationMethods: []controlagents.AuthenticationChallengeMethod{{
			ID: "agent-login", Name: "Agent login", Type: controlagents.AuthenticationAgent,
		}},
		ContentDigest: strings.Repeat("e", 64), ExpiresAt: time.Now().Add(time.Minute),
	}
	ready := challenge
	ready.Ref = "prep-auth-ready-warning"
	ready.ParentRef = challenge.Ref
	ready.State = controlagents.PreparationStateReady
	ready.SelectedAuthentication = controlagents.Authentication{MethodID: "agent-login", Type: controlagents.AuthenticationAgent}
	ready.Connection.Authentication = ready.SelectedAuthentication
	ready.Discovery = controlagents.DiscoverySnapshot{ConnectionID: "custom", SelectedModelID: controlagents.DefaultRemoteModelID}
	ready.ContentDigest = strings.Repeat("f", 64)
	agents := &acpOnboardingAgentClientProbe{
		preparation: challenge, authenticatedPreparation: ready,
		authDetail: "authenticated preparation directory sync failed after commit",
	}
	status := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
		{Configuration: controlstatus.StatusConfiguration{Revision: 101}},
		{Configuration: controlstatus.StatusConfiguration{Revision: 101}},
	}}
	adapter := &SessionClientAdapter{
		agentClient: agents, statusClient: status, surface: "tui",
		acpPreparations: map[string]controlagents.ACPPreparation{acpPreparationCacheKey(request): challenge},
	}
	discovery, err := adapter.DiscoverACPConnection(context.Background(), request)
	var receiptErr *appserver.CommandReceiptError
	if discovery.ConnectionID != "custom" || !errors.As(err, &receiptErr) ||
		receiptErr.Receipt.Outcome != appserver.OutcomeCommitted || !strings.Contains(err.Error(), "do not retry blindly") {
		t.Fatalf("DiscoverACPConnection(auth warning) = %#v, %#v, %v", discovery, receiptErr, err)
	}
	if agents.prepareCalls != 0 || agents.authCalls != 1 || agents.observeCalls != 1 || agents.connectCalls != 0 {
		t.Fatalf("auth warning calls prepare=%d auth=%d observe=%d connect=%d", agents.prepareCalls, agents.authCalls, agents.observeCalls, agents.connectCalls)
	}

	agents.authDetail = ""
	connected, err := adapter.ConnectACP(context.Background(), request)
	if err != nil || len(connected.Profiles) != 1 {
		t.Fatalf("ConnectACP(cached authenticated preparation) = %#v, %v", connected, err)
	}
	if agents.prepareCalls != 0 || agents.authCalls != 1 || agents.observeCalls != 1 || agents.connectCalls != 1 {
		t.Fatalf("cached auth calls prepare=%d auth=%d observe=%d connect=%d", agents.prepareCalls, agents.authCalls, agents.observeCalls, agents.connectCalls)
	}
}

func TestSessionClientAdapterACPAuthenticationObservationRetryDoesNotRepeatEffect(t *testing.T) {
	request := controlagents.ConnectRequest{
		AdapterID: "custom", Launcher: controlagents.LauncherChoiceCommand,
		CommandLine: "/tmp/custom-acp", ModelID: controlagents.DefaultRemoteModelID, CWD: "/workspace",
	}
	challenge := controlagents.ACPPreparation{
		Ref: "prep-auth-parent", State: controlagents.PreparationStateNeedsAuth,
		Request: controlagents.ACPPrepareRequest{
			AdapterID: request.AdapterID, Launcher: request.Launcher, CommandLine: request.CommandLine,
			ModelID: request.ModelID, CWD: request.CWD,
		},
		Connection: controlagents.Connection{ID: "custom", Name: "Custom"},
		AuthenticationMethods: []controlagents.AuthenticationChallengeMethod{{
			ID: "agent-login", Name: "Agent login", Type: controlagents.AuthenticationAgent,
		}},
		ContentDigest: strings.Repeat("2", 64), ExpiresAt: time.Now().Add(time.Minute),
	}
	ready := challenge
	ready.Ref = "prep-auth-pending"
	ready.ParentRef = challenge.Ref
	ready.State = controlagents.PreparationStateReady
	ready.SelectedAuthentication = controlagents.Authentication{MethodID: "agent-login", Type: controlagents.AuthenticationAgent}
	ready.Connection.Authentication = ready.SelectedAuthentication
	ready.Discovery = controlagents.DiscoverySnapshot{ConnectionID: "custom", SelectedModelID: controlagents.DefaultRemoteModelID}
	ready.ContentDigest = strings.Repeat("3", 64)
	agents := &acpOnboardingAgentClientProbe{
		preparation: challenge, authenticatedPreparation: ready,
		observeErrors: []error{nil, errors.New("temporary HTTP observation failure"), nil},
	}
	status := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
		{Configuration: controlstatus.StatusConfiguration{Revision: 121}},
		{Configuration: controlstatus.StatusConfiguration{Revision: 121}},
	}}
	adapter := &SessionClientAdapter{agentClient: agents, statusClient: status, surface: "tui"}

	if _, err := adapter.DiscoverACPConnection(context.Background(), request); err == nil || !strings.Contains(err.Error(), "temporary HTTP observation failure") {
		t.Fatalf("DiscoverACPConnection(first) error = %v", err)
	}
	if agents.prepareCalls != 1 || agents.authCalls != 1 || agents.observeCalls != 2 || len(adapter.acpPending) != 1 {
		t.Fatalf("first calls/pending = prepare %d auth %d observe %d pending %d", agents.prepareCalls, agents.authCalls, agents.observeCalls, len(adapter.acpPending))
	}
	discovery, err := adapter.DiscoverACPConnection(context.Background(), request)
	if err != nil || discovery.ConnectionID != "custom" {
		t.Fatalf("DiscoverACPConnection(retry) = %#v, %v", discovery, err)
	}
	if agents.prepareCalls != 1 || agents.authCalls != 1 || agents.observeCalls != 3 || len(adapter.acpPending) != 0 {
		t.Fatalf("retry calls/pending = prepare %d auth %d observe %d pending %d", agents.prepareCalls, agents.authCalls, agents.observeCalls, len(adapter.acpPending))
	}
}

func TestSessionClientAdapterConnectUsesSeparateHostAndSessionMutations(t *testing.T) {
	statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
		{},
		{Configuration: controlstatus.StatusConfiguration{Revision: 1}, ModelStatus: controlstatus.StatusModel{Alias: "ollama/first"}},
		{Session: controlstatus.StatusSession{ID: "session-1"}, ModelStatus: controlstatus.StatusModel{Alias: "ollama/first"}},
	}}
	configuration := &modelConfigurationClientProbe{
		connectResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 1},
	}
	adapter := &SessionClientAdapter{
		sessionClient: &sessionClientAdapterTestClient{state: appserver.SessionState{
			SessionID: "session-1", Revision: 7, Controller: session.ControllerBinding{EpochID: "epoch-1"},
		}},
		statusClient: statusClient, configClient: configuration, surface: "tui", sessionID: "session-1",
	}
	status, err := adapter.Connect(context.Background(), controlprompt.ConnectConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if status.Session.ID != "session-1" || configuration.connectCalls != 1 || configuration.sessionCalls != 1 {
		t.Fatalf("Connect() status/calls = %#v/%d/%d", status, configuration.connectCalls, configuration.sessionCalls)
	}
	request := configuration.sessionRequest
	if request.SessionID != "session-1" || request.ExpectedRevision == nil || *request.ExpectedRevision != 7 ||
		request.ExpectedControllerEpoch != "epoch-1" || request.Model != "ollama/first" ||
		!strings.HasPrefix(request.OperationID, "session-model-") {
		t.Fatalf("Session selection request = %#v", request)
	}
}

func TestSessionClientAdapterConnectDoesNotReplaceExistingSessionSelection(t *testing.T) {
	statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
		{ModelStatus: controlstatus.StatusModel{Alias: "ollama/existing"}},
		{ModelStatus: controlstatus.StatusModel{Alias: "ollama/existing"}},
		{Session: controlstatus.StatusSession{ID: "session-1"}, ModelStatus: controlstatus.StatusModel{Alias: "ollama/existing"}},
	}}
	configuration := &modelConfigurationClientProbe{
		connectResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 1},
	}
	adapter := &SessionClientAdapter{
		statusClient: statusClient, configClient: configuration, surface: "tui", sessionID: "session-1",
	}
	if _, err := adapter.Connect(context.Background(), controlprompt.ConnectConfig{}); err != nil {
		t.Fatal(err)
	}
	if configuration.connectCalls != 1 || configuration.sessionCalls != 0 {
		t.Fatalf("Connect() calls = host %d, Session %d; want 1/0", configuration.connectCalls, configuration.sessionCalls)
	}
}

func TestSessionClientAdapterConnectPreservesHostCommitWhenSessionSelectionFails(t *testing.T) {
	connectedStatus := controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: 1},
		ModelStatus:   controlstatus.StatusModel{Alias: "ollama/first"},
	}
	statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{}, connectedStatus}}
	selectionErr := errors.New("Session selection conflicted")
	configuration := &modelConfigurationClientProbe{
		connectResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 1},
		sessionResult: appserver.CommandResult{Outcome: appserver.OutcomeConflicted, Revision: 8},
		sessionErr:    selectionErr,
	}
	adapter := &SessionClientAdapter{
		sessionClient: &sessionClientAdapterTestClient{state: appserver.SessionState{
			SessionID: "session-1", Revision: 7, Controller: session.ControllerBinding{EpochID: "epoch-1"},
		}},
		statusClient: statusClient, configClient: configuration, surface: "tui", sessionID: "session-1",
	}
	status, err := adapter.Connect(context.Background(), controlprompt.ConnectConfig{})
	var receiptErr *appserver.CommandReceiptError
	if !reflect.DeepEqual(status, connectedStatus) || !errors.Is(err, selectionErr) || !errors.As(err, &receiptErr) ||
		receiptErr.Receipt.Outcome != appserver.OutcomeConflicted ||
		!strings.Contains(err.Error(), "Host model connection committed") || !strings.Contains(err.Error(), "do not retry the connection blindly") {
		t.Fatalf("Connect(Session selection failure) = %#v, %#v, %v", status, receiptErr, err)
	}
	if configuration.connectCalls != 1 || configuration.sessionCalls != 1 || len(statusClient.requests) != 2 {
		t.Fatalf("Connect(Session selection failure) calls = host %d Session %d status %d", configuration.connectCalls, configuration.sessionCalls, len(statusClient.requests))
	}
}

func TestSessionClientAdapterConnectDoesNotInferSelectionFromAdvancedHostStatus(t *testing.T) {
	connectedStatus := controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: 2},
		ModelStatus:   controlstatus.StatusModel{Alias: "ollama/external-writer-default"},
	}
	statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{}, connectedStatus}}
	configuration := &modelConfigurationClientProbe{
		connectResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 1},
	}
	adapter := &SessionClientAdapter{
		sessionClient: &sessionClientAdapterTestClient{state: appserver.SessionState{
			SessionID: "session-1", Revision: 7, Controller: session.ControllerBinding{EpochID: "epoch-1"},
		}},
		statusClient: statusClient, configClient: configuration, surface: "tui", sessionID: "session-1",
	}
	status, err := adapter.Connect(context.Background(), controlprompt.ConnectConfig{Provider: "ollama", Model: "first"})
	var receiptErr *appserver.CommandReceiptError
	if !reflect.DeepEqual(status, connectedStatus) || !errors.As(err, &receiptErr) ||
		receiptErr.Receipt.Outcome != appserver.OutcomeCommitted ||
		!strings.Contains(err.Error(), "status advanced") || !strings.Contains(err.Error(), "selection was not attempted") {
		t.Fatalf("Connect(advanced observation) = %#v, %#v, %v", status, receiptErr, err)
	}
	if configuration.connectCalls != 1 || configuration.sessionCalls != 0 {
		t.Fatalf("Connect(advanced observation) calls = host %d Session %d; want 1/0", configuration.connectCalls, configuration.sessionCalls)
	}
}

func TestSessionClientAdapterConnectDoesNotRetargetSelectionAfterSessionChange(t *testing.T) {
	connectedStatus := controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: 1},
		ModelStatus:   controlstatus.StatusModel{Alias: "ollama/first"},
	}
	statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{}, connectedStatus}}
	adapter := &SessionClientAdapter{
		sessionClient: &sessionClientAdapterTestClient{state: appserver.SessionState{
			SessionID: "session-2", Revision: 11, Controller: session.ControllerBinding{EpochID: "epoch-2"},
		}},
		statusClient: statusClient, surface: "tui", sessionID: "session-1",
	}
	configuration := &modelConfigurationClientProbe{
		connectResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 1},
		connectHook: func() {
			adapter.sessionChangeMu.Lock()
			adapter.setClientSession("session-2", "")
			adapter.sessionChangeMu.Unlock()
		},
	}
	adapter.configClient = configuration

	status, err := adapter.Connect(context.Background(), controlprompt.ConnectConfig{Provider: "ollama", Model: "first"})
	var receiptErr *appserver.CommandReceiptError
	if !reflect.DeepEqual(status, connectedStatus) || !errors.As(err, &receiptErr) ||
		receiptErr.Receipt.Outcome != appserver.OutcomeCommitted ||
		!strings.Contains(err.Error(), `selected Session changed from "session-1" to "session-2"`) ||
		!strings.Contains(err.Error(), "do not retry the connection blindly") {
		t.Fatalf("Connect(changed Session) = %#v, %#v, %v", status, receiptErr, err)
	}
	if configuration.connectCalls != 1 || configuration.sessionCalls != 0 || configuration.hostCalls != 0 {
		t.Fatalf(
			"Connect(changed Session) calls = connect %d Session %d Host use %d; want 1/0/0",
			configuration.connectCalls,
			configuration.sessionCalls,
			configuration.hostCalls,
		)
	}
}

func TestSessionClientAdapterHostModelReceiptsAndObservation(t *testing.T) {
	t.Run("use without selected Session changes Host default", func(t *testing.T) {
		committedStatus := controlstatus.StatusSnapshot{
			Configuration: controlstatus.StatusConfiguration{Revision: 42},
			ModelStatus:   controlstatus.StatusModel{Alias: "xiaomi/mimo-v2.5-pro"},
		}
		statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
			{Configuration: controlstatus.StatusConfiguration{Revision: 41}},
			committedStatus,
		}}
		configuration := &modelConfigurationClientProbe{
			hostResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 42},
		}
		adapter := &SessionClientAdapter{statusClient: statusClient, configClient: configuration, surface: "tui"}

		got, err := adapter.UseModel(context.Background(), "xiaomi/mimo-v2.5-pro", "high")
		if err != nil || !reflect.DeepEqual(got, committedStatus) {
			t.Fatalf("UseModel(Host) = %#v, %v", got, err)
		}
		req := configuration.hostRequest
		if configuration.hostCalls != 1 || configuration.sessionCalls != 0 || req.SessionID != "" ||
			req.ExpectedControllerEpoch != "" || req.ExpectedRevision == nil || *req.ExpectedRevision != 41 ||
			req.Model != "xiaomi/mimo-v2.5-pro" || req.ReasoningEffort != "high" ||
			!strings.HasPrefix(req.OperationID, "model-use-") {
			t.Fatalf("Host use request/calls = %#v / %d / %d", req, configuration.hostCalls, configuration.sessionCalls)
		}
		if len(statusClient.requests) != 2 || statusClient.requests[0].SessionID != "" ||
			statusClient.requests[1].SessionID != "" || !statusClient.requests[1].IncludeDiagnostics {
			t.Fatalf("Host use status requests = %#v", statusClient.requests)
		}
	})

	t.Run("use with selected Session changes only Session", func(t *testing.T) {
		committedStatus := controlstatus.StatusSnapshot{
			Session:     controlstatus.StatusSession{ID: "session-1"},
			ModelStatus: controlstatus.StatusModel{Alias: "xiaomi/mimo-v2.5-pro"},
		}
		statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{committedStatus}}
		configuration := &modelConfigurationClientProbe{}
		adapter := &SessionClientAdapter{
			sessionClient: &sessionClientAdapterTestClient{state: appserver.SessionState{
				SessionID: "session-1", Revision: 7, Controller: session.ControllerBinding{EpochID: "epoch-1"},
			}},
			statusClient: statusClient, configClient: configuration, surface: "tui", sessionID: "session-1",
		}

		got, err := adapter.UseModel(context.Background(), "xiaomi/mimo-v2.5-pro", "high")
		if err != nil || !reflect.DeepEqual(got, committedStatus) {
			t.Fatalf("UseModel(Session) = %#v, %v", got, err)
		}
		req := configuration.sessionRequest
		if configuration.hostCalls != 0 || configuration.sessionCalls != 1 || req.SessionID != "session-1" ||
			req.ExpectedRevision == nil || *req.ExpectedRevision != 7 || req.ExpectedControllerEpoch != "epoch-1" ||
			req.Model != "xiaomi/mimo-v2.5-pro" || req.ReasoningEffort != "high" ||
			!strings.HasPrefix(req.OperationID, "session-model-") {
			t.Fatalf("Session use request/calls = %#v / %d / %d", req, configuration.hostCalls, configuration.sessionCalls)
		}
		if len(statusClient.requests) != 1 || statusClient.requests[0].SessionID != "session-1" {
			t.Fatalf("Session use status requests = %#v", statusClient.requests)
		}
	})

	t.Run("connect without selected Session", func(t *testing.T) {
		committedStatus := controlstatus.StatusSnapshot{
			Configuration: controlstatus.StatusConfiguration{Revision: 32},
			ModelStatus:   controlstatus.StatusModel{Alias: "ollama/first"},
		}
		statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
			{Configuration: controlstatus.StatusConfiguration{Revision: 31}},
			committedStatus,
		}}
		configuration := &modelConfigurationClientProbe{
			connectResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 32},
		}
		adapter := &SessionClientAdapter{statusClient: statusClient, configClient: configuration, surface: "tui"}
		got, err := adapter.Connect(context.Background(), controlprompt.ConnectConfig{Provider: "ollama", Model: "first"})
		if err != nil || !reflect.DeepEqual(got, committedStatus) {
			t.Fatalf("Connect() = %#v, %v", got, err)
		}
		req := configuration.connectRequest
		if configuration.connectCalls != 1 || configuration.sessionCalls != 0 || req.SessionID != "" ||
			req.ExpectedControllerEpoch != "" || req.ExpectedRevision == nil || *req.ExpectedRevision != 31 ||
			!strings.HasPrefix(req.OperationID, "model-connect-") {
			t.Fatalf("Host connect request/calls = %#v / %d / %d", req, configuration.connectCalls, configuration.sessionCalls)
		}
		if len(statusClient.requests) != 2 || statusClient.requests[0].SessionID != "" || statusClient.requests[1].SessionID != "" ||
			!statusClient.requests[1].IncludeDiagnostics {
			t.Fatalf("Host connect status requests = %#v", statusClient.requests)
		}
	})

	t.Run("unknown connect is not observed", func(t *testing.T) {
		commandErr := errors.New("connect outcome unknown")
		statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{{Configuration: controlstatus.StatusConfiguration{Revision: 8}}}}
		configuration := &modelConfigurationClientProbe{
			connectResult: appserver.CommandResult{Outcome: appserver.OutcomeUnknown}, connectErr: commandErr,
		}
		adapter := &SessionClientAdapter{statusClient: statusClient, configClient: configuration, surface: "tui"}
		_, err := adapter.Connect(context.Background(), controlprompt.ConnectConfig{Provider: "ollama", Model: "first"})
		var receiptErr *appserver.CommandReceiptError
		if !errors.Is(err, commandErr) || !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeUnknown {
			t.Fatalf("Connect(unknown) error = %#v, %v", receiptErr, err)
		}
		if len(statusClient.requests) != 1 {
			t.Fatalf("unknown connect performed %d status reads, want precondition only", len(statusClient.requests))
		}
	})

	t.Run("committed connect observation failure keeps receipt", func(t *testing.T) {
		observationErr := errors.New("Host status unavailable")
		statusClient := &sandboxStatusClientProbe{
			statuses: []controlstatus.StatusSnapshot{{Configuration: controlstatus.StatusConfiguration{Revision: 8}}, {}},
			errors:   []error{nil, observationErr},
		}
		configuration := &modelConfigurationClientProbe{
			connectResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 9},
		}
		adapter := &SessionClientAdapter{statusClient: statusClient, configClient: configuration, surface: "tui"}
		_, err := adapter.Connect(context.Background(), controlprompt.ConnectConfig{Provider: "ollama", Model: "first"})
		var receiptErr *appserver.CommandReceiptError
		if !errors.Is(err, observationErr) || !errors.As(err, &receiptErr) || receiptErr.Receipt.Outcome != appserver.OutcomeCommitted ||
			!strings.Contains(err.Error(), "do not retry blindly") {
			t.Fatalf("Connect(observation failure) error = %#v, %v", receiptErr, err)
		}
	})

	t.Run("delete carries Host revision", func(t *testing.T) {
		statusClient := &sandboxStatusClientProbe{statuses: []controlstatus.StatusSnapshot{
			{Configuration: controlstatus.StatusConfiguration{Revision: 45}},
			{Configuration: controlstatus.StatusConfiguration{Revision: 46}},
		}}
		configuration := &modelConfigurationClientProbe{
			deleteResult: appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: 46},
		}
		adapter := &SessionClientAdapter{statusClient: statusClient, configClient: configuration, surface: "tui", sessionID: "session-1"}
		if err := adapter.DeleteModel(context.Background(), "ollama/old"); err != nil {
			t.Fatal(err)
		}
		req := configuration.deleteRequest
		if configuration.deleteCalls != 1 || req.SessionID != "" || req.ExpectedControllerEpoch != "" ||
			req.ExpectedRevision == nil || *req.ExpectedRevision != 45 || !strings.HasPrefix(req.OperationID, "model-delete-") {
			t.Fatalf("Host delete request/calls = %#v / %d", req, configuration.deleteCalls)
		}
		if len(statusClient.requests) != 2 || statusClient.requests[1].SessionID != "" {
			t.Fatalf("Host delete status requests = %#v", statusClient.requests)
		}
	})
}

func collectSessionClientAdapterEvents(events <-chan eventstream.Envelope) []eventstream.Envelope {
	var out []eventstream.Envelope
	for envelope := range events {
		out = append(out, envelope)
	}
	return out
}

type sessionClientAdapterTestClient struct {
	target                 appserver.TurnTarget
	subscription           *sessionClientAdapterTestSubscription
	reconnectSubscriptions []*sessionClientAdapterTestSubscription
	state                  appserver.SessionState
	list                   session.SessionList
	createSessionID        string
	reconnectErr           error

	mu                  sync.Mutex
	prompt              appserver.PromptRequest
	steer               appserver.SteerRequest
	approval            appserver.ResolveApprovalRequest
	cancel              appserver.CancelRequest
	compact             appserver.CompactSessionRequest
	create              appserver.CreateSessionRequest
	promptStarted       chan struct{}
	promptRelease       chan struct{}
	promptIgnoreContext bool
	promptErr           error
	promptOutcome       appserver.Outcome
	omitPromptTarget    bool
	cancelErr           error
	cancelErrs          []error
	cancelOutcomes      []appserver.Outcome
	cancelIDs           []string
	cancelCalled        chan appserver.CancelRequest
	cancelStarted       chan struct{}
	cancelRelease       chan struct{}
}

type detachOnlyTargetTurn struct {
	target      appserver.TurnTarget
	events      chan eventstream.Envelope
	cancelCalls int
	closeCalls  int
}

func (*detachOnlyTargetTurn) SessionID() string                     { return "session-running" }
func (t *detachOnlyTargetTurn) Target() appserver.TurnTarget        { return t.target }
func (t *detachOnlyTargetTurn) Events() <-chan eventstream.Envelope { return t.events }
func (*detachOnlyTargetTurn) ResolveApproval(context.Context, appserver.ApprovalResolution) error {
	return nil
}
func (t *detachOnlyTargetTurn) Cancel(context.Context, string) error {
	t.cancelCalls++
	return nil
}
func (*detachOnlyTargetTurn) LastCursor() string { return "" }
func (*detachOnlyTargetTurn) Err() error         { return nil }
func (t *detachOnlyTargetTurn) Close() error {
	t.closeCalls++
	return nil
}

func (*sessionClientAdapterTestClient) Initialize(context.Context) (appserver.ServerInfo, error) {
	return appserver.ServerInfo{}, nil
}

func (c *sessionClientAdapterTestClient) ListSessions(context.Context, appserver.ListSessionsRequest) (session.SessionList, error) {
	return c.list, nil
}

func (c *sessionClientAdapterTestClient) CreateSession(_ context.Context, request appserver.CreateSessionRequest) (appserver.CommandResult, error) {
	c.create = request
	sessionID := strings.TrimSpace(c.createSessionID)
	if sessionID == "" {
		return appserver.CommandResult{}, errors.New("unexpected CreateSession")
	}
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, SessionID: sessionID}, nil
}

func (*sessionClientAdapterTestClient) CloseSession(context.Context, appserver.CloseSessionRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{}, errors.New("unexpected CloseSession")
}

func (c *sessionClientAdapterTestClient) CompactSession(_ context.Context, request appserver.CompactSessionRequest) (appserver.CommandResult, error) {
	c.mu.Lock()
	c.compact = request
	c.mu.Unlock()
	return appserver.CommandResult{
		OperationID: request.OperationID,
		Outcome:     appserver.OutcomeCommitted,
		SessionID:   request.SessionID,
	}, nil
}

func (c *sessionClientAdapterTestClient) InspectSession(_ context.Context, request appserver.StateRequest) (appserver.SessionState, error) {
	state := c.state
	if state.SessionID == "" {
		state.SessionID = strings.TrimSpace(request.SessionID)
	}
	if state.SessionID == "" {
		state.SessionID = "session-1"
	}
	state.BoundaryCursor = "boundary-0"
	return state, nil
}

func (c *sessionClientAdapterTestClient) Reconnect(_ context.Context, request appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	if c.reconnectErr != nil {
		return appserver.ReconnectResult{}, c.reconnectErr
	}
	state := c.state
	if state.SessionID == "" {
		state.SessionID = strings.TrimSpace(request.SessionID)
	}
	if state.Revision == 0 {
		state.Revision = 4
	}
	if state.Controller.EpochID == "" {
		state.Controller.EpochID = "epoch-1"
	}
	subscription := c.subscription
	c.mu.Lock()
	if len(c.reconnectSubscriptions) > 0 {
		subscription = c.reconnectSubscriptions[0]
		c.reconnectSubscriptions = c.reconnectSubscriptions[1:]
	}
	c.mu.Unlock()
	return appserver.ReconnectResult{
		State:        state,
		Subscription: subscription,
	}, nil
}

type sessionClientAdapterTestStatusClient struct{}

func (sessionClientAdapterTestStatusClient) SessionStatus(context.Context, appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	return controlstatus.StatusSnapshot{}, nil
}

type recordingStatusClient struct {
	request appserver.StatusRequest
}

func (c *recordingStatusClient) SessionStatus(_ context.Context, request appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	c.request = request
	return controlstatus.StatusSnapshot{}, nil
}

type sandboxStatusClientProbe struct {
	requests []appserver.StatusRequest
	statuses []controlstatus.StatusSnapshot
	errors   []error
}

func (c *sandboxStatusClientProbe) SessionStatus(_ context.Context, request appserver.StatusRequest) (controlstatus.StatusSnapshot, error) {
	index := len(c.requests)
	c.requests = append(c.requests, request)
	var status controlstatus.StatusSnapshot
	var err error
	if index < len(c.statuses) {
		status = c.statuses[index]
	}
	if index < len(c.errors) {
		err = c.errors[index]
	}
	return status, err
}

type sandboxConfigurationClientProbe struct {
	appserver.ConfigurationClient
	request appserver.SandboxRequest
	result  appserver.CommandResult
	err     error
	calls   int
}

type agentBindingClientProbe struct {
	appserver.AgentClient
	bindRequest  appserver.BindAgentBindingRequest
	resetRequest appserver.ResetAgentBindingRequest
	result       appserver.CommandResult
	err          error
	status       agentbinding.Status
	statusErr    error
	bindCalls    int
	statusCalls  int
}

func (c *agentBindingClientProbe) BindAgentBinding(_ context.Context, request appserver.BindAgentBindingRequest) (appserver.CommandResult, error) {
	c.bindCalls++
	c.bindRequest = request
	result := c.result
	result.OperationID = request.OperationID
	return result, c.err
}

func (c *agentBindingClientProbe) ResetAgentBinding(_ context.Context, request appserver.ResetAgentBindingRequest) (appserver.CommandResult, error) {
	c.resetRequest = request
	result := c.result
	result.OperationID = request.OperationID
	return result, c.err
}

func (c *agentBindingClientProbe) AgentBindingStatus(_ context.Context, _ appserver.AgentRequest) (agentbinding.Status, error) {
	c.statusCalls++
	return c.status, c.statusErr
}

type modelConfigurationClientProbe struct {
	appserver.ConfigurationClient
	hostResult     appserver.CommandResult
	hostErr        error
	hostRequest    appserver.UseModelRequest
	connectResult  appserver.CommandResult
	connectErr     error
	connectRequest appserver.ConnectModelRequest
	connectHook    func()
	deleteResult   appserver.CommandResult
	deleteErr      error
	deleteRequest  appserver.DeleteModelRequest
	sessionRequest appserver.SessionModelRequest
	sessionResult  appserver.CommandResult
	sessionErr     error
	connectCalls   int
	hostCalls      int
	deleteCalls    int
	sessionCalls   int
}

func (c *modelConfigurationClientProbe) UseModel(_ context.Context, request appserver.UseModelRequest) (appserver.CommandResult, error) {
	c.hostCalls++
	c.hostRequest = request
	result := c.hostResult
	result.OperationID = request.OperationID
	return result, c.hostErr
}

func (c *modelConfigurationClientProbe) ConnectModel(_ context.Context, request appserver.ConnectModelRequest) (appserver.CommandResult, error) {
	c.connectCalls++
	c.connectRequest = request
	if c.connectHook != nil {
		c.connectHook()
	}
	result := c.connectResult
	result.OperationID = request.OperationID
	return result, c.connectErr
}

func (c *modelConfigurationClientProbe) UseSessionModel(_ context.Context, request appserver.SessionModelRequest) (appserver.CommandResult, error) {
	c.sessionCalls++
	c.sessionRequest = request
	if c.sessionResult.Outcome.Valid() || c.sessionErr != nil {
		result := c.sessionResult
		result.OperationID = request.OperationID
		result.SessionID = request.SessionID
		return result, c.sessionErr
	}
	revision := uint64(0)
	if request.ExpectedRevision != nil {
		revision = *request.ExpectedRevision + 1
	}
	return appserver.CommandResult{
		OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted, Revision: revision,
	}, nil
}

func (c *modelConfigurationClientProbe) DeleteModel(_ context.Context, request appserver.DeleteModelRequest) (appserver.CommandResult, error) {
	c.deleteCalls++
	c.deleteRequest = request
	result := c.deleteResult
	result.OperationID = request.OperationID
	return result, c.deleteErr
}

func (c *sandboxConfigurationClientProbe) SetSandboxBackend(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	c.calls++
	c.request = request
	result := c.result
	result.OperationID = request.OperationID
	return result, c.err
}

type blockingInspectSessionClient struct {
	*sessionClientAdapterTestClient
	started chan appserver.StateRequest
	release chan struct{}
	once    sync.Once
}

func (c *blockingInspectSessionClient) InspectSession(ctx context.Context, request appserver.StateRequest) (appserver.SessionState, error) {
	c.once.Do(func() {
		c.started <- request
		select {
		case <-c.release:
		case <-ctx.Done():
		}
	})
	return c.sessionClientAdapterTestClient.InspectSession(ctx, request)
}

type sessionClientAdapterTestConfigurationClient struct{}

func (sessionClientAdapterTestConfigurationClient) ConfigureSessionMode(_ context.Context, request appserver.SessionModeRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (sessionClientAdapterTestConfigurationClient) UseSessionModel(_ context.Context, request appserver.SessionModelRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (sessionClientAdapterTestConfigurationClient) ConfigureSessionControllerMode(_ context.Context, request appserver.SessionControllerModeRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (sessionClientAdapterTestConfigurationClient) ConfigureSessionPresentationMode(_ context.Context, request appserver.SessionPresentationModeRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (sessionClientAdapterTestConfigurationClient) ConfigureSessionPresentation(_ context.Context, request appserver.SessionPresentationConfigRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, SessionID: request.SessionID, Outcome: appserver.OutcomeCommitted}, nil
}

func (sessionClientAdapterTestConfigurationClient) RefreshSandbox(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted}, nil
}

type sessionClientAdapterTestAgentClient struct {
	appserver.AgentClient
}

type disconnectAgentClientProbe struct {
	appserver.AgentClient
	snapshot        appserver.DisconnectCandidatesSnapshot
	request         appserver.DisconnectACPRequest
	result          appserver.CommandResult
	err             error
	candidateCalls  int
	disconnectCalls int
}

type acpOnboardingAgentClientProbe struct {
	appserver.AgentClient
	preparation              controlagents.ACPPreparation
	authenticatedPreparation controlagents.ACPPreparation
	prepareDetail            string
	authDetail               string
	prepareRequest           appserver.PrepareACPRequest
	authRequest              appserver.PrepareACPAuthenticationRequest
	connectRequest           appserver.ConnectACPRequest
	observeErrors            []error
	prepareCalls             int
	authCalls                int
	observeCalls             int
	connectCalls             int
}

func (c *acpOnboardingAgentClientProbe) PrepareACP(_ context.Context, request appserver.PrepareACPRequest) (appserver.CommandResult, error) {
	c.prepareCalls++
	c.prepareRequest = request
	return appserver.CommandResult{
		OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, Detail: c.prepareDetail,
		Resource: &appserver.CommandResource{
			Kind: appserver.CommandResourceACPPreparation, Ref: c.preparation.Ref, Digest: c.preparation.ContentDigest,
		},
	}, nil
}

func (c *acpOnboardingAgentClientProbe) ACPPreparation(_ context.Context, _ appserver.ACPPreparationRequest) (controlagents.ACPPreparation, error) {
	c.observeCalls++
	if len(c.observeErrors) > 0 {
		err := c.observeErrors[0]
		c.observeErrors = c.observeErrors[1:]
		if err != nil {
			return controlagents.ACPPreparation{}, err
		}
	}
	return c.preparation, nil
}

func (c *acpOnboardingAgentClientProbe) PrepareACPAuthentication(_ context.Context, request appserver.PrepareACPAuthenticationRequest) (appserver.CommandResult, error) {
	c.authCalls++
	c.authRequest = request
	if c.authenticatedPreparation.Ref != "" {
		c.preparation = c.authenticatedPreparation
	}
	return appserver.CommandResult{
		OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, Detail: c.authDetail,
		Resource: &appserver.CommandResource{
			Kind: appserver.CommandResourceACPPreparation, Ref: c.preparation.Ref, Digest: c.preparation.ContentDigest,
		},
	}, nil
}

func (c *acpOnboardingAgentClientProbe) ConnectACP(_ context.Context, request appserver.ConnectACPRequest) (appserver.CommandResult, error) {
	c.connectCalls++
	c.connectRequest = request
	return appserver.CommandResult{
		OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted, Revision: 72,
		Resource: &appserver.CommandResource{Kind: appserver.CommandResourceModelProfile, Ref: "acp:custom:default", Digest: request.PreparationDigest},
	}, nil
}

func writeControlAdapterWireJSON(t *testing.T, writer http.ResponseWriter, status int, value any) {
	t.Helper()
	payload, err := wirev1.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
}

type controlAdapterRoundTripFunc func(*http.Request) (*http.Response, error)

func (f controlAdapterRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func (c *disconnectAgentClientProbe) DisconnectCandidates(_ context.Context, _ appserver.AgentRequest) (appserver.DisconnectCandidatesSnapshot, error) {
	c.candidateCalls++
	return c.snapshot, nil
}

func (c *disconnectAgentClientProbe) DisconnectACP(_ context.Context, request appserver.DisconnectACPRequest) (appserver.CommandResult, error) {
	c.disconnectCalls++
	c.request = request
	result := c.result
	result.OperationID = request.OperationID
	return result, c.err
}

type sessionClientAdapterTestCompletionClient struct {
	appserver.CompletionClient
}

type sessionClientAdapterTestPluginClient struct {
	appserver.PluginClient
}

type recordingCompletionClient struct {
	appserver.CompletionClient
	skill   appserver.CompletionRequest
	slash   appserver.CompletionRequest
	resolve appserver.CompletionRequest
}

type fallbackSkillCompletionClient struct {
	appserver.CompletionClient
	sessionIDs []string
}

func (c *fallbackSkillCompletionClient) CompleteSkill(_ context.Context, request appserver.CompletionRequest) ([]appserver.CompletionCandidate, error) {
	c.sessionIDs = append(c.sessionIDs, request.SessionID)
	if request.SessionID != "" {
		return nil, appserver.ErrSessionClosed
	}
	return []appserver.CompletionCandidate{{Value: "review-code"}}, nil
}

func (c *fallbackSkillCompletionClient) ResolveSkill(_ context.Context, request appserver.CompletionRequest) (appserver.SkillResolveResult, error) {
	c.sessionIDs = append(c.sessionIDs, request.SessionID)
	if request.SessionID != "" {
		return appserver.SkillResolveResult{}, appserver.ErrSessionClosed
	}
	return appserver.SkillResolveResult{Canonical: request.Name}, nil
}

func (c *recordingCompletionClient) CompleteSkill(_ context.Context, request appserver.CompletionRequest) ([]appserver.CompletionCandidate, error) {
	c.skill = request
	return []appserver.CompletionCandidate{{Value: "review-code"}}, nil
}

func (c *recordingCompletionClient) CompleteSlashArg(_ context.Context, request appserver.CompletionRequest) ([]appserver.SlashArgCandidate, error) {
	c.slash = request
	return []appserver.SlashArgCandidate{{Value: "mimo"}}, nil
}

func (c *recordingCompletionClient) ResolveSkill(_ context.Context, request appserver.CompletionRequest) (appserver.SkillResolveResult, error) {
	c.resolve = request
	return appserver.SkillResolveResult{Canonical: request.Name}, nil
}

type recordingPluginClient struct {
	appserver.PluginClient
	enable appserver.EnablePluginRequest
}

func (c *recordingPluginClient) EnablePlugin(_ context.Context, request appserver.EnablePluginRequest) (appserver.CommandResult, error) {
	c.enable = request
	revision := uint64(0)
	if request.ExpectedRevision != nil {
		revision = *request.ExpectedRevision + 1
	}
	return appserver.CommandResult{
		OperationID: request.OperationID,
		Outcome:     appserver.OutcomeCommitted,
		Revision:    revision,
		Resource:    &appserver.CommandResource{Kind: appserver.CommandResourcePlugin, Ref: request.ID},
	}, nil
}

func (c *recordingPluginClient) InspectPlugin(_ context.Context, request appserver.PluginRequest) (appserver.PluginSnapshot, error) {
	return appserver.PluginSnapshot{ID: request.ID, Enabled: true}, nil
}

func (sessionClientAdapterTestConfigurationClient) ConnectModel(_ context.Context, request appserver.ConnectModelRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted}, nil
}
func (sessionClientAdapterTestConfigurationClient) UseModel(_ context.Context, request appserver.UseModelRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted}, nil
}
func (sessionClientAdapterTestConfigurationClient) DeleteModel(_ context.Context, request appserver.DeleteModelRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted}, nil
}
func (sessionClientAdapterTestConfigurationClient) SetSandboxBackend(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted}, nil
}
func (sessionClientAdapterTestConfigurationClient) PrepareSandbox(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted}, nil
}
func (sessionClientAdapterTestConfigurationClient) RepairSandbox(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted}, nil
}
func (sessionClientAdapterTestConfigurationClient) ResetSandbox(_ context.Context, request appserver.SandboxRequest) (appserver.CommandResult, error) {
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: appserver.OutcomeCommitted}, nil
}

func (c *sessionClientAdapterTestClient) Prompt(ctx context.Context, request appserver.PromptRequest) (appserver.CommandResult, error) {
	c.mu.Lock()
	c.prompt = request
	started := c.promptStarted
	release := c.promptRelease
	ignoreContext := c.promptIgnoreContext
	c.mu.Unlock()
	if started != nil {
		select {
		case started <- struct{}{}:
		default:
		}
	}
	if release != nil {
		if ignoreContext {
			<-release
		} else {
			select {
			case <-release:
			case <-ctx.Done():
				return appserver.CommandResult{}, ctx.Err()
			}
		}
	}
	c.mu.Lock()
	err := c.promptErr
	outcome := c.promptOutcome
	target := c.target
	if c.omitPromptTarget {
		target = appserver.TurnTarget{}
	}
	c.mu.Unlock()
	if outcome == "" {
		outcome = appserver.OutcomeCommitted
	}
	result := appserver.CommandResult{
		OperationID: request.OperationID,
		Outcome:     outcome,
		SessionID:   request.SessionID,
		Target:      target,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (c *sessionClientAdapterTestClient) Steer(_ context.Context, request appserver.SteerRequest) (appserver.CommandResult, error) {
	c.mu.Lock()
	c.steer = request
	c.mu.Unlock()
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted}, nil
}

func (c *sessionClientAdapterTestClient) Cancel(_ context.Context, request appserver.CancelRequest) (appserver.CommandResult, error) {
	c.mu.Lock()
	c.cancel = request
	cancelIDs := append(c.cancelIDs, request.OperationID)
	c.cancelIDs = cancelIDs
	call := len(cancelIDs) - 1
	err := c.cancelErr
	cancelCalled := c.cancelCalled
	cancelStarted := c.cancelStarted
	cancelRelease := c.cancelRelease
	var outcome appserver.Outcome
	if call < len(c.cancelOutcomes) {
		outcome = c.cancelOutcomes[call]
	}
	if call < len(c.cancelErrs) {
		err = c.cancelErrs[call]
	}
	c.mu.Unlock()
	if cancelStarted != nil {
		select {
		case cancelStarted <- struct{}{}:
		default:
		}
	}
	if cancelRelease != nil {
		<-cancelRelease
	}
	if cancelCalled != nil {
		select {
		case cancelCalled <- request:
		default:
		}
	}
	if err != nil {
		return appserver.CommandResult{OperationID: request.OperationID, Outcome: outcome}, err
	}
	if outcome == "" {
		outcome = appserver.OutcomeCommitted
	}
	return appserver.CommandResult{OperationID: request.OperationID, Outcome: outcome}, nil
}

func (c *sessionClientAdapterTestClient) ResolveApproval(_ context.Context, request appserver.ResolveApprovalRequest) (appserver.CommandResult, error) {
	c.mu.Lock()
	c.approval = request
	c.mu.Unlock()
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted}, nil
}

type sessionClientAdapterTestParticipantClient struct {
	target       appserver.TurnTarget
	start        appserver.StartParticipantRequest
	prompt       appserver.PromptParticipantRequest
	cancel       appserver.CancelParticipantRequest
	startStarted chan struct{}
	startRelease chan struct{}
}

func (*sessionClientAdapterTestParticipantClient) Handles(context.Context, string) ([]string, error) {
	return []string{"reviewer"}, nil
}

func (c *sessionClientAdapterTestParticipantClient) StartParticipant(ctx context.Context, request appserver.StartParticipantRequest) (appserver.CommandResult, error) {
	c.start = request
	if c.startStarted != nil {
		select {
		case c.startStarted <- struct{}{}:
		default:
		}
	}
	if c.startRelease != nil {
		select {
		case <-c.startRelease:
		case <-ctx.Done():
			return appserver.CommandResult{}, ctx.Err()
		}
	}
	return appserver.CommandResult{
		OperationID:   request.OperationID,
		Outcome:       appserver.OutcomeCommitted,
		SessionID:     request.SessionID,
		Target:        c.target,
		ParticipantID: "participant-1",
	}, nil
}

func (c *sessionClientAdapterTestParticipantClient) PromptParticipant(_ context.Context, request appserver.PromptParticipantRequest) (appserver.CommandResult, error) {
	c.prompt = request
	return appserver.CommandResult{
		OperationID: request.OperationID,
		Outcome:     appserver.OutcomeCommitted,
		SessionID:   request.SessionID,
		Target:      c.target,
	}, nil
}

func (c *sessionClientAdapterTestParticipantClient) CancelParticipant(_ context.Context, request appserver.CancelParticipantRequest) (appserver.CommandResult, error) {
	c.cancel = request
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted}, nil
}

type sessionClientAdapterTestSubscription struct {
	backfill     chan eventstream.Envelope
	backfillDone chan struct{}
	events       chan eventstream.Envelope
	closeOnce    sync.Once
	closed       bool
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
	s.closeOnce.Do(func() { s.closed = true })
	return nil
}

func (*sessionClientAdapterTestSubscription) Err() error         { return nil }
func (*sessionClientAdapterTestSubscription) LastCursor() string { return "" }

var _ appserver.SessionClient = (*sessionClientAdapterTestClient)(nil)
var _ appserver.ParticipantClient = (*sessionClientAdapterTestParticipantClient)(nil)
var _ appserver.FeedSubscription = (*sessionClientAdapterTestSubscription)(nil)
