package local

import (
	"context"
	"testing"

	taskfile "github.com/OnslaughtSnail/caelis/impl/task/file"
	"github.com/OnslaughtSnail/caelis/ports/sandbox"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
)

func TestStartCommandPersistsCompletedCommandTerminalStreams(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	completed := false
	fakeSession := &yieldProbeSandboxSession{
		statusRunning: &completed,
		stdout:        "out\n",
		stderr:        "err\n",
		result: sandbox.CommandResult{
			Stdout:   "out\n",
			Stderr:   "err\n",
			ExitCode: 0,
		},
	}
	fake := &yieldProbeSandboxRuntime{session: fakeSession}
	taskStore := taskfile.NewStore(taskfile.Config{RootDir: t.TempDir()})
	runtime.tasks.store = taskStore

	snapshot, err := runtime.tasks.StartCommand(context.Background(), activeSession, activeSession.SessionRef, fake, taskapi.CommandStartRequest{
		Command: "echo out; echo err >&2",
		Workdir: activeSession.CWD,
		Yield:   0,
	})
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if got, _ := snapshot.Result["result"].(string); got != "out\nerr\n" {
		t.Fatalf("snapshot result = %q, want merged terminal summary", got)
	}

	entry, err := taskStore.Get(context.Background(), snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get() error = %v", err)
	}
	if got, _ := entry.Result["stdout"].(string); got != "out\n" {
		t.Fatalf("hydrated stdout = %q, want out stream", got)
	}
	if got, _ := entry.Result["stderr"].(string); got != "err\n" {
		t.Fatalf("hydrated stderr = %q, want err stream", got)
	}
	listed, err := taskStore.ListSession(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("task store ListSession() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed tasks = %d, want 1", len(listed))
	}
	if _, exists := listed[0].Result["stdout"]; exists {
		t.Fatalf("listed result unexpectedly contains hydrated stdout: %#v", listed[0].Result)
	}
	if listed[0].Result["stdout_blob"] == nil || listed[0].Result["stderr_blob"] == nil {
		t.Fatalf("listed result missing stream blobs: %#v", listed[0].Result)
	}
}
