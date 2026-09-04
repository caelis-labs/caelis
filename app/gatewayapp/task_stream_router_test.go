package gatewayapp

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/terminal"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

func TestHostTaskStreamServiceRoutesByOwningSessionRuntime(t *testing.T) {
	stack, active := newLocalStateTestStack(t)
	t.Cleanup(func() { _ = stack.Close() })

	rootGateway := newTaskStreamRouterTestGateway(t, stack, "root")
	childGateway := newTaskStreamRouterTestGateway(t, stack, "child")
	stack.composition.mu.Lock()
	stack.composition.gateway = rootGateway
	stack.composition.mu.Unlock()

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
	router := hostTaskStreamService{host: &stack.composition, registry: stack.sessionRuntimes, lifecycle: lifecycle}
	root, err := router.Read(context.Background(), terminal.Ref{SessionID: active.SessionID, TaskID: "task-root"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := router.Read(context.Background(), terminal.Ref{SessionID: "session-child", TaskID: "task-child"})
	if err != nil {
		t.Fatal(err)
	}
	if root.FinalResult != "root" || child.FinalResult != "child" {
		t.Fatalf("routed snapshots = %#v / %#v", root, child)
	}
	if _, err := router.Read(context.Background(), terminal.Ref{SessionID: "session-not-loaded", TaskID: "task-missing"}); err == nil {
		t.Fatal("Task read used the default Runtime for an unowned Session")
	}

	waited, err := router.Wait(context.Background(), terminal.Ref{SessionID: "session-child", TaskID: "task-child"})
	if err != nil || waited.FinalResult != "child" {
		t.Fatalf("routed terminal wait = %#v, %v", waited, err)
	}
	if err := router.Release(context.Background(), terminal.Ref{SessionID: "session-child", TaskID: "task-child"}); err != nil {
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
	terminals terminal.Controller
}

func (taskStreamRouterTestRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (taskStreamRouterTestRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r taskStreamRouterTestRuntime) Terminals() terminal.Controller { return r.terminals }

type taskStreamRouterTestController struct {
	marker string
}

func (s taskStreamRouterTestController) Read(_ context.Context, ref terminal.Ref) (terminal.Snapshot, error) {
	return terminal.Snapshot{Ref: ref, FinalResult: s.marker}, nil
}

func (s taskStreamRouterTestController) Wait(_ context.Context, ref terminal.Ref) (terminal.Snapshot, error) {
	return terminal.Snapshot{Ref: ref, FinalResult: s.marker}, nil
}

func (taskStreamRouterTestController) Kill(context.Context, terminal.Ref) error    { return nil }
func (taskStreamRouterTestController) Release(context.Context, terminal.Ref) error { return nil }

var _ agent.Runtime = taskStreamRouterTestRuntime{}
var _ agent.TerminalProvider = taskStreamRouterTestRuntime{}
var _ terminal.Controller = taskStreamRouterTestController{}
