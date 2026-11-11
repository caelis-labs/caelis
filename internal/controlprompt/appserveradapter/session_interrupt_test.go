package appserveradapter

import (
	"context"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestSessionInterruptDispatchesIdleSessionCAS(t *testing.T) {
	client := &sessionClientAdapterTestClient{state: appserver.SessionState{
		SessionID: "session-1", Revision: 17, Controller: session.ControllerBinding{EpochID: "epoch-1"},
	}}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "session-1", "cli-tui")
	if err := adapter.InterruptSession(t.Context(), "session-1"); err != nil {
		t.Fatal(err)
	}
	request := client.interrupt
	if request.SessionID != "session-1" || request.ExpectedRevision == nil || *request.ExpectedRevision != 17 || request.ExpectedControllerEpoch != "epoch-1" || request.OperationID == "" {
		t.Fatalf("Session interrupt lost its CAS: %#v", request)
	}
	if client.cancel.OperationID != "" || client.create.OperationID != "" {
		t.Fatal("idle Session interrupt cancelled an exact Turn or created a Session")
	}
}

type noSessionInterruptCapabilityClient struct {
	*sessionClientAdapterTestClient
}

func (*noSessionInterruptCapabilityClient) Initialize(context.Context) (appserver.ServerInfo, error) {
	return appserver.ServerInfo{}, nil
}

func TestSessionInterruptRejectsUnsupportedHostWithoutTurnFallback(t *testing.T) {
	client := &noSessionInterruptCapabilityClient{&sessionClientAdapterTestClient{}}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "s", "cli-tui")
	if err := adapter.InterruptSession(t.Context(), "s"); err == nil {
		t.Fatal("unsupported Host accepted Session interrupt")
	}
	if client.cancel.OperationID != "" || client.interrupt.OperationID != "" {
		t.Fatal("unsupported Host received a mutation")
	}
}

func TestSessionInterruptUnknownRetryKeepsOriginalCAS(t *testing.T) {
	client := &sessionClientAdapterTestClient{
		state:          appserver.SessionState{SessionID: "s", Revision: 3, Controller: session.ControllerBinding{EpochID: "e1"}},
		cancelOutcomes: []appserver.Outcome{appserver.OutcomeUnknown, appserver.OutcomeCommitted},
	}
	adapter := newSessionClientAdapterForTest(t, client, &sessionClientAdapterTestParticipantClient{}, "s", "cli-tui")
	if err := adapter.InterruptSession(t.Context(), "s"); err == nil {
		t.Fatal("unknown outcome reported success")
	}
	first := client.interrupt
	client.state.Revision = 5
	client.state.Controller.EpochID = "e2"
	if err := adapter.InterruptSession(t.Context(), "s"); err != nil {
		t.Fatal(err)
	}
	retry := client.interrupt
	if retry.OperationID != first.OperationID || *retry.ExpectedRevision != 3 || retry.ExpectedControllerEpoch != "e1" {
		t.Fatalf("unknown retry retargeted newer work: %#v", retry)
	}
}
