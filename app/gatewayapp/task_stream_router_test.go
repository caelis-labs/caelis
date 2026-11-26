package gatewayapp

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/streamspool"
	"github.com/caelis-labs/caelis/control/taskstream"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

// NewTerminalReleaseTestFixture prepares an activated Session and an open Task
// spool for the external test that exercises the local AppServer.
func NewTerminalReleaseTestFixture(t *testing.T) (*Stack, appserver.TerminalRequest, func(streamspool.State)) {
	t.Helper()
	ctx := t.Context()
	stack, active := newLocalStateTestStack(t)
	const taskID, handle = "command-task", "release-command"
	ref := session.SessionRef{AppName: active.AppName, UserID: active.UserID, SessionID: active.SessionID}
	if err := stack.composition.authorities.taskStore.Upsert(ctx, &task.Entry{
		TaskID: taskID, Handle: handle, Kind: task.KindCommand, Session: ref, State: task.StateCompleted,
		Result: map[string]any{"result": "command output"},
	}); err != nil {
		t.Fatalf("seed command Task: %v", err)
	}
	lease, err := stack.ControlRuntimes().Acquire(ctx, appserver.Principal{ID: stack.UserID()}, appserver.ActionCancel, active.SessionID, true)
	if err != nil {
		t.Fatalf("activate owning Runtime: %v", err)
	}
	t.Cleanup(func() { _ = lease.Close(context.Background()) })

	recorder := stack.composition.authorities.taskOutput.(*taskstream.Recorder)
	observer := recorder.BindTaskOutput(ctx, task.OutputBinding{
		SessionID: active.SessionID, TaskID: taskID, Kind: task.KindCommand, StartsAtTaskOrigin: true,
	})
	if err := observer.ObserveTaskOutput(ctx, task.OutputEvent{Text: "command output"}); err != nil {
		t.Fatalf("record command output: %v", err)
	}
	if err := recorder.Flush(ctx, task.Ref{SessionID: active.SessionID, TaskID: taskID}); err != nil {
		t.Fatalf("flush command output: %v", err)
	}
	logical := streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings(active.SessionID, taskID)}
	assertState := func(want streamspool.State) {
		t.Helper()
		_, bounds, err := stack.composition.authorities.streamSpool.Resolve(ctx, logical)
		if err != nil || bounds.State != want {
			t.Fatalf("Task spool state = %v, %v, want %v", bounds.State, err, want)
		}
	}
	return stack, appserver.TerminalRequest{SessionID: active.SessionID, TerminalID: handle}, assertState
}

func TestHostTaskStreamServiceRoutesByOwningSessionRuntime(t *testing.T) {
	stack, active := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })

	rootGateway := newTaskStreamRouterTestGateway(t, stack, "root")
	childGateway := newTaskStreamRouterTestGateway(t, stack, "child")
	rootInstance := &sessionRuntimeInstance{runtimeComposition: runtimeComposition{gateway: rootGateway}}
	childInstance := &sessionRuntimeInstance{runtimeComposition: runtimeComposition{gateway: childGateway}}
	stack.sessionRuntimes.mu.Lock()
	stack.sessionRuntimes.sessions[active.SessionID] = &sessionRuntime{
		sessionID: active.SessionID,
		instance:  rootInstance,
	}
	stack.sessionRuntimes.sessions["session-child"] = &sessionRuntime{
		sessionID: "session-child",
		instance:  childInstance,
	}
	stack.sessionRuntimes.mu.Unlock()

	lifecycle := &recordingTaskOutputLifecycle{}
	router := hostTaskStreamService{registry: stack.sessionRuntimes, lifecycle: lifecycle}
	root, err := router.Read(context.Background(), task.TerminalRef{SessionID: active.SessionID, TaskID: "task-root"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := router.Read(context.Background(), task.TerminalRef{SessionID: "session-child", TaskID: "task-child"})
	if err != nil {
		t.Fatal(err)
	}
	if root.FinalResult != "root" || child.FinalResult != "child" {
		t.Fatalf("routed snapshots = %#v / %#v", root, child)
	}
	if _, err := router.Read(context.Background(), task.TerminalRef{SessionID: "session-not-loaded", TaskID: "task-missing"}); err == nil {
		t.Fatal("Task read used the default Runtime for an unowned Session")
	}

	waited, err := router.Wait(context.Background(), task.TerminalRef{SessionID: "session-child", TaskID: "task-child"})
	if err != nil || waited.FinalResult != "child" {
		t.Fatalf("routed terminal wait = %#v, %v", waited, err)
	}
	if err := router.Release(context.Background(), task.TerminalRef{SessionID: "session-child", TaskID: "task-child"}); err != nil {
		t.Fatal(err)
	}
	lifecycle.mu.Lock()
	released := append([]task.Ref(nil), lifecycle.tasks...)
	lifecycle.mu.Unlock()
	if len(released) != 1 || released[0].SessionID != "session-child" || released[0].TaskID != "task-child" {
		t.Fatalf("released Task traces = %#v", released)
	}
}

func newTaskStreamRouterTestGateway(t *testing.T, stack *Stack, marker string) *kernelimpl.Gateway {
	t.Helper()
	gateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.composition.sessions,
		Runtime: taskStreamRouterTestRuntime{
			terminals: taskStreamRouterTestController{marker: marker},
		},
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

type taskStreamRouterTestRuntime struct {
	terminals task.TerminalController
}

func (taskStreamRouterTestRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (taskStreamRouterTestRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r taskStreamRouterTestRuntime) Terminals() task.TerminalController { return r.terminals }

type taskStreamRouterTestController struct {
	marker string
}

func (s taskStreamRouterTestController) Read(_ context.Context, ref task.TerminalRef) (task.TerminalSnapshot, error) {
	return task.TerminalSnapshot{Ref: ref, FinalResult: s.marker}, nil
}

func (s taskStreamRouterTestController) Wait(_ context.Context, ref task.TerminalRef) (task.TerminalSnapshot, error) {
	return task.TerminalSnapshot{Ref: ref, FinalResult: s.marker}, nil
}

func (taskStreamRouterTestController) Kill(context.Context, task.TerminalRef) error    { return nil }
func (taskStreamRouterTestController) Release(context.Context, task.TerminalRef) error { return nil }

var _ agent.Runtime = taskStreamRouterTestRuntime{}
var _ task.TerminalProvider = taskStreamRouterTestRuntime{}
var _ task.TerminalController = taskStreamRouterTestController{}
