package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
)

func TestTerminalReadObservesExitWithoutConsumingTaskResult(t *testing.T) {
	for _, code := range []int{0, 7, -1} {
		running := false
		process := &yieldProbeSandboxSession{statusRunning: &running, result: sandbox.CommandResult{Stdout: "retained output", ExitCode: code, Backend: sandbox.BackendHost}}
		tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
		task := &commandTask{ref: taskapi.Ref{TaskID: "native-command", SessionID: "session", TerminalID: process.Terminal().TerminalID}, sessionRef: session.SessionRef{SessionID: "session"}, session: process, state: taskapi.StateRunning, running: true, metadata: map[string]any{"command_phase": commandPhaseRunning}, result: map[string]any{}, output: "retained output", outputState: commandOutputState{callback: true}}
		tm.installCommandTask(task)
		out, err := newTerminalService(tm).Read(t.Context(), terminal.Ref{SessionID: "session", TaskID: "native-command"})
		if err != nil || out.Running || out.ExitCode == nil || *out.ExitCode != code {
			t.Fatalf("exit=%d snapshot=%+v err=%v", code, out, err)
		}
		if !task.running || task.outputState.frontier.model != 0 || task.outputTerminal {
			t.Fatal("observation consumed/finalized model Task result")
		}
		for range 2 {
			waited, err := newTerminalService(tm).Wait(t.Context(), terminal.Ref{SessionID: "session", TaskID: "native-command"})
			if err != nil || waited.ExitCode == nil || *waited.ExitCode != code || waited.Running {
				t.Fatalf("exit observation: %+v, %v", waited, err)
			}
			if !task.running || task.outputState.frontier.model != 0 || task.outputTerminal {
				t.Fatal("terminal Wait consumed/finalized model Task result")
			}
		}
		result, err := tm.Read(t.Context(), task.sessionRef, taskapi.ControlRequest{TaskID: "native-command", Principal: session.ActorKindController})
		if err != nil || result.Running || result.Result["result"] != "retained output" {
			t.Fatalf("later model read lost result: %+v %v", result, err)
		}
	}
}

func TestTerminalReadPreservesCommittedTaskOutcome(t *testing.T) {
	for _, state := range []taskapi.State{taskapi.StateCancelled, taskapi.StateFailed, taskapi.StateCompleted, taskapi.StateUnknownOutcome} {
		t.Run(string(state), func(t *testing.T) {
			running := false
			process := &yieldProbeSandboxSession{statusRunning: &running, result: sandbox.CommandResult{ExitCode: 0}}
			tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
			task := &commandTask{ref: taskapi.Ref{TaskID: "command", SessionID: "session"}, sessionRef: session.SessionRef{SessionID: "session"}, session: process, state: state, metadata: map[string]any{"command_phase": commandPhaseRunning}, result: map[string]any{"result": "retained result", "exit_code": 7}, outputState: commandOutputState{callback: true}}
			tm.installCommandTask(task)
			out, err := newTerminalService(tm).Read(t.Context(), terminal.Ref{SessionID: "session", TaskID: "command"})
			if err != nil || out.Running || out.State != string(state) || out.ExitCode == nil || *out.ExitCode != 7 || out.FinalResult != "retained result" {
				t.Fatalf("committed %s overwritten by process exit: %+v, %v", state, out, err)
			}
			waited, err := newTerminalService(tm).Wait(t.Context(), terminal.Ref{SessionID: "session", TaskID: "command"})
			if err != nil || waited.State != string(state) || waited.ExitCode == nil || *waited.ExitCode != 7 || waited.FinalResult != "retained result" {
				t.Fatalf("terminal Wait overwrote committed %s: %+v, %v", state, waited, err)
			}
		})
	}
}

func TestTerminalWaitCancellationAndConcurrentObservationDoNotConsumeTask(t *testing.T) {
	process := newConcurrentCommandWaitSession()
	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	task := &commandTask{ref: taskapi.Ref{TaskID: "command", SessionID: "session"}, sessionRef: session.SessionRef{SessionID: "session"},
		session: process, state: taskapi.StateRunning, running: true, metadata: map[string]any{"command_phase": commandPhaseRunning},
		result: map[string]any{}, output: "retained output", outputState: commandOutputState{callback: true}}
	tm.installCommandTask(task)
	service := newTerminalService(tm)
	ref := terminal.Ref{SessionID: "session", TaskID: "command"}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := service.Wait(ctx, ref); result <- err }()
	select {
	case <-process.waitStarted:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}
	if !task.running || task.outputState.frontier.model != 0 || task.outputTerminal {
		t.Fatal("wait cancellation changed Task lifecycle or cursor")
	}
	observed := make(chan terminal.Snapshot, 2)
	for range 2 {
		go func() { snapshot, err := service.Wait(t.Context(), ref); result <- err; observed <- snapshot }()
	}
	process.complete()
	for range 2 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if snapshot := <-observed; snapshot.Running || snapshot.ExitCode == nil || *snapshot.ExitCode != 0 {
			t.Fatalf("concurrent waiter: %+v", snapshot)
		}
	}
	if !task.running || task.outputState.frontier.model != 0 || task.outputTerminal {
		t.Fatal("concurrent terminal observation consumed Task result")
	}
	read, err := tm.Read(t.Context(), task.sessionRef, taskapi.ControlRequest{TaskID: "command", Principal: session.ActorKindController})
	if err != nil || read.Running || read.Result["result"] != "retained output" {
		t.Fatalf("model output after terminal wait: %+v, %v", read, err)
	}
}

func TestTerminalWaitDoesNotInventExitForUnknownOutcome(t *testing.T) {
	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	task := &commandTask{ref: taskapi.Ref{TaskID: "unknown", SessionID: "session"}, sessionRef: session.SessionRef{SessionID: "session"},
		state: taskapi.StateUnknownOutcome, metadata: map[string]any{"command_phase": commandPhaseUnknown}, result: map[string]any{"error": "unconfirmed"}}
	tm.installCommandTask(task)
	observed, err := newTerminalService(tm).Wait(t.Context(), terminal.Ref{SessionID: "session", TaskID: "unknown"})
	if err != nil || observed.State != string(taskapi.StateUnknownOutcome) || observed.ExitCode != nil {
		t.Fatalf("unknown producer was presented as a settled exit: %+v, %v", observed, err)
	}
}
