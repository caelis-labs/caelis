package local

import (
	"context"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/impl/tool/builtin/shell"
	tasktool "github.com/OnslaughtSnail/caelis/impl/tool/builtin/task"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestRuntimeRunCommandToolAcceptsLegacyAdditionalPermissionsMode(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	targetTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeRunCommandTool(t, targetTool, map[string]any{
		"command":             "printf 'ok'",
		"workdir":             activeSession.CWD,
		"sandbox_permissions": "with_additional_permissions",
	})

	if got := fake.session.command; got != "printf 'ok'" {
		t.Fatalf("command = %q, want printf 'ok'", got)
	}
	assertRunningTaskSnapshot(t, result)
}

func TestRuntimeRunCommandToolRejectsUnsupportedArgs(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	targetTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	raw := mustJSONRaw(map[string]any{
		"command":    "printf 'ok'",
		"workdir":    activeSession.CWD,
		"timeout_ms": 1,
	})

	_, err := targetTool.Call(context.Background(), tool.Call{
		ID:    "command-unsupported-arg",
		Name:  shell.RunCommandToolName,
		Input: raw,
	})
	if err == nil {
		t.Fatal("RUN_COMMAND Call() error = nil, want unsupported arg rejection")
	}
	if !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("RUN_COMMAND Call() error = %v, want timeout_ms mention", err)
	}
}

func TestRuntimeTaskWaitRejectsTimeoutMSAlias(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{session: newYieldProbeSandboxSession()}
	runCommandTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	runCommandResult := callRuntimeRunCommandTool(t, runCommandTool, map[string]any{
		"command":       "printf 'ok'",
		"workdir":       activeSession.CWD,
		"yield_time_ms": 0,
	})
	taskID, _ := testToolResultRuntimeMeta(t, runCommandResult, "task")["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		t.Fatalf("command result metadata = %#v, want task_id", runCommandResult.Metadata)
	}

	raw := mustJSONRaw(map[string]any{
		"action":     "wait",
		"task_id":    taskID,
		"timeout_ms": "45000",
	})
	_, err := (runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}).Call(context.Background(), tool.Call{
		ID:    "task-timeout-alias",
		Name:  tasktool.ToolName,
		Input: raw,
	})
	if err == nil {
		t.Fatal("TASK wait error = nil, want timeout_ms rejection")
	}
	if !strings.Contains(err.Error(), "timeout_ms") {
		t.Fatalf("TASK wait error = %v, want timeout_ms mention", err)
	}
}
