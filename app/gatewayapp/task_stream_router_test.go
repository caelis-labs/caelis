package gatewayapp

import (
	"context"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
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

	router := hostTaskStreamService{host: &stack.composition, registry: stack.sessionRuntimes}
	root, err := router.Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{SessionID: active.SessionID, TaskID: "task-root"},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := router.Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{SessionID: "session-child", TaskID: "task-child"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if root.FinalText != "root" || child.FinalText != "child" {
		t.Fatalf("routed snapshots = %#v / %#v", root, child)
	}
	if _, err := router.Read(context.Background(), stream.ReadRequest{
		Ref: stream.Ref{SessionID: "session-not-loaded", TaskID: "task-missing"},
	}); err == nil {
		t.Fatal("Task read used the default Runtime for an unowned Session")
	}

	var subscribed []*stream.Frame
	for frame, subscribeErr := range router.Subscribe(context.Background(), stream.SubscribeRequest{
		Ref: stream.Ref{SessionID: "session-child", TaskID: "task-child"},
	}) {
		if subscribeErr != nil {
			t.Fatal(subscribeErr)
		}
		subscribed = append(subscribed, frame)
	}
	if len(subscribed) != 1 || subscribed[0].Text != "child" {
		t.Fatalf("subscribed frames = %#v", subscribed)
	}
}

func newTaskStreamRouterTestGateway(t *testing.T, stack *Stack, marker string) *kernelimpl.Gateway {
	t.Helper()
	gateway, err := kernelimpl.New(kernelimpl.Config{
		Sessions: stack.composition.sessions,
		Runtime: taskStreamRouterTestRuntime{
			streams: taskStreamRouterTestStreams{marker: marker},
		},
		Resolver: blockingResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

type taskStreamRouterTestRuntime struct {
	streams stream.Service
}

func (taskStreamRouterTestRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (taskStreamRouterTestRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}

func (r taskStreamRouterTestRuntime) Streams() stream.Service { return r.streams }

type taskStreamRouterTestStreams struct {
	marker string
}

func (s taskStreamRouterTestStreams) Read(_ context.Context, request stream.ReadRequest) (stream.Snapshot, error) {
	return stream.Snapshot{Ref: request.Ref, FinalText: s.marker}, nil
}

func (s taskStreamRouterTestStreams) Subscribe(
	_ context.Context,
	request stream.SubscribeRequest,
) iter.Seq2[*stream.Frame, error] {
	return func(yield func(*stream.Frame, error) bool) {
		yield(&stream.Frame{Ref: request.Ref, Text: s.marker}, nil)
	}
}

var _ agent.Runtime = taskStreamRouterTestRuntime{}
var _ agent.StreamProvider = taskStreamRouterTestRuntime{}
var _ stream.Service = taskStreamRouterTestStreams{}
