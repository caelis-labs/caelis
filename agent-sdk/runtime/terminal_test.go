package runtime

import (
	"context"
	"errors"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/cmdsession"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedCmdsessionRunner runs real cmdsession sessions through the production
// runner runtime while stalling output delivery until the test releases
// it, so "output has not completed" becomes a controllable state instead of a
// race.
type gatedCmdsessionRunner struct {
	manager *cmdsession.SessionManager
	stall   chan struct{}

	started     chan struct{}
	startedOnce sync.Once
	releaseOnce sync.Once
}

func newGatedCmdsessionRunner() *gatedCmdsessionRunner {
	return &gatedCmdsessionRunner{
		manager: cmdsession.NewSessionManager(cmdsession.SessionManagerConfig{}),
		stall:   make(chan struct{}),
		started: make(chan struct{}),
	}
}

func (r *gatedCmdsessionRunner) release() {
	r.releaseOnce.Do(func() { close(r.stall) })
}

func (r *gatedCmdsessionRunner) close() {
	r.release()
	_ = r.manager.Close()
}

func (r *gatedCmdsessionRunner) Run(ctx context.Context, req sandbox.CommandRequest) (sandbox.CommandResult, error) {
	sessionID, err := r.StartAsync(ctx, req)
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	return r.WaitSession(ctx, sessionID, 0)
}

func (r *gatedCmdsessionRunner) StartAsync(_ context.Context, req sandbox.CommandRequest) (string, error) {
	forward := cmdsession.UTF8OutputForwarder(req.OnOutput)
	session, err := r.manager.StartSession(cmdsession.AsyncSessionConfig{
		Command:     req.Command,
		Dir:         req.Dir,
		Timeout:     req.Timeout,
		IdleTimeout: req.IdleTimeout,
		TTY:         req.TTY,
		OnOutput: func(chunk cmdsession.AsyncOutputChunk) {
			if !chunk.Final && len(chunk.Data) > 0 {
				r.startedOnce.Do(func() { close(r.started) })
				<-r.stall
			}
			if forward != nil {
				forward(chunk)
			}
		},
	})
	if err != nil {
		return "", err
	}
	return session.ID, nil
}

func (r *gatedCmdsessionRunner) WriteInput(sessionID string, input []byte) error {
	return r.manager.WriteInput(sessionID, input)
}

func (r *gatedCmdsessionRunner) ReadOutput(sessionID string, stdoutMarker, stderrMarker int64) ([]byte, []byte, int64, int64, error) {
	return r.manager.ReadOutput(sessionID, stdoutMarker, stderrMarker)
}

func (r *gatedCmdsessionRunner) AwaitOutput(ctx context.Context, sessionID string, cursor sandbox.OutputCursor) (cmdsession.OutputObservation, error) {
	return r.manager.AwaitOutput(ctx, sessionID, cursor)
}

func (r *gatedCmdsessionRunner) GetSessionStatus(sessionID string) (cmdsession.SessionStatus, error) {
	return r.manager.GetSessionStatus(sessionID)
}

func (r *gatedCmdsessionRunner) WaitSession(ctx context.Context, sessionID string, timeout time.Duration) (sandbox.CommandResult, error) {
	if _, err := r.manager.WaitSessionWithContextTimeout(ctx, sessionID, timeout); err != nil {
		return sandbox.CommandResult{}, err
	}
	return r.manager.GetResult(sessionID)
}

func (r *gatedCmdsessionRunner) TerminateSession(sessionID string) error {
	return r.manager.TerminateSession(sessionID)
}

func (r *gatedCmdsessionRunner) Close() error {
	return r.manager.Close()
}

// TestTerminalReadPublishesCommandExitOnlyAfterOutputCompletes covers the
// terminate window of a yielded command: while a real cmdsession is terminating
// but has not finished delivering output, a terminal read must not publish an
// exit status. Only after the output completed may it return the final exit
// code with the tail output, and the ordinary Task read must still receive the
// retained result.
func TestTerminalReadPublishesCommandExitOnlyAfterOutputCompletes(t *testing.T) {
	t.Parallel()
	runner := newGatedCmdsessionRunner()
	t.Cleanup(runner.close)

	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	terminals := &terminalService{tasks: tm}
	sandboxRuntime := cmdsession.NewRuntime(cmdsession.RuntimeConfig{
		Backend: sandbox.BackendHost,
		Runner:  runner,
	})

	var outputTask *commandTask
	process, err := sandboxRuntime.Start(t.Context(), sandbox.CommandRequest{
		Command: shellPrintThenSleepForTest("tail-output", 30*time.Second),
		OnOutput: func(chunk sandbox.OutputChunk) {
			if outputTask != nil {
				outputTask.appendSandboxOutput(chunk)
			}
		},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	task := &commandTask{
		ref:        taskapi.Ref{TaskID: "cmdsession-terminal", SessionID: "session", TerminalID: process.Terminal().TerminalID},
		sessionRef: session.SessionRef{SessionID: "session"},
		session:    process,
		state:      taskapi.StateRunning,
		running:    true,
		metadata: map[string]any{
			"task_id": "cmdsession-terminal", "command_phase": commandPhaseRunning,
			"state": string(taskapi.StateRunning), "running": true,
		},
		result:      map[string]any{"state": string(taskapi.StateRunning)},
		outputState: commandOutputState{callback: true},
	}
	outputTask = task
	tm.installCommandTask(task)

	select {
	case <-runner.started:
	case <-time.After(10 * time.Second):
		t.Fatal("command produced no output to stall")
	}

	ref := taskapi.TerminalRef{SessionID: "session", TaskID: task.ref.TaskID, TerminalID: task.ref.TerminalID}
	if err := terminals.Kill(t.Context(), ref); err != nil {
		t.Fatalf("Kill() error = %v", err)
	}

	settling, err := terminals.Read(t.Context(), ref)
	if err != nil {
		t.Fatalf("Read(after Kill) error = %v", err)
	}
	if !settling.Running || settling.ExitCode != nil {
		t.Fatalf("Read(after Kill) = %+v, want running snapshot without an exit while output is incomplete", settling)
	}
	if settling.Output != "" {
		t.Fatalf("Read(after Kill).Output = %q, want the stalled tail withheld", settling.Output)
	}

	runner.release()

	deadline := time.Now().Add(10 * time.Second)
	exited := settling
	for {
		exited, err = terminals.Read(t.Context(), ref)
		if err != nil {
			t.Fatalf("Read(after output completion) error = %v", err)
		}
		if !exited.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Read(after output completion) = %+v, want a settled exit", exited)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if exited.ExitCode == nil {
		t.Fatalf("Read(final) = %+v, want an exit code", exited)
	}
	if !strings.Contains(exited.Output, "tail-output") {
		t.Fatalf("Read(final).Output = %q, want tail-output", exited.Output)
	}

	result, err := tm.Read(t.Context(), task.sessionRef, taskapi.ControlRequest{
		TaskID: task.ref.TaskID, Principal: session.ActorKindController,
	})
	if err != nil {
		t.Fatalf("Task read error = %v", err)
	}
	if result.Running {
		t.Fatalf("Task read = %+v, want a terminal state", result)
	}
	if got := taskRawStringValue(result.Result["result"]); !strings.Contains(got, "tail-output") {
		t.Fatalf("Task read result = %q, want retained tail-output", got)
	}
}

func TestTerminalReadObservesExitWithoutConsumingTaskResult(t *testing.T) {
	t.Parallel()
	for _, code := range []int{0, 7, -1} {
		running := false
		process := &yieldProbeSandboxSession{statusRunning: &running, result: sandbox.CommandResult{Stdout: "retained output", ExitCode: code, Backend: sandbox.BackendHost}}
		tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
		task := &commandTask{ref: taskapi.Ref{TaskID: "native-command", SessionID: "session", TerminalID: process.Terminal().TerminalID}, sessionRef: session.SessionRef{SessionID: "session"}, session: process, state: taskapi.StateRunning, running: true, metadata: map[string]any{"command_phase": commandPhaseRunning}, result: map[string]any{}, output: "retained output", outputState: commandOutputState{callback: true}}
		tm.installCommandTask(task)
		out, err := (&terminalService{tasks: tm}).Read(t.Context(), taskapi.TerminalRef{SessionID: "session", TaskID: "native-command"})
		if err != nil || out.Running || out.ExitCode == nil || *out.ExitCode != code {
			t.Fatalf("exit=%d snapshot=%+v err=%v", code, out, err)
		}
		if !task.running || task.outputState.frontier.model != 0 || task.outputTerminal {
			t.Fatal("observation consumed/finalized model Task result")
		}
		for range 2 {
			waited, err := (&terminalService{tasks: tm}).Wait(t.Context(), taskapi.TerminalRef{SessionID: "session", TaskID: "native-command"})
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
	t.Parallel()
	for _, state := range []taskapi.State{taskapi.StateCancelled, taskapi.StateFailed, taskapi.StateCompleted, taskapi.StateUnknownOutcome} {
		t.Run(string(state), func(t *testing.T) {
			running := false
			process := &yieldProbeSandboxSession{statusRunning: &running, result: sandbox.CommandResult{ExitCode: 0}}
			tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
			task := &commandTask{ref: taskapi.Ref{TaskID: "command", SessionID: "session"}, sessionRef: session.SessionRef{SessionID: "session"}, session: process, state: state, metadata: map[string]any{"command_phase": commandPhaseRunning}, result: map[string]any{"result": "retained result", "exit_code": 7}, outputState: commandOutputState{callback: true}}
			tm.installCommandTask(task)
			out, err := (&terminalService{tasks: tm}).Read(t.Context(), taskapi.TerminalRef{SessionID: "session", TaskID: "command"})
			if err != nil || out.Running || out.State != string(state) || out.ExitCode == nil || *out.ExitCode != 7 || out.FinalResult != "retained result" {
				t.Fatalf("committed %s overwritten by process exit: %+v, %v", state, out, err)
			}
			waited, err := (&terminalService{tasks: tm}).Wait(t.Context(), taskapi.TerminalRef{SessionID: "session", TaskID: "command"})
			if err != nil || waited.State != string(state) || waited.ExitCode == nil || *waited.ExitCode != 7 || waited.FinalResult != "retained result" {
				t.Fatalf("terminal Wait overwrote committed %s: %+v, %v", state, waited, err)
			}
		})
	}
}

func TestTerminalWaitCancellationAndConcurrentObservationDoNotConsumeTask(t *testing.T) {
	t.Parallel()
	process := newConcurrentCommandWaitSession()
	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	task := &commandTask{ref: taskapi.Ref{TaskID: "command", SessionID: "session"}, sessionRef: session.SessionRef{SessionID: "session"},
		session: process, state: taskapi.StateRunning, running: true, metadata: map[string]any{"command_phase": commandPhaseRunning},
		result: map[string]any{}, output: "retained output", outputState: commandOutputState{callback: true}}
	tm.installCommandTask(task)
	service := &terminalService{tasks: tm}
	ref := taskapi.TerminalRef{SessionID: "session", TaskID: "command"}
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
	observed := make(chan taskapi.TerminalSnapshot, 2)
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
	t.Parallel()
	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	task := &commandTask{ref: taskapi.Ref{TaskID: "unknown", SessionID: "session"}, sessionRef: session.SessionRef{SessionID: "session"},
		state: taskapi.StateUnknownOutcome, metadata: map[string]any{"command_phase": commandPhaseUnknown}, result: map[string]any{"error": "unconfirmed"}}
	tm.installCommandTask(task)
	observed, err := (&terminalService{tasks: tm}).Wait(t.Context(), taskapi.TerminalRef{SessionID: "session", TaskID: "unknown"})
	if err != nil || observed.State != string(taskapi.StateUnknownOutcome) || observed.ExitCode != nil {
		t.Fatalf("unknown producer was presented as a settled exit: %+v, %v", observed, err)
	}
}
