package runtime

import (
	"encoding/json"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/sendmessage"
	"strings"
	"testing"
)

func TestSubmitChildInputBindsCanonicalParentSourceNotLocalAgentName(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	r, task, runner := newLocalControllerChildActivityTask(t)
	stale := session.ControllerExecutor(session.ControllerBinding{
		Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "local",
	})
	handoffCtx, release, err := r.sessions.AcquireWriter(t.Context(), task.sessionRef, "handoff")
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.sessions.BindController(handoffCtx, session.BindControllerRequest{
		SessionRef: task.sessionRef, MutationGuard: session.ControlMutationGuardWithRuntimeFence(handoffCtx, session.ControlMutationPurposeHandoff),
		Binding: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "controller-2", AgentName: "local"},
	})
	releaseErr := release()
	if err != nil || releaseErr != nil {
		t.Fatalf("handoff bind = %v, release = %v", err, releaseErr)
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
		SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeLifecycle),
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

func TestChildInputRejectsPersistedUnresolvedExecution(t *testing.T) {
	t.Parallel()
	r, task, runner := newLocalControllerChildActivityTask(t)
	task.mu.Lock()
	task.applyResult(delegation.Result{State: delegation.StateUnknownOutcome, Error: "prompt observation lost"})
	entry := task.entrySnapshot(r.now())
	task.mu.Unlock()
	if err := r.tasks.persistTaskEntryWithConflictInvalidation(t.Context(), entry, false); err != nil {
		t.Fatal(err)
	}
	stored, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	// Discard process-local activity state. The persisted result must still
	// prevent endpoint binding/resume from turning an unresolved Run into idle.
	r.tasks.mu.Lock()
	r.tasks.subagents[task.ref.TaskID] = r.tasks.rehydrateSubagentTask(stored)
	r.tasks.mu.Unlock()
	_, err = r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: task.handle,
		Source: session.ControllerExecutor(session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "local"}),
		Input:  "do not start a second Run",
	})
	if !errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("unresolved input = %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.request.Input != "" {
		t.Fatalf("unresolved input dispatched: %#v", runner.request)
	}
	after, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != stored.Revision || after.State != taskapi.StateUnknownOutcome {
		t.Fatalf("rejected input mutated Task: %#v", after)
	}
}

func TestUserChildInputPreservesUserAndRejectsReplacedBinding(t *testing.T) {
	t.Parallel()
	runtime, task, runner := newLocalControllerChildActivityTask(t)
	trace := &recordingProducerLifecycle{}
	runtime.taskOutput = trace
	active, err := runtime.sessions.Session(t.Context(), task.sessionRef)
	if err != nil {
		t.Fatal(err)
	}
	var binding session.ParticipantBinding
	for _, p := range active.Participants {
		if p.Kind == session.ParticipantKindSubagent {
			binding = p
			break
		}
	}
	if binding.ID == "" {
		t.Fatal("missing child fixture")
	}
	parts := []model.ContentPart{{Type: model.ContentPartImage, MimeType: "image/png", Data: "aGk="}}
	_, err = runtime.SubmitUserChildInput(t.Context(), task.sessionRef, binding, "owner", "user guide", parts)
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	got := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if trace.binding.ActivityID == "" || trace.binding.ActivityID != got.ActivityID {
		t.Fatalf("user follow-up bypassed shared output binding: %#v", trace.binding)
	}
	if !got.UserInput || got.Source.Kind != session.ActorKindUser || got.Source.ID != "owner" || got.Input != "user guide" {
		t.Fatalf("human input=%#v", got)
	}
	if len(got.ContentParts) != 1 || got.ContentParts[0].Data != parts[0].Data {
		t.Fatalf("image lost: %#v", got)
	}
	binding.AttachmentGeneration += "stale"
	if _, err = runtime.SubmitUserChildInput(t.Context(), task.sessionRef, binding, "owner", "do not send", nil); err == nil {
		t.Fatal("stale binding accepted")
	}
	_, err = runtime.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{Target: task.handle, Source: session.ActorRef{Kind: session.ActorKindUser, ID: "owner"}, Input: "spoof", UserInput: true})
	if !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("Agent endpoint accepted human flag: %v", err)
	}
}
