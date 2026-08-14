package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
)

type recordingAgentMessageSender struct {
	request  agentmessage.Request
	requests []agentmessage.Request
}

func (s *recordingAgentMessageSender) SendMessage(_ context.Context, req agentmessage.Request) (agentmessage.Response, error) {
	s.request = agentmessage.NormalizeRequest(req)
	s.requests = append(s.requests, s.request)
	return agentmessage.Response{
		MessageID: req.MessageID, Accepted: true, State: "delivered",
		TurnID: "child-turn-2", StartedTurn: true,
	}, nil
}

func TestRuntimeSendMessageToolReusesStableMessageIDForSameCall(t *testing.T) {
	t.Parallel()

	sender := &recordingAgentMessageSender{}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), external: sender,
		session:    session.Session{Controller: session.ControllerBinding{ControllerID: "controller-1"}},
		sessionRef: session.SessionRef{SessionID: "session-1"},
	}
	raw, _ := json.Marshal(map[string]any{"to": "parent", "message": "status update"})
	for range 2 {
		if _, err := target.Call(context.Background(), tool.Call{ID: "message-call-1", Name: "SendMessage", Input: raw}); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.requests) != 2 || sender.requests[0].MessageID == "" || sender.requests[0].MessageID != sender.requests[1].MessageID {
		t.Fatalf("routed requests = %#v, want stable retry identity", sender.requests)
	}
	if _, err := target.Call(context.Background(), tool.Call{ID: "message-call-2", Name: "SendMessage", Input: raw}); err != nil {
		t.Fatal(err)
	}
	if sender.requests[2].MessageID == sender.requests[0].MessageID {
		t.Fatalf("different calls reused message id %q", sender.requests[0].MessageID)
	}
}

func TestRuntimeInjectsSendMessageForHostedChildWithoutOtherTools(t *testing.T) {
	t.Parallel()

	sender := &recordingAgentMessageSender{}
	runtime := &Runtime{}
	wrapped := runtime.wrapToolsForRuntime(session.Session{}, session.SessionRef{SessionID: "child"}, agent.AgentSpec{}, runtimeToolContext{messageSender: sender})
	if len(wrapped) != 1 || wrapped[0].Definition().Name != "SendMessage" {
		t.Fatalf("wrapped tools = %#v, want only SendMessage", wrapped)
	}
}

func TestRuntimeInjectedSendMessageSurvivesWorkspaceWritePolicy(t *testing.T) {
	t.Parallel()

	registry, err := presets.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	sender := &recordingAgentMessageSender{}
	activeSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "user", SessionID: "child", WorkspaceKey: "workspace"},
		CWD:        "/workspace",
		Controller: session.ControllerBinding{ControllerID: "child-controller"},
	}
	runtime := &Runtime{policies: registry, defaultPolicyMode: presets.ModeWorkspaceWrite}
	runtimeTools := runtime.wrapToolsForRuntime(activeSession, activeSession.SessionRef, agent.AgentSpec{}, runtimeToolContext{messageSender: sender})
	wrapped := runtime.wrapToolsForPolicy(activeSession, activeSession.SessionRef, nil, agent.AgentSpec{Tools: runtimeTools}, approvalContext{
		ctx: context.Background(), session: activeSession, sessionRef: activeSession.SessionRef,
	})
	if len(wrapped) != 1 {
		t.Fatalf("wrapped tools = %d, want only SendMessage", len(wrapped))
	}
	raw, _ := json.Marshal(map[string]any{"to": "parent", "message": "status update"})
	result, err := wrapped[0].Call(context.Background(), tool.Call{ID: "message-policy-1", Name: "SendMessage", Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	if sender.request.To != "parent" || sender.request.Text != "status update" {
		t.Fatalf("routed request = %#v, want policy-approved parent delivery", sender.request)
	}
	if got := testToolResultText(t, result); got != "Message delivered." {
		t.Fatalf("tool result = %q, want mailbox delivery acknowledgement", got)
	}
}

func TestRuntimeSendMessageToolDelegatesWithTrustedIdentity(t *testing.T) {
	t.Parallel()

	sender := &recordingAgentMessageSender{}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), external: sender,
		session:    session.Session{Controller: session.ControllerBinding{ControllerID: "controller-1"}},
		sessionRef: session.SessionRef{SessionID: "session-1"},
	}
	raw, _ := json.Marshal(map[string]any{"to": "parent", "message": " status update "})
	result, err := target.Call(context.Background(), tool.Call{ID: "message-call-1", Name: "SendMessage", Input: raw})
	if err != nil {
		t.Fatal(err)
	}
	if sender.request.To != "parent" || sender.request.Text != "status update" || sender.request.MessageID == "" {
		t.Fatalf("routed request = %#v", sender.request)
	}
	if sender.request.From.Kind != session.ActorKindController || sender.request.From.ID != "controller-1" {
		t.Fatalf("routed actor = %#v, want trusted controller identity", sender.request.From)
	}
	if got := testToolResultText(t, result); got != "Message delivered." {
		t.Fatalf("tool result = %q, want mailbox delivery acknowledgement", got)
	}
	messageMeta := testToolResultRuntimeMeta(t, result, "message")
	if messageMeta["accepted"] != true || messageMeta["state"] != "delivered" || messageMeta["message_id"] != sender.request.MessageID ||
		messageMeta["turn_id"] != "child-turn-2" || messageMeta["started_turn"] != true || messageMeta["to"] != "parent" {
		t.Fatalf("message metadata = %#v", messageMeta)
	}
}

