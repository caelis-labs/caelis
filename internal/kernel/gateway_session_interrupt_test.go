package kernel

import (
	"context"
	"errors"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Exercise the Host composition: close admission before selecting the active
// Turn, cancel it, revoke delegates, and reopen admission even on callback error.
func TestSessionAdmissionBlockScopesInterrupt(t *testing.T) {
	t.Parallel()
	for _, callbackErr := range []error{nil, errors.New("delegate cancellation failed")} {
		name := "success"
		if callbackErr != nil {
			name = "callback error"
		}
		t.Run(name, func(t *testing.T) {
			ref := session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "s1", WorkspaceKey: "ws"}
			started := make(chan struct{})
			cancelled := make(chan struct{})
			producerRelease := make(chan struct{})
			rt := &sessionInterruptRuntime{run: func(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
				if req.Input == "original" {
					close(started)
					<-ctx.Done()
					close(cancelled)
					<-producerRelease // Cancellation does not prove producer quiescence.
					return agent.RunResult{}, ctx.Err()
				}
				return agent.RunResult{Handle: &recordingRunner{}}, nil
			}}
			gw, err := New(Config{Sessions: interruptSessionService{}, Runtime: rt, Control: rt, Resolver: staticResolver{}})
			if err != nil {
				t.Fatal(err)
			}
			original, err := gw.BeginTurn(t.Context(), BeginTurnRequest{SessionRef: ref, Input: "original"})
			if err != nil {
				t.Fatal(err)
			}
			waitInterruptSignal(t, started)
			callbackStarted := make(chan struct{})
			callbackRelease := make(chan struct{})
			interrupted := make(chan error, 1)
			go func() {
				interrupted <- func() error {
					release, err := gw.BlockSessionTurnAdmission(ref)
					if err != nil {
						return err
					}
					defer release()
					if err := gw.Interrupt(t.Context(), InterruptRequest{SessionRef: ref}); err != nil {
						return err
					}
					close(callbackStarted)
					<-callbackRelease
					return callbackErr
				}()
			}()
			waitInterruptSignal(t, callbackStarted)
			waitInterruptSignal(t, cancelled)
			if active, ok := gw.ActiveTurn(ref.SessionID); !ok || active.HandleID != original.Handle.HandleID() {
				t.Fatalf("cancel released noncooperative producer: %+v, %v", active, ok)
			}
			_, err = gw.BeginTurn(t.Context(), BeginTurnRequest{SessionRef: ref, Input: "cannot overlap producer"})
			if err == nil {
				t.Fatal("accepted Turn while cancelled producer still owns active slot")
			}
			assertTurnAdmissionBlocked(t, gw.SubmitActiveTurn(t.Context(), SubmitActiveTurnRequest{SessionRef: ref, Kind: SubmissionKindConversation, Text: "new input"}))
			close(producerRelease)
			waitInterruptSignal(t, original.Handle.(*turnHandle).done)

			// The old mailbox callback can now observe an idle Session, but cannot
			// begin a replacement Turn while delegate cancellation is in flight.
			_, err = gw.BeginTurn(t.Context(), BeginTurnRequest{SessionRef: ref, Input: "old mailbox callback"})
			assertTurnAdmissionBlocked(t, err)
			_, err = gw.PromptParticipant(t.Context(), PromptParticipantRequest{SessionRef: ref, ParticipantID: "p", Input: "callback"})
			assertTurnAdmissionBlocked(t, err)
			_, err = gw.StartParticipant(t.Context(), StartParticipantRequest{SessionRef: ref, Agent: "side", Input: "callback"})
			assertTurnAdmissionBlocked(t, err)
			if rt.attachReq.Agent != "" {
				t.Fatal("blocked StartParticipant attached a participant")
			}
			for _, kind := range []SubmissionKind{SubmissionKindConversation, SubmissionKindApproval, SubmissionKindAgentCommunication} {
				assertTurnAdmissionBlocked(t, gw.SubmitActiveTurn(t.Context(), SubmitActiveTurnRequest{SessionRef: ref, Kind: kind}))
			}

			other := ref
			other.SessionID = "s2"
			unrelated, err := gw.BeginTurn(t.Context(), BeginTurnRequest{SessionRef: other, Input: "unrelated"})
			if err != nil {
				t.Fatalf("unrelated Session admission: %v", err)
			}
			waitInterruptSignal(t, unrelated.Handle.(*turnHandle).done)
			close(callbackRelease)
			select {
			case err := <-interrupted:
				if !errors.Is(err, callbackErr) {
					t.Fatalf("interrupt = %v, want %v", err, callbackErr)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("interrupt did not return")
			}
			next, err := gw.BeginTurn(t.Context(), BeginTurnRequest{SessionRef: ref, Input: "explicit new work"})
			if err != nil {
				t.Fatalf("admission did not reopen: %v", err)
			}
			waitInterruptSignal(t, next.Handle.(*turnHandle).done)
		})
	}
}

func assertTurnAdmissionBlocked(t *testing.T, err error) {
	t.Helper()
	var gatewayErr *Error
	if !errors.As(err, &gatewayErr) || gatewayErr.Code != CodeTurnAdmissionBlocked {
		t.Fatalf("error = %v, want Turn admission blocked", err)
	}
}

func waitInterruptSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for interrupt lifecycle signal")
	}
}

type interruptSessionService struct{ staticSessionService }

func (interruptSessionService) Session(_ context.Context, ref session.SessionRef) (session.Session, error) {
	return session.Session{SessionRef: ref}, nil
}

type sessionInterruptRuntime struct {
	controlPlaneRuntime
	run func(context.Context, agent.RunRequest) (agent.RunResult, error)
}

func (r *sessionInterruptRuntime) Run(ctx context.Context, req agent.RunRequest) (agent.RunResult, error) {
	return r.run(ctx, req)
}
