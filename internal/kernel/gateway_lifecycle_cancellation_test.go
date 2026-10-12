package kernel

import (
	"context"
	"errors"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// Select the Host shutdown schedule where parent cancellation drains the
// producer before explicit Gateway cancellation gets to the active handle.
func TestAcceptedTurnClassifiesParentCancellation(t *testing.T) {
	t.Parallel()
	for _, participant := range []bool{false, true} {
		name := "main"
		if participant {
			name = "participant"
		}
		t.Run(name, func(t *testing.T) {
			for _, phase := range []string{"startup", "runner", "success", "failure", "observer failure"} {
				t.Run(phase, func(t *testing.T) {
					active := session.Session{SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s", WorkspaceKey: "ws"}}
					started := make(chan struct{})
					rt := &lifecycleCancellationRuntime{controlPlaneRuntime: controlPlaneRuntime{session: active, attachResp: active}}
					rt.produce = func(ctx context.Context, observer agent.SourceEventObserver) (agent.RunResult, error) {
						switch phase {
						case "startup":
							close(started)
							<-ctx.Done()
							return agent.RunResult{}, ctx.Err()
						case "runner":
							return agent.RunResult{Session: active, Handle: &parentCancellationRunner{ctx: ctx, started: started}}, nil
						case "failure":
							return agent.RunResult{}, errors.New("producer failed")
						case "observer failure":
							message := model.NewTextMessage(model.RoleAssistant, "hello")
							publishTestRunnerEvents(ctx, observer, &recordingRunner{events: []*session.Event{{Type: session.EventTypeAssistant, Message: &message, Text: "hello"}}})
						}
						return agent.RunResult{Session: active, Handle: &recordingRunner{}}, nil
					}
					gw, err := New(Config{Sessions: staticSessionService{session: active}, Runtime: rt, Control: rt, Resolver: staticResolver{resolved: ResolvedTurn{}}})
					if err != nil {
						t.Fatal(err)
					}
					parent, cancelParent := context.WithCancel(t.Context())
					defer cancelParent()
					recorder := newTestTurnEventRecorder()
					observer := TurnEventObserver(recorder)
					if phase == "observer failure" {
						observer = TurnEventObserverFunc(func(ctx context.Context, env eventstream.Envelope) error {
							if env.Kind != eventstream.KindLifecycle {
								return errors.New("observer failed")
							}
							return recorder.ObserveTurnEvent(ctx, env)
						})
					}
					var result BeginTurnResult
					if participant {
						result, err = gw.PromptParticipant(t.Context(), PromptParticipantRequest{SessionRef: active.SessionRef, RuntimeContext: parent, ParticipantID: "participant-1", Input: "wait", Observer: observer})
					} else {
						result, err = gw.BeginTurn(t.Context(), BeginTurnRequest{SessionRef: active.SessionRef, RuntimeContext: parent, Input: "wait", Observer: observer})
					}
					if err != nil {
						t.Fatal(err)
					}
					defer result.Handle.Close()
					want := eventstream.LifecycleStateCompleted
					switch phase {
					case "startup", "runner":
						select {
						case <-started:
						case <-time.After(2 * time.Second):
							t.Fatal("producer did not start")
						}
						cancelParent()
						want = eventstream.LifecycleStateCancelled
					case "failure", "observer failure":
						want = eventstream.LifecycleStateFailed
					}
					for {
						env := recorder.waitNext(t)
						if !eventstream.IsTurnTerminalLifecycle(env) {
							continue
						}
						if env.Lifecycle.State != want {
							t.Fatalf("terminal = %+v, want %s", env.Lifecycle, want)
						}
						break
					}
					select {
					case <-result.Handle.(*turnHandle).done:
					case <-time.After(2 * time.Second):
						t.Fatal("handle did not finish")
					}
					if phase == "success" && parent.Err() != nil {
						t.Fatal("normal cleanup cancelled the parent")
					}
				})
			}
		})
	}
}

type lifecycleCancellationRuntime struct {
	controlPlaneRuntime
	produce func(context.Context, agent.SourceEventObserver) (agent.RunResult, error)
}

func (r *lifecycleCancellationRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	return r.produce(ctx, req.SourceObserver)
}

func (r *lifecycleCancellationRuntime) PromptParticipant(ctx context.Context, req agent.PromptParticipantRequest) (agent.RunResult, error) {
	return r.produce(ctx, req.SourceObserver)
}

type parentCancellationRunner struct {
	ctx     context.Context
	started chan struct{}
}

func (*parentCancellationRunner) RunID() string { return "run-parent-cancel" }
func (r *parentCancellationRunner) WaitCompletion(context.Context) error {
	close(r.started)
	<-r.ctx.Done()
	return r.ctx.Err()
}
func (*parentCancellationRunner) Submit(agent.Submission) error { return nil }
func (*parentCancellationRunner) Cancel() agent.CancelResult {
	return agent.CancelResult{Status: agent.CancelStatusCancelled}
}
func (*parentCancellationRunner) Close() error { return nil }
