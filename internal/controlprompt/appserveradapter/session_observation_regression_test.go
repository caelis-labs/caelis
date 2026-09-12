package appserveradapter

import (
	"context"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type admissionContextClient struct {
	*sessionClientAdapterTestClient
	contexts chan context.Context
}

func (c *admissionContextClient) Prompt(ctx context.Context, req appserver.PromptRequest) (appserver.CommandResult, error) {
	c.contexts <- ctx
	return c.sessionClientAdapterTestClient.Prompt(ctx, req)
}

func TestSessionInterruptDoesNotCancelAnotherSessionsAdmission(t *testing.T) {
	for _, fresh := range []bool{false, true} {
		t.Run(map[bool]string{false: "resumed", true: "created"}[fresh], func(t *testing.T) {
			release := make(chan struct{})
			client := &admissionContextClient{
				sessionClientAdapterTestClient: &sessionClientAdapterTestClient{
					createSessionID: "b",
					target:          appserver.TurnTarget{HandleID: "h-b", RunID: "r-b", TurnID: "t-b"},
					subscription:    newSessionClientAdapterTestSubscription(), promptRelease: release,
				},
				contexts: make(chan context.Context, 1),
			}
			adapter := newSessionClientAdapterForTest(t, client.sessionClientAdapterTestClient, &sessionClientAdapterTestParticipantClient{}, "a", "cli-tui")
			var err error
			adapter.turns, err = appserver.NewSessionTurnClient(client)
			if err != nil {
				t.Fatal(err)
			}
			if fresh {
				if err := adapter.ResetSession(context.Background()); err != nil {
					t.Fatal(err)
				}
			} else if _, err := adapter.ResumeSession(context.Background(), "b"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = adapter.Close() })
			defer close(release)
			done := make(chan error, 1)
			go func() {
				_, err := adapter.Submit(context.Background(), controlprompt.Submission{Text: "B input"})
				done <- err
			}()
			var admissionCtx context.Context
			select {
			case admissionCtx = <-client.contexts:
			case <-time.After(time.Second):
				t.Fatal("B admission did not start")
			}
			if err := adapter.InterruptSession(context.Background(), "a"); err == nil {
				t.Error("old Session interrupt was accepted")
			}
			if err := admissionCtx.Err(); err != nil {
				t.Errorf("B admission context cancelled: %v", err)
			}
			adapter.activeMu.Lock()
			if len(adapter.admissions) != 1 {
				t.Error("B admission was removed")
			}
			for _, admission := range adapter.admissions {
				if admission.cancelled || admission.interrupted {
					t.Error("B admission marked cancelled or interrupted")
				}
			}
			adapter.activeMu.Unlock()
			if err := adapter.InterruptSession(context.Background(), "b"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("current Session interrupt did not stop admission")
				}
			case <-time.After(time.Second):
				t.Fatal("current Session interrupt did not finish")
			}
		})
	}
}

func TestSessionObservationRefreshesEpochForLaterTurn(t *testing.T) {
	client := &sessionClientAdapterTestClient{
		subscription: newSessionClientAdapterTestSubscription(),
		state:        appserver.SessionState{SessionID: "s", Revision: 10, Controller: session.ControllerBinding{EpochID: "e1"}},
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "s", "cli-tui")
	snapshot, err := adapter.ResumeSession(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	r := snapshot.Reconnect.(*clientSessionReconnect)
	client.state.Revision = 20
	client.state.Controller.EpochID = "e2"
	client.state.Run = appserver.RunState{Active: true, HandleID: "h2", RunID: "r2", TurnID: "t2"}
	r.ObserveEvent(eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "s", HandleID: "h2", RunID: "r2", TurnID: "t2", Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}})
	if err := r.SubmitApproval(context.Background(), controlprompt.ApprovalDecision{RequestID: "approval-2", Approved: true}); err != nil {
		t.Fatal(err)
	}
	if err := r.steer(context.Background(), "later input", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := adapter.InterruptSession(context.Background(), "s"); err != nil {
		t.Fatal(err)
	}
	for name, base := range map[string]appserver.WriteBase{"approval": client.approval.WriteBase, "steer": client.steer.WriteBase, "cancel": client.cancel.WriteBase} {
		if base.ExpectedControllerEpoch != "e2" || base.ExpectedRevision == nil || *base.ExpectedRevision != 20 {
			t.Errorf("%s wrote stale controller state: %#v", name, base)
		}
	}
	if r.State().Controller.EpochID != "e2" {
		t.Error("observer did not retain the new Turn controller")
	}
}

func TestSessionObservationRejectsUnmatchedControllerSnapshot(t *testing.T) {
	for _, change := range []string{"session", "handle", "run", "turn", "epoch", "observation"} {
		t.Run(change, func(t *testing.T) {
			state := appserver.SessionState{SessionID: "s", Controller: session.ControllerBinding{EpochID: "e2"}, Run: appserver.RunState{Active: true, HandleID: "h2", RunID: "r2", TurnID: "t2"}}
			client := &observationContextClient{sessionClientAdapterTestClient: &sessionClientAdapterTestClient{state: state}}
			r := &clientSessionReconnect{client: client, state: appserver.SessionState{SessionID: "s"}}
			env := eventstream.Envelope{Kind: eventstream.KindLifecycle, SessionID: "s", HandleID: "h2", RunID: "r2", TurnID: "t2", Lifecycle: &eventstream.Lifecycle{State: eventstream.LifecycleStateRunning}}
			r.ObserveEvent(env)
			switch change {
			case "session":
				client.state.SessionID = "other"
			case "handle":
				client.state.Run.HandleID = "other"
			case "run":
				client.state.Run.RunID = "other"
			case "turn":
				client.state.Run.TurnID = "other"
			case "epoch":
				client.state.Controller.EpochID = ""
			case "observation":
				client.inspectHook = func() { env.TurnID = "t3"; r.ObserveEvent(env) }
			}
			if err := r.steer(context.Background(), "stale input", "", nil); err == nil {
				t.Fatal("unmatched snapshot authorized input")
			}
			if client.steer.Target != (appserver.TurnTarget{}) {
				t.Fatal("unmatched snapshot reached Steer")
			}
			if r.State().Controller.EpochID != "" {
				t.Fatal("unmatched snapshot pinned controller")
			}
		})
	}
}

func TestSessionObservationKeepsPinnedEpochAfterHandoff(t *testing.T) {
	state := appserver.SessionState{SessionID: "s", Controller: session.ControllerBinding{EpochID: "e1"}, Run: appserver.RunState{Active: true, HandleID: "h1", RunID: "r1", TurnID: "t1"}}
	client := &sessionClientAdapterTestClient{state: state}
	r := &clientSessionReconnect{state: state, client: client}
	client.state.Controller.EpochID = "e2"
	if err := r.steer(context.Background(), "captured input", "", nil); err != nil {
		t.Fatal(err)
	}
	if client.steer.ExpectedControllerEpoch != "e1" || client.steer.Target != reconnectTarget(state) {
		t.Fatal("old input adopted latest authority")
	}
}
