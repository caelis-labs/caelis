package runtime

import (
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
		})
	}
}
