package kernel

import (
	"context"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestBeginTurnAgentCommunicationBatchAdmitsOrderedInputs(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "batch-admit", WorkspaceKey: "ws"},
	}
	orbit := session.ActorRef{Kind: session.ActorKindParticipant, ID: "orbit-1", Name: "orbit"}
	zenith := session.ActorRef{Kind: session.ActorKindParticipant, ID: "zenith-1", Name: "zenith"}
	runtime := &recordingRuntime{session: activeSession, ran: make(chan struct{})}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  runtime,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		InputKind:  SubmissionKindAgentCommunication,
		Inputs: []agent.AgentCommunicationInput{
			{Source: orbit, Input: "from orbit"},
			{Source: zenith, Input: "from zenith"},
		},
	})
	if err != nil {
		t.Fatalf("BeginTurn(batch) error = %v", err)
	}
	defer result.Handle.Close()
	select {
	case <-runtime.ran:
	case <-time.After(2 * time.Second):
		t.Fatal("batch BeginTurn did not reach Runtime")
	}
	if runtime.lastReq.InputKind != agent.SubmissionKindAgentCommunication ||
		len(runtime.lastReq.Inputs) != 2 ||
		runtime.lastReq.Inputs[0].Input != "from orbit" || runtime.lastReq.Inputs[0].Source != orbit ||
		runtime.lastReq.Inputs[1].Input != "from zenith" || runtime.lastReq.Inputs[1].Source != zenith ||
		runtime.lastReq.Input != "" || session.ActorRefHasIdentity(runtime.lastReq.InputActor) {
		t.Fatalf("RunRequest = %#v, want ordered Inputs without singular fields", runtime.lastReq)
	}
}

func TestBeginTurnAgentCommunicationBatchRejectsMixedOrUntrustedAdmission(t *testing.T) {
	t.Parallel()

	activeSession := session.Session{
		SessionRef: session.SessionRef{AppName: "caelis", UserID: "u", SessionID: "batch-reject", WorkspaceKey: "ws"},
	}
	actor := session.ActorRef{Kind: session.ActorKindParticipant, ID: "orbit-1", Name: "orbit"}
	runtime := &recordingRuntime{session: activeSession, ran: make(chan struct{})}
	gw, err := New(Config{
		Sessions: staticSessionService{session: activeSession},
		Runtime:  runtime,
		Resolver: staticResolver{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		InputKind:  SubmissionKindAgentCommunication,
		Input:      "singular",
		InputActor: actor,
		Inputs:     []agent.AgentCommunicationInput{{Source: actor, Input: "batch"}},
	})
	var gatewayErr *Error
	if !As(err, &gatewayErr) || gatewayErr.Code != CodeInvalidRequest {
		t.Fatalf("BeginTurn(mixed) error = %v, want invalid_request", err)
	}
	_, err = gw.BeginTurn(context.Background(), BeginTurnRequest{
		SessionRef: activeSession.SessionRef,
		InputKind:  SubmissionKindAgentCommunication,
		Inputs: []agent.AgentCommunicationInput{
			{Source: actor, Input: "ok"},
			{Source: session.ActorRef{Kind: session.ActorKindUser, ID: "user-1", Name: "user"}, Input: "nope"},
		},
	})
	if !As(err, &gatewayErr) || gatewayErr.Code != CodeInvalidRequest {
		t.Fatalf("BeginTurn(user member) error = %v, want invalid_request", err)
	}
	select {
	case <-runtime.ran:
		t.Fatal("rejected batch reached Runtime")
	default:
	}
}
