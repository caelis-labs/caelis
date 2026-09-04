package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
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

type recordingAgentInputSender struct {
	request  agent.AgentInput
	requests []agent.AgentInput
}

type runtimeChildInputRunner struct {
	mu          sync.Mutex
	spawnResult delegation.Result
	request     agent.ChildInputRequest
}

func (r *runtimeChildInputRunner) Spawn(_ context.Context, spawn agent.SubagentSpawnContext, _ delegation.Request) (delegation.Anchor, delegation.Result, error) {
	return delegation.Anchor{
		TaskID:    spawn.TaskID,
		SessionID: "child-1",
		AgentID:   spawn.TaskID,
	}, delegation.CloneResult(r.spawnResult), nil
}

func (*runtimeChildInputRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	return delegation.Result{}, nil
}

func (*runtimeChildInputRunner) Cancel(context.Context, delegation.Anchor) error { return nil }

func (r *runtimeChildInputRunner) SubmitChildInput(_ context.Context, req agent.ChildInputRequest) (agent.ChildInputResult, error) {
	r.mu.Lock()
	r.request = agent.CloneChildInputRequest(req)
	r.mu.Unlock()
	return agent.ChildInputResult{ActivityID: req.ActivityID, StartedActivity: true}, nil
}

type preRuntimeToolWrapper struct {
	tool.Tool
}

func TestRuntimeTaskWrappingRequiresConcreteBuiltinAuthority(t *testing.T) {
	t.Parallel()

	runtime := &Runtime{}
	active := session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}
	wrap := func(candidate tool.Tool) []tool.Tool {
		return runtime.wrapToolsForRuntime(active, active.SessionRef, agent.AgentSpec{Tools: []tool.Tool{candidate}}, runtimeToolContext{})
	}

	builtin := wrap(new(shell.RunCommandTool))
	if len(builtin) < 1 {
		t.Fatal("concrete RunCommand builtin was dropped")
	}
	if _, ok := builtin[0].(runtimeCommandTool); !ok {
		t.Fatalf("concrete RunCommand builtin wrapped as %T, want runtimeCommandTool", builtin[0])
	}

	external := &tool.NamedTool{Def: tool.Definition{Name: shell.RunCommandToolName}}
	if got := wrap(external); len(got) != 1 || got[0] != external {
		t.Fatalf("external same-name tool = %#v, want unchanged ordinary capability", got)
	}

	preWrapped := &preRuntimeToolWrapper{Tool: new(shell.RunCommandTool)}
	if got := wrap(preWrapped); len(got) != 1 || got[0] != preWrapped {
		t.Fatalf("untrusted pre-Runtime wrapper = %#v, want no inherited builtin authority", got)
	}
}

func (s *recordingAgentInputSender) SendAgentInput(_ context.Context, req agent.AgentInput) error {
	s.request = agent.CloneAgentInput(req)
	s.requests = append(s.requests, s.request)
	return nil
}

func TestRuntimeSendMessageToolForwardsEachCallAsOrdinaryInput(t *testing.T) {
	t.Parallel()

	sender := &recordingAgentInputSender{}
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
	if len(sender.requests) != 2 || sender.requests[0].Target != "parent" || sender.requests[0].Input != "status update" ||
		sender.requests[1].Target != sender.requests[0].Target || sender.requests[1].Input != sender.requests[0].Input {
		t.Fatalf("routed inputs = %#v, want two independent ordinary inputs", sender.requests)
	}
}

func TestRuntimeSendMessageToolAcceptsConcurrentIndependentInputs(t *testing.T) {
	t.Parallel()

	const calls = 16
	sender := &recordingConcurrentAgentInputSender{}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), external: sender,
		session:    session.Session{Controller: session.ControllerBinding{ControllerID: "controller-1"}},
		sessionRef: session.SessionRef{SessionID: "session-1"},
	}
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for index := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			raw, _ := json.Marshal(map[string]any{"to": "parent", "message": fmt.Sprintf("update-%d", index)})
			_, err := target.Call(context.Background(), tool.Call{ID: fmt.Sprintf("message-call-%d", index), Name: "SendMessage", Input: raw})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SendMessage error = %v", err)
		}
	}
	if got := sender.Count(); got != calls {
		t.Fatalf("ordinary input calls = %d, want %d independent dispatches", got, calls)
	}
}

type recordingConcurrentAgentInputSender struct {
	mu    sync.Mutex
	count int
}

func (s *recordingConcurrentAgentInputSender) SendAgentInput(context.Context, agent.AgentInput) error {
	s.mu.Lock()
	s.count++
	s.mu.Unlock()
	return nil
}

func (s *recordingConcurrentAgentInputSender) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.count
}