func TestSendMessageToolResultHidesTargetLifecycleFromModel(t *testing.T) {
	t.Parallel()

	for _, state := range []string{
		agentmessage.StatePending,
		agentmessage.StateDelivered,
		agentmessage.StateRunning,
		agentmessage.StateCompleted,
		agentmessage.StateAcceptedUnpersisted,
	} {
		result := sendMessageToolResult(tool.Call{ID: "call-1"}, "research", agentmessage.Response{
			MessageID: "message-1", Accepted: true, State: state,
			TurnID: "turn-1", StartedTurn: true,
		})
		if got := testToolResultText(t, result); got != "Message delivered." {
			t.Fatalf("state %q result = %q, want one mailbox acknowledgement", state, got)
		}
		if result.Content[0].JSON != nil {
			t.Fatalf("state %q exposed JSON lifecycle payload: %#v", state, result.Content[0])
		}
	}

	unknown := sendMessageToolResult(tool.Call{ID: "call-unknown"}, "research", agentmessage.Response{
		MessageID: "message-unknown", State: agentmessage.StateUnknownOutcome,
	})
	if got := testToolResultText(t, unknown); got != "Delivery outcome unknown; do not resend." {
		t.Fatalf("unknown result = %q, want bounded non-retry guidance", got)
	}
}

func testToolResultText(t *testing.T, result tool.Result) string {
	t.Helper()
	if len(result.Content) != 1 || result.Content[0].Text == nil {
		t.Fatalf("result content = %#v, want one text part", result.Content)
	}
	return result.Content[0].Text.Text
}

func TestRuntimeSendMessageToolRoutesUnknownHandleThroughExternal(t *testing.T) {
	t.Parallel()

	external := &recordingAgentMessageSender{}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), external: external,
		session:    session.Session{Controller: session.ControllerBinding{ControllerID: "child-controller"}},
		sessionRef: session.SessionRef{SessionID: "child-session"},
	}
	raw, _ := json.Marshal(map[string]any{"to": "research", "message": "status for sibling"})
	if _, err := target.Call(context.Background(), tool.Call{ID: "message-sibling-1", Name: "SendMessage", Input: raw}); err != nil {
		t.Fatal(err)
	}
	if external.request.To != "research" || external.request.Text != "status for sibling" {
		t.Fatalf("external request = %#v, want sibling handle routed through parent transport", external.request)
	}
}

func TestRuntimeSendMessageToolPrefersLocalSpawnTargetOverACPParent(t *testing.T) {
	runner := &recordingSubagentRunner{
		spawnResult:    delegation.Result{State: delegation.StateCompleted, Result: "first done"},
		continueResult: delegation.Result{State: delegation.StateCompleted, Result: "message done"},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.tasks.StartSubagent(context.Background(), activeSession, activeSession.SessionRef, runner, taskapi.SubagentStartRequest{
		Agent: "helper", Prompt: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	external := &recordingAgentMessageSender{}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), runtime: runtime, external: external,
		session: activeSession, sessionRef: activeSession.SessionRef,
	}
	raw, _ := json.Marshal(map[string]any{"to": started.Handle, "message": "inspect locally"})
	if _, err := target.Call(context.Background(), tool.Call{ID: "message-local-1", Name: "SendMessage", Input: raw}); err != nil {
		t.Fatal(err)
	}
	if runner.continuePrompt != "inspect locally" {
		t.Fatalf("local child prompt = %q, want local delivery", runner.continuePrompt)
	}
	if external.request.MessageID != "" {
		t.Fatalf("external ACP request = %#v, want no parent callback for local handle", external.request)
	}
}

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

func TestRuntimeTaskWriteRequiresNonBlankInput(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	targetTool := runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	for _, input := range []any{nil, "   "} {
		args := map[string]any{"action": "write", "handle": "command-1"}
		if input != nil {
			args["input"] = input
		}
		_, err := targetTool.Call(context.Background(), tool.Call{
			ID:    "task-write-missing-input",
			Name:  tasktool.ToolName,
			Input: mustJSONRaw(args),
		})
		if err == nil || !strings.Contains(err.Error(), "input") {
			t.Fatalf("Task write input %#v error = %v, want input validation failure", input, err)
		}
	}
}

func TestRuntimeTaskRejectsNonStringInput(t *testing.T) {
	t.Parallel()

	_, activeSession, runtime := newRuntimeRunCommandToolTestHarness(t)
	targetTool := runtimeTaskTool{
		base:       tasktool.New(),
		sessionRef: activeSession.SessionRef,
		tasks:      runtime.tasks,
	}
	_, err := targetTool.Call(context.Background(), tool.Call{
		ID:    "task-read-invalid-input",
		Name:  tasktool.ToolName,
		Input: mustJSONRaw(map[string]any{"action": "read", "handle": "command-1", "input": 1}),
	})
	if err == nil || !strings.Contains(err.Error(), "input") {
		t.Fatalf("Task non-string input error = %v, want input validation failure", err)
	}
}
