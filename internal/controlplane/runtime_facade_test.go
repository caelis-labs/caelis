package controlplane

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestRuntimeFacadeScopesLiveRunsBySessionAndGeneration(t *testing.T) {
	inner := &facadeAttachRuntime{}
	facade := newRuntimeFacade(inner)
	refA := session.SessionRef{SessionID: "session-a"}
	refB := session.SessionRef{SessionID: "session-b"}

	var finishA1, finishA2 func()
	a1 := facade.wrapLiveHandle(agent.RunResult{Handle: &singleEventRunner{id: "run-1"}}, refA,
		func(inner agent.Runner, onFinish func()) agent.Runner { finishA1 = onFinish; return inner })
	b1 := facade.wrapLiveHandle(agent.RunResult{Handle: &singleEventRunner{id: "run-1"}}, refB,
		func(inner agent.Runner, onFinish func()) agent.Runner { return inner })
	inner.result = agent.RunResult{Handle: &singleEventRunner{id: "run-1"}}

	attachedA, err := facade.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: refA, RunID: "run-1"})
	if err != nil || attachedA.Handle != a1.Handle {
		t.Fatalf("attach session A = %#v, %v; want A handle", attachedA.Handle, err)
	}
	attachedB, err := facade.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: refB, RunID: "run-1"})
	if err != nil || attachedB.Handle != b1.Handle {
		t.Fatalf("attach session B = %#v, %v; want B handle", attachedB.Handle, err)
	}

	a2 := facade.wrapLiveHandle(agent.RunResult{Handle: &singleEventRunner{id: "run-1"}}, refA,
		func(inner agent.Runner, onFinish func()) agent.Runner { finishA2 = onFinish; return inner })
	finishA1() // A late predecessor must not delete its successor.
	attachedA, err = facade.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: refA, RunID: "run-1"})
	if err != nil || attachedA.Handle != a2.Handle {
		t.Fatalf("attach successor A = %#v, %v; want successor", attachedA.Handle, err)
	}
	finishA2()
	if _, err := facade.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: refA, RunID: "run-1"}); err == nil {
		t.Fatal("attach after successor finish error = nil")
	}
}

func TestRuntimeFacadeOlderWrapCannotOverwriteNewerGeneration(t *testing.T) {
	t.Parallel()
	inner := &facadeAttachRuntime{result: agent.RunResult{Handle: &singleEventRunner{id: "same-run"}}}
	facade := newRuntimeFacade(inner)
	ref := session.SessionRef{SessionID: "generation-session"}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan agent.RunResult, 1)
	go func() {
		firstDone <- facade.wrapLiveHandle(agent.RunResult{Handle: &singleEventRunner{id: "same-run"}}, ref,
			func(inner agent.Runner, _ func()) agent.Runner {
				close(entered)
				<-release
				return inner
			})
	}()
	<-entered
	second := facade.wrapLiveHandle(agent.RunResult{Handle: &singleEventRunner{id: "same-run"}}, ref,
		func(inner agent.Runner, _ func()) agent.Runner { return inner })
	close(release)
	first := <-firstDone
	if first.Handle == second.Handle {
		t.Fatal("test handles unexpectedly share identity")
	}
	attached, err := facade.AttachLiveRun(context.Background(), agent.AttachLiveRunRequest{SessionRef: ref, RunID: "same-run"})
	if err != nil {
		t.Fatal(err)
	}
	if attached.Handle != second.Handle {
		t.Fatalf("attached handle = %p, want newer generation %p", attached.Handle, second.Handle)
	}
}

type facadeAttachRuntime struct{ result agent.RunResult }

func (r *facadeAttachRuntime) Run(context.Context, agent.RunRequest) (agent.RunResult, error) {
	return r.result, nil
}
func (*facadeAttachRuntime) RunState(context.Context, session.SessionRef) (agent.RunState, error) {
	return agent.RunState{}, nil
}
func (r *facadeAttachRuntime) AttachLiveRun(context.Context, agent.AttachLiveRunRequest) (agent.RunResult, error) {
	return r.result, nil
}