func TestRuntimeInjectsSendMessageForHostedChildWithoutOtherTools(t *testing.T) {
	t.Parallel()

	sender := &recordingAgentInputSender{}
	runtime := &Runtime{}
	wrapped := runtime.wrapToolsForRuntime(session.Session{}, session.SessionRef{SessionID: "child"}, agent.AgentSpec{}, runtimeToolContext{inputSender: sender})
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
	sender := &recordingAgentInputSender{}
	activeSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "user", SessionID: "child", WorkspaceKey: "workspace"},
		CWD:        "/workspace",
		Controller: session.ControllerBinding{ControllerID: "child-controller"},
	}
	runtime := &Runtime{policies: registry, defaultPolicyMode: presets.ModeWorkspaceWrite}
	runtimeTools := runtime.wrapToolsForRuntime(activeSession, activeSession.SessionRef, agent.AgentSpec{}, runtimeToolContext{inputSender: sender})
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
	if sender.request.Target != "parent" || sender.request.Input != "status update" {
		t.Fatalf("routed request = %#v, want policy-approved parent delivery", sender.request)
	}
	if got := testToolResultText(t, result); got != "Message sent." {
		t.Fatalf("tool result = %q, want input dispatch acknowledgement", got)
	}
}

func TestRuntimeSendMessageToolDelegatesWithoutLifecycleProtocol(t *testing.T) {
	t.Parallel()

	sender := &recordingAgentInputSender{}
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
	if sender.request.Target != "parent" || sender.request.Input != "status update" {
		t.Fatalf("routed request = %#v", sender.request)
	}
	if got := testToolResultText(t, result); got != "Message sent." {
		t.Fatalf("tool result = %q, want input dispatch acknowledgement", got)
	}
	messageMeta := testToolResultRuntimeMeta(t, result, "message")
	if len(messageMeta) != 1 || messageMeta["to"] != "parent" {
		t.Fatalf("message metadata = %#v", messageMeta)
	}
}

func TestSendMessageToolResultContainsNoTargetLifecycle(t *testing.T) {
	t.Parallel()

	result := sendMessageToolResult(tool.Call{ID: "call-1"}, "research")
	if got := testToolResultText(t, result); got != "Message sent." {
		t.Fatalf("result = %q, want ordinary input acknowledgement", got)
	}
	if result.Content[0].JSON != nil {
		t.Fatalf("result exposed lifecycle payload: %#v", result.Content[0])
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

	external := &recordingAgentInputSender{}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), external: external,
		session:    session.Session{Controller: session.ControllerBinding{ControllerID: "child-controller"}},
		sessionRef: session.SessionRef{SessionID: "child-session"},
	}
	raw, _ := json.Marshal(map[string]any{"to": "research", "message": "status for sibling"})
	if _, err := target.Call(context.Background(), tool.Call{ID: "message-sibling-1", Name: "SendMessage", Input: raw}); err != nil {
		t.Fatal(err)
	}
	if external.request.Target != "research" || external.request.Input != "status for sibling" {
		t.Fatalf("external request = %#v, want sibling handle routed through parent transport", external.request)
	}
}

func TestRuntimeSendMessageToolErrorHasNoImplementationPrefix(t *testing.T) {
	t.Parallel()

	target := runtimeSendMessageTool{
		base:       sendmessage.New(),
		session:    session.Session{Controller: session.ControllerBinding{ControllerID: "controller-1"}},
		sessionRef: session.SessionRef{SessionID: "session-1"},
	}
	raw, _ := json.Marshal(map[string]any{"to": "research", "message": "status"})
	_, err := target.Call(context.Background(), tool.Call{ID: "message-call-1", Name: "SendMessage", Input: raw})
	if err == nil || err.Error() != "target Agent messaging is unavailable" {
		t.Fatalf("Call() error = %v", err)
	}
}

func TestRuntimeSendMessageToolPrefersLocalSpawnTargetOverACPParent(t *testing.T) {
	runner := &runtimeChildInputRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "first done"}}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)
	started, err := runtime.tasks.StartSubagent(context.Background(), activeSession, activeSession.SessionRef, runner, taskapi.SubagentStartRequest{
		Agent: "helper", Prompt: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	currentSession, err := runtime.sessions.Session(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	currentSession, err = runtime.sessions.BindController(context.Background(), session.BindControllerRequest{
		SessionRef: currentSession.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Binding: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	external := &recordingAgentInputSender{}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), runtime: runtime, external: external,
		session: currentSession, sessionRef: activeSession.SessionRef,
	}
	raw, _ := json.Marshal(map[string]any{"to": started.Handle, "message": "inspect locally"})
	if _, err := target.Call(context.Background(), tool.Call{ID: "message-local-1", Name: "SendMessage", Input: raw}); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	delivered := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if delivered.Input != "inspect locally" || delivered.Target.EndpointKey != started.Ref.TaskID {
		t.Fatalf("local child input = %#v, want exact local delivery", delivered)
	}
	if external.request.Target != "" {
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
			Name:      "Write",
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
