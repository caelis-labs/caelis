package kernel

import (
	"context"
	"reflect"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type batchRecordingRunner struct {
	*submitRecordingBlockingRunner
	batches [][]agent.AgentCommunicationInput
}

func (r *batchRecordingRunner) SubmitBatch(_ context.Context, inputs []agent.AgentCommunicationInput) error {
	r.batches = append(r.batches, agent.CloneAgentCommunicationInputs(inputs))
	return nil
}

func TestActiveTurnBatchRequiresExactTargetAndBatchCapability(t *testing.T) {
	ref := session.SessionRef{SessionID: "active-batch"}
	base := &submitRecordingBlockingRunner{release: make(chan struct{})}
	runner := &batchRecordingRunner{submitRecordingBlockingRunner: base}
	h := newTurnHandle(turnHandleConfig{sessionRef: ref, handleID: "handle", runID: "run", turnID: "turn"})
	h.setRunner(runner)
	gw := &Gateway{active: map[string]*turnHandle{ref.SessionID: h}}
	inputs := []agent.AgentCommunicationInput{
		{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "one"}, Input: "one"},
		{Source: session.ActorRef{Kind: session.ActorKindParticipant, ID: "two"}, Input: "two"},
	}
	req := SubmitActiveTurnRequest{SessionRef: ref, HandleID: "handle", RunID: "run", TurnID: "stale", Kind: SubmissionKindAgentCommunication, Inputs: inputs}
	if err := gw.SubmitActiveTurn(t.Context(), req); err == nil {
		t.Fatal("stale Turn accepted batch")
	}
	req.TurnID = "turn"
	if err := gw.SubmitActiveTurn(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if len(runner.batches) != 1 || !reflect.DeepEqual(runner.batches[0], inputs) || len(base.snapshot()) != 0 {
		t.Fatalf("batch was split or changed: %#v", runner.batches)
	}
	h.setRunner(base)
	if err := gw.SubmitActiveTurn(t.Context(), req); !errorcode.Is(err, errorcode.Unsupported) {
		t.Fatalf("singular runner error = %v", err)
	}
	if len(base.snapshot()) != 0 {
		t.Fatal("batch fell back to singular input")
	}
	h.setRunner(runner)
	req.Inputs = agent.CloneAgentCommunicationInputs(inputs)
	req.Inputs[1].Source = session.ActorRef{Kind: session.ActorKindUser}
	if err := gw.SubmitActiveTurn(t.Context(), req); err == nil || len(runner.batches) != 1 {
		t.Fatal("untrusted member partially admitted")
	}
}

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
