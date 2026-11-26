package subagent

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
)

func TestCancellationSettlementRequiresPromptResponse(t *testing.T) {
	for _, test := range []struct {
		name string
		stop string
		err  error
		want task.State
	}{
		{"confirmed cancellation", "cancelled", nil, task.StateCancelled},
		{"completion won cancellation race", "end_turn", nil, task.StateCompleted},
		{"cancelled observation", "", joinChildInputUnknown("unconfirmed prompt", context.Canceled), task.StateUnknownOutcome},
		{"rejected authentication", "", errors.New("configured authentication method unavailable"), task.StateFailed},
		{"cancelled before dispatch", "", context.Canceled, task.StateInterrupted},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &Runner{clock: time.Now}
			run := &childRun{running: true, cancelRequested: true, state: task.StateRunning}
			runner.finishDrive(t.Context(), run, test.stop, test.err)
			if got := childResultLocked(run); got.State != test.want || got.Running {
				t.Fatalf("settlement = %#v, want %s", got, test.want)
			}
		})
	}
}

func TestInternalPromptErrorQuarantinesSubprocessActivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	registry, err := NewRegistry([]AgentConfig{{Name: "helper", Command: os.Args[0],
		Args: []string{"-test.run=TestRunnerPromptFailureHelperProcess", "--"},
		Env:  map[string]string{"CAELIS_ACP_SUBAGENT_HELPER": "prompt-internal-error"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(RunnerConfig{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	completions := make(chan task.DelegationResult, 1)
	anchor, _, err := runner.Spawn(ctx, task.SpawnContext{
		ActivityID: "activity-internal-error", TaskID: "task-internal-error", CWD: t.TempDir(),
		Output:     &recordingStreams{},
		Completion: completionSinkFunc(func(result task.DelegationResult) { completions <- result }),
	}, task.DelegationRequest{Agent: "helper", Prompt: "work"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-completions:
		if result.State != task.StateUnknownOutcome {
			t.Fatalf("internal RPC error = %#v, want unresolved execution", result)
		}
		if !strings.Contains(result.Error, "ACP response code -32603") {
			t.Fatalf("missing safe response-phase diagnostic: %q", result.Error)
		}
		for _, secret := range []string{"rpc-super-secret", "stderr-super-secret", "/Users/private"} {
			if strings.Contains(result.Error, secret) {
				t.Fatalf("diagnostic leaked %q", secret)
			}
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.SubmitChildInput(ctx, agent.ChildInputRequest{
		Target: run.slot.target, Source: session.ParentCommunicationActor(), Input: "must not start another prompt",
	})
	if err == nil {
		t.Fatal("unresolved execution accepted another prompt")
	}
	if got := run.slot.currentRun(); got != run {
		t.Fatal("unresolved execution was silently reconnected")
	}
}

func TestConfirmedCancellationSurvivesConnectionCleanupFailure(t *testing.T) {
	reader, peer := io.Pipe()
	defer peer.Close()
	remote, err := client.NewStreamClient(io.Discard, struct{ io.Reader }{reader}, client.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Keep the transport reader blocked while cleanup loses its observation.
	cleanupCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := remote.Close(cleanupCtx); err == nil {
		t.Fatal("expected connection join failure")
	}
	_ = peer.Close()
	runner := &Runner{clock: time.Now}
	run := &childRun{running: true, cancelRequested: true, state: task.StateRunning, client: remote}
	runner.finishDrive(t.Context(), run, "cancelled", nil)
	if got := childResultLocked(run); got.State != task.StateCancelled || got.Running {
		t.Fatalf("confirmed cancellation changed after cleanup: %#v", got)
	}
	if err := remote.Close(t.Context()); err == nil {
		t.Fatal("expected connection cleanup failure")
	}
}
