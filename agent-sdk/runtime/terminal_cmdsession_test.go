package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/cmdsession"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/backend/runnerruntime"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
)

// gatedCmdsessionRunner runs real cmdsession sessions through the production
// runner translation while stalling output delivery until the test releases
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

func (r *gatedCmdsessionRunner) Run(ctx context.Context, req runnerruntime.Request) (sandbox.CommandResult, error) {
	sessionID, err := r.StartAsync(ctx, req)
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	return r.WaitSession(ctx, sessionID, 0)
}

func (r *gatedCmdsessionRunner) StartAsync(_ context.Context, req runnerruntime.Request) (string, error) {
	forward := runnerruntime.UTF8OutputForwarder(req.OnOutput)
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
	runner := newGatedCmdsessionRunner()
	t.Cleanup(runner.close)

	tm := newTaskRuntime(&Runtime{clock: time.Now}, nil)
	terminals := newTerminalService(tm)
	sandboxRuntime := runnerruntime.New(runnerruntime.Config{
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

	ref := terminal.Ref{SessionID: "session", TaskID: task.ref.TaskID, TerminalID: task.ref.TerminalID}
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
