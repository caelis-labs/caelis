package tuiapp

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func TestEscInterruptsSessionWithIdleMainAndRunningChildren(t *testing.T) {
	calls := 0
	model := NewModel(Config{NoColor: true, NoAnimation: true,
		cancelSession: func(sessionID string) bool {
			calls++
			if sessionID != "session-1" {
				t.Errorf("interrupt session = %q", sessionID)
			}
			return true
		},
	})
	model.currentSessionID = "session-1"
	_, _ = model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := model.ensureSubagentOutputView("spawn-child")
	view.taskHandle = "child"
	model.subagentRosterTasks[view.callID] = taskstream.TaskDescriptor{SessionID: "session-1", TaskID: "task-child", Handle: "child", Running: true}
	if model.turnRunning() || model.subagentRosterRunningCount() != 1 {
		t.Fatal("expected idle main with running child")
	}
	updated, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(*Model)
	if cmd == nil || !model.runningInterruptRequested || model.turnRunning() {
		t.Fatal("Esc did not request Session interrupt while keeping main idle")
	}
	frame := ansi.Strip(model.View().Content)
	if !strings.Contains(frame, "interrupt requested") {
		t.Fatalf("missing interrupt hint:\n%s", frame)
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) == 0 {
		t.Fatalf("Esc command = %T", cmd())
	}
	result, ok := unwrapSessionViewMessage(batch[len(batch)-1]()).(RunningInterruptResultMsg)
	if !ok || !result.Accepted || calls != 1 {
		t.Fatalf("interrupt result = %#v, calls = %d", result, calls)
	}
	_, _ = model.handleRunningInterruptResultMsg(result)
	if model.runningInterruptRequested {
		t.Fatal("idle Session interrupt stayed latched awaiting a main Turn terminal")
	}
}

func TestEscKeepsPalettePriorityOverIdleChildInterrupt(t *testing.T) {
	calls := 0
	model := NewModel(Config{NoColor: true, NoAnimation: true, CancelRunning: func() bool { calls++; return true }})
	view := model.ensureSubagentOutputView("spawn-child")
	view.taskHandle = "child"
	model.subagentRosterTasks[view.callID] = taskstream.TaskDescriptor{Handle: "child", Running: true}
	model.showPalette = true
	_, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if cmd != nil {
		_ = cmd()
	}
	if calls != 0 || model.runningInterruptRequested || model.showPalette {
		t.Fatal("Esc interrupted Session before closing palette")
	}
}

type addressedInterruptBridgeStub struct {
	interruptBridgeStub
	interruptedSession string
}

func (s *addressedInterruptBridgeStub) InterruptSession(ctx context.Context, sessionID string) error {
	s.interruptedSession = sessionID
	return s.Interrupt(ctx)
}
func (*addressedInterruptBridgeStub) SessionID() string { return "session-1" }

func TestSessionInterruptFailureDoesNotFallBackToLocalAdmissionCancel(t *testing.T) {
	for _, outcome := range []appserver.Outcome{appserver.OutcomeUnknown, appserver.OutcomeRejected} {
		t.Run(string(outcome), func(t *testing.T) {
			service := &addressedInterruptBridgeStub{interruptBridgeStub: interruptBridgeStub{interruptErr: appserver.NewOutcomeError(outcome, errors.New("Session interrupt failed"))}}
			sender := &ProgramSender{}
			defer sender.Close()
			viewCtx, _ := sender.replaceSessionView(t.Context(), "session-1")
			pendingCtx, finish := sender.beginRunContext(viewCtx)
			defer finish()
			cfg := ConfigFromControlService(service, sender, Config{})
			if cfg.cancelSession == nil || cfg.cancelSession("session-1") {
				t.Fatal("failed Session interrupt reported success")
			}
			if pendingCtx.Err() != nil {
				t.Fatal("Surface cancelled pending admission after failed Session interrupt")
			}
			if service.interruptedSession != "session-1" || service.interruptCalls != 1 {
				t.Fatal("Session interrupt was not addressed exactly once")
			}
		})
	}
}
