package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

func TestSubagentApprovalRequesterPreservesCanonicalToolPayload(t *testing.T) {
	t.Parallel()

	var captured agent.ApprovalRequest
	requester := subagentApprovalRequester{
		requester: approvalRequesterFunc(func(_ context.Context, req agent.ApprovalRequest) (agent.ApprovalResponse, error) {
			captured = req
			return agent.ApprovalResponse{Outcome: "selected", OptionID: "allow-once", Approved: true}, nil
		}),
		sessionRef: session.SessionRef{SessionID: "parent-1"},
	}
	_, err := requester.RequestSubagentApproval(context.Background(), tasksubagent.ApprovalRequest{
		TaskID: "task-1",
		ToolCall: tasksubagent.ApprovalToolCall{
			ID:        "call-1",
			Name:      "WRITE",
			RawInput:  map[string]any{"path": "a.txt"},
			RawOutput: map[string]any{"preview": "new text"},
			Content: []session.ProtocolToolCallContent{{
				Type:    "content",
				Content: session.ProtocolTextContent("permission detail"),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if captured.Approval == nil || captured.Approval.ToolCall.RawOutput["preview"] != "new text" {
		t.Fatalf("approval = %#v, want preserved raw output", captured.Approval)
	}
	if len(captured.Approval.ToolCall.Content) != 1 {
		t.Fatalf("content = %#v, want preserved canonical content", captured.Approval.ToolCall.Content)
	}
}

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

func TestRuntimeRunCommandToolAddsHostApprovalHintWhenStartRejected(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	fake := &yieldProbeSandboxRuntime{
		startErr: fmt.Errorf("ports/sandbox: %s", sandbox.HostExecutionRequiresApprovalMessage),
	}
	targetTool := runtimeCommandTool{
		base:       mustRuntimeRunCommandTool(t, fake),
		session:    session.CloneSession(activeSession),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}

	result := callRuntimeRunCommandTool(t, targetTool, map[string]any{
		"command": "git log --oneline -3",
		"workdir": activeSession.CWD,
	})

	if !result.IsError {
		t.Fatal("result.IsError = false, want structured command start failure")
	}
	payload := testToolResultPayload(t, result)
	if got, _ := payload["system_hint"].(string); got != sandbox.HostExecutionRequiresApprovalMessage {
		t.Fatalf("system_hint = %q, want %q", got, sandbox.HostExecutionRequiresApprovalMessage)
	}
	if _, ok := payload["hint_code"]; ok {
		t.Fatalf("hint_code = %#v, want omitted from model-facing payload", payload["hint_code"])
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
	handle, _ := testToolResultRuntimeMeta(t, runCommandResult, "task")["handle"].(string)
	if strings.TrimSpace(handle) == "" {
		t.Fatalf("command result metadata = %#v, want handle", runCommandResult.Metadata)
	}

	raw := mustJSONRaw(map[string]any{
		"action":     "wait",
		"handle":     handle,
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
