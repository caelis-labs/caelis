package controlplane

import (
	"context"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type sessionControlParticipantProbe struct {
	attach func(context.Context, agent.AttachParticipantRequest) (session.Session, error)
}

func (p sessionControlParticipantProbe) AttachParticipant(ctx context.Context, req agent.AttachParticipantRequest) (session.Session, error) {
	return p.attach(ctx, req)
}

func (sessionControlParticipantProbe) PromptParticipant(context.Context, agent.PromptParticipantRequest) (agent.RunResult, error) {
	return agent.RunResult{}, nil
}

func (sessionControlParticipantProbe) DetachParticipant(context.Context, agent.DetachParticipantRequest) (session.Session, error) {
	return session.Session{}, nil
}

func TestSessionControlAttachesResolvedRuntimePlacementDirectly(t *testing.T) {
	want := session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}}
	attachCalls := 0
	control, err := NewSessionControl(SessionControlConfig{
		Controllers: &Coordinator{},
		Participants: sessionControlParticipantProbe{attach: func(context.Context, agent.AttachParticipantRequest) (session.Session, error) {
			attachCalls++
			return want, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := control.AttachParticipant(context.Background(), agent.AttachParticipantRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if attachCalls != 1 || got.SessionID != want.SessionID {
		t.Fatalf("AttachParticipant() = %#v, calls=%d", got, attachCalls)
	}
}
