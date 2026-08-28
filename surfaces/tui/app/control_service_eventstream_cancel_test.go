package tuiapp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func TestRunContextCancelKeepsLiveTurnObservationUntilTerminal(t *testing.T) {
	t.Parallel()

	sender := &ProgramSender{}
	runCtx, finish := sender.beginRunContext(context.Background())
	turn := &surfaceDetachTurn{events: make(chan eventstream.Envelope, 1)}
	messages := make(chan tea.Msg, 8)
	sender.Send = func(message tea.Msg) {
		messages <- message
	}
	result := make(chan executeLineResult, 1)
	go func() {
		result <- forwardTurnEventStream(runCtx, turn, sender)
	}()

	time.Sleep(20 * time.Millisecond)
	finish()
	select {
	case got := <-result:
		t.Fatalf("forward result = %#v, want live observation to survive executeLine run cancel", got)
	case <-time.After(30 * time.Millisecond):
	}

	terminal := eventstream.TurnCancelled(turn.HandleID(), turn.RunID(), turn.TurnID(), "tui interrupt", time.Now())
	turn.events <- terminal
	close(turn.events)

	select {
	case got := <-result:
		if !got.queued {
			t.Fatalf("forward result = %#v, want queued terminal delivery", got)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarder did not finish after cancelled lifecycle")
	}
	select {
	case message := <-messages:
		env, ok := message.(eventstream.Envelope)
		if !ok || !eventstream.IsTurnTerminalLifecycle(env) {
			t.Fatalf("message = %#v, want cancelled turn lifecycle", message)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled lifecycle was not forwarded to the TUI")
	}
	if calls := turn.cancelCalls.Load(); calls != 0 {
		t.Fatalf("Turn.Cancel calls = %d, want Interrupt to cancel the Host turn", calls)
	}
}

func TestCancelRunningKeepsFeedUntilHostTerminal(t *testing.T) {
	t.Parallel()

	turn := &surfaceDetachTurn{events: make(chan eventstream.Envelope, 1)}
	service := &interruptBridgeStub{
		turn:      turn,
		submitted: make(chan struct{}, 1),
	}
	sender := &ProgramSender{}
	messages := make(chan tea.Msg, 8)
	sender.Send = func(message tea.Msg) {
		messages <- message
	}
	cfg := ConfigFromControlService(service, sender, Config{
		Commands:            DefaultCommands(),
		PromptRouterFactory: controlprompt.New,
	})
	if cfg.CancelRunning == nil || cfg.executeLineCmd == nil {
		t.Fatal("ConfigFromControlService() missing CancelRunning or executeLineCmd")
	}

	done := make(chan tea.Msg, 1)
	go func() {
		done <- cfg.executeLineCmd(Submission{Text: "hello"})
	}()
	select {
	case <-service.submitted:
	case <-time.After(time.Second):
		t.Fatal("Submit() was not reached")
	}
	time.Sleep(20 * time.Millisecond)

	model := NewModel(cfg)
	model.liveTurn.Active = true
	model.runningHintTracker.beginTurn(time.Now())
	model.refreshRunningActivity()
	updated, cmd := model.requestRunningInterrupt()
	next := updated.(*Model)
	if cmd == nil {
		t.Fatal("requestRunningInterrupt() cmd = nil")
	}
	accepted := cfg.CancelRunning()
	service.mu.Lock()
	interruptCalls := service.interruptCalls
	service.mu.Unlock()
	if !accepted || interruptCalls != 1 {
		t.Fatalf("CancelRunning() accepted=%v Interrupt() calls=%d, want accepted Control interrupt", accepted, interruptCalls)
	}
	select {
	case got := <-done:
		t.Fatalf("executeLine returned %#v, want feed to stay attached after CancelRunning", got)
	case <-time.After(30 * time.Millisecond):
	}
	updated, _ = next.handleRunningInterruptResultMsg(RunningInterruptResultMsg{Accepted: accepted})
	next = updated.(*Model)
	if next.runningActivity.Phase != runningPhaseInterrupt {
		t.Fatalf("runningActivity = %#v, want Interrupting after accepted Esc", next.runningActivity)
	}

	terminal := eventstream.TurnCancelled(turn.HandleID(), turn.RunID(), turn.TurnID(), "tui interrupt", time.Now())
	turn.events <- terminal
	close(turn.events)

	select {
	case got := <-done:
		if got != nil {
			t.Fatalf("executeLine message = %#v, want queued nil completion", got)
		}
	case <-time.After(time.Second):
		t.Fatal("executeLine did not finish after cancelled lifecycle")
	}
	select {
	case message := <-messages:
		env, ok := message.(eventstream.Envelope)
		if !ok || !eventstream.IsTurnTerminalLifecycle(env) {
			t.Fatalf("message = %#v, want cancelled turn lifecycle", message)
		}
		updated, _ = next.handleACPEventEnvelope(env)
		next = updated.(*Model)
	case <-time.After(time.Second):
		t.Fatal("cancelled lifecycle was not forwarded to the TUI")
	}
	if next.turnRunning() {
		t.Fatal("live turn still active after cancelled lifecycle")
	}
	if next.runningInterruptRequested {
		t.Fatal("runningInterruptRequested still set after cancelled lifecycle")
	}
	if activity, _ := next.runningActivityText(); activity == "Interrupting" {
		t.Fatalf("running activity = %q after terminal, want interrupt overlay cleared", activity)
	}
}

func TestCancelRunningFailedInterruptClearsInterrupting(t *testing.T) {
	t.Parallel()

	turn := &surfaceDetachTurn{events: make(chan eventstream.Envelope)}
	service := &interruptBridgeStub{
		turn:         turn,
		submitted:    make(chan struct{}, 1),
		interruptErr: errors.New("cancel rejected"),
	}
	sender := &ProgramSender{}
	sender.Send = func(tea.Msg) {}
	cfg := ConfigFromControlService(service, sender, Config{
		Commands:            DefaultCommands(),
		PromptRouterFactory: controlprompt.New,
	})
	done := make(chan tea.Msg, 1)
	go func() {
		done <- cfg.executeLineCmd(Submission{Text: "hello"})
	}()
	select {
	case <-service.submitted:
	case <-time.After(time.Second):
		t.Fatal("Submit() was not reached")
	}
	time.Sleep(20 * time.Millisecond)

	model := NewModel(cfg)
	model.liveTurn.Active = true
	model.runningHintTracker.beginTurn(time.Now())
	model.refreshRunningActivity()
	updated, _ := model.requestRunningInterrupt()
	next := updated.(*Model)
	accepted := cfg.CancelRunning()
	if accepted {
		t.Fatal("CancelRunning() = true, want failed Control interrupt")
	}
	updated, _ = next.handleRunningInterruptResultMsg(RunningInterruptResultMsg{Accepted: accepted})
	next = updated.(*Model)
	if !next.turnRunning() {
		t.Fatal("live turn ended after failed interrupt")
	}
	if next.runningInterruptRequested {
		t.Fatal("runningInterruptRequested still set after failed interrupt")
	}
	if activity, _ := next.runningActivityText(); activity == "Interrupting" {
		t.Fatalf("running activity = %q, want Interrupting cleared after failed interrupt", activity)
	}
	select {
	case got := <-done:
		t.Fatalf("executeLine returned %#v, want feed to stay attached after failed interrupt", got)
	default:
	}
}

func TestCancelRunningAbortsPendingAdmissionWhenControlHasNoTarget(t *testing.T) {
	t.Parallel()

	service := &interruptBridgeStub{
		submitted:     make(chan struct{}, 1),
		interruptErr:  errors.New("no active turn target"),
		waitForCancel: true,
	}
	sender := &ProgramSender{}
	cfg := ConfigFromControlService(service, sender, Config{
		Commands:            DefaultCommands(),
		PromptRouterFactory: controlprompt.New,
	})
	done := make(chan tea.Msg, 1)
	go func() {
		done <- cfg.executeLineCmd(Submission{Text: "hello"})
	}()
	select {
	case <-service.submitted:
	case <-time.After(time.Second):
		t.Fatal("Submit() was not reached")
	}
	if accepted := cfg.CancelRunning(); !accepted {
		t.Fatal("CancelRunning() rejected local admission abort")
	}
	select {
	case message := <-done:
		result, ok := message.(TaskResultMsg)
		if !ok || !result.Interrupted || result.Err != nil {
			t.Fatalf("executeLine message = %#v, want interrupted completion", message)
		}
	case <-time.After(time.Second):
		t.Fatal("executeLine remained stuck after Esc")
	}
	service.mu.Lock()
	interruptCalls := service.interruptCalls
	service.mu.Unlock()
	if interruptCalls != 1 {
		t.Fatalf("Interrupt() calls = %d, want one Control attempt before local abort", interruptCalls)
	}
}

func TestForwardTurnEventStreamDetachesWithoutCancellingOnSurfaceShutdown(t *testing.T) {
	t.Parallel()

	turn := &surfaceDetachTurn{events: make(chan eventstream.Envelope)}
	ctx, cancel := context.WithCancel(context.Background())
	messages := make(chan tea.Msg, 4)
	result := make(chan executeLineResult, 1)
	go func() {
		result <- forwardTurnEventStream(ctx, turn, &ProgramSender{Send: func(message tea.Msg) {
			messages <- message
		}})
	}()

	cancel()
	select {
	case got := <-result:
		if !got.queued {
			t.Fatalf("forward result = %#v, want detached queued result", got)
		}
	case <-time.After(time.Second):
		t.Fatal("forwarder did not detach after surface shutdown")
	}
	select {
	case message := <-messages:
		t.Fatalf("surface shutdown synthesized message %#v", message)
	case <-time.After(30 * time.Millisecond):
	}
	if calls := turn.cancelCalls.Load(); calls != 0 {
		t.Fatalf("Turn.Cancel calls = %d, want zero for detach", calls)
	}
}

type surfaceDetachTurn struct {
	events      chan eventstream.Envelope
	cancelCalls atomic.Int32
}

func (*surfaceDetachTurn) HandleID() string { return "handle-detach" }
func (*surfaceDetachTurn) RunID() string    { return "run-detach" }
func (*surfaceDetachTurn) TurnID() string   { return "turn-detach" }

func (t *surfaceDetachTurn) Events() <-chan eventstream.Envelope { return t.events }

func (*surfaceDetachTurn) SubmitApproval(context.Context, controlprompt.ApprovalDecision) error {
	return nil
}

func (t *surfaceDetachTurn) Cancel() {
	t.cancelCalls.Add(1)
}

func (*surfaceDetachTurn) Close() error { return nil }
