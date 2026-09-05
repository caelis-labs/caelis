package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
)

func TestSubmitChildInputBindsCanonicalParentSourceNotLocalAgentName(t *testing.T) {
	r, task, runner := newLocalControllerChildActivityTask(t)
	_, err := r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: task.handle,
		Source: session.ControllerExecutor(session.ControllerBinding{
			Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "local",
		}),
		Input: "follow up from parent",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	got := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if got.Source != session.ParentCommunicationActor() {
		t.Fatalf("child input source = %#v, want canonical parent identity", got.Source)
	}
	if strings.EqualFold(got.Source.Name, "local") || strings.EqualFold(got.Source.ID, "local") {
		t.Fatalf("child input source leaked local controller name: %#v", got.Source)
	}
}

func TestSubmitChildInputRejectsReservedParentSourceAlias(t *testing.T) {
	r, task, runner := newLocalControllerChildActivityTask(t)
	_, err := r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: task.handle, Source: session.ParentCommunicationActor(), Input: "must not bypass controller identity",
	})
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("SubmitChildInput(source=parent) error = %v, want permission denied", err)
	}
	runner.mu.Lock()
	got := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if got.Input != "" {
		t.Fatalf("reserved parent source delivered %#v, want no child dispatch", got)
	}
}

func TestSubmitChildInputRejectsStaleControllerAfterHandoff(t *testing.T) {
	r, task, runner := newLocalControllerChildActivityTask(t)
	stale := session.ControllerExecutor(session.ControllerBinding{
		Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "local",
	})
	_, err := r.sessions.BindController(t.Context(), session.BindControllerRequest{
		SessionRef: task.sessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Binding: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "controller-2", AgentName: "local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: task.handle, Source: stale, Input: "must not authenticate after handoff",
	})
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("SubmitChildInput(stale controller) error = %v, want permission denied", err)
	}
	runner.mu.Lock()
	got := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if got.Input != "" {
		t.Fatalf("stale controller source delivered %#v, want no child dispatch", got)
	}
	_, err = r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: task.handle,
		Source: session.ControllerExecutor(session.ControllerBinding{
			Kind: session.ControllerKindKernel, ControllerID: "controller-2", AgentName: "local",
		}),
		Input: "handoff successor",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	got = agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if got.Source != session.ParentCommunicationActor() || got.Input != "handoff successor" {
		t.Fatalf("successor controller input = %#v, want recipient-visible parent identity", got)
	}
}

func TestSubmitChildInputRejectsLocalAsParentSourceAlias(t *testing.T) {
	r, task, _ := newLocalControllerChildActivityTask(t)
	_, err := r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: task.handle,
		Source: session.ActorRef{Kind: session.ActorKindController, ID: "local", Name: "local"},
		Input:  "must not authenticate as parent",
	})
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("SubmitChildInput(source=local) error = %v, want permission denied", err)
	}
}

func TestSubmitChildInputDoesNotRouteLocalHandleToParent(t *testing.T) {
	r, task, _ := newLocalControllerChildActivityTask(t)
	_, err := r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: "local",
		Source: session.ControllerExecutor(session.ControllerBinding{
			Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "local",
		}),
		Input: "must not reach parent",
	})
	if !errorcode.Is(err, errorcode.NotFound) || err == nil || !strings.Contains(err.Error(), `Target Agent "local" is not attached`) {
		t.Fatalf("SubmitChildInput(to=local) error = %v, want not attached", err)
	}
	if strings.EqualFold(task.handle, "local") {
		t.Fatal("fixture child handle collided with the local product name")
	}
}

func TestRuntimeSendMessageToChildUsesParentSourceNotLocal(t *testing.T) {
	r, task, runner := newLocalControllerChildActivityTask(t)
	active, err := r.sessions.Session(t.Context(), task.sessionRef)
	if err != nil {
		t.Fatal(err)
	}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), runtime: r, session: active, sessionRef: task.sessionRef,
	}
	raw, _ := json.Marshal(map[string]any{"to": task.handle, "message": "inspect locally"})
	if _, err := target.Call(t.Context(), tool.Call{ID: "message-parent-1", Name: "SendMessage", Input: raw}); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	got := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if got.Source != session.ParentCommunicationActor() {
		t.Fatalf("SendMessage source = %#v, want canonical parent identity", got.Source)
	}
}

func TestRuntimeSendMessageDoesNotTreatLocalAsParentTarget(t *testing.T) {
	r, task, runner := newLocalControllerChildActivityTask(t)
	active, err := r.sessions.Session(t.Context(), task.sessionRef)
	if err != nil {
		t.Fatal(err)
	}
	target := runtimeSendMessageTool{
		base: sendmessage.New(), runtime: r, session: active, sessionRef: task.sessionRef,
	}
	raw, _ := json.Marshal(map[string]any{"to": "local", "message": "must not authenticate as parent"})
	_, err = target.Call(t.Context(), tool.Call{ID: "message-local-1", Name: "SendMessage", Input: raw})
	if err == nil || !strings.Contains(err.Error(), `Target Agent "local" is not attached`) {
		t.Fatalf("SendMessage(to=local) error = %v, want not attached", err)
	}
	runner.mu.Lock()
	got := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if got.Input != "" {
		t.Fatalf("SendMessage(to=local) delivered %#v, want no child dispatch", got)
	}
}

func newLocalControllerChildActivityTask(t *testing.T) (*Runtime, *subagentTask, *runtimeChildInputRunner) {
	t.Helper()
	runner := &runtimeChildInputRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "first done"}}
	r, active := newSubagentTaskTestRuntime(t, runner)
	_, err := r.sessions.BindController(t.Context(), session.BindControllerRequest{
		SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Binding: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "local"},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := r.tasks.StartSubagent(t.Context(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{Agent: "helper", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := r.tasks.lookupSubagent(t.Context(), active.SessionRef, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	runner.request = agent.ChildInputRequest{}
	runner.mu.Unlock()
	return r, task, runner
}
